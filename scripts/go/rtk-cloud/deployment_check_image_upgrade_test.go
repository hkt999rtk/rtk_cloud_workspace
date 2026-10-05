package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deploymentUpgradeFixture(t *testing.T) (deploymentUpgradeTarget, string) {
	t.Helper()
	image := "ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:" + strings.Repeat("a", 64)
	var target deploymentUpgradeTarget
	raw := `{"kind":"Deployment","metadata":{"namespace":"video-cloud-staging-billing","name":"payment-simulator"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"` + image + `","command":["/rtk-billing-payment-simulator"],"env":[{"name":"BILLING_DB_MIGRATE_ON_STARTUP","value":"false"}]}]}}}}`
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		t.Fatal(err)
	}
	return target, image
}

func TestDeploymentImageUpgradeCandidateScopePreservesEffectiveConfiguration(t *testing.T) {
	for _, name := range []string{"image-only", "unselected-image", "changed-config", "missing-owner", "not-deployed", "already-deployed", "missing-template", "missing-pod-spec", "missing-containers", "bad-container", "duplicate-container", "missing-image", "empty-containers"} {
		t.Run(name, func(t *testing.T) {
			target, image := deploymentUpgradeFixture(t)
			raw, _ := json.Marshal(target)
			var current deploymentUpgradeTarget
			_ = json.Unmarshal(raw, &current)
			_, _, err := deploymentUpgradeSpec(target.Spec)
			if err != nil {
				t.Fatal(err)
			}
			containers := target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
			live := []deploymentUpgradeTarget{current}
			images := []string{image}
			phase := deploymentCheckPreDeploy
			switch name {
			case "image-only":
				current.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "old-image"
				live[0] = current
			case "unselected-image":
				images = nil
			case "changed-config":
				target.Spec["replicas"] = float64(2)
			case "missing-owner":
				live = nil
			case "not-deployed":
				phase = deploymentCheckPostDeploy
				live[0].Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "old-image"
			case "already-deployed":
				phase = deploymentCheckPostDeploy
			case "missing-template":
				target.Spec = map[string]any{}
			case "missing-pod-spec":
				target.Spec["template"] = map[string]any{}
			case "missing-containers":
				target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"] = "bad"
			case "bad-container":
				target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"] = []any{"bad"}
			case "duplicate-container":
				target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"] = append(containers, containers[0])
			case "missing-image":
				delete(containers[0].(map[string]any), "image")
			case "empty-containers":
				target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"] = []any{}
			}
			err = validateDeploymentUpgradeTargets([]deploymentUpgradeTarget{target}, live, images, phase, "staging")
			wantPass := name == "image-only" || name == "already-deployed"
			if (err == nil) != wantPass {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

func TestDeploymentImageUpgradeManifestInventoryFailsClosed(t *testing.T) {
	target, _ := deploymentUpgradeFixture(t)
	raw, _ := json.Marshal(target)
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{{"deployment", string(raw), true}, {"List", `{"kind":"List","items":[` + string(raw) + `]}`, true}, {"empty-list", `{"kind":"List","items":[]}`, false}, {"malformed", "{broken", false}, {"unsupported-kind", strings.Replace(string(raw), "Deployment", "StatefulSet", 1), false}, {"other-env", strings.Replace(string(raw), "video-cloud-staging", "video-cloud-dev", 1), false}, {"duplicate", `{"kind":"List","items":[` + string(raw) + `,` + string(raw) + `]}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "candidate.json")
			if err := os.WriteFile(p, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := deploymentUpgradeTargets([]string{p}, "staging")
			if (err == nil) != tc.pass {
				t.Fatal(err)
			}
		})
	}
	if _, err := deploymentUpgradeTargets([]string{"/missing/readiness-candidate"}, "staging"); err == nil {
		t.Fatal("missing file passed")
	}
}

func TestDeploymentImageUpgradeCIRequiresExactSuccessfulMainAndPublisher(t *testing.T) {
	sha := strings.Repeat("b", 40)
	good := `{"total_count":1,"workflow_runs":[{"id":1,"head_sha":"` + sha + `","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}`
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{{"valid", good, true}, {"pending", strings.Replace(good, "completed", "in_progress", 1), false}, {"failed", strings.Replace(good, "success", "failure", 1), false}, {"PR", strings.Replace(good, "push", "pull_request", 1), false}, {"wrong-sha", strings.Replace(good, sha, strings.Repeat("c", 40), 1), false}, {"wrong-branch", strings.Replace(good, "main", "feature", 1), false}, {"truncated", strings.Replace(good, `"total_count":1`, `"total_count":2`, 1), false}, {"malformed", "broken", false}, {"empty", `{"total_count":0,"workflow_runs":[]}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := deploymentUpgradeSuccessfulRun([]byte(tc.body), sha)
			if (err == nil) != tc.pass {
				t.Fatal(err)
			}
		})
	}
	job := `{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`
	for _, raw := range []string{job, strings.Replace(job, "success", "skipped", 1), strings.Replace(job, "Publish LKE image", "Package release", 1), `{}`, strings.Replace(job, `"total_count":1`, `"total_count":2`, 1)} {
		err := deploymentUpgradePublishedJob([]byte(raw))
		if (err == nil) != (raw == job) {
			t.Fatal(err)
		}
	}
	for _, image := range []string{"ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:" + strings.Repeat("a", 64), "ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:" + strings.Repeat("a", 64), "unreviewed:latest"} {
		_, err := deploymentUpgradeImageSource(image)
		if (err == nil) == (image == "unreviewed:latest") {
			t.Fatal(err)
		}
	}
}

func deploymentUpgradeFakeTools(t *testing.T, healthURL string, fail string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_UPGRADE_FAKE_FAILURE", fail)
	t.Setenv("RTK_UPGRADE_FAKE_ADDRESS", strings.TrimPrefix(healthURL, "http://"))
	sha := strings.Repeat("b", 40)
	tools := map[string]string{
		"git": "[ \"$RTK_UPGRADE_FAKE_FAILURE\" != git ] || exit 1; printf '%s' '" + sha + "'",
		"gh":  `case "$*" in *'/jobs?'*) printf '%s' '{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}';; *) printf '%s' '{"total_count":1,"workflow_runs":[{"id":1,"head_sha":"` + sha + `","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}' ;; esac`,
		"docker": `[ "$RTK_UPGRADE_FAKE_FAILURE" != docker ] || exit 1
case "$*" in
  *'image inspect'*) if [ "$RTK_UPGRADE_FAKE_FAILURE" = revision ]; then printf '{}'; else printf '%s' '{"org.opencontainers.image.revision":"` + sha + `"}'; fi ;;
  *'pg_isready'*) case "$*" in *'pg_isready -h 127.0.0.1'*) [ "$RTK_UPGRADE_FAKE_FAILURE" != postgres ] ;; *) exit 98 ;; esac ;;
  *'has_schema_privilege'*) if [ "$RTK_UPGRADE_FAKE_FAILURE" = privilege ]; then printf t; else printf f; fi ;;
  *'/internal/v1/health'*) if [ "$RTK_UPGRADE_FAKE_FAILURE" = http ]; then printf 'HTTP/1.1 503 Unavailable\r\n'; else printf 'HTTP/1.1 200 OK\r\n'; fi ;;
  inspect*) if [ "$RTK_UPGRADE_FAKE_FAILURE" = startup ]; then printf false; else printf true; fi ;;
  ps*) if [ "$RTK_UPGRADE_FAKE_FAILURE" = inventory ]; then printf 'unowned-id'; else printf '%s' 123456abcdef; fi ;;
  rm*) [ "$RTK_UPGRADE_FAKE_FAILURE" != cleanup ] ;;
  'network rm'*) [ "$RTK_UPGRADE_FAKE_FAILURE" != network ] ;;
  *) printf '%s' 123456abcdef ;;
esac`,
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeploymentImageUpgradeSimulatorPublishedArtifactStartupAndCleanup(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{true: "candidate crashes", false: "candidate healthy"}[failed], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/health" {
					t.Error("wrong probe")
				}
				w.WriteHeader(200)
			}))
			defer server.Close()
			fail := ""
			if failed {
				fail = "startup"
			}
			deploymentUpgradeFakeTools(t, server.URL, fail)
			err := verifyDeploymentSimulatorStartup(context.Background(), "selected-image")
			if (err != nil) != failed {
				t.Fatal(err)
			}
		})
	}
}

func TestDeploymentImageUpgradeVerifierCIAndSchemaEvidence(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing migration", false: "complete migration"}[missing], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			defer server.Close()
			deploymentUpgradeFakeTools(t, server.URL, "")
			target, image := deploymentUpgradeFixture(t)
			workspace := t.TempDir()
			dir := filepath.Join(workspace, "repos", "rtk_billing", "migrations")
			_ = os.MkdirAll(dir, 0700)
			_ = os.WriteFile(filepath.Join(dir, "001_base.sql"), []byte("-- fixture"), 0600)
			candidate := filepath.Join(workspace, "candidate.json")
			raw, _ := json.Marshal(target)
			_ = os.WriteFile(candidate, raw, 0600)
			store := makeIsolatedTestSecretStore(t, "staging")
			store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
			schema := "[]"
			if missing {
				schema = `["001_base.sql"]`
			}
			deploymentRuntimeTestKubectl(t, "case \"$*\" in *'psql'*) printf '%s' '"+schema+"';; *) printf '%s' '{\"items\":["+string(raw)+"]}' ;; esac")
			o := deploymentCheckOptions{phase: deploymentCheckPreDeploy, qualification: deploymentCredentialCheckOptions{images: []string{image}, manifests: []string{candidate}}}
			err := verifyDeploymentImageUpgrade(context.Background(), deploymentConfig{Environment: "staging", Workspace: workspace}, store, o)
			if (err != nil) != missing {
				t.Fatal(err)
			}
		})
	}
}

func TestDeploymentImageUpgradeOptionsAndFacadeOrdering(t *testing.T) {
	target, image := deploymentUpgradeFixture(t)
	p := filepath.Join(t.TempDir(), "candidate.json")
	raw, _ := json.Marshal(target)
	_ = os.WriteFile(p, raw, 0600)
	base := []string{"--environment", "staging", "--operation", "image-upgrade", "--phase", "pre-deploy", "--manifest", p, "--image", image}
	for _, extra := range [][]string{{"--fast"}, {"--checks", "dns"}, {"--operation", "typo"}} {
		if _, err := parseDeploymentCheckOptions(append(append([]string{}, base...), extra...), io.Discard); err == nil {
			t.Fatal("invalid image-upgrade inputs passed")
		}
	}
	for _, flags := range [][]string{{"--environment", "staging", "--operation", "image-upgrade"}, {"--environment", "staging", "--operation", "image-upgrade", "--image", image}} {
		if _, err := parseDeploymentCheckOptions(flags, io.Discard); err == nil {
			t.Fatal("incomplete release evidence passed")
		}
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "blocked", false: "ordered"}[fail], func(t *testing.T) {
			deps, _ := facadeFixture(t, true)
			collected := false
			called := false
			original := deps.collect
			deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, q deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
				collected = true
				return original(ctx, cfg, path, q, allow, emit)
			}
			deps.upgrade = func(context.Context, deploymentConfig, secretStore, deploymentCheckOptions) error {
				called = true
				if !collected {
					t.Fatal("candidate checked before full image pull")
				}
				return nil
			}
			if fail {
				deps.workloads = func(secretStore) error { return &deploymentRuntimeError{"KUBE_UNAVAILABLE", "simulator not ready"} }
			}
			err := runDeploymentCheckWithDependencies(context.Background(), base, io.Discard, io.Discard, deps)
			if (err != nil) != fail || called == fail {
				t.Fatalf("fail=%t called=%t err=%v", fail, called, err)
			}
		})
	}
}

func TestDeploymentImageUpgradeSharedPackageRequiresEveryConsumer(t *testing.T) {
	target, image := deploymentUpgradeFixture(t)
	raw, _ := json.Marshal(target)
	for _, foreign := range []bool{false, true} {
		var worker deploymentUpgradeTarget
		_ = json.Unmarshal(raw, &worker)
		worker.Metadata.Name = "billing-worker"
		if foreign {
			worker.Metadata.Namespace = "video-cloud-dev-billing"
		}
		err := validateDeploymentUpgradeTargets([]deploymentUpgradeTarget{target}, []deploymentUpgradeTarget{target, worker}, []string{image}, deploymentCheckPreDeploy, "staging")
		if (err == nil) != foreign {
			t.Fatalf("foreign=%t: %v", foreign, err)
		}
		if !foreign {
			if err := validateDeploymentUpgradeTargets([]deploymentUpgradeTarget{target, worker}, []deploymentUpgradeTarget{target, worker}, []string{image}, deploymentCheckPreDeploy, "staging"); err != nil {
				t.Fatal(err)
			}
		}
	}
	pod := target.Spec["template"].(map[string]any)["spec"].(map[string]any)
	pod["initContainers"] = []any{map[string]any{"name": "wait", "image": "postgres:16"}}
	_, refs, err := deploymentUpgradeSpec(target.Spec)
	if err != nil || refs["initContainers/wait"] != "postgres:16" {
		t.Fatal(err)
	}
}

func TestDeploymentImageUpgradeEffectiveSimulatorMigrationGate(t *testing.T) {
	for _, name := range []string{"direct-false", "direct-true", "direct-case", "direct-valueFrom", "explicit-overrides", "secret-false", "secret-true", "secret-invalid-json", "secret-invalid-base64", "secret-missing", "secret-read-failure", "last-source-wins", "bad-source", "prefixed", "configmap", "unnamed", "renamed-container", "no-executable", "duplicate-executable", "unsupported-image", "absent"} {
		t.Run(name, func(t *testing.T) {
			target, _ := deploymentUpgradeFixture(t)
			app := target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
			store := makeIsolatedTestSecretStore(t, "staging")
			response := `{"data":{"BILLING_DB_MIGRATE_ON_STARTUP":"` + base64.StdEncoding.EncodeToString([]byte("false")) + `"}}`
			body := "printf '%s' '" + response + "'"
			pass := name == "direct-false" || name == "explicit-overrides" || name == "secret-false" || name == "renamed-container"
			if strings.HasPrefix(name, "secret-") || name == "last-source-wins" {
				delete(app, "env")
				app["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": "runtime"}}}
			}
			switch name {
			case "direct-true":
				app["env"] = []any{map[string]any{"name": "BILLING_DB_MIGRATE_ON_STARTUP", "value": "true"}}
			case "direct-case":
				app["env"] = []any{map[string]any{"name": "BILLING_DB_MIGRATE_ON_STARTUP", "value": "False"}}
			case "direct-valueFrom":
				app["env"] = []any{map[string]any{"name": "BILLING_DB_MIGRATE_ON_STARTUP", "valueFrom": map[string]any{}}}
			case "explicit-overrides":
				app["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "unsupported"}}}
			case "secret-true":
				body = `printf '%s' '{"data":{"BILLING_DB_MIGRATE_ON_STARTUP":"dHJ1ZQ=="}}'`
			case "secret-invalid-json":
				body = `printf '%s' 'private-sensitive-marker'`
			case "secret-invalid-base64":
				body = `printf '%s' '{"data":{"BILLING_DB_MIGRATE_ON_STARTUP":"!"}}'`
			case "secret-missing":
				body = `printf '%s' '{"data":{}}'`
			case "secret-read-failure":
				body = `printf '%s' 'private-sensitive-marker' >&2; exit 1`
			case "last-source-wins":
				app["envFrom"] = append(app["envFrom"].([]any), map[string]any{"secretRef": map[string]any{"name": "override"}})
				body = `case "$*" in *'secret override'*) printf '%s' '{"data":{"BILLING_DB_MIGRATE_ON_STARTUP":"dHJ1ZQ=="}}';; *) ` + body + `;; esac`
			case "bad-source":
				delete(app, "env")
				app["envFrom"] = []any{"broken"}
			case "prefixed":
				delete(app, "env")
				app["envFrom"] = []any{map[string]any{"prefix": "PREFIX_", "secretRef": map[string]any{"name": "runtime"}}}
			case "configmap":
				delete(app, "env")
				app["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "runtime"}}}
			case "unnamed":
				delete(app, "env")
				app["envFrom"] = []any{map[string]any{"secretRef": map[string]any{}}}
			case "renamed-container":
				app["name"] = "simulator"
			case "no-executable":
				delete(app, "command")
			case "duplicate-executable":
				pod := target.Spec["template"].(map[string]any)["spec"].(map[string]any)
				pod["containers"] = append(pod["containers"].([]any), app)
			case "unsupported-image":
				app["image"] = "unreviewed:latest"
			case "absent":
				delete(app, "env")
			}
			deploymentRuntimeTestKubectl(t, body)
			err := verifyDeploymentSimulatorMigrationSetting(store, target)
			if (err == nil) != pass {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "private-sensitive-marker") {
				t.Fatal("private response leaked")
			}
		})
	}
}

func TestDeploymentImageUpgradeStartupFailsClosedAndCleansFixtures(t *testing.T) {
	for _, failure := range []string{"privilege", "inventory", "cleanup", "network", "docker", "postgres", "http"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "http" {
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			deploymentUpgradeFakeTools(t, server.URL, failure)
			ctx := context.Background()
			cancel := func() {}
			if failure == "postgres" || failure == "http" {
				ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
			}
			defer cancel()
			if err := verifyDeploymentSimulatorStartup(ctx, "selected-image"); err == nil {
				t.Fatal("unqualified startup passed")
			}
		})
	}
}

func TestDeploymentImageUpgradePublicationFailsForUnavailableToolAndWrongRevision(t *testing.T) {
	_, image := deploymentUpgradeFixture(t)
	for _, failure := range []string{"git", "docker", "revision"} {
		t.Run(failure, func(t *testing.T) {
			deploymentUpgradeFakeTools(t, "http://127.0.0.1:1", failure)
			if err := verifyDeploymentUpgradeImageCI(context.Background(), t.TempDir(), image); err == nil {
				t.Fatal("unqualified image passed")
			}
		})
	}
}

func TestDeploymentImageUpgradeSchemaInventoryAndReadFailures(t *testing.T) {
	_, image := deploymentUpgradeFixture(t)
	for _, failure := range []string{"missing-directory", "empty", "read-failure", "invalid", "null", "complete"} {
		t.Run(failure, func(t *testing.T) {
			workspace := t.TempDir()
			dir := filepath.Join(workspace, "repos", "rtk_billing", "migrations")
			if failure != "missing-directory" {
				_ = os.MkdirAll(dir, 0700)
			}
			if failure != "empty" && failure != "missing-directory" {
				_ = os.WriteFile(filepath.Join(dir, "001_base.sql"), []byte("-- fixture"), 0600)
			}
			body := `printf '%s' '[]'`
			if failure == "read-failure" {
				body = `printf private-sensitive-marker >&2; exit 1`
			}
			if failure == "invalid" {
				body = `printf private-sensitive-marker`
			}
			if failure == "null" {
				body = `printf null`
			}
			deploymentRuntimeTestKubectl(t, body)
			err := verifyDeploymentUpgradeSchemas(makeIsolatedTestSecretStore(t, "staging"), workspace, []string{image, image})
			if (err == nil) != (failure == "complete") {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "private-sensitive-marker") {
				t.Fatal("private response leaked")
			}
		})
	}
}

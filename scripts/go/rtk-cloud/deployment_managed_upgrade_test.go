package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func managedUpgradeFixture(t *testing.T) (*managedUpgradePlan, []map[string]any) {
	t.Helper()
	const fixture = `{"kind":"Deployment","metadata":{"name":"certissuer","namespace":"video-cloud-staging-video-cloud","uid":"owner-1","resourceVersion":"42","labels":{"rtk.realtek.com/stack":"video-cloud-staging"}},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"certissuer"}},"template":{"spec":{"securityContext":{"runAsUser":1000},"containers":[{"name":"certissuer","image":"ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","command":["/app/certissuer"],"env":[{"name":"CERT_ISSUER_HOST_IDENTITY_STATE","value":"/state/identity.json"},{"name":"LOG_LEVEL","value":"info"}],"volumeMounts":[{"name":"identity","mountPath":"/state"}]}],"volumes":[{"name":"identity","persistentVolumeClaim":{"claimName":"identity"}}]}}},"status":{"availableReplicas":1}}`
	var live map[string]any
	if err := json.Unmarshal([]byte(fixture), &live); err != nil {
		t.Fatal(err)
	}
	var desired deploymentUpgradeTarget
	_ = json.Unmarshal([]byte(fixture), &desired)
	raw, _ := json.Marshal(desired.Spec)
	var baseline map[string]any
	_ = json.Unmarshal(raw, &baseline)
	image := certIssuerObjectString(certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(certIssuerObjectMap(desired.Spec["template"])["spec"])["containers"])[0])["image"])
	plan := &managedUpgradePlan{Version: 1, Environment: "staging", Stack: "video-cloud-staging", Policy: managedUpgradePolicy, Images: []string{image}, Workloads: []managedUpgradeWorkload{{UID: "owner-1", Baseline: baseline, Desired: desired}}}
	return plan, []map[string]any{live}
}

func TestManagedUpgradeAllowsFullDeclaredSettingsWithoutPKIRegression(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	plan.Workloads[0].Desired.Spec["replicas"] = float64(2)
	pod := certIssuerObjectMap(certIssuerObjectMap(plan.Workloads[0].Desired.Spec["template"])["spec"])
	c := certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])
	c["resources"] = map[string]any{"requests": map[string]any{"memory": "256Mi"}}
	env := certIssuerObjectList(c["env"])
	certIssuerObjectMap(env[1])["value"] = "debug"
	if err := validateManagedUpgradeInventory(plan, live); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(plan.Workloads[0].Desired.Spec, plan.Workloads[0].Baseline) {
		t.Fatal("fixture did not overwrite configuration")
	}
	replacement, err := managedUpgradeReplacement(plan.Workloads[0], live[0])
	if err != nil {
		t.Fatal(err)
	}
	if replacement["status"] != nil || certIssuerObjectMap(replacement["metadata"])["resourceVersion"] != "42" || !reflect.DeepEqual(replacement["spec"], plan.Workloads[0].Desired.Spec) {
		t.Fatal("replacement lost desired spec or API concurrency identity")
	}
	if live[0]["status"] == nil || reflect.DeepEqual(live[0]["spec"], replacement["spec"]) {
		t.Fatal("replacement mutated the rollback source")
	}
}

func TestManagedUpgradeRejectsIncompleteChangedAndForeignInventories(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*managedUpgradePlan, []map[string]any)
	}{
		{"missing workload", func(p *managedUpgradePlan, l []map[string]any) { p.Workloads = nil }},
		{"duplicate workload", func(p *managedUpgradePlan, l []map[string]any) { p.Workloads = append(p.Workloads, p.Workloads[0]) }},
		{"foreign owner", func(p *managedUpgradePlan, l []map[string]any) { p.Workloads[0].UID = "different" }},
		{"foreign stack", func(p *managedUpgradePlan, l []map[string]any) {
			certIssuerObjectMap(certIssuerObjectMap(l[0]["metadata"])["labels"])["rtk.realtek.com/stack"] = "other"
		}},
		{"deleting owner", func(p *managedUpgradePlan, l []map[string]any) {
			certIssuerObjectMap(l[0]["metadata"])["deletionTimestamp"] = "now"
		}},
		{"concurrent configuration", func(p *managedUpgradePlan, l []map[string]any) {
			certIssuerObjectMap(l[0]["spec"])["replicas"] = float64(3)
		}},
		{"unknown image", func(p *managedUpgradePlan, l []map[string]any) { p.Images = nil }},
		{"mutable release", func(p *managedUpgradePlan, l []map[string]any) {
			p.Images = []string{"ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud:main"}
		}},
		{"malformed pod", func(p *managedUpgradePlan, l []map[string]any) { delete(p.Workloads[0].Desired.Spec, "template") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, live := managedUpgradeFixture(t)
			test.change(plan, live)
			if validateManagedUpgradeInventory(plan, live) == nil {
				t.Fatal("unsafe full upgrade passed")
			}
		})
	}
}

func TestManagedUpgradeRetainsAllProviderControllers(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	var provider map[string]any
	_ = json.Unmarshal([]byte(`{"kind":"StatefulSet","metadata":{"name":"openbao","namespace":"video-cloud-staging-platform","uid":"provider-1"},"spec":{"replicas":1}}`), &provider)
	live = append(live, provider)
	if validateManagedUpgradeInventory(plan, live) == nil {
		t.Fatal("omitted provider controller passed")
	}
	var desired deploymentUpgradeTarget
	raw, _ := json.Marshal(provider)
	_ = json.Unmarshal(raw, &desired)
	plan.Retained = []managedUpgradeWorkload{{UID: "provider-1", Baseline: map[string]any{"replicas": float64(1)}, Desired: desired}}
	if err := validateManagedUpgradeInventory(plan, live); err != nil {
		t.Fatal(err)
	}
	plan.Retained[0].Desired.Spec["replicas"] = float64(2)
	if validateManagedUpgradeInventory(plan, live) == nil {
		t.Fatal("unqualified provider mutation passed")
	}
}

func TestManagedUpgradeCannotStripManagedIdentityOrCredentialBindings(t *testing.T) {
	for _, field := range []string{"volumes", "securityContext", "command", "env", "volumeMounts"} {
		t.Run(field, func(t *testing.T) {
			plan, live := managedUpgradeFixture(t)
			pod := certIssuerObjectMap(certIssuerObjectMap(plan.Workloads[0].Desired.Spec["template"])["spec"])
			if field == "volumes" || field == "securityContext" {
				delete(pod, field)
			} else {
				delete(certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0]), field)
			}
			if validateManagedUpgradeInventory(plan, live) == nil {
				t.Fatal("managed identity plumbing was discarded")
			}
		})
	}
}

func TestManagedUpgradePrivatePolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := secretStore{Root: filepath.Join(root, "staging"), Environment: "staging"}
	path := managedUpgradePath(store)
	if p, err := loadManagedUpgradePlan(store, "video-cloud-staging"); err != nil || p != nil {
		t.Fatalf("missing plan should select legacy behavior: %v", err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	plan, _ := managedUpgradeFixture(t)
	raw, _ := json.Marshal(plan)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadManagedUpgradePlan(store, plan.Stack)
	if err != nil || loaded.digest == "" {
		t.Fatalf("private exact plan rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func()
	}{
		{"wrong stack", func() {}},
		{"production", func() { store.Environment = "production" }},
		{"world readable", func() { _ = os.Chmod(path, 0o644) }},
		{"policy alone", func() {
			_ = os.WriteFile(path, []byte(`{"schema_version":1,"environment":"staging","stack":"video-cloud-staging","policy":"replace-managed-workloads"}`), 0o600)
		}},
		{"unknown field", func() { _ = os.WriteFile(path, append(raw[:len(raw)-1], []byte(`,"unsafe":true}`)...), 0o600) }},
		{"symlink", func() { _ = os.Remove(path); _ = os.Symlink(filepath.Join(root, "elsewhere"), path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.Environment = "staging"
			_ = os.Remove(path)
			_ = os.WriteFile(path, raw, 0o600)
			tc.change()
			stack := plan.Stack
			if tc.name == "wrong stack" {
				stack = "other"
			}
			if p, err := loadManagedUpgradePlan(store, stack); err == nil {
				t.Fatalf("invalid policy accepted: %v", p)
			}
		})
	}
}

func TestManagedUpgradeServerDryRunDoesNotPersistResources(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	body := filepath.Join(root, "body")
	kubectl := filepath.Join(root, "kubectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$MANAGED_CALLS\"\ncat > \"$MANAGED_BODY\"\n"
	_ = os.WriteFile(kubectl, []byte(script), 0o700)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("MANAGED_CALLS", calls)
	t.Setenv("MANAGED_BODY", body)
	if err := managedUpgradeDryRun(context.Background(), secretStore{Root: root}, plan, live); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(calls)
	if !strings.Contains(string(args), "replace --dry-run=server -f -") {
		t.Fatalf("preflight attempted a persisted write: %s", args)
	}
	raw, _ := os.ReadFile(body)
	if !bytes.Contains(raw, []byte(`"resourceVersion":"42"`)) || bytes.Contains(raw, []byte(`"status"`)) {
		t.Fatal("admission dry-run did not use exact guarded replacement")
	}
	_ = os.WriteFile(kubectl, []byte("#!/bin/sh\nexit 1\n"), 0o700)
	if managedUpgradeDryRun(context.Background(), secretStore{Root: root}, plan, live) == nil {
		t.Fatal("admission failure passed")
	}
}

func TestManagedUpgradePreparationIsPrivateCompleteAndNonMutating(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	root := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	raw, _ := json.Marshal(map[string]any{"items": live})
	data := filepath.Join(root, "inventory.json")
	_ = os.WriteFile(data, raw, 0o600)
	calls := filepath.Join(root, "calls")
	kubectl := filepath.Join(root, "kubectl")
	_ = os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MANAGED_CALLS\"\ncat \"$MANAGED_INVENTORY\"\n"), 0o700)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("MANAGED_CALLS", calls)
	t.Setenv("MANAGED_INVENTORY", data)
	image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud@sha256:" + strings.Repeat("b", 64)
	cfg := deploymentConfig{Adapter: "lke", Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": plan.Stack}}
	var out bytes.Buffer
	if err := prepareManagedUpgrade(cfg, []string{image}, &out); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore("", "staging")
	prepared, err := loadManagedUpgradePlan(store, plan.Stack)
	if err != nil || len(prepared.Workloads) != 1 || prepared.Images[0] != image {
		t.Fatalf("plan not prepared correctly: %v", err)
	}
	if !reflect.DeepEqual(prepared.Workloads[0].Baseline, plan.Workloads[0].Baseline) {
		t.Fatal("rollback source overwritten by candidate")
	}
	raw, _ = os.ReadFile(calls)
	if strings.Contains(string(raw), "replace") || strings.Contains(string(raw), "apply") || strings.Contains(string(raw), "delete") {
		t.Fatal("planning mutated cluster")
	}
	if prepareManagedUpgrade(cfg, nil, &out) == nil {
		t.Fatal("planning silently replaced a reviewed plan")
	}
	env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": plan.Stack, "RTK_MANAGED_UPGRADE_PLAN_SHA256": prepared.digest}
	if _, _, err := managedUpgradeStore(env); err != nil {
		t.Fatal(err)
	}
	path := managedUpgradePath(store)
	contents, _ := os.ReadFile(path)
	_ = os.WriteFile(path, append(contents, '\n'), 0o600)
	if _, _, err := managedUpgradeStore(env); err == nil {
		t.Fatal("changed plan passed digest fence")
	}
	_ = os.Remove(path)
	if _, _, err := managedUpgradeStore(env); err == nil {
		t.Fatal("deleted plan fell back to legacy rendering")
	}
	cfg.Environment = "production"
	if prepareManagedUpgrade(cfg, nil, &out) == nil {
		t.Fatal("production overwrite planning was permitted")
	}
}

func TestDefaultPreflightAutomaticallyQualifiesManagedPlan(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(map[bool]string{false: "standard", true: "fast cannot GO"}[fast], func(t *testing.T) {
			root := t.TempDir()
			store := secretStore{Root: filepath.Join(root, "staging"), Environment: "staging"}
			path := managedUpgradePath(store)
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			plan, _ := managedUpgradeFixture(t)
			raw, _ := json.Marshal(plan)
			_ = os.WriteFile(path, raw, 0o600)
			cfg := deploymentConfig{Environment: "staging", Workspace: root, Values: map[string]string{"CLOUD_STACK_NAME": plan.Stack}}
			preflightCalled := false
			imagesCollected := false
			deps := deploymentCheckDependencies{
				store:     func(string) (secretStore, error) { return store, nil },
				local:     func(secretStore) error { return nil },
				preflight: func(context.Context, deploymentConfig, io.Writer) error { preflightCalled = true; return nil },
				collect: func(_ context.Context, _ deploymentConfig, _ string, q deploymentCredentialCheckOptions, _ bool, _ func(deploymentCredentialCheck)) []deploymentCredentialCheck {
					imagesCollected = reflect.DeepEqual(q.images, plan.Images) && q.readOnly && !q.imageUpgrade
					return nil
				},
			}
			reporter := newDeploymentCheckReporter(io.Discard)
			executeDeploymentCheck(context.Background(), deploymentCheckOptions{phase: deploymentCheckPreDeploy, operation: "full-deployment", qualification: deploymentCredentialCheckOptions{fast: fast}}, cfg, deps, reporter)
			report := reporter.snapshot("staging", deploymentCheckPreDeploy, fast, true)
			if !imagesCollected {
				t.Fatal("default full preflight omitted exact plan images or became image-only")
			}
			if fast {
				if preflightCalled || report.Overall == "PASS" {
					t.Fatal("reduced qualification permitted managed overwrite")
				}
			} else {
				if !preflightCalled || report.Overall != "PASS" || !strings.Contains(reporter.scope, "full managed workload replacement") {
					t.Fatal("default full preflight did not consume the managed plan")
				}
			}
		})
	}
}

func TestManagedUpgradeReplacementSavesRollbackAndNeverRendersStaticPKI(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	store, _ := newSecretStore("", "staging")
	path := managedUpgradePath(store)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	plan.Workloads[0].Desired.Spec["replicas"] = float64(2)
	raw, _ := json.Marshal(plan)
	_ = os.WriteFile(path, raw, 0o600)
	plan, err := loadManagedUpgradePlan(store, plan.Stack)
	if err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	input := filepath.Join(root, "replacement.json")
	inventory := filepath.Join(root, "inventory.json")
	raw, _ = json.Marshal(map[string]any{"items": live})
	_ = os.WriteFile(inventory, raw, 0o600)
	object := filepath.Join(root, "object.json")
	raw, _ = json.Marshal(live[0])
	_ = os.WriteFile(object, raw, 0o600)
	kubectl := filepath.Join(root, "kubectl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$MANAGED_CALLS"
case "$*" in
 *'get deployments,statefulsets,daemonsets'*) cat "$MANAGED_INVENTORY" ;;
 *'get deployment certissuer'*) cat "$MANAGED_OBJECT" ;;
 *'replace -f -'*) cat > "$MANAGED_REPLACEMENT" ;;
 *'rollout status'*) exit 0 ;;
 *) exit 9 ;;
esac
`
	_ = os.WriteFile(kubectl, []byte(script), 0o700)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("MANAGED_CALLS", calls)
	t.Setenv("MANAGED_INVENTORY", inventory)
	t.Setenv("MANAGED_OBJECT", object)
	t.Setenv("MANAGED_REPLACEMENT", input)
	env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": plan.Stack, "RTK_MANAGED_UPGRADE_PLAN_SHA256": plan.digest}
	qualified := false
	qualify := func(context.Context, provisionPaths, map[string]string, secretStore, *managedUpgradePlan) error {
		qualified = true
		return nil
	}
	if err := deployManagedUpgradeWithQualification(provisionPaths{}, env, store, plan, qualify); err != nil {
		t.Fatal(err)
	}
	if !qualified {
		t.Fatal("deployment skipped its exact-plan gate")
	}
	raw, _ = os.ReadFile(calls)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "apply") || strings.Contains(string(raw), "delete") || strings.Contains(string(raw), "statefulset/") {
		t.Fatalf("legacy dependency rendering leaked into managed replacement: %s", raw)
	}
	raw, _ = os.ReadFile(input)
	var written map[string]any
	_ = json.Unmarshal(raw, &written)
	if written["kind"] != "Deployment" || certIssuerObjectMap(written["spec"])["replicas"] != float64(2) || certIssuerObjectMap(written["metadata"])["uid"] != "owner-1" {
		t.Fatal("full desired spec was not replaced with its owner fence")
	}
	backup := filepath.Join(store.Root, "deployment", "rollback", plan.digest, "workloads.json")
	raw, err = os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(raw, &saved)
	if len(saved.Items) != 1 || certIssuerObjectMap(saved.Items[0]["spec"])["replicas"] != float64(1) {
		t.Fatal("rollback did not retain the original complete spec")
	}
	info, _ := os.Stat(backup)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("rollback credentials are not private")
	}
	_ = os.Remove(calls)
	qualify = func(context.Context, provisionPaths, map[string]string, secretStore, *managedUpgradePlan) error {
		return errors.New("candidate is not ready")
	}
	if deployManagedUpgradeWithQualification(provisionPaths{}, env, store, plan, qualify) == nil {
		t.Fatal("failed readiness gate permitted replacement")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("failed qualification reached runtime mutations")
	}
}

func TestManagedUpgradeCannotExpandTargetedOperationScope(t *testing.T) {
	if !managedUpgradeSelected(provisionOptions{}) {
		t.Fatal("full deployment did not select managed upgrade")
	}
	for _, opts := range []provisionOptions{{videoOnly: true}, {loggerOnly: true}, {workloads: []string{"video-cloud"}}} {
		if managedUpgradeSelected(opts) {
			t.Fatal("targeted request was expanded to the full environment")
		}
	}
}

func TestManagedUpgradeDeploymentCredentialsCannotOverrideGitHubCISession(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "keyring login", true: "caller CI token"}[configured], func(t *testing.T) {
			for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR"} {
				t.Setenv(key, "")
				_ = os.Unsetenv(key)
			}
			if configured {
				t.Setenv("GH_TOKEN", "caller-ci-fixture")
				t.Setenv("GITHUB_TOKEN", "caller-actions-fixture")
				t.Setenv("GH_HOST", "github.example")
				t.Setenv("GH_CONFIG_DIR", "caller-config-fixture")
			}
			t.Setenv("LINODE_TOKEN", "unselected-provider-fixture")
			t.Setenv("GHCR_PULL_TOKEN", "unselected-pull-fixture")
			values := map[string]string{"GH_TOKEN": "stale-operator-fixture", "GITHUB_TOKEN": "operator-actions-fixture", "GH_HOST": "unselected-host", "GH_CONFIG_DIR": "unselected-config", "LINODE_TOKEN": "selected-provider-fixture", "GHCR_PULL_TOKEN": "selected-pull-fixture"}
			restore := installAllCredentialEnvironment(values)
			defer restore()
			if configured {
				if os.Getenv("GH_TOKEN") != "caller-ci-fixture" || os.Getenv("GITHUB_TOKEN") != "caller-actions-fixture" || os.Getenv("GH_HOST") != "github.example" || os.Getenv("GH_CONFIG_DIR") != "caller-config-fixture" {
					t.Fatal("canonical deployment profile replaced the authenticated GitHub CLI session")
				}
			} else {
				for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR"} {
					if _, ok := os.LookupEnv(key); ok {
						t.Fatal("deployment profile shadowed keyring GitHub login")
					}
				}
			}
			if os.Getenv("LINODE_TOKEN") != "selected-provider-fixture" || os.Getenv("GHCR_PULL_TOKEN") != "selected-pull-fixture" {
				t.Fatal("deployment stopped using its canonical provider/package credentials")
			}
			restore()
			if os.Getenv("LINODE_TOKEN") != "unselected-provider-fixture" || os.Getenv("GHCR_PULL_TOKEN") != "unselected-pull-fixture" {
				t.Fatal("deployment credential scope was not restored")
			}
			if values["GH_TOKEN"] != "stale-operator-fixture" {
				t.Fatal("private operator credential was mutated")
			}
		})
	}
}

func managedUpgradeReadFixture(t *testing.T, plan *managedUpgradePlan, live []map[string]any) secretStore {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	store, _ := newSecretStore("", "staging")
	_ = os.MkdirAll(filepath.Join(store.Root, "operator", "env"), 0o700)
	_ = os.MkdirAll(filepath.Dir(managedUpgradePath(store)), 0o700)
	raw, _ := json.Marshal(plan)
	_ = os.WriteFile(managedUpgradePath(store), raw, 0o600)
	inventory := filepath.Join(root, "inventory.json")
	raw, _ = json.Marshal(map[string]any{"items": live})
	_ = os.WriteFile(inventory, raw, 0o600)
	kubectl := filepath.Join(root, "kubectl")
	_ = os.WriteFile(kubectl, []byte("#!/bin/sh\ncat \"$MANAGED_INVENTORY\"\n"), 0o700)
	t.Setenv("MANAGED_INVENTORY", inventory)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	return store
}

func managedUpgradeQualificationFixture(t *testing.T, plan *managedUpgradePlan, failure string, calls *[]string) deploymentCheckDependencies {
	t.Helper()
	gate := func(name string) error {
		*calls = append(*calls, name)
		if name == failure {
			return errors.New("required gate failed: " + name)
		}
		return nil
	}
	return deploymentCheckDependencies{
		bindings:  func(secretStore) error { return gate("bindings") },
		runtime:   func(secretStore, time.Time) error { return gate("runtime") },
		workloads: func(secretStore) error { return gate("workloads") },
		public:    func(context.Context, deploymentConfig, secretStore) error { return gate("public") },
		preflight: func(context.Context, deploymentConfig, io.Writer) error { return gate("dryrun") },
		collect: func(_ context.Context, cfg deploymentConfig, _ string, q deploymentCredentialCheckOptions, repair bool, _ func(deploymentCredentialCheck)) []deploymentCredentialCheck {
			*calls = append(*calls, "pull")
			if cfg.Adapter != "lke" || repair || !q.readOnly || q.fast || q.imageUpgrade || !reflect.DeepEqual(q.images, plan.Images) || !q.selected["ghcr"] {
				t.Fatal("managed qualification changed scope, credentials or image depth")
			}
			results := []deploymentCredentialCheck{{ID: "credentials.profile", Required: true, Passed: true, Status: "PASS"}}
			if failure == "missing-profile" {
				results = nil
			}
			for _, image := range q.images {
				if failure == "missing-pull" {
					continue
				}
				check := deploymentCredentialCheck{ID: "provider.ghcr.image." + image, Required: true, Passed: true, Status: "PASS"}
				if failure == "pull" {
					check.Status = "FAIL"
					check.Passed = false
				}
				if failure == "pending-pull" {
					check.Status = "PENDING"
					check.Passed = false
				}
				if failure == "skipped-pull" {
					check.Status = "SKIPPED"
					check.Required = false
					check.Passed = false
				}
				results = append(results, check)
			}
			return results
		},
		upgrade: func(_ context.Context, _ deploymentConfig, _ secretStore, o deploymentCheckOptions) error {
			if o.operation != "full-deployment" || o.phase != deploymentCheckPreDeploy || o.qualification.imageUpgrade {
				t.Fatal("whole desired-state replacement was qualified as image-only")
			}
			return gate("publication-schema-startup")
		},
	}
}

func TestManagedUpgradeRequiresEveryOwnerAdmissionAndReleaseGate(t *testing.T) {
	for _, failure := range []string{"", "bindings", "runtime", "workloads", "public", "dryrun", "pull", "publication-schema-startup", "pending-pull", "skipped-pull", "missing-pull", "missing-profile"} {
		t.Run(firstNonEmpty(failure, "all required evidence"), func(t *testing.T) {
			plan, live := managedUpgradeFixture(t)
			store := managedUpgradeReadFixture(t, plan, live)
			var calls []string
			deps := managedUpgradeQualificationFixture(t, plan, failure, &calls)
			err := qualifyManagedUpgradeWithDependencies(context.Background(), provisionPaths{Workspace: t.TempDir()}, map[string]string{"CLOUD_STACK_NAME": plan.Stack}, store, plan, deps)
			if failure == "" {
				if err != nil || !reflect.DeepEqual(calls, []string{"bindings", "runtime", "workloads", "public", "dryrun", "pull", "publication-schema-startup"}) {
					t.Fatalf("full qualification skipped a gate: %v %v", err, calls)
				}
			} else {
				if err == nil {
					t.Fatal("required failure was treated as GO")
				}
				if failure != "publication-schema-startup" && strings.Contains(strings.Join(calls, ","), "publication-schema-startup") {
					t.Fatal("failed prerequisite proceeded to release qualification")
				}
			}
		})
	}
	plan, live := managedUpgradeFixture(t)
	store := managedUpgradeReadFixture(t, plan, live)
	plan.Workloads[0].UID = "different"
	var calls []string
	deps := managedUpgradeQualificationFixture(t, plan, "", &calls)
	if qualifyManagedUpgradeWithDependencies(context.Background(), provisionPaths{}, nil, store, plan, deps) == nil || len(calls) != 0 {
		t.Fatal("foreign owner reached prerequisite or release checks")
	}
}

func TestManagedUpgradePostDeployRequiresActualDesiredSpecsAndPKIHealth(t *testing.T) {
	for _, failure := range []string{"", "desired-spec", "bindings", "runtime", "workloads", "public", "missing-plan", "changed-owner"} {
		t.Run(firstNonEmpty(failure, "complete rollout"), func(t *testing.T) {
			plan, live := managedUpgradeFixture(t)
			plan.Workloads[0].Desired.Spec["replicas"] = float64(2)
			if failure != "desired-spec" {
				certIssuerObjectMap(live[0]["spec"])["replicas"] = float64(2)
			}
			store := managedUpgradeReadFixture(t, plan, live)
			loaded, err := loadManagedUpgradePlan(store, plan.Stack)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "missing-plan" {
				_ = os.Remove(managedUpgradePath(store))
			}
			if failure == "changed-owner" {
				certIssuerObjectMap(live[0]["metadata"])["uid"] = "different"
				raw, _ := json.Marshal(map[string]any{"items": live})
				_ = os.WriteFile(os.Getenv("MANAGED_INVENTORY"), raw, 0o600)
			}
			var calls []string
			deps := managedUpgradeQualificationFixture(t, plan, failure, &calls)
			env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": plan.Stack, "RTK_MANAGED_UPGRADE_PLAN_SHA256": loaded.digest}
			err = verifyManagedUpgradePostDeployWithDependencies(provisionPaths{}, env, deps)
			if failure == "" {
				if err != nil || !reflect.DeepEqual(calls, []string{"bindings", "runtime", "workloads", "public"}) {
					t.Fatalf("post-rollout checks incomplete: %v %v", err, calls)
				}
			} else {
				if err == nil {
					t.Fatal("partial rollout or failed health completed deployment")
				}
			}
		})
	}
}

func TestManagedUpgradeMainEntrypointsFailClosedAndKeepArtifactsAccurate(t *testing.T) {
	plan, live := managedUpgradeFixture(t)
	store := managedUpgradeReadFixture(t, plan, live)
	loaded, err := loadManagedUpgradePlan(store, plan.Stack)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": plan.Stack, "RTK_MANAGED_UPGRADE_PLAN_SHA256": loaded.digest}
	// A reviewed full entry reaches managed qualification, rather than the old
	// static identity renderer. Its missing actual catalog evidence blocks writes.
	err = lkeDeployWorkloads(provisionPaths{Workspace: t.TempDir()}, env, provisionOptions{})
	if err == nil || strings.Contains(err.Error(), "legacy whole-Deployment") {
		t.Fatalf("full entry used static PKI reconstruction or skipped its prerequisites: %v", err)
	}
	if err := lkeApplyBase(env, provisionOptions{}); err != nil {
		t.Fatal(err)
	}
	// Public artifacts describe actual candidate images, never unused templates
	// or private baseline/desired specifications.
	dir, err := writeLKEProvisionArtifacts(provisionPaths{ArtifactsDir: t.TempDir()}, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Digest string            `json:"managed_upgrade_plan_sha256"`
		Images map[string]string `json:"workload_images"`
	}
	_ = json.Unmarshal(raw, &summary)
	if summary.Digest != loaded.digest || len(summary.Images) != 1 {
		t.Fatal("artifacts did not describe the qualified plan")
	}
	for _, image := range summary.Images {
		if image != plan.Images[0] {
			t.Fatal("artifact used a legacy image")
		}
	}
	if bytes.Contains(raw, []byte("baseline_spec")) || bytes.Contains(raw, []byte("identity.json")) {
		t.Fatal("public artifacts exported private workload settings")
	}
	env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] = "changed"
	if _, err := writeLKEProvisionArtifacts(provisionPaths{ArtifactsDir: t.TempDir()}, env); err == nil {
		t.Fatal("changed plan generated successful deployment artifacts")
	}
	// Guarding provider retention must also apply in the complete provision graph.
	env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] = loaded.digest
	for _, step := range kubernetesProvisionSteps(lkeCloudProvider{}) {
		if step.Name == "ensure-lke-node-pool" && step.Enabled(provisionContext{Env: env, Opts: provisionOptions{mode: provisionMode{deploy: true}}}) {
			t.Fatal("managed full upgrade implicitly resized provider pools")
		}
	}
	_ = store
}

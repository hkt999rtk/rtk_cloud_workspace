package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOldOTAAPIImage = "ghcr.io/example/video-cloud-api@sha256:" + "aab6ac79cce0ce4b50c7babab0649fe489445188b0431a79d1f1d7e63b86b77c"
	testNewOTAAPIImage = "ghcr.io/example/video-cloud-api@sha256:" + "dda6fc79cce0ce4b50c7babab0649fe489445188b0431a79d1f1d7e63b86b77c"
	testOTAUpstream    = "http://video-cloud-otaservice.video-cloud-dev-video-cloud.svc.cluster.local:18084"
)

func otaCoreDeploymentFixture() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "video-cloud-api", "resourceVersion": "17", "generation": float64(1)},
		"spec": map[string]any{"replicas": float64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "sidecar", "image": "other@sha256:123"},
			map[string]any{"name": "app", "image": testOldOTAAPIImage, "env": []any{
				map[string]any{"name": "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "value": "true"},
				map[string]any{"name": "UNRELATED", "value": "keep"},
			}},
		}}}},
		"status": map[string]any{"observedGeneration": float64(1), "replicas": float64(2), "updatedReplicas": float64(2), "availableReplicas": float64(2), "readyReplicas": float64(2)},
	}
}

func applyOTACoreFixturePatch(t *testing.T, deployment map[string]any, patch string) {
	t.Helper()
	var operations []map[string]any
	if err := json.Unmarshal([]byte(patch), &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) < 5 || operations[0]["op"] != "test" || operations[0]["path"] != "/metadata/resourceVersion" || operations[1]["op"] != "test" || operations[2]["op"] != "test" {
		t.Fatalf("cutover patch lacks identity/concurrency guards: %+v", operations)
	}
	container := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
	env := container["env"].([]any)
	for _, op := range operations[3:] {
		path, _ := op["path"].(string)
		switch {
		case path == "/spec/template/spec/containers/1/image":
			container["image"] = op["value"]
		case path == "/spec/template/spec/containers/1/env/-":
			env = append(env, op["value"])
		case strings.HasPrefix(path, "/spec/template/spec/containers/1/env/") && strings.HasSuffix(path, "/value"):
			var index int
			if _, err := fmt.Sscanf(path, "/spec/template/spec/containers/1/env/%d/value", &index); err != nil {
				t.Fatal(err)
			}
			env[index].(map[string]any)["value"] = op["value"]
		default:
			t.Fatalf("unexpected cutover patch path: %s", path)
		}
	}
	container["env"] = env
	deployment["metadata"].(map[string]any)["resourceVersion"] = "18"
}

func TestOTACoreCutoverPatchIsScopedAndIdempotent(t *testing.T) {
	deployment := otaCoreDeploymentFixture()
	patch, ready, err := otaCoreCutoverPatch(deployment, testNewOTAAPIImage, testOTAUpstream)
	if err != nil || ready {
		t.Fatalf("patch = %q, ready=%t, err=%v", patch, ready, err)
	}
	if !strings.Contains(patch, `"path":"/spec/template/spec/containers/1/image"`) ||
		!strings.Contains(patch, `"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED"`) ||
		!strings.Contains(patch, testOTAUpstream) {
		t.Fatalf("patch did not select the app image and OTA settings: %s", patch)
	}
	applyOTACoreFixturePatch(t, deployment, patch)
	if updated, done, err := otaCoreCutoverPatch(deployment, testNewOTAAPIImage, testOTAUpstream); err != nil || !done || updated != "" {
		t.Fatalf("read-back = %q, %t, %v", updated, done, err)
	}
	containers := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if containers[0].(map[string]any)["name"] != "sidecar" || containers[1].(map[string]any)["env"].([]any)[1].(map[string]any)["value"] != "keep" {
		t.Fatal("OTA patch changed an unrelated container or setting")
	}
}

func TestOTACoreCutoverPatchRejectsUnsafeLiveState(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong deployment", func(d map[string]any) { d["metadata"].(map[string]any)["name"] = "other" }},
		{"no resource version", func(d map[string]any) { delete(d["metadata"].(map[string]any), "resourceVersion") }},
		{"missing app", func(d map[string]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)["name"] = "other"
		}},
		{"mutable old image", func(d map[string]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)["image"] = "latest"
		}},
		{"entitlement disabled", func(d map[string]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)["env"].([]any)[0].(map[string]any)["value"] = "false"
		}},
		{"unexpected upstream", func(d map[string]any) {
			app := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
			app["env"] = append(app["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_UPSTREAM_URL", "value": "http://other"})
		}},
		{"duplicate flag", func(d map[string]any) {
			app := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
			app["env"] = append(app["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "value": "true"})
		}},
		{"incomplete live cutover", func(d map[string]any) {
			app := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
			app["env"] = append(app["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"})
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			deployment := otaCoreDeploymentFixture()
			scenario.change(deployment)
			if _, _, err := otaCoreCutoverPatch(deployment, testNewOTAAPIImage, testOTAUpstream); err == nil {
				t.Fatal("unsafe core API state was accepted")
			}
		})
	}
}

type otaCoreCommandFixture struct {
	args       []string
	deployment map[string]any
	ops        otaCoreCutoverOps
	patches    int
	rollouts   int
	persisted  int
	fail       string
	pods       []map[string]any
}

func newOTACoreCommandFixture(t *testing.T) *otaCoreCommandFixture {
	return newOTACoreCommandFixtureForEnvironment(t, "dev")
}

func newOTACoreCommandFixtureForEnvironment(t *testing.T, environment string) *otaCoreCommandFixture {
	t.Helper()
	workspace := writeDeploymentFixture(t, environment, "lke")
	configRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(store.Root, "env", "stack.env"), strings.Join([]string{
		"CLOUD_ENV_NAME=" + environment, "CLOUD_PROVIDER=lke", "CLOUD_STACK_NAME=video-cloud-" + environment,
		"CLOUD_DNS_ROOT_DOMAIN=example.test", "CLOUD_REGION=us-sea", "",
	}, "\n"))
	for name, value := range map[string]string{
		"operator/env/LKE_VIDEO_CLOUD_IMAGE":                  testNewOTAAPIImage,
		"operator/env/LKE_OTA_SERVICE_REGISTRATION_ENABLED":   "true",
		"operator/env/LKE_OTA_SERVICE_EDGE_ENABLED":           "true",
		"operator/env/LKE_OTA_CORE_CUTOVER_ENABLED":           "false",
		"operator/env/LKE_OTA_REGISTRAR_REGISTRATION_ENABLED": "false",
		"operator/env/VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED":  "true",
	} {
		if err := store.write(name, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	f := &otaCoreCommandFixture{args: []string{"--workspace", workspace, "--environment", environment}, deployment: otaCoreDeploymentFixture()}
	f.ops = otaCoreCutoverOps{
		credentials: func(environment string) (func(), error) {
			if environment != store.Environment || f.fail == "credentials" {
				return nil, errors.New("operator credentials unavailable")
			}
			return func() {}, nil
		},
		stoppedRegistrar: func(map[string]string) error {
			if f.fail == "registrar" {
				return errors.New("old registrar running")
			}
			return nil
		},
		readyService: func(map[string]string) error {
			if f.fail == "service" {
				return errors.New("OTA service unavailable")
			}
			return nil
		},
		activeEdge: func(map[string]string) error {
			if f.fail == "edge" {
				return errors.New("mTLS edge unavailable")
			}
			return nil
		},
		get: func(namespace, kind, name string) (map[string]any, error) {
			if namespace != "video-cloud-"+environment+"-video-cloud" || kind != "deployment" || name != "video-cloud-api" || f.fail == "get" {
				return nil, errors.New("core API unavailable")
			}
			return f.deployment, nil
		},
		pods: func(namespace string) ([]map[string]any, error) {
			if namespace != "video-cloud-"+environment+"-video-cloud" || f.fail == "pods" {
				return nil, errors.New("core API Pods unavailable")
			}
			if f.pods != nil {
				return f.pods, nil
			}
			return otaCoreReadyFixturePods(t, f.deployment), nil
		},
		patch: func(namespace, patch string) error {
			if f.fail == "patch" {
				return errors.New("patch denied")
			}
			f.patches++
			if f.fail != "readback" {
				applyOTACoreFixturePatch(t, f.deployment, patch)
			}
			return nil
		},
		rollout: func(namespace string) error {
			f.rollouts++
			if f.fail == "rollout" {
				return errors.New("rollout failed")
			}
			return nil
		},
		persist: func(selected secretStore) error {
			if f.fail == "persist" {
				return errors.New("operator store unavailable")
			}
			if f.rollouts == 0 {
				return errors.New("cutover was persisted before rollout")
			}
			if _, ready, err := otaCoreCutoverPatch(f.deployment, testNewOTAAPIImage, "http://video-cloud-otaservice.video-cloud-"+environment+"-video-cloud.svc.cluster.local:18084"); err != nil || !ready {
				return errors.New("cutover was persisted before live read-back")
			}
			f.persisted++
			return selected.write("operator/env/LKE_OTA_CORE_CUTOVER_ENABLED", []byte("true"), true)
		},
	}
	return f
}

func TestOTACoreCutoverCommandPlanApplyReadbackAndFailures(t *testing.T) {
	f := newOTACoreCommandFixture(t)
	if err := runDeploymentWithOperations(append([]string{"ota-core-cutover"}, f.args...), deploymentOperations{}); err != nil {
		t.Fatalf("deployment dispatcher plan: %v", err)
	}
	if err := runDeploymentOTACoreCutoverWithOps(f.args, f.ops); err != nil || f.patches != 0 {
		t.Fatalf("plan mutated dev: %v, patches=%d", err, f.patches)
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--read-only"), f.ops); err == nil || f.patches != 0 {
		t.Fatalf("pre-cutover read-only result = %v", err)
	}
	apply := append(f.args, "--confirm", "video-cloud-dev")
	if err := runDeploymentOTACoreCutoverWithOps(apply, f.ops); err != nil || f.patches != 1 || f.rollouts != 1 || f.persisted != 1 {
		t.Fatalf("apply = %v, patches=%d, rollouts=%d, persisted=%d", err, f.patches, f.rollouts, f.persisted)
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--read-only"), f.ops); err != nil {
		t.Fatalf("post-cutover read-only result = %v", err)
	}
	if err := runDeploymentOTACoreCutoverWithOps(apply, f.ops); err != nil || f.patches != 1 || f.persisted != 1 {
		t.Fatalf("idempotent apply = %v, patches=%d, persisted=%d", err, f.patches, f.persisted)
	}
	for _, failure := range []string{"credentials", "registrar", "service", "edge", "get", "patch", "rollout", "readback", "pods", "persist"} {
		t.Run(failure, func(t *testing.T) {
			bad := newOTACoreCommandFixture(t)
			bad.fail = failure
			if err := runDeploymentOTACoreCutoverWithOps(append(bad.args, "--confirm", "video-cloud-dev"), bad.ops); err == nil {
				t.Fatal("failed cutover was reported successful")
			}
			if failure != "persist" && bad.persisted != 0 {
				t.Fatal("failed cutover updated operator state")
			}
			if failure == "persist" {
				if err := runDeploymentOTACoreCutoverWithOps(append(bad.args, "--read-only"), bad.ops); err == nil {
					t.Fatal("live cutover without persisted operator state passed read-only verification")
				}
			}
		})
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--confirm", "wrong"), f.ops); err == nil {
		t.Fatal("wrong stack confirmation was accepted")
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--read-only", "--confirm", "video-cloud-dev"), f.ops); err == nil {
		t.Fatal("read-only check accepted mutation confirmation")
	}
}

func TestOTACoreCutoverStagingUsesSelectedIdentityAndNamespace(t *testing.T) {
	f := newOTACoreCommandFixtureForEnvironment(t, "staging")
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--confirm", "video-cloud-dev"), f.ops); err == nil || f.patches != 0 {
		t.Fatalf("cross-environment confirmation was accepted: %v", err)
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--confirm", "video-cloud-staging"), f.ops); err != nil || f.patches != 1 || f.persisted != 1 {
		t.Fatalf("staging cutover: err=%v patches=%d persisted=%d", err, f.patches, f.persisted)
	}
	if err := runDeploymentOTACoreCutoverWithOps(append(f.args, "--read-only"), f.ops); err != nil {
		t.Fatal(err)
	}
}

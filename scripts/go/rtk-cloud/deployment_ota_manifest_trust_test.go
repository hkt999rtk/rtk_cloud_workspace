package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otaManifestTrustDeployment(image, trust string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"resourceVersion": "1234"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "otaservice", "image": image,
				"env": []any{map[string]any{"name": "OTHER", "value": "keep"},
					map[string]any{"name": "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON", "value": trust}},
			}},
		}}},
	}
}

func TestOTAManifestTrustPatchOnlyAddsPublicKeys(t *testing.T) {
	image := "ghcr.io/example/ota@sha256:" + strings.Repeat("a", 64)
	old := `{"first":"` + strings.Repeat("A", 43) + `="}`
	desired := `{"first":"` + strings.Repeat("A", 43) + `=","second":"` + strings.Repeat("B", 43) + `="}`
	deployment := otaManifestTrustDeployment(image, old)
	patch, already, err := otaManifestTrustPatch(deployment, image, desired)
	if err != nil || already {
		t.Fatalf("additive trust patch = %q, %t, %v", patch, already, err)
	}
	var operations []map[string]any
	if err := json.Unmarshal([]byte(patch), &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 5 || operations[0]["op"] != "test" || operations[0]["value"] != "1234" ||
		operations[1]["value"] != "otaservice" || operations[2]["value"] != image ||
		operations[3]["value"] != old || operations[4]["op"] != "replace" || operations[4]["value"] != desired ||
		operations[4]["path"] != "/spec/template/spec/containers/0/env/1/value" {
		t.Fatalf("OTA trust patch changed unrelated Deployment fields: %+v", operations)
	}
	deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)[1].(map[string]any)["value"] = desired
	patch, already, err = otaManifestTrustPatch(deployment, image, desired)
	if err != nil || !already || patch != "" {
		t.Fatalf("idempotent trust update = %q, %t, %v", patch, already, err)
	}
}

func TestOTAManifestTrustPatchRejectsLiveDriftAndKeyRemoval(t *testing.T) {
	image := "ghcr.io/example/ota@sha256:" + strings.Repeat("a", 64)
	old := `{"first":"` + strings.Repeat("A", 43) + `="}`
	for _, tc := range []struct {
		name    string
		desired string
		change  func(map[string]any)
	}{
		{"removed key", `{}`, nil},
		{"changed key", `{"first":"` + strings.Repeat("B", 43) + `="}`, nil},
		{"missing version", old, func(d map[string]any) { delete(d["metadata"].(map[string]any), "resourceVersion") }},
		{"wrong image", old, func(d map[string]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "other"
		}},
		{"missing trust", old, func(d map[string]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := otaManifestTrustDeployment(image, old)
			if tc.change != nil {
				tc.change(deployment)
			}
			if _, _, err := otaManifestTrustPatch(deployment, image, tc.desired); err == nil {
				t.Fatal("unsafe OTA manifest trust update was accepted")
			}
		})
	}
}

func TestOTAManifestTrustCommandPatchesOnlyLivePublicKey(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	old := `{"first":"` + strings.Repeat("aa", 32) + `"}`
	desired := `{"first":"` + strings.Repeat("aa", 32) + `","second":"` + strings.Repeat("bb", 32) + `"}`
	writeTestFile(t, filepath.Join(workspace, "cloud_env/dev/overrides/architecture.env"),
		"VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON="+desired+"\n")
	storeRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", storeRoot)
	writeTestFile(t, filepath.Join(storeRoot, "dev/env/stack.env"), strings.Join([]string{
		"CLOUD_ENV_NAME=dev", "CLOUD_PROVIDER=lke", "CLOUD_STACK_NAME=video-cloud-dev",
		"CLOUD_DNS_ROOT_DOMAIN=example.test", "CLOUD_REGION=us-sea", "",
	}, "\n"))
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "false")
	t.Setenv("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "false")
	t.Setenv("VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "true")
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	image := "ghcr.io/example/video-cloud-api@sha256:" + strings.Repeat("a", 64)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", image)
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}}]}]}`)
	logPath := fakeKubectl(t)
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := loadLKEImageEnv(workspace, filepath.Join(storeRoot, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	env := appendMap(root.Values, cfg.Values)
	env = appendMap(env, cfg.AdapterValues)
	env = appendMap(env, cfg.AdapterResolved)
	ingress := otaEdgeIngressFixture(env)
	patch, _, err := otaDeviceEdgePatch(env, ingress)
	if err != nil {
		t.Fatal(err)
	}
	var ingressOps []map[string]any
	if err := json.Unmarshal([]byte(patch), &ingressOps); err != nil {
		t.Fatal(err)
	}
	paths := otaEdgePaths(ingress)
	ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = append(paths, ingressOps[2]["value"])
	ingressJSON, _ := json.Marshal(ingress)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(ingressJSON))
	statePath := filepath.Join(t.TempDir(), "ota-deployment.json")
	afterPath := filepath.Join(t.TempDir(), "ota-deployment-after.json")
	beforeJSON, _ := json.Marshal(otaManifestTrustDeployment(image, old))
	afterJSON, _ := json.Marshal(otaManifestTrustDeployment(image, desired))
	writeTestFile(t, statePath, string(beforeJSON))
	writeTestFile(t, afterPath, string(afterJSON))
	t.Setenv("FAKE_OTA_DEPLOYMENT_JSON_FILE", statePath)
	t.Setenv("FAKE_OTA_DEPLOYMENT_AFTER_PATCH_JSON_FILE", afterPath)
	credentials := func(environment string) (func(), error) {
		if environment != "dev" {
			return nil, fmt.Errorf("unexpected environment %s", environment)
		}
		return func() {}, nil
	}
	args := []string{"--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev"}
	if err := runDeploymentWithOperations([]string{"ota-manifest-trust", "--workspace", workspace, "--environment", "dev"}, deploymentOperations{}); err != nil {
		t.Fatalf("read-only trust update plan: %v", err)
	}
	for _, invalid := range [][]string{
		{"--workspace", workspace},
		{"--workspace", workspace, "--environment", "dev", "unexpected"},
		{"--workspace", workspace, "--environment", "dev", "--unknown"},
		{"--workspace", workspace, "--environment", "dev", "--confirm", "another-stack"},
	} {
		if err := runDeploymentOTAManifestTrustWithCredentials(invalid, credentials); err == nil {
			t.Fatalf("invalid trust update arguments accepted: %v", invalid)
		}
	}
	if err := runDeploymentOTAManifestTrustWithCredentials(args, func(string) (func(), error) {
		return nil, fmt.Errorf("operator credential unavailable")
	}); err == nil || !strings.Contains(err.Error(), "operator credential unavailable") {
		t.Fatalf("missing operator credential was accepted: %v", err)
	}
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "ghcr.io/example/video-cloud-api:latest")
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err == nil {
		t.Fatal("mutable OTA Service image was accepted")
	}
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", image)
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "patch deployment video-cloud-otaservice --type=json") != 1 ||
		!strings.Contains(string(log), "rollout status deployment/video-cloud-otaservice") ||
		strings.Contains(string(log), "apply -f -") || strings.Contains(string(log), "patch deployment video-cloud-api") {
		t.Fatalf("OTA trust command changed more than its workload: %s", log)
	}
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err != nil {
		t.Fatalf("idempotent trust update: %v", err)
	}
	log, _ = os.ReadFile(logPath)
	if strings.Count(string(log), "patch deployment video-cloud-otaservice --type=json") != 1 {
		t.Fatal("idempotent update patched the Deployment again")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{}`)
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err == nil {
		t.Fatal("trust update ignored missing private OTA Service")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{}`)
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err == nil {
		t.Fatal("trust update ignored missing device mTLS route")
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(ingressJSON))
	unknownJSON, _ := json.Marshal(otaManifestTrustDeployment(image, `{"unknown":"`+strings.Repeat("cc", 32)+`"}`))
	writeTestFile(t, statePath, string(unknownJSON))
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err == nil {
		t.Fatal("trust update would remove an unreviewed live key")
	}
	writeTestFile(t, statePath, string(afterJSON))
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	if err := runDeploymentOTAManifestTrustWithCredentials(args, credentials); err == nil {
		t.Fatal("trust update ignored core cutover")
	}
}

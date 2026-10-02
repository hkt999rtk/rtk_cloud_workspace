package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOTAServiceRolloutAppliesOnlyIndependentServiceAfterHandoff(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	trustFile := filepath.Join(workspace, "cloud_env/dev/overrides/architecture.env")
	trustedValue := "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON={\"dev\":\"" + strings.Repeat("ab", 32) + "\"}\n"
	writeTestFile(t, trustFile, trustedValue)
	store := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store)
	// Canonical SecretStore has no generated stack.env; storage comes from the
	// selected plan and its matching operator endpoint.
	operatorDir := filepath.Join(store, "dev", "operator", "env")
	if err := os.MkdirAll(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"LINODE_OBJ_BUCKET":                  "unrelated-artifact-bucket",
		"LINODE_OBJ_ENDPOINT":                "https://artifact.example.test",
		"LINODE_TOKEN":                       "test-token",
		"LINODE_MEDIA_OBJ_ACCESS_KEY_ID":     "media-access",
		"LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "media-secret",
	} {
		if err := os.WriteFile(filepath.Join(operatorDir, key), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":%q,"s3_endpoint":"https://objects.example.test"}]}`, cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region)
		case "/object-storage/keys":
			_, _ = fmt.Fprintf(w, `{"data":[{"access_key":"media-access","bucket_access":[{"bucket_name":%q,"region":%q,"permissions":"read_write"}]}]}`, cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "false")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "true")
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "ghcr.io/example/video-cloud-api@sha256:"+strings.Repeat("a", 64))
	logPath := fakeKubectl(t)
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-reg","port":8443,"targetPort":"service-reg"}]}}`)
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "media-access", "AWS_SECRET_ACCESS_KEY": "media-secret",
	}))
	t.Setenv("FAKE_OTA_WORKERS_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "test"}))
	certificateFixture := setFakeLKEPlatformIdentitySecrets(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
	credentials := func(environment string) (func(), error) {
		if environment != "dev" {
			return nil, fmt.Errorf("wrong environment %s", environment)
		}
		return func() {}, nil
	}
	args := []string{"--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev"}
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(log)
	for _, want := range []string{
		"name: video-cloud-otaservice", "name: allow-video-cloud-api-otaservice",
		"name: allow-ota-billing", "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON",
		"value: \"https://objects.example.test\"", "value: \"" + cfg.Storage.RuntimeMedia.Bucket + "\"",
		"rollout status deployment/video-cloud-otaservice",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("scoped OTA rollout lacks %q", want)
		}
	}
	if strings.Contains(output, "kind: Deployment\nmetadata:\n  name: video-cloud-api\n") ||
		strings.Contains(output, "kind: Deployment\nmetadata:\n  name: video-cloud-logingester\n") {
		t.Fatal("scoped OTA rollout modified a PKI-managed core workload")
	}
	if got := strings.Count(output, "ARGS apply -f -"); got != 6 {
		t.Fatalf("scoped apply count = %d, want 6", got)
	}
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "artifact-access", "AWS_SECRET_ACCESS_KEY": "media-secret",
	}))
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), "do not match the selected media grant") {
		t.Fatalf("stale runtime Secret access key was accepted: %v", err)
	}
	logAfterMismatch, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logAfterMismatch), "ARGS apply -f -") != 6 {
		t.Fatal("stale storage access key changed Kubernetes resources")
	}
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "media-access", "AWS_SECRET_ACCESS_KEY": "artifact-secret",
	}))
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), "do not match the selected media grant") {
		t.Fatalf("stale runtime Secret credential was accepted: %v", err)
	}
	logAfterSecretMismatch, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logAfterSecretMismatch), "ARGS apply -f -") != 6 {
		t.Fatal("stale storage secret changed Kubernetes resources")
	}
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "media-access", "AWS_SECRET_ACCESS_KEY": "media-secret",
	}))
	// A desired flag cannot restart the legacy process during this handoff.
	t.Setenv("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "true")
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), "must be false") {
		t.Fatalf("dual registrar rollout was accepted: %v", err)
	}
	t.Setenv("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "false")
	for _, scenario := range []struct {
		name, key, value, want string
	}{
		{"registration disabled", "LKE_OTA_SERVICE_REGISTRATION_ENABLED", "false", "must be true"},
		{"edge premature", "LKE_OTA_SERVICE_EDGE_ENABLED", "true", "edge and core cutover"},
		{"core premature", "LKE_OTA_CORE_CUTOVER_ENABLED", "true", "edge and core cutover"},
		{"image mutable", "LKE_VIDEO_CLOUD_IMAGE", "ghcr.io/example/video-cloud-api:latest", "immutable"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			previous := os.Getenv(scenario.key)
			t.Setenv(scenario.key, scenario.value)
			if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("unsafe rollout accepted: %v", err)
			}
			t.Setenv(scenario.key, previous)
		})
	}
	for _, scenario := range []struct{ value, want string }{
		{"{}", "at least one configured"},
		{`{"dev":"bad"}`, "32-byte Ed25519"},
	} {
		writeTestFile(t, trustFile, "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON="+scenario.value+"\n")
		if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), scenario.want) {
			t.Fatalf("invalid manifest trust key accepted: %v", err)
		}
	}
	writeTestFile(t, trustFile, trustedValue)
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", "")
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), "registration endpoint") {
		t.Fatalf("missing private registration listener accepted: %v", err)
	}
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	if err := runDeploymentOTAServiceRolloutWithCredentials([]string{"--workspace", workspace, "--environment", "dev", "--confirm", "wrong-stack"}, credentials); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("wrong stack confirmation accepted: %v", err)
	}
	if err := runDeploymentOTAServiceRolloutWithCredentials([]string{"--workspace", workspace, "--environment", "dev"}, credentials); err != nil {
		t.Fatalf("read-only plan failed: %v", err)
	}
	if err := runDeploymentOTAServiceRolloutWithCredentials(args, func(string) (func(), error) {
		return nil, fmt.Errorf("credential store unavailable")
	}); err == nil || !strings.Contains(err.Error(), "credential store unavailable") {
		t.Fatalf("credential failure did not block rollout: %v", err)
	}
	if err := runDeploymentOTAServiceRolloutWithCredentials([]string{"--workspace", workspace}, credentials); err == nil || !strings.Contains(err.Error(), "--environment is required") {
		t.Fatalf("missing environment accepted: %v", err)
	}
	if err := runDeploymentOTAServiceRolloutWithCredentials([]string{"--workspace", filepath.Join(workspace, "missing"), "--environment", "dev"}, credentials); err == nil {
		t.Fatal("unresolved deployment configuration was accepted")
	}
	if err := runDeploymentOTAServiceRollout([]string{"--unknown-option"}); err == nil {
		t.Fatal("unknown rollout option was accepted")
	}
	if err := runDeploymentWithOperations([]string{"ota-service-rollout", "--unknown-option"}, deploymentOperations{}); err == nil {
		t.Fatal("deployment dispatcher did not route the OTA command")
	}
	t.Run("active image-only update", func(t *testing.T) {
		oldImage := "ghcr.io/example/video-cloud-api@sha256:" + strings.Repeat("b", 64)
		newImage := "ghcr.io/example/video-cloud-api@sha256:" + strings.Repeat("a", 64)
		for key, value := range map[string]string{
			"LKE_OTA_SERVICE_REGISTRATION_ENABLED":   "true",
			"LKE_OTA_SERVICE_EDGE_ENABLED":           "true",
			"LKE_OTA_CORE_CUTOVER_ENABLED":           "true",
			"LKE_OTA_REGISTRAR_REGISTRATION_ENABLED": "false",
			"VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED":  "true",
			"LKE_VIDEO_CLOUD_IMAGE":                  newImage,
		} {
			if err := os.WriteFile(filepath.Join(operatorDir, key), []byte(value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, value)
		}
		t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-reg","port":8443,"targetPort":"service-reg"}]}}`)
		t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
		t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}}]}]}`)
		withUID := func(raw, uid string) string {
			var secret map[string]any
			if err := json.Unmarshal([]byte(raw), &secret); err != nil {
				t.Fatal(err)
			}
			secret["metadata"] = map[string]any{"uid": uid}
			encoded, err := json.Marshal(secret)
			if err != nil {
				t.Fatal(err)
			}
			return string(encoded)
		}
		t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", withUID(otaTestSecretJSON(t, map[string]string{
			"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
			"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "media-access", "AWS_SECRET_ACCESS_KEY": "media-secret",
		}), "runtime-uid"))
		t.Setenv("FAKE_OTA_WORKERS_RUNTIME_SECRET_JSON", withUID(otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "test"}), "workers-uid"))
		identity := os.Getenv("FAKE_OTA_SERVICE_IDENTITY_SECRET_JSON")
		t.Setenv("FAKE_OTA_SERVICE_IDENTITY_SECRET_JSON", withUID(identity, "service-uid"))
		activeCfg, err := resolveDeploymentConfig(workspace, "dev", "")
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectedOTAEnvironment(activeCfg)
		if err != nil {
			t.Fatal(err)
		}
		secretStore, err := newSecretStore("", "dev")
		if err != nil {
			t.Fatal(err)
		}
		writeRecord := func(f lkePlatformCertificateFixture) {
			identity := f.identities["service:ota"]
			cert, certErr := kubernetesSecretBytes(identity, "client.crt")
			key, keyErr := kubernetesSecretBytes(identity, "client.key")
			ca, caErr := kubernetesSecretBytes(identity, "server-ca.crt")
			chain, chainErr := pemCertificates(cert)
			if certErr != nil || keyErr != nil || caErr != nil || chainErr != nil {
				t.Fatal("invalid test OTA identity fixture")
			}
			record := deploymentServiceIdentity{
				Version: 1, Environment: "dev", Stack: "video-cloud-dev", Subject: "service:ota",
				RootSHA256: certificateSHA256(f.issuer), CertificateChain: string(cert), PrivateKey: string(key),
				ServerCA: string(ca), Fingerprint: certificateSHA256(chain[0]), Source: "adopted",
			}
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := secretStore.write("pki/services/ota/identity.json", body, true); err != nil {
				t.Fatal(err)
			}
		}
		operatorValues, err := secretStore.readOperator()
		if err != nil {
			t.Fatal(err)
		}
		selected = appendMap(selected, operatorValues)
		if _, err := bindSelectedOTAStorage(activeCfg, secretStore, selected); err != nil {
			t.Fatal(err)
		}
		ingress := otaEdgeIngressFixture(selected)
		patch, _, err := otaDeviceEdgePatch(selected, ingress)
		if err != nil {
			t.Fatal(err)
		}
		var edgePatch []map[string]any
		if err := json.Unmarshal([]byte(patch), &edgePatch); err != nil {
			t.Fatal(err)
		}
		paths := otaEdgePaths(ingress)
		ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = append(paths, edgePatch[2]["value"])
		ingressJSON, _ := json.Marshal(ingress)
		t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(ingressJSON))
		manifest := lkeOTAServiceDeploymentManifest(selected)
		var manifestMap map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &manifestMap); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(manifestMap)
		var deployment map[string]any
		if err := json.Unmarshal(encoded, &deployment); err != nil {
			t.Fatal(err)
		}
		deployment["metadata"].(map[string]any)["resourceVersion"] = "17"
		deployment["metadata"].(map[string]any)["generation"] = float64(1)
		deployment["status"] = map[string]any{"observedGeneration": float64(1), "replicas": float64(1), "updatedReplicas": float64(1), "readyReplicas": float64(1), "availableReplicas": float64(1)}
		container := func(d map[string]any) map[string]any {
			return d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		}
		container(deployment)["image"] = oldImage
		clone := func(d map[string]any) map[string]any {
			body, _ := json.Marshal(d)
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		podList := func(d map[string]any) map[string]any {
			pod := clone(d["spec"].(map[string]any)["template"].(map[string]any))
			pod["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
			return map[string]any{"items": []any{pod}}
		}
		before := clone(deployment)
		after := clone(deployment)
		for _, scenario := range []struct {
			name, want string
			change     func(map[string]any)
		}{
			{"wrong runtime checksum", "runtime checksum", func(template map[string]any) {
				template["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = "stale"
			}},
			{"wrong manifest trust", "differs from selected runtime", func(template map[string]any) {
				variables := template["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
				for _, raw := range variables {
					variable := raw.(map[string]any)
					if variable["name"] == "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON" {
						variable["value"] = "{}"
					}
				}
			}},
			{"duplicate manifest trust", "exactly once", func(template map[string]any) {
				container := template["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
				variables := container["env"].([]any)
				for _, raw := range variables {
					if raw.(map[string]any)["name"] == "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON" {
						container["env"] = append(variables, clone(raw.(map[string]any)))
						break
					}
				}
			}},
			{"wrong Secret reference", "selected runtime Secret", func(template map[string]any) {
				variables := template["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
				for _, raw := range variables {
					variable := raw.(map[string]any)
					if variable["name"] == "AWS_ACCESS_KEY_ID" {
						variable["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)["name"] = "unrelated"
					}
				}
			}},
			{"missing identity mount", "identity mount", func(template map[string]any) {
				template["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["volumeMounts"] = []any{}
			}},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				template := clone(before["spec"].(map[string]any)["template"].(map[string]any))
				scenario.change(template)
				if err := otaServiceTemplateMatches(template, oldImage, selected); err == nil || !strings.Contains(err.Error(), scenario.want) {
					t.Fatalf("unsafe current OTA template accepted: %v", err)
				}
			})
		}
		stale := clone(before)
		stale["status"].(map[string]any)["observedGeneration"] = float64(0)
		currentPod := podList(before)["items"].([]any)[0].(map[string]any)
		if err := otaServiceCurrentReady(stale, []map[string]any{currentPod}, oldImage, selected); err == nil || !strings.Contains(err.Error(), "current singleton Recreate revision") {
			t.Fatalf("unobserved OTA Deployment generation accepted: %v", err)
		}
		unready := clone(before)
		unready["status"].(map[string]any)["readyReplicas"] = float64(0)
		if err := otaServiceCurrentReady(unready, []map[string]any{currentPod}, oldImage, selected); err == nil || !strings.Contains(err.Error(), "readyReplicas") {
			t.Fatalf("OTA Deployment without a ready replica accepted: %v", err)
		}
		if err := otaServiceCurrentReady(before, []map[string]any{currentPod, clone(currentPod)}, oldImage, selected); err == nil || !strings.Contains(err.Error(), "exactly one current live Pod") {
			t.Fatalf("overlapping OTA Service Pods accepted: %v", err)
		}
		versionless := clone(before)
		delete(versionless["metadata"].(map[string]any), "resourceVersion")
		if _, _, _, err := otaServiceImagePatch(versionless, newImage); err == nil || !strings.Contains(err.Error(), "resourceVersion") {
			t.Fatalf("OTA image update without resourceVersion CAS accepted: %v", err)
		}
		container(after)["image"] = newImage
		after["metadata"].(map[string]any)["resourceVersion"] = "18"
		after["metadata"].(map[string]any)["generation"] = float64(2)
		after["status"].(map[string]any)["observedGeneration"] = float64(2)
		writeState := func(path string, value any) {
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, path, string(body))
		}
		core := otaCoreDeploymentFixture()
		coreApp := core["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
		coreApp["image"] = oldImage
		corePatch, ready, err := otaCoreCutoverPatch(core, oldImage, testOTAUpstream)
		if err != nil || ready {
			t.Fatalf("prepare live core cutover fixture: %v, ready=%t", err, ready)
		}
		applyOTACoreFixturePatch(t, core, corePatch)
		corePath := filepath.Join(t.TempDir(), "core.json")
		corePodsPath := filepath.Join(t.TempDir(), "core-pods.json")
		writeState(corePath, core)
		writeState(corePodsPath, map[string]any{"items": otaCoreReadyFixturePods(t, core)})
		t.Setenv("FAKE_OTA_CORE_DEPLOYMENT_JSON_FILE", corePath)
		t.Setenv("FAKE_OTA_CORE_PODS_JSON_FILE", corePodsPath)
		statePath := filepath.Join(t.TempDir(), "deployment.json")
		afterPath := filepath.Join(t.TempDir(), "deployment-after.json")
		podsPath := filepath.Join(t.TempDir(), "pods.json")
		podsAfterPath := filepath.Join(t.TempDir(), "pods-after.json")
		writeState(statePath, before)
		writeState(afterPath, after)
		writeState(podsPath, podList(before))
		writeState(podsAfterPath, podList(after))
		t.Setenv("FAKE_OTA_DEPLOYMENT_JSON_FILE", statePath)
		t.Setenv("FAKE_OTA_DEPLOYMENT_AFTER_PATCH_JSON_FILE", afterPath)
		t.Setenv("FAKE_OTA_PODS_JSON_FILE", podsPath)
		t.Setenv("FAKE_OTA_PODS_AFTER_PATCH_JSON_FILE", podsAfterPath)
		updateArgs := append(append([]string{}, args...), "--update-image")
		readOnlyArgs := []string{"--workspace", workspace, "--environment", "dev", "--update-image", "--read-only"}
		priorLog, _ := os.ReadFile(logPath)
		if err := runDeploymentOTAServiceRolloutWithCredentials(readOnlyArgs, credentials); err == nil || !strings.Contains(err.Error(), "identity record is unavailable") {
			t.Fatalf("missing canonical OTA identity record was accepted: %v", err)
		}
		writeRecord(newLKEPlatformCertificateFixture(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}))
		if err := runDeploymentOTAServiceRolloutWithCredentials(readOnlyArgs, credentials); err == nil || !strings.Contains(err.Error(), "differs from the selected environment record") {
			t.Fatalf("different valid canonical OTA identity was accepted: %v", err)
		}
		writeRecord(certificateFixture)
		if err := runDeploymentOTAServiceRolloutWithCredentials(readOnlyArgs, credentials); err != nil {
			t.Fatalf("active read-only preflight: %v", err)
		}
		readOnlyLog, _ := os.ReadFile(logPath)
		if strings.Count(string(readOnlyLog), "patch deployment video-cloud-otaservice --type=json") != strings.Count(string(priorLog), "patch deployment video-cloud-otaservice --type=json") || strings.Count(string(readOnlyLog), "rollout status deployment/video-cloud-otaservice") != strings.Count(string(priorLog), "rollout status deployment/video-cloud-otaservice") {
			t.Fatal("read-only image qualification patched or waited on the Deployment")
		}
		if err := runDeploymentOTAServiceRolloutWithCredentials(append(readOnlyArgs, "--confirm", "video-cloud-dev"), credentials); err == nil || !strings.Contains(err.Error(), "cannot accompany") {
			t.Fatalf("read-only accepted confirmation: %v", err)
		}
		wrongCore := clone(core)
		wrongCoreApp := wrongCore["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[1].(map[string]any)
		for _, raw := range wrongCoreApp["env"].([]any) {
			variable := raw.(map[string]any)
			if variable["name"] == "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED" {
				variable["value"] = "false"
			}
		}
		writeState(corePath, wrongCore)
		if err := runDeploymentOTAServiceRolloutWithCredentials(readOnlyArgs, credentials); err == nil || !strings.Contains(err.Error(), "has not cut over") {
			t.Fatalf("live core without cutover was accepted: %v", err)
		}
		writeState(corePath, core)
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err == nil || !strings.Contains(err.Error(), "must reach the selected immutable image") {
			t.Fatalf("OTA update accepted core before its selected image: %v", err)
		}
		coreApp["image"] = newImage
		core["metadata"].(map[string]any)["generation"] = float64(2)
		core["status"].(map[string]any)["observedGeneration"] = float64(2)
		writeState(corePath, core)
		writeState(corePodsPath, map[string]any{"items": otaCoreReadyFixturePods(t, core)})
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err != nil {
			t.Fatalf("active image-only update: %v", err)
		}
		log, _ := os.ReadFile(logPath)
		if strings.Count(string(log), "patch deployment video-cloud-otaservice --type=json") != 1 || strings.Count(string(log), "ARGS apply -f -") != 6 || strings.Contains(string(log), "patch deployment video-cloud-api") {
			t.Fatalf("image update wrote outside the OTA Deployment: %s", log)
		}
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err != nil {
			t.Fatalf("active image no-op: %v", err)
		}
		log, _ = os.ReadFile(logPath)
		if strings.Count(string(log), "patch deployment video-cloud-otaservice --type=json") != 1 {
			t.Fatal("no-op image update issued another patch")
		}
		writeState(statePath, before)
		writeState(podsPath, podList(before))
		t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "false")
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err == nil || !strings.Contains(err.Error(), "canonical operator") {
			t.Fatalf("nonactive edge accepted: %v", err)
		}
		t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
		stalePods := podList(before)
		stalePods["items"].([]any)[0].(map[string]any)["status"].(map[string]any)["conditions"] = []any{}
		writeState(podsPath, stalePods)
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err == nil || !strings.Contains(err.Error(), "not Running and Ready") {
			t.Fatalf("unready OTA Pod accepted: %v", err)
		}
		writeState(podsPath, podList(before))
		t.Setenv("FAKE_OTA_PATCH_FAIL", "1")
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err == nil || !strings.Contains(err.Error(), "replace OTA Service image") {
			t.Fatalf("concurrent patch race accepted: %v", err)
		}
		t.Setenv("FAKE_OTA_PATCH_FAIL", "")
		driftedAfter := clone(after)
		container(driftedAfter)["env"].([]any)[0].(map[string]any)["value"] = "unexpected-setting"
		writeState(afterPath, driftedAfter)
		if err := runDeploymentOTAServiceRolloutWithCredentials(updateArgs, credentials); err == nil || !strings.Contains(err.Error(), "changed beyond the selected image") {
			t.Fatalf("unrelated Pod template edit was accepted after image patch: %v", err)
		}
		writeState(afterPath, after)
		live := clone(before)
		container(live)["image"] = "ghcr.io/example/video-cloud-api:mutable"
		if _, _, _, err := otaServiceImagePatch(live, newImage); err == nil {
			t.Fatal("mutable existing OTA image was accepted")
		}
		container(live)["image"] = "ghcr.io/example/unrelated@sha256:" + strings.Repeat("b", 64)
		if _, _, _, err := otaServiceImagePatch(live, newImage); err == nil || !strings.Contains(err.Error(), "repository") {
			t.Fatalf("unrelated OTA image repository was accepted: %v", err)
		}
		patch, already, old, err := otaServiceImagePatch(before, newImage)
		if err != nil || already || old != oldImage {
			t.Fatalf("CAS patch selection: %v, %t, %s", err, already, old)
		}
		var operations []map[string]any
		if err := json.Unmarshal([]byte(patch), &operations); err != nil {
			t.Fatal(err)
		}
		if len(operations) != 4 || operations[0]["op"] != "test" || operations[0]["path"] != "/metadata/resourceVersion" || operations[0]["value"] != "17" || operations[1]["op"] != "test" || operations[1]["value"] != "otaservice" || operations[2]["op"] != "test" || operations[2]["value"] != oldImage || operations[3]["op"] != "replace" || operations[3]["path"] != "/spec/template/spec/containers/0/image" {
			t.Fatalf("image patch lacks resourceVersion and old-image CAS: %+v", operations)
		}
	})
}

func TestOTAServiceRolloutRequiresOldRegistrarStopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\ncase \"$*\" in\n  *\"get deployment video-cloud-otaregistrar\"*) printf '%s\\n' \"$FAKE_REGISTRAR\" ;;\n  *\"get pods -l app.kubernetes.io/name=video-cloud-otaregistrar\"*) printf '%s\\n' \"$FAKE_REGISTRAR_PODS\" ;;\n  *) exit 2 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	for _, state := range []string{
		`{"spec":{"replicas":1},"status":{"replicas":1,"readyReplicas":1}}`,
		`{"spec":{"replicas":0},"status":{"replicas":1}}`,
		`{"spec":{"replicas":0},"status":{"readyReplicas":1}}`,
	} {
		t.Setenv("FAKE_REGISTRAR", state)
		if err := lkeRequireStoppedOTARegistrar(env); err == nil || !strings.Contains(err.Error(), "scaled to zero") {
			t.Fatalf("accepted overlapping registrar state %s: %v", state, err)
		}
	}
	t.Setenv("FAKE_REGISTRAR", `{"spec":{"replicas":0},"status":{}}`)
	t.Setenv("FAKE_REGISTRAR_PODS", "pod/video-cloud-otaregistrar-old")
	if err := lkeRequireStoppedOTARegistrar(env); err == nil || !strings.Contains(err.Error(), "still terminating") {
		t.Fatalf("accepted terminating registrar Pod: %v", err)
	}
	t.Setenv("FAKE_REGISTRAR_PODS", "")
	if err := lkeRequireStoppedOTARegistrar(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_REGISTRAR", "")
	if err := lkeRequireStoppedOTARegistrar(env); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOTAServiceRolloutAppliesOnlyIndependentServiceAfterHandoff(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	trustFile := filepath.Join(workspace, "cloud_env/dev/overrides/architecture.env")
	trustedValue := "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON={\"dev\":\"" + strings.Repeat("ab", 32) + "\"}\n"
	writeTestFile(t, trustFile, trustedValue)
	store := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store)
	writeTestFile(t, filepath.Join(store, "dev/env/stack.env"), strings.Join([]string{
		"CLOUD_ENV_NAME=dev", "CLOUD_PROVIDER=lke", "CLOUD_STACK_NAME=video-cloud-dev",
		"CLOUD_DNS_ROOT_DOMAIN=example.test", "CLOUD_REGION=us-sea",
		"VIDEO_CLOUD_BLOB_ENDPOINT=https://objects.example.test", "VIDEO_CLOUD_BLOB_REGION=us-sea",
		"VIDEO_CLOUD_BLOB_BUCKET=firmware", "",
	}, "\n"))
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
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
	}))
	t.Setenv("FAKE_OTA_WORKERS_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "test"}))
	setFakeLKEPlatformIdentitySecrets(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
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

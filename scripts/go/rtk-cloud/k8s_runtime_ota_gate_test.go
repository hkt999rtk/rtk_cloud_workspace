package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOTAProvisionRequiresReadyCutoverReceiptBeforeMutation(t *testing.T) {
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "")
	root := t.TempDir()
	ctx := provisionContext{
		Paths: provisionPaths{EnvRoot: root},
		Env: map[string]string{
			"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev",
			"LKE_OTA_SERVICE_REGISTRATION_ENABLED": "true", "VIDEO_CLOUD_OTA_STORAGE_MODE": "dedicated",
			"VIDEO_CLOUD_OTA_BLOB_BUCKET": "rtk-ota-firmware-dev-us-sea", "VIDEO_CLOUD_OTA_BLOB_REGION": "us-sea",
			"VIDEO_CLOUD_OTA_BLOB_PREFIX": "environments/video-cloud-dev",
		},
		Opts: provisionOptions{mode: provisionMode{deploy: true}},
	}
	if err := runKubernetesProvision(lkeCloudProvider{}, ctx); err == nil || !strings.Contains(err.Error(), "storage-cutover-ota receipt") {
		t.Fatalf("normal deploy bypassed OTA cutover: %v", err)
	}
	path := filepath.Join(root, "state", "storage-cutover-ota.json")
	receipt := map[string]any{
		"environment": "dev", "bucket": "rtk-ota-firmware-dev-us-sea", "region": "us-sea",
		"prefix": "environments/video-cloud-dev", "cutover_at": time.Now().UTC().Format(time.RFC3339),
		"rollback_credentials_retained": true, "service_ready": true,
	}
	write := func() {
		t.Helper()
		if err := writeStorageState(path, receipt); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"wrong bucket", "bucket", "other"},
		{"wrong region", "region", "sg-sin-2"},
		{"wrong prefix", "prefix", "other"},
		{"missing readiness", "service_ready", false},
		{"invalid time", "cutover_at", "not-a-time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := receipt[tc.key]
			receipt[tc.key] = tc.value
			write()
			if err := validateOTAProvisionCutoverReceipt(ctx); err == nil {
				t.Fatal("invalid OTA cutover receipt accepted")
			}
			receipt[tc.key] = previous
		})
	}
	write()
	if err := validateOTAProvisionCutoverReceipt(ctx); err != nil {
		t.Fatalf("completed matching OTA cutover rejected: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx.Opts.mode.deploy = false
	if err := validateOTAProvisionCutoverReceipt(ctx); err != nil {
		t.Fatalf("non-deploy operation required OTA cutover: %v", err)
	}
	ctx.Opts.mode.deploy = true
	ctx.Opts.workloads = []string{"billing"}
	if err := validateOTAProvisionCutoverReceipt(ctx); err != nil {
		t.Fatalf("unrelated workload required OTA cutover: %v", err)
	}
	ctx.Opts.workloads = nil
	ctx.Env["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "false"
	if err := validateOTAProvisionCutoverReceipt(ctx); err != nil {
		t.Fatalf("initial cluster setup without OTA registration required cutover: %v", err)
	}
}

func TestOTACDNSecretCheckRunsAtWorkloadDeployment(t *testing.T) {
	fakeKubectl(t)
	steps := kubernetesProvisionSteps(lkeCloudProvider{})
	indices := map[string]int{}
	var deployStep provisionStep
	for i, step := range steps {
		indices[step.Name] = i
		if step.Name == "deploy-workloads" {
			deployStep = step
		}
	}
	if indices["ensure-kube-access"] >= indices["deploy-workloads"] || indices["apply-base"] >= indices["deploy-workloads"] {
		t.Fatal("OTA CDN Secret check can run before Kubernetes access or namespace setup")
	}
	ctx := provisionContext{Env: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "VIDEO_CLOUD_OTA_CDN_BASE_URL": "https://firmware.example.test"}, Opts: provisionOptions{mode: provisionMode{deploy: true}}}
	if err := deployStep.Run(ctx); err == nil || !strings.Contains(err.Error(), "CDN runtime Secret") {
		t.Fatalf("OTA workload deployment bypassed CDN pairing check: %v", err)
	}
	delete(ctx.Env, "VIDEO_CLOUD_OTA_CDN_BASE_URL")
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": " \t"}))
	if err := deployStep.Run(ctx); err == nil || !strings.Contains(err.Error(), "without a CDN base URL") {
		t.Fatalf("OTA workload deployment accepted whitespace-only CDN key: %v", err)
	}
}

func TestOTACDNURLIsValidatedBeforeProvisionSteps(t *testing.T) {
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	ctx := provisionContext{
		Paths: provisionPaths{EnvRoot: t.TempDir()},
		Env: map[string]string{
			"CLOUD_STACK_NAME": "video-cloud-dev", "VIDEO_CLOUD_OTA_CDN_BASE_URL": "http://firmware.example.test",
		},
		Opts: provisionOptions{mode: provisionMode{deploy: true}},
	}
	if err := runKubernetesProvision(lkeCloudProvider{}, ctx); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("invalid OTA CDN URL reached provision steps: %v", err)
	}
	ctx.Env["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = " \t"
	if err := runKubernetesProvision(lkeCloudProvider{}, ctx); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("whitespace-only OTA CDN URL reached provision steps: %v", err)
	}
}

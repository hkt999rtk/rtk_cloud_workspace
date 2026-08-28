package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesBlockedEvidenceForIncompleteEnvironment(t *testing.T) {
	disableProcessExit(t)
	outDir := filepath.Join(t.TempDir(), "out")
	err := run(t.TempDir(), t.TempDir(), "RTK", outDir, "smoke", 1, 0, 1, false, "summary", "", loadOptions{
		RunID: "run-incomplete", ShardCount: 1, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := readRunResult(t, outDir)
	if result["status"] != "BLOCKED" || len(result["blockers"].([]any)) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunSDKProfileBuildsCompleteSelectionAndEvidenceWithoutLiveProbe(t *testing.T) {
	disableProcessExit(t)
	envRoot := t.TempDir()
	writeRunFixture(t, envRoot)
	outDir := filepath.Join(t.TempDir(), "out")
	err := run(envRoot, envRoot, "RTK", outDir, "smoke", 1, 0, 7, false, "full", "", loadOptions{
		RunID: "run-sdk-selection", ShardCount: 1, Concurrency: 2, LoadModel: "sdk-device-simulator",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := readRunResult(t, outDir)
	if result["status"] != "BLOCKED" || result["overall"] != "blocked" {
		t.Fatalf("result = %#v", result)
	}
	metrics := result["metrics"].(map[string]any)
	if metrics["devices_selected"] != float64(3) || metrics["users_selected"] != float64(3) {
		t.Fatalf("metrics = %#v", metrics)
	}
	mqtt := result["mqtt"].(map[string]any)
	if mqtt["client_identities_checked"] != float64(3) || mqtt["probe_result"] != "NOT_RUN" {
		t.Fatalf("mqtt = %#v", mqtt)
	}
}

func TestRunOTAProfileWritesFailureEvidenceWhenMQTTReadyBarrierFails(t *testing.T) {
	disableProcessExit(t)
	envRoot := t.TempDir()
	writeRunFixture(t, envRoot)
	outDir := filepath.Join(t.TempDir(), "out")
	ota := defaultOTAOptions()
	ota.CampaignID, ota.TargetVersion, ota.CurrentVersion, ota.HardwareRevision = "campaign-1", "2.0.0", "1.0.0", "rev-a"
	err := run(envRoot, envRoot, "RTK", outDir, "smoke", 1, 0, 7, true, "full", "", loadOptions{
		RunID: "run-ota-barrier", ShardCount: 1, Concurrency: 2, LoadModel: "ota-device-simulator", OTA: ota,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := readRunResult(t, outDir)
	if result["status"] != "FAIL" || result["overall"] != "fail" {
		t.Fatalf("result = %#v", result)
	}
	otaResult := result["ota"].(map[string]any)
	if otaResult["devices_selected"] != float64(3) || otaResult["mqtt_ready"] != float64(0) || otaResult["unexpected_failures"] != float64(3) {
		t.Fatalf("OTA result = %#v", otaResult)
	}
	if _, err := os.Stat(filepath.Join(outDir, "ota-devices.jsonl")); err != nil {
		t.Fatalf("OTA device evidence: %v", err)
	}
}

func disableProcessExit(t *testing.T) {
	t.Helper()
	previous := exitProcess
	exitProcess = func(int) {}
	t.Cleanup(func() { exitProcess = previous })
}

func writeRunFixture(t *testing.T, envRoot string) {
	t.Helper()
	files := map[string]string{
		"env/stack.env": "ACCOUNT_MANAGER_DOMAIN=account.example.test\nVIDEO_CLOUD_DOMAIN=video.example.test\n",
		"services/account-manager/account-manager.env":     "ACCOUNT_MANAGER_DOMAIN=account.example.test\n",
		"services/video-cloud/video-cloud.env":             "VIDEO_CLOUD_BASE_URL=https://video.example.test\n",
		"state/video-cloud-staging.state.json":             `{"mqtt":{"host":"mqtt.example.test","tls_port":8883}}`,
		"devices/test_device/loadtest.env":                 "MQTT_HOST=mqtt.example.test\nMQTT_PORT=8883\n",
		"devices/test_device/manifests/device_ids.txt":     "device-light\ndevice-air\ndevice-meter\n",
		"devices/test_device/certs/device-light.crt":       "certificate",
		"devices/test_device/certs/device-light.key":       "key",
		"devices/test_device/certs/device-light-chain.crt": "chain",
		"devices/test_device/certs/device-air.crt":         "certificate",
		"devices/test_device/certs/device-air.key":         "key",
		"devices/test_device/certs/device-air-chain.crt":   "chain",
		"devices/test_device/certs/device-meter.crt":       "certificate",
		"devices/test_device/certs/device-meter.key":       "key",
		"devices/test_device/certs/device-meter-chain.crt": "chain",
	}
	for relative, contents := range files {
		path := filepath.Join(envRoot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	users := userArtifact{
		Brandname: "RTK", BrandCloudID: "brand-001", TenantSlug: "rtk",
		Users: []userCredential{
			{Email: "light@example.test", Password: "pw"},
			{Email: "air@example.test", Password: "pw"},
			{Email: "meter@example.test", Password: "pw"},
		},
	}
	bind := bindArtifact{
		Brandname: "RTK", BrandCloudID: "brand-001", TenantSlug: "rtk",
		Assignments: []assignment{
			{AssignedEmail: "light@example.test", DeviceID: "device-light", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
			{AssignedEmail: "air@example.test", DeviceID: "device-air", DeviceType: "air_conditioner", ServiceOptions: []string{"mqtt"}},
			{AssignedEmail: "meter@example.test", DeviceID: "device-meter", DeviceType: "smart_meter", ServiceOptions: []string{"mqtt"}},
		},
	}
	manifest := []manifestRecord{
		{DeviceID: "device-light", DeviceType: "light", CertificatePath: "certs/device-light.crt", KeyPath: "certs/device-light.key", CertificateChainPath: "certs/device-light-chain.crt"},
		{DeviceID: "device-air", DeviceType: "air_conditioner", CertificatePath: "certs/device-air.crt", KeyPath: "certs/device-air.key", CertificateChainPath: "certs/device-air-chain.crt"},
		{DeviceID: "device-meter", DeviceType: "smart_meter", CertificatePath: "certs/device-meter.crt", KeyPath: "certs/device-meter.key", CertificateChainPath: "certs/device-meter-chain.crt"},
	}
	writeRunJSON(t, filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260724.json"), users)
	writeRunJSON(t, filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260724.json"), bind)
	writeRunJSON(t, filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"), manifest)
}

func writeRunJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRunResult(t *testing.T, outDir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

package accountvideosmoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanBlocksWhenRequiredInputsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		RunID:                 "test-run",
		ArtifactDir:           filepath.Join(dir, "artifacts"),
		AccountManagerBaseURL: "https://account-manager.example.test",
		VideoCloudBaseURL:     "https://video.example.test",
	}

	result := Plan(cfg)

	if result.Overall != StatusBlocked {
		t.Fatalf("expected blocked result, got %s", result.Overall)
	}
	if !result.HasStep("load_account_fixture", StatusBlocked) {
		t.Fatalf("expected missing account fixture to be blocked: %+v", result.Steps)
	}
	if !result.HasStep("load_device_certset", StatusBlocked) {
		t.Fatalf("expected missing device certset to be blocked: %+v", result.Steps)
	}
}

func TestRedactSensitiveMaterial(t *testing.T) {
	input := strings.Join([]string{
		"Authorization: Bearer abc.def.ghi",
		"claim_token=claim_secret_value",
		"PASSWORD=hunter2",
		"-----BEGIN PRIVATE KEY-----",
		"raw line",
		"-----END PRIVATE KEY-----",
		"-----BEGIN CERTIFICATE-----",
		"MIIBsecretcert",
		"-----END CERTIFICATE-----",
	}, "\n")

	got := Redact(input)
	for _, secret := range []string{"abc.def.ghi", "claim_secret_value", "hunter2", "MIIBsecretcert"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker in %q", got)
	}
}

func TestLoadDeviceCertsetSelectsFirstDeviceWithoutLeakingKey(t *testing.T) {
	dir := t.TempDir()
	deviceDir := filepath.Join(dir, "device-material", "device-001")
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device.key"), []byte("PRIVATE KEY SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device-chain.crt"), []byte("CERT CHAIN SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "factory-enroll-results.json"), []byte(`{
		"devices": [
			{"index": 1, "devid": "pk-test-device", "success": true}
		]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	certset, err := LoadDeviceCertset(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if certset.DeviceID != "pk-test-device" {
		t.Fatalf("unexpected device id %q", certset.DeviceID)
	}
	if strings.Contains(certset.Summary(), "PRIVATE KEY SECRET") || strings.Contains(certset.Summary(), "CERT CHAIN SECRET") {
		t.Fatalf("certset summary leaked key/cert material: %s", certset.Summary())
	}
}

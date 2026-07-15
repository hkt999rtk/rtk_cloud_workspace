package home100k

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateE2EFixtureUsesBrandPlanDistribution(t *testing.T) {
	out := t.TempDir()
	manifest, err := GenerateE2EFixture("../../scenarios/cloud-admin-e2e.json", "run-test-001", out, time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BrandCount != 2 || manifest.DeviceCount != 16 || manifest.UserCount != 4 {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, name := range []string{"brand-clouds.json", "brand-cloud-users.json", "brand-cloud-members.json", "devices.json", "operations.json", "service-logs.json", "prometheus-series.json", "sessions.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	var brands []E2EBrandCloud
	data, err := os.ReadFile(filepath.Join(out, "brand-clouds.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &brands); err != nil {
		t.Fatal(err)
	}
	if brands[0].Status != "active" || brands[1].Status != "disabled" {
		t.Fatalf("brand statuses = %q, %q", brands[0].Status, brands[1].Status)
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTestCatalogIsStableAndSorted(t *testing.T) {
	catalog := testCatalog{SchemaVersion: 2, Cases: []testCatalogCase{
		{ID: "UI-CA-ZETA-002", Title: "Zeta", Layer: "ui", Owner: "owner", Targets: []string{"mobile"}, Environments: []string{"local"}, Runner: "test-ui", Status: "active"},
		{ID: "E2E-SDK-AUTH-001", Title: "Auth", Layer: "e2e", Owner: "owner", Environments: []string{"staging"}, Runner: "test-e2e", Status: "active"},
	}}
	out := string(renderTestCatalog(catalog))
	if strings.Index(out, "E2E-SDK-AUTH-001") > strings.Index(out, "UI-CA-ZETA-002") {
		t.Fatalf("catalog is not sorted:\n%s", out)
	}
}

func TestTestCaseIDPattern(t *testing.T) {
	for _, id := range []string{"UI-CA-REPORT-001", "E2E-SDK-SHADOW-001", "LIVE-STG-ONBOARD-001", "LOAD-HOME-SHADOW-001", "SVC-AM-SUITE-001"} {
		if !testCaseIDPattern.MatchString(id) {
			t.Fatalf("expected %s to be valid", id)
		}
	}
	for _, id := range []string{"ui-ca-report-001", "UI-CA-1", "UI-CA-REPORT-0001"} {
		if testCaseIDPattern.MatchString(id) {
			t.Fatalf("expected %s to be invalid", id)
		}
	}
}

func TestExpectedUITestIDsAllowsPartialServiceCheckout(t *testing.T) {
	workspace := t.TempDir()
	uiSource := filepath.Join(workspace, "repos", "rtk_cloud_admin", "web", "e2e", "catalog.spec.mjs")
	if err := os.MkdirAll(filepath.Dir(uiSource), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiSource, []byte(`test('[UI-CA-SMOKE-001] renders dashboard', async () => {})`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := `schema_version: 2
cases:
  - id: UI-CA-SMOKE-001
    title: Renders dashboard
    layer: ui
    owner: rtk_cloud_admin
    source: repos/rtk_cloud_admin/web/e2e/catalog.spec.mjs
    selector: renders dashboard
    method: Headless browser
    runner: test-ui
    targets: [desktop]
    environments: [local]
    evidence: [screenshot]
    status: active
  - id: SVC-AM-SUITE-001
    title: Account Manager suite
    layer: service
    owner: rtk_account_manager
    source: repos/rtk_account_manager/go.mod
    selector: module
    method: Go tests
    runner: test-services
    environments: [ci]
    evidence: [junit]
    change_paths: [repos/rtk_account_manager/internal/**]
    status: active
`
	if err := os.WriteFile(filepath.Join(workspace, "tests", "catalog.yaml"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := expectedUITestIDs(workspace, "desktop", "local", false)
	if err != nil {
		t.Fatalf("UI catalog validation should not require service sources: %v", err)
	}
	if len(ids) != 1 || ids[0] != "UI-CA-SMOKE-001" {
		t.Fatalf("unexpected UI test IDs: %v", ids)
	}
	if _, err := loadAndValidateTestCatalog(workspace); err == nil {
		t.Fatal("full catalog validation should still require service sources")
	}
}

func TestCatalogGlobRegexpSupportsRecursivePaths(t *testing.T) {
	re, err := catalogGlobRegexp("repos/rtk_video_cloud/internal/deviceshadow/**")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("repos/rtk_video_cloud/internal/deviceshadow/service.go") {
		t.Fatal("recursive catalog glob did not match a nested source file")
	}
	if re.MatchString("repos/rtk_video_cloud/internal/mqtt/service.go") {
		t.Fatal("recursive catalog glob matched another package")
	}
}

func TestCatalogCoversRequiresMatchingFeature(t *testing.T) {
	catalog := testCatalog{SchemaVersion: 2, Cases: []testCatalogCase{
		{ID: "E2E-HOME-SHADOW-001", Layer: "e2e", Feature: "device-shadow", Status: "active"},
		{ID: "LOAD-HOME-SHADOW-001", Layer: "load", Feature: "video-webrtc", Status: "active", Covers: []string{"E2E-HOME-SHADOW-001"}},
	}}
	if err := validateCatalogRelationships(catalog); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected covers feature mismatch, got %v", err)
	}
}

func TestCatalogCoversRejectsUnknownCase(t *testing.T) {
	catalog := testCatalog{SchemaVersion: 2, Cases: []testCatalogCase{
		{ID: "LOAD-HOME-SHADOW-001", Layer: "load", Feature: "device-shadow", Status: "active", Covers: []string{"E2E-HOME-MISSING-001"}},
	}}
	if err := validateCatalogRelationships(catalog); err == nil || !strings.Contains(err.Error(), "unknown or inactive") {
		t.Fatalf("expected unknown covers error, got %v", err)
	}
}

func TestCatalogChangePathRejectsEmptyAndUnmatchedGlobs(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"add", "tracked.txt"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := validateCatalogChangePath(workspace, ""); err == nil {
		t.Fatal("empty change path must fail")
	}
	if err := validateCatalogChangePath(workspace, "missing/**"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unmatched change path error = %v", err)
	}
	if err := validateCatalogChangePath(workspace, "tracked.txt"); err != nil {
		t.Fatalf("tracked change path rejected: %v", err)
	}
}

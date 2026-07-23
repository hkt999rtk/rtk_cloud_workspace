package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTestCatalogIsStableAndSorted(t *testing.T) {
	catalog := testCatalog{SchemaVersion: 1, Cases: []testCatalogCase{
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
	catalog := `schema_version: 1
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

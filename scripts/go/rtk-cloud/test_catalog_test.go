package main

import (
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

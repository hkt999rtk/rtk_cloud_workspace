package main

import (
	"strings"
	"testing"
)

func TestQualificationGoTestStatusRequiresTopLevelPass(t *testing.T) {
	passed, skipped, err := qualificationGoTestStatus([]byte(strings.Join([]string{
		`{"Action":"run","Test":"TestIntegrationAuthorizationAndTenancyMatrix"}`,
		`{"Action":"pass","Test":"TestIntegrationAuthorizationAndTenancyMatrix/subtest"}`,
		`{"Action":"pass","Test":"TestIntegrationAuthorizationAndTenancyMatrix"}`,
	}, "\n")), "TestIntegrationAuthorizationAndTenancyMatrix")
	if err != nil || !passed || skipped {
		t.Fatalf("expected top-level pass, got passed=%t skipped=%t err=%v", passed, skipped, err)
	}
}

func TestQualificationGoTestStatusRejectsSkipAndMissingTerminal(t *testing.T) {
	passed, skipped, err := qualificationGoTestStatus(
		[]byte(`{"Action":"skip","Test":"TestACLSeedPermissionCatalogAndSystemRoles"}`),
		"TestACLSeedPermissionCatalogAndSystemRoles",
	)
	if err != nil || passed || !skipped {
		t.Fatalf("expected explicit skip, got passed=%t skipped=%t err=%v", passed, skipped, err)
	}
	if _, _, err := qualificationGoTestStatus(
		[]byte(`{"Action":"pass","Test":"another-test"}`),
		"TestACLSeedPermissionCatalogAndSystemRoles",
	); err == nil {
		t.Fatal("missing top-level terminal event was accepted")
	}
}

func TestAuthorizationQualificationRequiresPerRequirementAssertions(t *testing.T) {
	tc := testCatalogCase{
		ID: "INT-AM-AUTHZ-MATRIX-001",
		Verifies: []string{
			"REQ-CONTRACT-AUTHZ-ROUTES-001",
			"REQ-CONTRACT-AUTHZ-COMPAT-001",
		},
	}
	spec := authorizationQualificationSpec{
		TestID: "INT-AM-AUTHZ-MATRIX-001",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-ROUTES-001": {"cross_tenant_denial": "PASS"},
		},
	}
	if err := validateAuthorizationQualificationAssertions(tc, spec); err == nil ||
		!strings.Contains(err.Error(), "REQ-CONTRACT-AUTHZ-COMPAT-001") {
		t.Fatalf("missing per-requirement assertion accepted: %v", err)
	}
}

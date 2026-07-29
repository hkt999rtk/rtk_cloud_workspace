package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestRunAuthorizationQualificationEmitsPassingEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fakeGo := filepath.Join(binDir, "go")
	if err := os.WriteFile(fakeGo, []byte(`#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
name="${last#^}"
name="${name%\$}"
printf '{"Action":"pass","Test":"%s"}\n' "$name"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	outputDir := filepath.Join(t.TempDir(), "qualification")
	if err := runAuthorizationQualification(workspace, outputDir, ""); err != nil {
		t.Fatal(err)
	}

	var manifest featureEvidenceManifestV2
	raw, err := os.ReadFile(filepath.Join(outputDir, "feature-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != featureEvidenceSchemaV3 {
		t.Fatalf("schema=%q, want %q", manifest.SchemaVersion, featureEvidenceSchemaV3)
	}
	if len(manifest.Cases) != len(authorizationQualificationSpecs) {
		t.Fatalf("cases=%d, want %d", len(manifest.Cases), len(authorizationQualificationSpecs))
	}
	workflowCases := 0
	expectedWorkflowCases := 0
	for _, spec := range authorizationQualificationSpecs {
		if len(spec.Workflows) > 0 {
			expectedWorkflowCases++
		}
	}
	for _, evidenceCase := range manifest.Cases {
		if evidenceCase.Status != "PASS" || len(evidenceCase.Requirements) == 0 {
			t.Fatalf("incomplete evidence case: %+v", evidenceCase)
		}
		for _, requirement := range evidenceCase.Requirements {
			if requirement.Status != "PASS" || len(requirement.Assertions) == 0 || len(requirement.Evidence) != 2 {
				t.Fatalf("incomplete requirement evidence: %+v", requirement)
			}
		}
		if len(evidenceCase.Workflows) > 0 {
			workflowCases++
		}
		for _, workflow := range evidenceCase.Workflows {
			if workflow.Revision == "" || workflow.Status != "PASS" || len(workflow.Steps) == 0 {
				t.Fatalf("incomplete workflow evidence: %+v", workflow)
			}
		}
	}
	if workflowCases != expectedWorkflowCases {
		t.Fatalf("workflow cases=%d, want %d", workflowCases, expectedWorkflowCases)
	}

	junit, err := os.ReadFile(filepath.Join(outputDir, "junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(junit), `failures="0"`) {
		t.Fatalf("unexpected JUnit: %s", junit)
	}
}

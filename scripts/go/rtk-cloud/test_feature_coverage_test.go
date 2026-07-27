package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func featureCoverageFixture(t *testing.T) (string, testCatalog, featureCaseEvidenceV2, time.Time) {
	t.Helper()
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(t.TempDir(), "proof.json")
	content := []byte(`{"assertion":"passed"}`)
	if err := os.WriteFile(evidencePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	now := time.Now().UTC().Truncate(time.Second)
	requirement := testCatalogRequirement{
		ID: "REQ-E2E-TEST-FLOW-001", Title: "Product flow completes", AcceptanceLayer: "e2e",
		Gate: "pr", Environments: []string{"ci"}, Evidence: []string{"json"}, Status: "active",
	}
	catalog := testCatalog{
		SchemaVersion: 3,
		Features: []testCatalogFeature{{
			ID: "FEAT-TEST-FLOW-001", Title: "Test flow", Owner: "cloud_platform", Risk: "critical",
			CommitAnchors: []string{"workspace"}, Status: "active", Requirements: []testCatalogRequirement{requirement},
		}},
		Cases: []testCatalogCase{{
			ID: "E2E-TEST-FLOW-001", Title: "Flow", Layer: "e2e", Runner: "test-e2e",
			Environments: []string{"ci"}, Verifies: []string{requirement.ID}, Status: "active",
		}},
	}
	item := featureCaseEvidenceV2{
		TestID: "E2E-TEST-FLOW-001", Status: "PASS", Environment: "ci",
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339), CompletedAt: now.Format(time.RFC3339), WorkspaceCommit: strings.TrimSpace(commit),
		Requirements: []featureRequirementAssertion{{
			RequirementID: requirement.ID, Status: "PASS",
			Assertions: map[string]string{"product_flow": "PASS"},
			Evidence:   []featureCoverageEvidenceFile{{Path: evidencePath, SHA256: fmt.Sprintf("%x", sum)}},
		}},
	}
	return workspace, catalog, item, now
}

func TestFeatureCoverageDoesNotTreatCodeCoverageAsFeatureEvidence(t *testing.T) {
	workspace, catalog, _, now := featureCoverageFixture(t)
	report := assessFeatureCoverage(workspace, catalog, nil, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" || report.Pass != 0 || report.CodeCoverage != "SEPARATE_NOT_SCORED" {
		t.Fatalf("100%% code coverage must not cover missing product proof: %+v", report)
	}
}

func TestFeatureCoverageRequiresExplicitRequirementAssertion(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	item.Requirements = nil
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" || report.Requirements[0].Status != "MISSING" {
		t.Fatalf("case PASS without requirement assertion must fail: %+v", report)
	}
}

func TestFeatureCoverageRejectsUnitOnlyProof(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	catalog.Cases[0].Layer = "unit"
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" {
		t.Fatalf("unit proof must remain supporting-only: %+v", report)
	}
}

func TestFeatureCoveragePassesQualifiedRequirementEvidence(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "PASS" || report.Pass != 1 || report.Required != 1 {
		t.Fatalf("qualified product evidence should pass: %+v", report)
	}
}

func TestFeatureCoverageRejectsTamperedAndStaleEvidence(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	item.Requirements[0].Evidence[0].SHA256 = strings.Repeat("0", 64)
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" {
		t.Fatal("tampered evidence passed")
	}
	catalog.Features[0].Requirements[0].AcceptanceLayer = "live"
	catalog.Features[0].Requirements[0].Gate = "scheduled"
	catalog.Features[0].Requirements[0].FreshnessHours = 36
	item.Requirements[0].Evidence[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(`{"assertion":"passed"}`)))
	item.CompletedAt = now.Add(-37 * time.Hour).Format(time.RFC3339)
	report = assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "main", now)
	if report.Requirements[0].Status != "STALE" {
		t.Fatalf("stale scheduled evidence = %+v", report)
	}
}

func TestGovernedProductSurfaceClassification(t *testing.T) {
	for _, path := range []string{
		"repos/rtk_account_manager/internal/http/signup.go",
		"repos/rtk_cloud_admin/web/src/routes.ts",
		"repos/rtk_cloud_client/packages/javascript/src/index.ts",
		"repos/rtk_cloud_frontend/internal/api/router.go",
		"scripts/staging_email_signup_e2e.py",
	} {
		if !governedProductSurfacePath(path) {
			t.Fatalf("new product surface %s escaped governance", path)
		}
	}
	if governedProductSurfacePath("docs/architecture.md") {
		t.Fatal("documentation was incorrectly classified as a product surface")
	}
}

func TestFeatureCoverageRequiresEveryDeclaredTarget(t *testing.T) {
	workspace, catalog, desktop, now := featureCoverageFixture(t)
	catalog.Features[0].Requirements[0].Targets = []string{"desktop", "mobile"}
	desktop.Target = "desktop"
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{desktop}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" || !strings.Contains(report.Requirements[0].Detail, "mobile") {
		t.Fatalf("desktop-only evidence must not cover mobile: %+v", report)
	}
	mobile := desktop
	mobile.Target = "mobile"
	report = assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{desktop, mobile}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "PASS" {
		t.Fatalf("both required targets should pass: %+v", report)
	}
}

func TestFeatureEvidenceContractRejectsMalformedCases(t *testing.T) {
	_, catalog, item, now := featureCoverageFixture(t)
	base := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV2, RunID: "contract-test",
		GeneratedAt: now.Format(time.RFC3339), Cases: []featureCaseEvidenceV2{item},
	}
	tests := map[string]func(*featureEvidenceManifestV2){
		"missing run":        func(value *featureEvidenceManifestV2) { value.RunID = "" },
		"skip":               func(value *featureEvidenceManifestV2) { value.Cases[0].Status = "SKIP" },
		"wrong target":       func(value *featureEvidenceManifestV2) { value.Cases[0].Target = "watch" },
		"wrong environment":  func(value *featureEvidenceManifestV2) { value.Cases[0].Environment = "production" },
		"missing commit":     func(value *featureEvidenceManifestV2) { value.Cases[0].WorkspaceCommit = "" },
		"invalid timestamps": func(value *featureEvidenceManifestV2) { value.Cases[0].CompletedAt = "yesterday" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Cases = append([]featureCaseEvidenceV2(nil), base.Cases...)
			mutate(&value)
			err := validateFeatureEvidenceManifestV2(value, catalog)
			if name == "skip" {
				if err != nil {
					t.Fatalf("SKIP must remain a valid, non-covering status: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("malformed evidence was accepted")
			}
		})
	}
}

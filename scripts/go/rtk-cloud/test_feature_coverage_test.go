package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		Revision: "fixture-revision",
		SpecSource: specRequirementSource{
			DocumentID: "SPEC-TEST", Path: "docs/SPEC.md", Section: "REQ-E2E-TEST-FLOW-001",
		},
	}
	catalog := testCatalog{
		SchemaVersion: 4,
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
			RequirementID: requirement.ID, Revision: requirement.Revision, SpecSource: requirement.SpecSource, Status: "PASS",
			Assertions: map[string]string{"product_flow": "PASS"},
			Evidence:   []featureCoverageEvidenceFile{{Path: evidencePath, SHA256: fmt.Sprintf("%x", sum), Type: "json"}},
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

func TestFeatureCoverageCannotPassWithIncompleteSpecInventory(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	inventory := specInventory{
		SchemaVersion: specInventorySchema,
		Candidates: []specRequirementCandidate{{
			DocumentID: "SPEC-TEST", SourcePath: "docs/SPEC.md", Status: "required", Revision: "candidate",
		}},
		Findings: []specInventoryFinding{{
			Code: "UNSPECIFIED_NORMATIVE_CLAUSE", Source: "docs/SPEC.md", Blocking: true,
		}},
	}
	report := assessFeatureCoverageWithInventory(
		workspace, catalog, inventory,
		[]featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}},
		[]string{"FEAT-TEST-FLOW-001"}, "pr", now,
	)
	if report.Pass != 1 || report.Required != 1 {
		t.Fatalf("qualified requirement evidence was lost: %+v", report)
	}
	if report.Overall != "INCOMPLETE_SPEC" || report.SpecInventory != "INCOMPLETE" ||
		report.SpecBlockingFindings != 1 || report.UnspecifiedRequired != 1 {
		t.Fatalf("incomplete source inventory was reported as covered: %+v", report)
	}
}

func TestFeatureCoverageRequiresEveryDeclaredEvidenceType(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	catalog.Features[0].Requirements[0].Evidence = []string{"json", "logs"}
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" || !strings.Contains(report.Requirements[0].Detail, "logs") {
		t.Fatalf("missing declared evidence type must fail: %+v", report)
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
	report = assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "release", now)
	if report.Requirements[0].Status != "STALE" {
		t.Fatalf("stale scheduled evidence = %+v", report)
	}
}

func TestFeatureCoverageGateModesDeferLiveEvidenceUntilRelease(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	requirement := &catalog.Features[0].Requirements[0]

	requirement.Gate = "pr"
	for _, mode := range []string{"pr", "main", "scheduled", "release"} {
		report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, mode, now)
		if report.Required != 1 || report.Pass != 1 || report.DeferredLive != 0 {
			t.Fatalf("%s must qualify deterministic PR evidence: %+v", mode, report)
		}
	}

	requirement.Gate = "scheduled"
	for _, mode := range []string{"pr", "main"} {
		report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, mode, now)
		if report.Required != 0 || report.Pass != 0 || report.DeferredLive != 1 || report.Requirements[0].Status != "DEFERRED_LIVE" {
			t.Fatalf("%s gate scheduled must defer live evidence: %+v", mode, report)
		}
	}
	for _, mode := range []string{"scheduled", "release"} {
		report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, mode, now)
		if report.Required != 1 || report.Pass != 1 || report.DeferredLive != 0 {
			t.Fatalf("%s must require scheduled evidence: %+v", mode, report)
		}
	}

	requirement.Gate = "operator-release"
	for _, mode := range []string{"pr", "main", "scheduled"} {
		report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, mode, now)
		if report.Required != 0 || report.Pass != 0 || report.DeferredLive != 1 || report.Requirements[0].Status != "DEFERRED_LIVE" {
			t.Fatalf("%s gate operator-release must defer live evidence: %+v", mode, report)
		}
	}
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "release", now)
	if report.Required != 1 || report.Pass != 1 || report.DeferredLive != 0 {
		t.Fatalf("release must require operator evidence: %+v", report)
	}
}

func TestFeatureCoverageMarksOldRequirementRevisionStaleSpec(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	item.Requirements[0].Revision = "prior-spec-revision"
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" || report.StaleSpec != 1 || report.Requirements[0].Status != "STALE_SPEC" {
		t.Fatalf("old spec revision must be STALE_SPEC: %+v", report)
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
		SchemaVersion: featureEvidenceSchemaV3, RunID: "contract-test", SpecCommit: "fixture-spec-commit",
		GeneratedAt: now.Format(time.RFC3339), Cases: []featureCaseEvidenceV2{item},
	}
	tests := map[string]func(*featureEvidenceManifestV2){
		"missing run":        func(value *featureEvidenceManifestV2) { value.RunID = "" },
		"skip":               func(value *featureEvidenceManifestV2) { value.Cases[0].Status = "SKIP" },
		"wrong target":       func(value *featureEvidenceManifestV2) { value.Cases[0].Target = "watch" },
		"wrong environment":  func(value *featureEvidenceManifestV2) { value.Cases[0].Environment = "production" },
		"missing commit":     func(value *featureEvidenceManifestV2) { value.Cases[0].WorkspaceCommit = "" },
		"invalid timestamps": func(value *featureEvidenceManifestV2) { value.Cases[0].CompletedAt = "yesterday" },
		"missing assertion":  func(value *featureEvidenceManifestV2) { value.Cases[0].Requirements = nil },
		"empty assertion": func(value *featureEvidenceManifestV2) {
			value.Cases[0].Requirements[0].Assertions = nil
		},
		"duplicate assertion": func(value *featureEvidenceManifestV2) {
			value.Cases[0].Requirements = append(value.Cases[0].Requirements, value.Cases[0].Requirements[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Cases = append([]featureCaseEvidenceV2(nil), base.Cases...)
			value.Cases[0].Requirements = append([]featureRequirementAssertion(nil), base.Cases[0].Requirements...)
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

func TestFeatureCoverageCommandAuditSelectAndCheck(t *testing.T) {
	auditDir := filepath.Join(t.TempDir(), "audit")
	if err := runTestFeatureCoverage([]string{"audit", "--mode", "pr", "--output-dir", auditDir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"feature-coverage.json", "FEATURE_COVERAGE.md"} {
		if _, err := os.Stat(filepath.Join(auditDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	selectDir := filepath.Join(t.TempDir(), "select")
	if err := runTestFeatureCoverage([]string{"select", "--output-dir", selectDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selectDir, "feature-selection.json")); err != nil {
		t.Fatal(err)
	}
	if err := runTestFeatureCoverage([]string{"check", "--mode", "pr", "--output-dir", filepath.Join(t.TempDir(), "check")}); err == nil {
		t.Fatal("check unexpectedly accepted missing deterministic evidence")
	}
	if err := runTestFeatureCoverage([]string{"audit", "--mode", "production"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := runTestFeatureCoverage([]string{"unknown"}); err == nil {
		t.Fatal("invalid action accepted")
	}
	if err := runTestFeatureCoverage([]string{"audit", "--unknown"}); err == nil {
		t.Fatal("invalid flag accepted")
	}
	recordDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recordDir, "evidence.json"), []byte(`{
  "status":"PASS",
  "workflow":{
    "workflow_id":"WF-AM-SIGNUP-001",
    "steps":{
      "submit_signup":"PASS",
      "verify_email":"PASS",
      "read_authenticated_user":"PASS",
      "password_login":"PASS",
      "reject_token_replay":"PASS"
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "run.log"), []byte("status=PASS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	if err := runTestFeatureCoverage([]string{
		"record", "--test-id", "E2E-CA-SIGNUP-EMAIL-001", "--run-id", "record-command-test",
		"--environment", "staging", "--started-at", now, "--completed-at", now, "--output-dir", recordDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runTestFeatureCoverage([]string{"record"}); err == nil {
		t.Fatal("incomplete record arguments accepted")
	}
}

func TestLoadFeatureEvidenceV3AndLegacyUIAdapter(t *testing.T) {
	workspace, catalog, item, now := featureCoverageFixture(t)
	root := t.TempDir()
	v3 := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3, RunID: "v3-run",
		GeneratedAt: now.Format(time.RFC3339), Cases: []featureCaseEvidenceV2{item},
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		t.Fatal(err)
	}
	v3.SpecCommit = specCommit
	if err := writeJSON(filepath.Join(root, "feature-evidence.json"), v3); err != nil {
		t.Fatal(err)
	}
	manifests, files, err := loadFeatureEvidence(workspace, catalog, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || len(files) != 1 {
		t.Fatalf("v3 load = %d manifests, %d files", len(manifests), len(files))
	}

	evidencePath := item.Requirements[0].Evidence[0].Path
	content, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	legacyPath := filepath.Join(t.TempDir(), "evidence-manifest.json")
	legacy := map[string]any{
		"schema_version": 1, "run_id": "legacy-ui", "environment": "ci",
		"generated_at": now.Format(time.RFC3339), "workspace_commit": item.WorkspaceCommit,
		"cases": []map[string]any{{
			"test_id": item.TestID, "assessment": "PASS", "generated_at": now.Format(time.RFC3339),
			"workspace_commit": item.WorkspaceCommit, "screenshot_path": evidencePath,
			"screenshot_sha256": fmt.Sprintf("%x", sum),
		}},
	}
	if err := writeJSON(legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	manifests, _, err = loadFeatureEvidence(workspace, catalog, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifests[0].Cases[0].Requirements; len(got) != 1 || got[0].Assertions["case_assessment"] != "PASS" {
		t.Fatalf("legacy adapter = %+v", manifests[0])
	}
}

func TestFeatureEvidenceLoaderRejectsUnsupportedManifestAndMissingPath(t *testing.T) {
	workspace, catalog, _, _ := featureCoverageFixture(t)
	path := filepath.Join(t.TempDir(), "evidence-manifest.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadFeatureEvidence(workspace, catalog, path); err == nil {
		t.Fatal("unsupported manifest accepted")
	}
	if _, _, err := loadFeatureEvidence(workspace, catalog, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing evidence path accepted")
	}
}

func TestFeatureSelectionUsesChangePathsAndRejectsUnmappedSurface(t *testing.T) {
	repository := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("init", "-q")
	if err := os.MkdirAll(filepath.Join(repository, "repos", "rtk_cloud_admin", "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repository, "repos", "rtk_cloud_admin", "web", "src", "routes.ts")
	if err := os.WriteFile(source, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-q", "-m", "base")
	base, _ := gitOutput(repository, "rev-parse", "HEAD")
	if err := os.WriteFile(source, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("commit", "-qam", "change")
	catalog := testCatalog{Features: []testCatalogFeature{{
		ID: "FEAT-CA-TEST-001", Status: "active", ChangePaths: []string{"repos/rtk_cloud_admin/**"},
	}}}
	selected, err := selectCatalogFeatures(repository, catalog, strings.TrimSpace(base), "HEAD")
	if err != nil || len(selected) != 1 || selected[0] != "FEAT-CA-TEST-001" {
		t.Fatalf("selection = %v, %v", selected, err)
	}
	catalog.Features[0].ChangePaths = []string{"docs/**"}
	if _, err := selectCatalogFeatures(repository, catalog, strings.TrimSpace(base), "HEAD"); err == nil {
		t.Fatal("unmapped product surface accepted")
	}

	harnessBase, _ := gitOutput(repository, "rev-parse", "HEAD")
	harness := filepath.Join(repository, "loadtests", "home-100k", "scripts", "runner.sh")
	if err := os.MkdirAll(filepath.Dir(harness), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-q", "-m", "shared harness change")
	catalog.Features = []testCatalogFeature{
		{ID: "FEAT-CA-TEST-001", Status: "active", ChangePaths: []string{"repos/rtk_cloud_admin/**"}},
		{ID: "FEAT-VC-TEST-001", Status: "active", ChangePaths: []string{"repos/rtk_video_cloud/**"}},
	}
	selected, err = selectCatalogFeatures(repository, catalog, strings.TrimSpace(harnessBase), "HEAD")
	if err != nil || strings.Join(selected, ",") != "FEAT-CA-TEST-001,FEAT-VC-TEST-001" {
		t.Fatalf("shared harness selection = %v, %v", selected, err)
	}

	selected, err = selectFeatureCoverageScope(repository, catalog, "invalid-base-is-not-read", "HEAD", "main")
	if err != nil || strings.Join(selected, ",") != "FEAT-CA-TEST-001,FEAT-VC-TEST-001" {
		t.Fatalf("main qualification selection = %v, %v", selected, err)
	}
}

func TestWriteFeatureCoverageReportAndCommitValidation(t *testing.T) {
	workspace, catalog, _, now := featureCoverageFixture(t)
	report := featureCoverageReport{
		SchemaVersion: "rtk-cloud-feature-coverage-report/v4", GeneratedAt: now.Format(time.RFC3339),
		Mode: "pr", Overall: "FAIL", Required: 1, Missing: 1, CodeCoverage: "SEPARATE_NOT_SCORED",
		Requirements: []featureRequirementResult{{
			FeatureID: "FEAT-TEST-FLOW-001", RequirementID: "REQ-E2E-TEST-FLOW-001",
			Risk: "critical", Gate: "pr", Status: "MISSING", Detail: "proof | absent",
		}},
	}
	dir := t.TempDir()
	summary := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	if err := writeFeatureCoverageReport(dir, report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "feature-coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded featureCoverageReport
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Missing != 1 {
		t.Fatalf("report JSON = %+v, %v", decoded, err)
	}
	summaryRaw, err := os.ReadFile(summary)
	if err != nil || !strings.Contains(string(summaryRaw), "Product requirements") || !strings.Contains(string(summaryRaw), "proof \\| absent") {
		t.Fatalf("GitHub summary missing feature report: %v\n%s", err, summaryRaw)
	}
	if err := validateFeatureCommits(workspace, catalog.Features[0], nil); err != nil {
		t.Fatalf("workspace-only anchor should pass: %v", err)
	}
	catalog.Features[0].CommitAnchors = []string{"workspace", "cloud_admin"}
	if err := validateFeatureCommits(workspace, catalog.Features[0], map[string]string{"cloud_admin": "wrong"}); err == nil {
		t.Fatal("commit mismatch accepted")
	}
	if err := validateFeatureCommits(workspace, catalog.Features[0], nil); err == nil {
		t.Fatal("missing commit anchor accepted")
	}
	catalog.Features[0].CommitAnchors = []string{"workspace", "unknown"}
	if err := validateFeatureCommits(workspace, catalog.Features[0], map[string]string{"unknown": "commit"}); err == nil {
		t.Fatal("unsupported commit anchor accepted")
	}
}

func TestFeatureEvidenceFileErrorsAndFailedAssertion(t *testing.T) {
	if err := verifyFeatureEvidenceFiles(nil); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if err := verifyFeatureEvidenceFiles([]featureCoverageEvidenceFile{{Path: filepath.Join(t.TempDir(), "missing"), SHA256: "x"}}); err == nil {
		t.Fatal("missing evidence file accepted")
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFeatureEvidenceFiles([]featureCoverageEvidenceFile{{Path: path, SHA256: strings.Repeat("0", 64)}}); err == nil {
		t.Fatal("incorrect evidence digest accepted")
	}
	workspace, catalog, item, now := featureCoverageFixture(t)
	item.Status = "FAIL"
	item.Requirements[0].Status = "FAIL"
	item.Requirements[0].Assertions["product_flow"] = "FAIL"
	report := assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Requirements[0].Status != "FAIL" {
		t.Fatalf("failed assertion = %+v", report)
	}
	requirement := catalog.Features[0].Requirements[0]
	if status, _, _ := evaluateRequirementEvidence(workspace, requirement, catalog.Features[0], map[string]testCatalogCase{}, nil, now); status != "MISSING" {
		t.Fatalf("unmapped requirement status = %s", status)
	}
	item.Status = "PASS"
	item.Requirements[0].Status = "PASS"
	item.Requirements[0].Assertions = nil
	report = assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" {
		t.Fatal("missing assertion results passed")
	}
	item.Requirements[0].Assertions = map[string]string{"product_flow": "PASS"}
	item.Environment = "local"
	report = assessFeatureCoverage(workspace, catalog, []featureEvidenceManifestV2{{Cases: []featureCaseEvidenceV2{item}}}, []string{"FEAT-TEST-FLOW-001"}, "pr", now)
	if report.Overall != "FAIL" {
		t.Fatal("environment mismatch passed")
	}
}

func TestWriteCaseFeatureEvidenceProducesCompleteLiveContract(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"overall":"pass"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TEST_REPORT.md"), []byte("# PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junit.xml"), []byte(`<?xml version="1.0"?><testsuite tests="1" failures="0"><testcase name="live"/></testsuite>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "run.log"), []byte("status=PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range inventory.Workflows {
		if !catalogContainsString(workflow.RequirementIDs, "REQ-LIVE-STG-ONBOARD-001") {
			continue
		}
		steps := map[string]string{}
		assertions := map[string]map[string]string{}
		for _, step := range workflow.Steps {
			steps[step.ID] = "PASS"
			assertions[step.ID] = map[string]string{"observable_product_fact": "PASS"}
		}
		if err := writeJSON(filepath.Join(dir, "workflow-"+workflow.ID+".json"), map[string]any{
			"workflow": map[string]any{"workflow_id": workflow.ID, "steps": steps, "assertions": assertions},
		}); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	completed := time.Now().UTC().Truncate(time.Second)
	if err := writeCaseFeatureEvidence(
		workspace, dir, "LIVE-STG-ONBOARD-001", "live-contract-test",
		"staging", "", started, completed,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"feature-evidence.json", "feature-results.json", "feature-junit.xml", "FEATURE_REPORT.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	manifests, _, err := loadFeatureEvidence(workspace, catalog, filepath.Join(dir, "feature-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertion := manifests[0].Cases[0].Requirements[0]
	if missing := missingFeatureEvidenceTypes([]string{"json", "markdown", "logs"}, assertion.Evidence); len(missing) != 0 {
		t.Fatalf("live evidence types missing: %v", missing)
	}
}

func TestWriteCaseFeatureEvidenceKeepsWorkflowGapIncompleteInObserveMode(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	makeEvidenceDir := func() string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"overall":"pass"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "TEST_REPORT.md"), []byte("# PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "junit.xml"), []byte(`<?xml version="1.0"?><testsuite tests="1" failures="0"><testcase name="live"/></testsuite>`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "logs", "run.log"), []byte("status=PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	now := time.Now().UTC().Truncate(time.Second)
	t.Setenv("FEATURE_QUALIFICATION_MODE", "observe")
	observeDir := makeEvidenceDir()
	if err := writeCaseFeatureEvidence(workspace, observeDir, "LIVE-STG-ONBOARD-001", "observe-workflow-gap", "staging", "", now, now); err != nil {
		t.Fatalf("observe mode rejected a reportable workflow gap: %v", err)
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(observeDir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	item := manifest.Cases[0]
	if item.Status != "INCOMPLETE" || item.Requirements[0].Status != "INCOMPLETE" || len(item.Workflows) != 0 {
		t.Fatalf("observe workflow gap was not explicit INCOMPLETE evidence: %+v", item)
	}
	if item.Requirements[0].Assertions["workflow_contract"] != "INCOMPLETE" {
		t.Fatalf("workflow gap assertion missing: %+v", item.Requirements[0].Assertions)
	}

	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := catalogCaseByID(catalog.Cases, "LIVE-STG-ONBOARD-001")
	if !ok {
		t.Fatal("live onboarding catalog case missing")
	}
	_, _, _, err = qualifyLiveWorkflowEvidence(makeEvidenceDir(), tc, inventory, "required")
	if err == nil || !strings.Contains(err.Error(), "requires step-level live workflow evidence") {
		t.Fatalf("required mode accepted a workflow gap: %v", err)
	}
	statusOnlyDir := makeEvidenceDir()
	for _, workflow := range inventory.Workflows {
		matchesCase := false
		for _, requirementID := range tc.Verifies {
			if catalogContainsString(workflow.RequirementIDs, requirementID) {
				matchesCase = true
				break
			}
		}
		if !matchesCase {
			continue
		}
		steps := map[string]string{}
		for _, step := range workflow.Steps {
			steps[step.ID] = "PASS"
		}
		if err := writeJSON(filepath.Join(statusOnlyDir, "workflow-"+workflow.ID+".json"), map[string]any{
			"workflow": map[string]any{"workflow_id": workflow.ID, "steps": steps},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, err = qualifyLiveWorkflowEvidence(statusOnlyDir, tc, inventory, "required")
	if err == nil || !strings.Contains(err.Error(), "requires explicit live assertions") {
		t.Fatalf("required mode accepted status-only workflow evidence: %v", err)
	}
}

func TestWriteCaseFeatureEvidenceAppendsMultipleCasesFromOneLiveRun(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, workflowID := range []string{"WF-AM-SIGNUP-001", "WF-CONTRACT-AUTH-ACCOUNT-001"} {
		for _, workflow := range inventory.Workflows {
			if workflow.ID != workflowID {
				continue
			}
			steps := map[string]string{}
			assertions := map[string]map[string]string{}
			for _, step := range workflow.Steps {
				steps[step.ID] = "PASS"
				assertions[step.ID] = map[string]string{"observable_live_behavior": "PASS"}
			}
			if err := writeJSON(filepath.Join(dir, workflowID+".json"), map[string]any{
				"workflow": map[string]any{"workflow_id": workflowID, "steps": steps, "assertions": assertions},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "run.log"), []byte("status=PASS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, testID := range []string{"E2E-CA-SIGNUP-EMAIL-001", "E2E-AUTH-ACCOUNT-ONBOARDING-001"} {
		if err := writeCaseFeatureEvidence(workspace, dir, testID, "shared-email-run", "staging", "", now, now); err != nil {
			t.Fatal(err)
		}
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(dir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 2 || manifest.Cases[0].Status != "PASS" || manifest.Cases[1].Status != "PASS" {
		t.Fatalf("multi-case live manifest = %#v", manifest.Cases)
	}
	var results struct {
		Status string `json:"status"`
		Cases  []any  `json:"cases"`
	}
	if err := readJSONFile(filepath.Join(dir, "feature-results.json"), &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != "PASS" || len(results.Cases) != 2 {
		t.Fatalf("multi-case live results = %#v", results)
	}
	junit, err := os.ReadFile(filepath.Join(dir, "feature-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(junit), `tests="2"`) {
		t.Fatalf("multi-case JUnit does not contain both cases: %s", junit)
	}
	if err := writeCaseFeatureEvidence(workspace, dir, "E2E-CA-SIGNUP-EMAIL-001", "shared-email-run", "staging", "", now, now); err == nil {
		t.Fatal("duplicate Test ID was appended to live evidence")
	}
}

func TestWriteCaseFeatureEvidenceRejectsMissingRequiredTypeAndSecrets(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	missingLogs := t.TempDir()
	if err := os.WriteFile(filepath.Join(missingLogs, "evidence.json"), []byte(`{"status":"PASS"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCaseFeatureEvidence(workspace, missingLogs, "E2E-CA-SIGNUP-EMAIL-001", "missing-log-test", "staging", "", now, now); err == nil || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("missing log evidence error = %v", err)
	}
	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "evidence.json"), []byte(`{"status":"PASS","password":"not-redacted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "run.log"), []byte("status=PASS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCaseFeatureEvidence(workspace, secretDir, "E2E-CA-SIGNUP-EMAIL-001", "secret-test", "staging", "", now, now); err == nil || !strings.Contains(err.Error(), "unredacted") {
		t.Fatalf("secret evidence error = %v", err)
	}
}

func TestWorkflowBoundRequirementNeedsEveryCurrentStep(t *testing.T) {
	workflow := specWorkflow{
		ID: "WF-TEST-FLOW-001", Revision: "workflow-revision",
		Steps: []specWorkflowStep{
			{ID: "create", OperationRef: "SPEC-API#create"},
			{ID: "read", OperationRef: "SPEC-API#read"},
		},
	}
	item := featureCaseEvidenceV2{}
	if ok, reason := qualifyingWorkflowEvidence(item, []specWorkflow{workflow}); ok ||
		!strings.Contains(reason, "missing") {
		t.Fatalf("missing workflow evidence accepted: ok=%t reason=%q", ok, reason)
	}
	item.Workflows = []featureWorkflowAssertion{{
		WorkflowID: workflow.ID, Revision: workflow.Revision, Status: "PASS",
		Steps: []featureWorkflowStepAssertion{{
			StepID: "create", OperationRef: "SPEC-API#create", Status: "PASS",
			Assertions: map[string]string{"created": "PASS"},
		}},
	}}
	if ok, reason := qualifyingWorkflowEvidence(item, []specWorkflow{workflow}); ok ||
		!strings.Contains(reason, "read") {
		t.Fatalf("partial workflow evidence accepted: ok=%t reason=%q", ok, reason)
	}
	item.Workflows[0].Steps = append(item.Workflows[0].Steps, featureWorkflowStepAssertion{
		StepID: "read", OperationRef: "SPEC-API#read", Status: "PASS",
		Assertions: map[string]string{"state_matches": "PASS"},
	})
	if ok, reason := qualifyingWorkflowEvidence(item, []specWorkflow{workflow}); !ok {
		t.Fatalf("complete workflow evidence rejected: %s", reason)
	}
	item.Workflows[0].Steps[1].Assertions["state_matches"] = "FAIL"
	if ok, _ := qualifyingWorkflowEvidence(item, []specWorkflow{workflow}); ok {
		t.Fatal("failed step assertion counted as workflow PASS")
	}
}

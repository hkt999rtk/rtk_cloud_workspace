package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTestCatalogIsStableAndSorted(t *testing.T) {
	catalog := testCatalog{SchemaVersion: 4, Cases: []testCatalogCase{
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
	specDir := filepath.Join(workspace, "specs")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `schema_version: 1
sources:
  - id: SPEC-CA-SMOKE
    path: specs/smoke.md
    parser: markdown
    authority: service
    owner: rtk_cloud_admin
  - id: SPEC-NOT-CHECKED-OUT
    path: repos/not-checked-out/docs/SPEC.md
    parser: markdown
    authority: service
    owner: cloud_platform
`
	spec := `---
rtk_spec:
  id: SPEC-CA-SMOKE
  status: normative
  owner: rtk_cloud_admin
---
## [FEAT-CA-SMOKE-001] Dashboard smoke
<!-- rtk-feature
owner: rtk_cloud_admin
risk: high
status: active
change_paths: [repos/rtk_cloud_admin/web/e2e/catalog.spec.mjs]
commit_anchors: [workspace, cloud_admin]
surfaces:
  - kind: ui-route
    source: repos/rtk_cloud_admin/web/e2e/catalog.spec.mjs
    selector: renders dashboard
-->
### [REQ-UI-CA-SMOKE-001] Dashboard renders
<!-- rtk-requirement
acceptance_layer: ui
gate: pr
environments: [local]
targets: [desktop]
evidence: [screenshot]
status: active
-->
The dashboard renders.
`
	if err := os.WriteFile(filepath.Join(workspace, "tests", "spec-sources.yaml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "smoke.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := `schema_version: 4
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
    verifies: [REQ-UI-CA-SMOKE-001]
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
	catalog := testCatalog{SchemaVersion: 4, Cases: []testCatalogCase{
		{ID: "E2E-HOME-SHADOW-001", Layer: "e2e", Feature: "device-shadow", Status: "active"},
		{ID: "LOAD-HOME-SHADOW-001", Layer: "load", Feature: "video-webrtc", Status: "active", Covers: []string{"E2E-HOME-SHADOW-001"}},
	}}
	if err := validateCatalogRelationships(catalog); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected covers feature mismatch, got %v", err)
	}
}

func TestCatalogRequiredRequirementRejectsSupportingOnlyProof(t *testing.T) {
	catalog := testCatalog{
		SchemaVersion: 4,
		Features: []testCatalogFeature{{
			ID: "FEAT-TEST-FLOW-001", Status: "active",
			Requirements: []testCatalogRequirement{{
				ID: "REQ-E2E-TEST-FLOW-001", Status: "active",
				AcceptanceLayer: "e2e", Environments: []string{"ci"},
			}},
		}},
		Cases: []testCatalogCase{{
			ID: "UNIT-TEST-FLOW-001", Layer: "unit", Status: "active",
			Verifies: []string{"REQ-E2E-TEST-FLOW-001"}, Environments: []string{"ci"},
		}},
	}
	if err := validateRequirementProofMappings(catalog); err == nil || !strings.Contains(err.Error(), "no qualifying") {
		t.Fatalf("unit-only proof should be incomplete, got %v", err)
	}
}

func TestCatalogUIRequirementAcceptsBrowserIntegrationWithScreenshotProof(t *testing.T) {
	requirement := testCatalogRequirement{
		ID: "REQ-UI-TEST-FLOW-001", Status: "active",
		AcceptanceLayer: "ui", Environments: []string{"ci"},
	}
	catalog := testCatalog{
		SchemaVersion: 4,
		Features: []testCatalogFeature{{
			ID: "FEAT-TEST-FLOW-001", Status: "active",
			Requirements: []testCatalogRequirement{requirement},
		}},
		Cases: []testCatalogCase{{
			ID: "INT-TEST-FLOW-001", Layer: "integration", Status: "active",
			Verifies: []string{requirement.ID}, Environments: []string{"ci"},
			Evidence: []string{"json", "screenshot"},
		}},
	}
	if err := validateRequirementProofMappings(catalog); err != nil {
		t.Fatalf("browser integration with screenshot should qualify as UI proof: %v", err)
	}
	catalog.Cases[0].Evidence = []string{"json"}
	if err := validateRequirementProofMappings(catalog); err == nil || !strings.Contains(err.Error(), "no qualifying") {
		t.Fatalf("integration without screenshot must not qualify as UI proof, got %v", err)
	}
}

func TestCatalogObserveReportsProofGapsWithoutBlockingMigration(t *testing.T) {
	catalog := testCatalog{
		SchemaVersion: 4,
		Features: []testCatalogFeature{{
			ID: "FEAT-TEST-FLOW-001", Status: "active",
			Requirements: []testCatalogRequirement{{
				ID: "REQ-E2E-TEST-FLOW-001", Status: "active",
				AcceptanceLayer: "e2e", Environments: []string{"ci"},
			}},
		}},
	}
	t.Setenv("FEATURE_QUALIFICATION_MODE", "observe")
	if err := validateCatalogRelationships(catalog); err != nil {
		t.Fatalf("observe mode must leave missing proof to the feature coverage report: %v", err)
	}
	t.Setenv("FEATURE_QUALIFICATION_MODE", "required")
	if err := validateCatalogRelationships(catalog); err == nil || !strings.Contains(err.Error(), "no qualifying") {
		t.Fatalf("required mode must reject missing proof, got %v", err)
	}
}

func TestCatalogRejectsScheduledRequirementMappedOnlyToOperatorLoad(t *testing.T) {
	catalog := testCatalog{
		Features: []testCatalogFeature{{
			ID: "FEAT-TEST-FLOW-001", Status: "active",
			Requirements: []testCatalogRequirement{{
				ID: "REQ-LOAD-TEST-FLOW-001", Status: "active", Gate: "scheduled",
				AcceptanceLayer: "live", Environments: []string{"staging"},
			}},
		}},
		Cases: []testCatalogCase{{
			ID: "LOAD-TEST-FLOW-001", Status: "active", Layer: "load", Runner: "test-live",
			Profile: "capacity", Environments: []string{"staging"}, Verifies: []string{"REQ-LOAD-TEST-FLOW-001"},
		}},
	}
	if err := validateRequirementProofMappings(catalog); err == nil || !strings.Contains(err.Error(), "no scheduled-executable proof case") {
		t.Fatalf("scheduled gate accepted an operator-only capacity proof: %v", err)
	}
	catalog.Cases[0] = testCatalogCase{
		ID: "E2E-TEST-FLOW-001", Status: "active", Layer: "e2e", Runner: "test-feature",
		Profile: "canary", Environments: []string{"staging"}, Verifies: []string{"REQ-LOAD-TEST-FLOW-001"},
	}
	if err := validateRequirementProofMappings(catalog); err != nil {
		t.Fatalf("scheduled canary proof should be reachable: %v", err)
	}
}

func TestScheduledGateProofCaseMatchesNightlyExecutionRoutes(t *testing.T) {
	tests := []struct {
		name  string
		case_ testCatalogCase
		want  bool
	}{
		{name: "feature canary", case_: testCatalogCase{Runner: "test-feature", Profile: "canary", Environments: []string{"staging"}}, want: true},
		{name: "feature qualification", case_: testCatalogCase{Runner: "test-feature", Profile: "qualification-1k", Environments: []string{"staging"}}},
		{name: "capacity", case_: testCatalogCase{Runner: "test-live", Profile: "capacity", Environments: []string{"staging"}}},
		{name: "staging UI", case_: testCatalogCase{Runner: "test-ui", Layer: "ui", Environments: []string{"staging"}}, want: true},
		{name: "local UI", case_: testCatalogCase{Runner: "test-ui", Layer: "ui", Environments: []string{"local"}}},
		{name: "scheduled SDK", case_: testCatalogCase{Runner: "test-e2e", Owner: "rtk_cloud_client", Environments: []string{"staging"}}, want: true},
		{name: "operator E2E", case_: testCatalogCase{Runner: "test-e2e", Owner: "factory_enroll", Environments: []string{"staging"}}},
		{name: "runtime onboarding", case_: testCatalogCase{ID: "LIVE-STG-ONBOARD-001", Runner: "test-live", Environments: []string{"staging"}}, want: true},
		{name: "runtime scrape", case_: testCatalogCase{ID: "LIVE-CA-SCRAPE-001", Runner: "test-live", Environments: []string{"staging"}}, want: true},
		{name: "operator live", case_: testCatalogCase{ID: "E2E-CA-SIGNUP-EMAIL-001", Runner: "test-live", Environments: []string{"staging"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduledGateProofCase(tc.case_); got != tc.want {
				t.Fatalf("scheduledGateProofCase() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCatalogRejectsUnknownFeatureQualificationMode(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATURE_QUALIFICATION_MODE", "typo")
	if _, err := loadAndValidateTestCatalog(workspace); err == nil || !strings.Contains(err.Error(), "must be observe or required") {
		t.Fatalf("unknown feature qualification mode accepted: %v", err)
	}
}

func TestLoadCasesUseScenarioProfilesAtTheirDeclaredScale(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct {
		devices string
		profile string
	}{
		"LOAD-HOME-SHADOW-001": {devices: "1000"},
		"LOAD-HOME-SHADOW-002": {devices: "100000", profile: "video-100k-turn-v1"},
		"LOAD-HOME-VIDEO-001":  {devices: "1000", profile: "video-1k-v1"},
		"LOAD-HOME-TURN-001":   {devices: "50000", profile: "video-50k-turn-v1"},
		"LOAD-HOME-TURN-002":   {devices: "100000", profile: "video-100k-turn-v1"},
		"LOAD-VC-CLIP-001":     {devices: "10000", profile: "clip-storage-10k-v2"},
		"LOAD-VC-CLIP-002":     {devices: "1000", profile: "clip-storage-1k-v1"},
	}
	for testID, want := range expected {
		t.Run(testID, func(t *testing.T) {
			tc, ok := catalogCaseByID(catalog.Cases, testID)
			if !ok {
				t.Fatalf("catalog case %s is missing", testID)
			}
			scenario := filepath.Join(workspace, filepath.FromSlash(tc.Source))
			if got := envFileValue(scenario, "HOME100K_DEVICES"); got != want.devices {
				t.Fatalf("%s HOME100K_DEVICES = %q, want %q", tc.Source, got, want.devices)
			}
			if got := envFileValue(scenario, "HOME100K_SCENARIO_PROFILE"); got != want.profile {
				t.Fatalf("%s HOME100K_SCENARIO_PROFILE = %q, want %q", tc.Source, got, want.profile)
			}
		})
	}
}

func TestVideo1KQualificationUsesSameRegionGenerator(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	scenario := filepath.Join(workspace, "loadtests", "home-100k", "scenarios", "video-1k.description.env")
	for key, want := range map[string]string{
		"HOME100K_VIDEO_LOADTEST_MODE":                  "remote-sharded",
		"HOME100K_VIDEO_LOADTEST_SHARD_MODE":            "global",
		"HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST":  "100",
		"HOME100K_VIDEO_LOADTEST_DEVICES":               "100",
		"HOME100K_VIDEO_LOADTEST_VIEWERS":               "100",
		"HOME100K_VIDEO_LOADTEST_CONCURRENCY":           "100",
		"HOME100K_FUNCTIONAL_SUCCESS_THRESHOLD_PERCENT": "99.5",
	} {
		if got := envFileValue(scenario, key); got != want {
			t.Fatalf("%s %s = %q, want %q", scenario, key, got, want)
		}
	}
}

func TestCatalogSurfaceExclusionRequiresOwnedUnexpiredReason(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	surface := &catalog.Features[0].Surfaces[0]
	surface.Exclusion = "temporary compatibility surface"
	surface.Owner = "cloud_platform"
	surface.Expires = "2000-01-01"
	if err := validateFeatureRequirements(workspace, catalog, "test-ui"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired surface exclusion accepted: %v", err)
	}
	surface.Expires = "2999-01-01"
	surface.Owner = "unknown"
	if err := validateFeatureRequirements(workspace, catalog, "test-ui"); err == nil || !strings.Contains(err.Error(), "requires owner") {
		t.Fatalf("unowned surface exclusion accepted: %v", err)
	}
}

func TestCatalogRejectsOrphanRequirementReference(t *testing.T) {
	catalog := testCatalog{
		Features: []testCatalogFeature{{
			Requirements: []testCatalogRequirement{{ID: "REQ-E2E-TEST-OTHER-001"}},
		}},
		Cases: []testCatalogCase{{
			ID: "E2E-TEST-FLOW-001", Status: "active", Verifies: []string{"REQ-E2E-TEST-MISSING-001"},
		}},
	}
	requirements := catalogRequirementIndex(catalog)
	for _, id := range catalog.Cases[0].Verifies {
		if _, ok := requirements[id]; ok {
			t.Fatalf("orphan requirement %s unexpectedly resolved", id)
		}
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

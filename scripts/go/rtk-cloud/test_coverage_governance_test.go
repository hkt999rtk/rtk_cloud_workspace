package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeGoCoverageProfileDeduplicatesAndExcludesWiring(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "coverage.out")
	raw := strings.Join([]string{
		"mode: atomic",
		"example.test/module/internal/core/core.go:1.1,2.1 4 1",
		"example.test/module/internal/core/core.go:1.1,2.1 4 3",
		"example.test/module/cmd/server/main.go:1.1,2.1 2 0",
		"",
	}, "\n")
	if err := os.WriteFile(profile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	module := coverageModule{
		Owner:       "owner",
		DefaultRisk: "normal",
		PackagePolicies: []coveragePackagePolicy{
			{Package: "example.test/module/internal/core", Risk: "critical", MinimumStatementPercent: 80, TargetStatementPercent: 80},
			{Package: "example.test/module/cmd/server", Risk: "wiring", ExclusionReason: "bootstrap only"},
		},
	}
	rawPercent, governedPercent, packages, err := analyzeGoCoverageProfile(profile, module, "unit")
	if err != nil {
		t.Fatal(err)
	}
	if rawPercent != 100*4.0/6.0 || governedPercent != 100 || len(packages) != 2 {
		t.Fatalf("coverage raw=%v governed=%v packages=%#v", rawPercent, governedPercent, packages)
	}
	byName := map[string]coveragePackageResult{}
	for _, item := range packages {
		byName[item.Package] = item
	}
	if byName["example.test/module/internal/core"].Status != "PASS" ||
		byName["example.test/module/cmd/server"].Risk != "wiring" ||
		byName["example.test/module/cmd/server"].GovernedStatementPercent != nil {
		t.Fatalf("package governance = %#v", packages)
	}
}

func TestParseGoTestEventsBuildsCanonicalSubtestInventory(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/module\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(moduleDir, "internal", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "internal", "auth", "auth_test.go"), []byte("package auth\n\nfunc TestLogin(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.json")
	lines := []goTestEvent{
		{Time: "2026-07-24T00:00:00Z", Action: "run", Package: "example.test/module/internal/auth", Test: "TestLogin"},
		{Time: "2026-07-24T00:00:00.010Z", Action: "run", Package: "example.test/module/internal/auth", Test: "TestLogin/denied"},
		{Time: "2026-07-24T00:00:00.020Z", Action: "pass", Package: "example.test/module/internal/auth", Test: "TestLogin/denied", Elapsed: 0.01},
		{Time: "2026-07-24T00:00:00.030Z", Action: "pass", Package: "example.test/module/internal/auth", Test: "TestLogin", Elapsed: 0.03},
	}
	var encoded strings.Builder
	for _, event := range lines {
		raw, _ := json.Marshal(event)
		encoded.Write(raw)
		encoded.WriteByte('\n')
	}
	if err := os.WriteFile(events, []byte(encoded.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	units, err := parseGoTestEvents(events, moduleDir, coverageModule{Name: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("units = %#v", units)
	}
	if units[1].CanonicalKey != "go://service/internal/auth#TestLogin/denied" || units[1].Source != "internal/auth/auth_test.go" || units[1].Status != "PASS" {
		t.Fatalf("subtest inventory = %#v", units[1])
	}
}

func TestRuntimeCoverageDirsRequiresMetadataAndCounters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pod")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "covmeta.hash"), []byte("meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeCoverageDirs(root); err == nil || !strings.Contains(err.Error(), "no counters") {
		t.Fatalf("missing-counter error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "covcounters.hash.1.1"), []byte("counter"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := runtimeCoverageDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("runtime dirs = %#v", dirs)
	}
}

func TestValidateRequiredGoTestsRejectsSkippedCriticalCase(t *testing.T) {
	module := coverageModule{
		PRRequiredTests: []string{"TestDatabase"},
		CriticalCases: []coverageCriticalCase{{
			TestID: "UNIT-AM-AUTH-001", CanonicalKey: "go://service/internal/auth#TestSecurity",
		}},
	}
	units := []goUnitResult{
		{CanonicalKey: "go://service/internal/database#TestDatabase", Test: "TestDatabase", Status: "PASS"},
		{CanonicalKey: "go://service/internal/auth#TestSecurity", Test: "TestSecurity", Status: "SKIP"},
	}
	if err := validateRequiredGoTests(module, units); err == nil || !strings.Contains(err.Error(), "critical test") {
		t.Fatalf("critical SKIP error = %v", err)
	}
}

func TestValidateRuntimeCoverageAnchorChecksRunAndCommits(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	module := coverageModule{Name: "workspace-tooling", Path: "scripts/go"}
	workspaceCommit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	moduleCommit, err := gitOutput(filepath.Join(workspace, module.Path), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	anchor := runtimeCoverageAnchor{
		SchemaVersion: 1, RunID: "runtime-test", Module: module.Name,
		WorkspaceCommit: strings.TrimSpace(workspaceCommit),
		ModuleCommit:    strings.TrimSpace(moduleCommit),
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeJSON(filepath.Join(root, "coverage-runtime.json"), anchor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRuntimeCoverageAnchor(workspace, module, root, "runtime-test"); err != nil {
		t.Fatal(err)
	}
	anchor.ModuleCommit = "wrong"
	if err := writeJSON(filepath.Join(root, "coverage-runtime.json"), anchor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRuntimeCoverageAnchor(workspace, module, root, "runtime-test"); err == nil || !strings.Contains(err.Error(), "commit mismatch") {
		t.Fatalf("commit mismatch error = %v", err)
	}
}

func TestRunRuntimeCoverageReportsMissingEvidenceAsIncomplete(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	report := coverageReport{
		SchemaVersion: 2,
		RunID:         "missing-runtime",
		Profile:       "runtime",
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	cfg := coverageConfig{Modules: []coverageModule{{
		TestID: "SVC-WS-TOOL-001",
		Name:   "workspace-tooling",
		Kind:   "go",
		Path:   "scripts/go",
	}}}
	err = runRuntimeCoverage(workspace, outDir, cfg, report, map[string]bool{"workspace-tooling": true}, t.TempDir())
	if err == nil {
		t.Fatal("runRuntimeCoverage returned nil for missing runtime evidence")
	}
	raw, readErr := os.ReadFile(filepath.Join(outDir, "results.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result coverageReport
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "INCOMPLETE" || len(result.Cases) != 1 || result.Cases[0].Status != "INCOMPLETE" ||
		result.Cases[0].CompletedAt == "" || result.Cases[0].DurationMS < 0 {
		t.Fatalf("runtime result = %#v", result)
	}
}

func TestRunTestCoverageAggregateCombinesTenGoModules(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	input := t.TempDir()
	for _, module := range cfg.Modules {
		if module.Kind != "go" {
			continue
		}
		source := filepath.Join(input, module.Name)
		profileRel := filepath.ToSlash(filepath.Join("modules", module.Name, "coverage.out"))
		profilePath := filepath.Join(source, filepath.FromSlash(profileRel))
		if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		report := coverageReport{
			SchemaVersion: 2, RunID: "source-" + module.Name, Profile: "unit",
			WorkspaceCommit: strings.TrimSpace(commit), Status: "PASS",
			Cases: []coverageCaseResult{{
				TestID: module.TestID, Name: module.Name, Kind: "go", Path: module.Path,
				Purpose: module.Purpose, Method: module.Method, Status: "PASS",
				ProfilePath: profileRel, Assessment: "source passed",
			}},
		}
		if err := writeJSON(filepath.Join(source, "results.json"), report); err != nil {
			t.Fatal(err)
		}
	}
	runID := "unit-coverage-aggregate"
	outDir := filepath.Join(workspace, ".artifacts", "test-runs", runID)
	defer os.RemoveAll(outDir)
	if err := runTestCoverageAggregate([]string{"--input-dir", input, "--run-id", runID}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "coverage", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report coverageReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASS" || report.Profile != "aggregate" || len(report.Cases) != 10 {
		t.Fatalf("aggregate report = %#v", report)
	}
}

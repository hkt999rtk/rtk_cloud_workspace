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

func TestRunTestCoverageAggregateCombinesGoAndJavaScriptModules(t *testing.T) {
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
	ledger, err := loadUnitInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range cfg.Modules {
		source := filepath.Join(input, module.Name)
		moduleRel := filepath.ToSlash(filepath.Join("modules", module.Name))
		if err := os.MkdirAll(filepath.Join(source, filepath.FromSlash(moduleRel)), 0o755); err != nil {
			t.Fatal(err)
		}
		result := coverageCaseResult{
			TestID: module.TestID, Name: module.Name, Kind: module.Kind, Path: module.Path,
			Purpose: module.Purpose, Method: module.Method, Status: "PASS",
			Assessment: "source passed",
		}
		if len(module.CriticalCases) > 0 {
			result.CriticalGate = "PASS"
		}
		result.UnitManifestPath = filepath.ToSlash(filepath.Join(moduleRel, "unit-manifest.json"))
		result.JUnitPath = filepath.ToSlash(filepath.Join(moduleRel, "junit.xml"))
		result.TestEventsPath = filepath.ToSlash(filepath.Join(moduleRel, "test-events.json"))
		result.LogPath = filepath.ToSlash(filepath.Join(moduleRel, "coverage.log"))
		if module.Kind == "go" {
			result.ProfilePath = filepath.ToSlash(filepath.Join(moduleRel, "coverage.out"))
			result.PackageCoveragePath = filepath.ToSlash(filepath.Join(moduleRel, "package-coverage.json"))
			if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(result.ProfilePath)), []byte("mode: set\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(source, filepath.FromSlash(result.PackageCoveragePath)), map[string]any{"module": module.Name}); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(source, filepath.FromSlash(result.UnitManifestPath)), map[string]any{
				"schema_version": 1, "module": module.Name, "tests": []goUnitResult{},
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			manifest := nodeUnitManifest{SchemaVersion: 1, Module: module.Name, Profile: "unit"}
			for _, item := range ledger.Cases {
				if item.Module == module.Name && item.Status == "active" {
					manifest.Tests = append(manifest.Tests, nodeUnitResult{
						CanonicalKey: item.CanonicalKey, Module: item.Module, Language: item.Language,
						Source: item.Source, Title: item.Title, Status: "PASS", TestID: item.TestID,
					})
				}
			}
			if err := writeJSON(filepath.Join(source, filepath.FromSlash(result.UnitManifestPath)), manifest); err != nil {
				t.Fatal(err)
			}
		}
		for _, rel := range []string{result.JUnitPath, result.TestEventsPath, result.LogPath} {
			if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(rel)), []byte("evidence\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, rel := range []string{
			result.ProfilePath, result.PackageCoveragePath, result.UnitManifestPath,
			result.JUnitPath, result.TestEventsPath, result.LogPath,
		} {
			if rel == "" {
				continue
			}
			sha, err := fileSHA256(filepath.Join(source, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			result.Evidence = append(result.Evidence, coverageArtifactEvidence{Path: rel, SHA256: sha})
		}
		report := coverageReport{
			SchemaVersion: 2, RunID: "source-" + module.Name, Profile: "unit",
			WorkspaceCommit: strings.TrimSpace(commit), Status: "PASS",
			Cases: []coverageCaseResult{result},
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
	if report.Status != "PASS" || report.Profile != "aggregate" || len(report.Cases) != len(cfg.Modules) {
		t.Fatalf("aggregate report = %#v", report)
	}
}

func TestCoverageGovernanceHandlesPolicyAndInputBranches(t *testing.T) {
	for _, risk := range []string{"critical", "high", "normal", "wiring"} {
		if !validCoverageRisk(risk) {
			t.Fatalf("validCoverageRisk(%q) = false", risk)
		}
	}
	if validCoverageRisk("unknown") {
		t.Fatal("unknown risk accepted")
	}

	dir := t.TempDir()
	profile := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(profile, []byte(strings.Join([]string{
		"mode: set",
		"example.test/module/internal/auth/auth.go:1.1,2.1 10 0",
		"example.test/module/internal/store/store.go:1.1,2.1 10 1",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	module := coverageModule{
		Owner:       "team",
		DefaultRisk: "normal",
		PackagePolicies: []coveragePackagePolicy{
			{
				Package: "example.test/module/internal/auth", Risk: "critical", Owner: "security",
				MinimumStatementPercent: 0, PRMinimumStatement: 80, TargetStatementPercent: 80,
			},
			{
				Package: "example.test/module/internal/store", Risk: "high",
				MinimumStatementPercent: 70, TargetStatementPercent: 70,
			},
		},
	}
	raw, governed, packages, err := analyzeGoCoverageProfile(profile, module, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if raw != 50 || governed != 50 || len(packages) != 2 {
		t.Fatalf("coverage = %v/%v, packages=%#v", raw, governed, packages)
	}
	if packages[0].Status != "FAIL" || packages[0].Owner != "security" ||
		!strings.Contains(packages[0].Assessment, "below") {
		t.Fatalf("failed package = %#v", packages[0])
	}
	if packages[1].Status != "PASS" || packages[1].Assessment != "ratchet and risk target passed" {
		t.Fatalf("passing package = %#v", packages[1])
	}
	module.PackagePolicies[1].TargetStatementPercent = 101
	_, _, packages, err = analyzeGoCoverageProfile(profile, module, "unit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packages[1].Assessment, "target remains tracked debt") {
		t.Fatalf("target debt assessment = %#v", packages[1])
	}
	if coveragePercent(1, 0) != 0 {
		t.Fatal("zero-statement coverage must be zero")
	}

	badProfile := filepath.Join(dir, "bad.out")
	if err := os.WriteFile(badProfile, []byte("mode: set\nnot a coverage row\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readUniqueGoCoverageBlocks(badProfile); err == nil {
		t.Fatal("malformed coverage row accepted")
	}
	if _, err := readUniqueGoCoverageBlocks(filepath.Join(dir, "missing.out")); err == nil {
		t.Fatal("missing coverage profile accepted")
	}
	if _, _, _, err := analyzeGoCoverageProfile(filepath.Join(dir, "missing.out"), module, "unit"); err == nil {
		t.Fatal("missing coverage profile was accepted by analyzer")
	}
}

func TestGoTestInventoryHandlesFailuresSkipsAndMalformedEvents(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/module\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testDir := filepath.Join(moduleDir, "pkg")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "thing_test.go"), []byte("package pkg\n\nfunc TestThing(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.json")
	rows := []goTestEvent{
		{Time: "2026-07-24T00:00:00Z", Action: "run", Package: "example.test/module/pkg", Test: "TestThing"},
		{Time: "2026-07-24T00:00:00.1Z", Action: "fail", Package: "example.test/module/pkg", Test: "TestThing", Elapsed: .1},
		{Time: "2026-07-24T00:00:00.2Z", Action: "skip", Package: "external.test/pkg", Test: "TestSkipped", Elapsed: .2},
		{Time: "2026-07-24T00:00:00.3Z", Action: "output", Package: "example.test/module/pkg", Test: "TestIgnored"},
		{Time: "2026-07-24T00:00:00.4Z", Action: "pass", Package: "example.test/module/pkg"},
	}
	var encoded strings.Builder
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		encoded.Write(raw)
		encoded.WriteByte('\n')
	}
	if err := os.WriteFile(events, []byte(encoded.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	units, err := parseGoTestEvents(events, moduleDir, coverageModule{
		Name: "module",
		CriticalCases: []coverageCriticalCase{{
			TestID: "UNIT-TEST-001", CanonicalKey: "go://module/pkg#TestThing",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]goUnitResult{}
	for _, unit := range units {
		byKey[unit.CanonicalKey] = unit
	}
	if len(units) != 2 || byKey["go://module/pkg#TestThing"].Status != "FAIL" ||
		byKey["go://module/pkg#TestThing"].TestID != "UNIT-TEST-001" ||
		byKey["go://module/external.test/pkg#TestSkipped"].Status != "SKIP" ||
		byKey["go://module/external.test/pkg#TestSkipped"].Source != "" {
		t.Fatalf("units = %#v", units)
	}
	junit := string(renderGoJUnit("module<&", units))
	for _, expected := range []string{"failures=\"1\"", "skipped=\"1\"", "<failure", "<skipped/>", "module&lt;&amp;"} {
		if !strings.Contains(junit, expected) {
			t.Fatalf("JUnit missing %q:\n%s", expected, junit)
		}
	}
	if err := validateRequiredGoTests(coverageModule{PRRequiredTests: []string{"TestMissing"}}, units); err == nil {
		t.Fatal("missing required test accepted")
	}

	if err := os.WriteFile(events, []byte("{bad json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseGoTestEvents(events, moduleDir, coverageModule{Name: "module"}); err == nil {
		t.Fatal("malformed Go event accepted")
	}
	if _, err := parseGoTestEvents(filepath.Join(moduleDir, "missing.json"), moduleDir, coverageModule{Name: "module"}); err == nil {
		t.Fatal("missing Go event stream accepted")
	}
	emptyEvents := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(emptyEvents, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseGoTestEvents(emptyEvents, t.TempDir(), coverageModule{Name: "module"}); err == nil {
		t.Fatal("event inventory without go.mod accepted")
	}
}

func TestRuntimeCoverageValidationCoversIncompleteLayoutsAndUnknownModules(t *testing.T) {
	root := t.TempDir()
	if _, err := runtimeCoverageDirs(root); err == nil || !strings.Contains(err.Error(), "no covmeta") {
		t.Fatalf("empty runtime error = %v", err)
	}
	counterOnly := filepath.Join(root, "counter-only")
	if err := os.MkdirAll(counterOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(counterOnly, "covcounters.hash.1.1"), []byte("counter"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaOnly := filepath.Join(root, "meta-only")
	if err := os.MkdirAll(metaOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaOnly, "covmeta.hash"), []byte("meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeCoverageDirs(root); err == nil || !strings.Contains(err.Error(), "no counters") {
		t.Fatalf("mixed incomplete runtime error = %v", err)
	}
	counterMismatch := t.TempDir()
	complete := filepath.Join(counterMismatch, "complete")
	orphan := filepath.Join(counterMismatch, "orphan")
	for _, dir := range []string{complete, orphan} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(complete, "covmeta.hash"), []byte("meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(complete, "covcounters.hash.1.1"), []byte("counter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "covcounters.hash.1.1"), []byte("counter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeCoverageDirs(counterMismatch); err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Fatalf("orphan counter error = %v", err)
	}

	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	module := coverageModule{Name: "workspace-tooling", Path: "scripts/go"}
	if _, _, err := validateRuntimeCoverageAnchor(workspace, module, t.TempDir(), "run"); err == nil {
		t.Fatal("missing runtime anchor accepted")
	}
	anchorRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(anchorRoot, "coverage-runtime.json"), []byte("{bad json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRuntimeCoverageAnchor(workspace, module, anchorRoot, "run"); err == nil {
		t.Fatal("malformed runtime anchor accepted")
	}
	if err := writeJSON(filepath.Join(anchorRoot, "coverage-runtime.json"), runtimeCoverageAnchor{
		SchemaVersion: 1, RunID: "other", Module: module.Name,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRuntimeCoverageAnchor(workspace, module, anchorRoot, "run"); err == nil ||
		!strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete anchor error = %v", err)
	}

	outDir := t.TempDir()
	report := coverageReport{
		SchemaVersion: 2, RunID: "unknown-runtime", Profile: "runtime",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := runRuntimeCoverage(workspace, outDir, coverageConfig{}, report, map[string]bool{"missing": true}, root); err == nil ||
		!strings.Contains(err.Error(), "unknown or non-Go") {
		t.Fatalf("unknown runtime module error = %v", err)
	}
	result := coverageCaseResult{StartedAt: "not-a-time"}
	completeRuntimeCoverageCase(&result)
	if result.CompletedAt == "" {
		t.Fatal("runtime completion timestamp missing")
	}
}

func TestRuntimeCoverageModuleFilterDoesNotExpandAfterSelectedModule(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	report := coverageReport{
		SchemaVersion: 2, RunID: "filtered-runtime", Profile: "runtime",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	cfg := coverageConfig{Modules: []coverageModule{
		{Name: "workspace-tooling", Kind: "go", Path: "scripts/go"},
		{Name: "godaddy-dns-toolkit", Kind: "go", Path: "repos/rtk_video_cloud/tools/godaddy-dns"},
	}}
	err = runRuntimeCoverage(
		workspace,
		outDir,
		cfg,
		report,
		map[string]bool{"workspace-tooling": true},
		t.TempDir(),
	)
	if err == nil {
		t.Fatal("missing selected runtime evidence unexpectedly passed")
	}
	raw, readErr := os.ReadFile(filepath.Join(outDir, "results.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result coverageReport
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Cases) != 1 || result.Cases[0].Name != "workspace-tooling" {
		t.Fatalf("runtime filter expanded after selected module: %#v", result.Cases)
	}
}

func TestRunCoverageJSONCommandAndEvidenceCopy(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "test.log")
	eventsPath := filepath.Join(dir, "events", "test.json")
	if err := runCoverageJSONCommand(dir, logPath, eventsPath, map[string]string{"COVERAGE_TEST_VALUE": "ok"},
		"sh", "-c", `printf '{"Action":"pass"}\n'; printf '%s' "$COVERAGE_TEST_VALUE" >&2`); err != nil {
		t.Fatal(err)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	eventsRaw, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logRaw), "ok") || !strings.Contains(string(eventsRaw), `"pass"`) {
		t.Fatalf("log=%q events=%q", logRaw, eventsRaw)
	}
	if err := runCoverageJSONCommand(dir, logPath, eventsPath, nil, "sh", "-c", "exit 7"); err == nil {
		t.Fatal("failed coverage command returned nil")
	}

	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	rel := "modules/example/coverage.out"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(source, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, rel), []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := fileSHA256(filepath.Join(source, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := copyCoverageCaseEvidence(source, target, coverageCaseResult{
		ProfilePath: rel,
		Evidence:    []coverageArtifactEvidence{{Path: rel, SHA256: sha}},
	}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(target, rel)) {
		t.Fatal("coverage evidence was not copied")
	}
	if err := copyCoverageCaseEvidence(source, target, coverageCaseResult{ProfilePath: "missing"}); err == nil {
		t.Fatal("missing coverage evidence accepted")
	}
}

func TestRuntimeCoverageEmptySelectionWritesPassingReport(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	report := coverageReport{
		SchemaVersion: 2, RunID: "empty-runtime", Profile: "runtime",
		StartedAt: "not-a-time",
	}
	cfg := coverageConfig{Modules: []coverageModule{{Name: "node-only", Kind: "node"}}}
	if err := runRuntimeCoverage(workspace, outDir, cfg, report, map[string]bool{}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result coverageReport
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "PASS" || result.Assessment == "" || result.RedactionStatus != "PASS" {
		t.Fatalf("empty runtime report = %#v", result)
	}
}

func TestCoverageAggregateRejectsMissingAndMalformedInputs(t *testing.T) {
	if err := runTestCoverageAggregate(nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing aggregate flags error = %v", err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "aggregate-missing-modules"
	defer os.RemoveAll(filepath.Join(workspace, ".artifacts", "test-runs", runID))
	if err := runTestCoverageAggregate([]string{
		"--input-dir", t.TempDir(), "--run-id", runID,
	}); err == nil {
		t.Fatal("aggregate accepted missing module results")
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".artifacts", "test-runs", runID, "coverage", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report coverageReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	cfg, configErr := loadCoverageConfig(workspace)
	if configErr != nil {
		t.Fatal(configErr)
	}
	if report.Status != "FAIL" || len(report.Cases) != len(cfg.Modules) || report.Cases[0].Status != "INCOMPLETE" {
		t.Fatalf("missing-module aggregate = %#v", report)
	}

	malformed := t.TempDir()
	if err := os.WriteFile(filepath.Join(malformed, "results.json"), []byte("{bad json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runTestCoverageAggregate([]string{
		"--input-dir", malformed, "--run-id", "aggregate-malformed",
	}); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("malformed aggregate error = %v", err)
	}
}

func TestPRCoverageRequiresEnvironmentBeforeExecutingGo(t *testing.T) {
	workspace := t.TempDir()
	moduleDir := filepath.Join(workspace, "module")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/module\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "COVERAGE_GOVERNANCE_REQUIRED_ENV"
	t.Setenv(name, "")
	result := coverageCaseResult{}
	err := runGoCoverageModuleProfile(workspace, t.TempDir(), filepath.Join(t.TempDir(), "coverage.log"),
		coverageConfig{}, coverageModule{
			Name: "module", Path: "module", Packages: []string{"./..."},
			PRRequiredEnv: []string{name}, CoverPackages: []string{"./..."},
		}, "", "HEAD", "pr", &result)
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("missing PR environment error = %v", err)
	}
}

func TestChangedGoLinesResolvesWorkspaceSubmoduleGitlinks(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedGoLines(workspace, "HEAD^", "HEAD", "repos/rtk_cloud_admin")
	if err != nil {
		t.Fatal(err)
	}
	if changed == nil {
		t.Fatal("submodule differential returned nil")
	}
}

func TestEnsureGitCommitFetchesMissingCommitFromOrigin(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	if _, err := gitOutput(root, "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "coverage@example.test"},
		{"config", "user.name", "Coverage Test"},
		{"remote", "add", "origin", remote},
	} {
		if _, err := gitOutput(source, args...); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(source, "value.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(source, "add", "value.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(source, "commit", "-m", "one"); err != nil {
		t.Fatal(err)
	}
	first, err := gitOutput(source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(source, "commit", "-am", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(source, "push", "origin", "HEAD:main"); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "clone")
	if _, err := gitOutput(root, "clone", "--depth=1", "--branch", "main", "file://"+remote, clone); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(clone, "cat-file", "-e", strings.TrimSpace(first)+"^{commit}"); err == nil {
		t.Fatal("shallow clone unexpectedly contains the old commit")
	}
	if err := ensureGitCommit(clone, strings.TrimSpace(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(clone, "cat-file", "-e", strings.TrimSpace(first)+"^{commit}"); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitCommit(clone, "not-a-commit"); err == nil || !strings.Contains(err.Error(), "fetch missing") {
		t.Fatalf("missing commit error = %v", err)
	}
}

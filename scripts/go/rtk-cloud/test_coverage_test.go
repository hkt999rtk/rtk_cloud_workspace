package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseGoCoverageProfileCalculatesStatementAndDifferentialCoverage(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "coverage.out")
	raw := "mode: set\n" +
		"example/module/a.go:10.1,12.2 2 1\n" +
		"example/module/a.go:14.1,14.8 1 0\n" +
		"example/module/b.go:3.1,3.9 3 1\n"
	if err := os.WriteFile(profile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	total, covered, statements, err := parseGoCoverageProfile(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if covered != 5 || statements != 6 || math.Abs(total-83.333333) > 0.001 {
		t.Fatalf("overall coverage = %.4f (%d/%d), want 83.3333 (5/6)", total, covered, statements)
	}
	changed := map[string]map[int]bool{
		"example/module/a.go": {11: true, 14: true},
	}
	total, covered, statements, err = parseGoCoverageProfile(profile, changed)
	if err != nil {
		t.Fatal(err)
	}
	if covered != 2 || statements != 3 || math.Abs(total-66.666667) > 0.001 {
		t.Fatalf("differential coverage = %.4f (%d/%d), want 66.6667 (2/3)", total, covered, statements)
	}
}

func TestParseChangedGoLinesTracksOnlyAddedGoLines(t *testing.T) {
	diff := "diff --git a/scripts/go/a.go b/scripts/go/a.go\n" +
		"--- a/scripts/go/a.go\n" +
		"+++ b/scripts/go/a.go\n" +
		"@@ -4,2 +4,3 @@\n" +
		"+one\n+two\n+three\n" +
		"diff --git a/docs/readme.md b/docs/readme.md\n" +
		"--- a/docs/readme.md\n" +
		"+++ b/docs/readme.md\n" +
		"@@ -1 +1,2 @@\n"
	changed, err := parseChangedGoLines(diff)
	if err != nil {
		t.Fatal(err)
	}
	lines := changed["scripts/go/a.go"]
	for _, line := range []int{4, 5, 6} {
		if !lines[line] {
			t.Fatalf("changed Go line %d was not recorded: %#v", line, lines)
		}
	}
	if _, ok := changed["docs/readme.md"]; ok {
		t.Fatalf("non-Go file was recorded: %#v", changed)
	}
}

func TestParseNodeCoverageSummary(t *testing.T) {
	output := "ℹ all files |  92.89 |    79.75 |   94.02 | \n"
	line, branch, function, err := parseNodeCoverageSummary(output)
	if err != nil {
		t.Fatal(err)
	}
	if line != 92.89 || branch != 79.75 || function != 94.02 {
		t.Fatalf("Node coverage = %.2f/%.2f/%.2f", line, branch, function)
	}
}

func TestGoDifferentialCoverageMapsWorkspacePathsToModuleProfile(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example/module\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(moduleDir, "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: set\nexample/module/pkg/a.go:5.1,5.8 2 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := map[string]map[int]bool{
		"scripts/go/pkg/a.go": {5: true},
	}
	total, covered, statements, err := goDifferentialCoverage(moduleDir, "scripts/go", profile, changed)
	if err != nil {
		t.Fatal(err)
	}
	if total != 100 || covered != 2 || statements != 2 {
		t.Fatalf("differential coverage = %.2f (%d/%d), want 100 (2/2)", total, covered, statements)
	}
}

func TestLoadCoverageConfigLinksEveryModuleToCatalog(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 3 || cfg.Differential.MinimumStatementPercent != 80 {
		t.Fatalf("coverage config header = %#v", cfg)
	}
	if len(cfg.Modules) != 12 {
		t.Fatalf("coverage policy entries = %d, want 12 (10 Go and 2 Node)", len(cfg.Modules))
	}
	for _, module := range cfg.Modules {
		if module.TestID == "" || module.Name == "" || module.Purpose == "" || module.Method == "" {
			t.Fatalf("incomplete coverage module: %#v", module)
		}
	}
}

func TestRenderCoverageReportIncludesMetricsAndEvidence(t *testing.T) {
	statement := 75.5
	minimum := 75.0
	changed := 90.0
	changedMinimum := 80.0
	report := coverageReport{
		RunID:           "coverage-test",
		WorkspaceCommit: "abc123",
		BaseRef:         "origin/main",
		HeadRef:         "HEAD",
		StartedAt:       time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		CompletedAt:     time.Date(2026, 7, 24, 0, 0, 1, 0, time.UTC).Format(time.RFC3339),
		DurationMS:      1000,
		Status:          "PASS",
		Assessment:      "coverage passed",
		Cases: []coverageCaseResult{{
			TestID: "SVC-HOME-LOAD-001", Name: "home-load-runner", Purpose: "purpose", Method: "method",
			Status: "PASS", Assessment: "case passed", DurationMS: 500, LogPath: "logs/home.log",
			ProfilePath: "profiles/home.out", ProfileSHA: "deadbeef",
			Metrics: coverageMetrics{
				StatementPercent: &statement, StatementMinimumPercent: &minimum,
				ChangedStatementPercent: &changed, ChangedStatementMinimum: &changedMinimum,
				ChangedStatements: 10, CoveredChangedStatements: 9,
				UncoveredChanged: []string{"loadtests/home-100k/example.go:12"},
			},
		}},
	}
	rendered := string(renderCoverageReport(report))
	for _, want := range []string{
		"Result: **PASS**",
		"75.50% ≥ 75.00%",
		"90.00% (9/10) ≥ 80.00%",
		"loadtests/home-100k/example.go:12",
		"profiles/home.out",
		"deadbeef",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("coverage report missing %q:\n%s", want, rendered)
		}
	}
}

func TestRunCoverageModuleProfileReportsConfiguredTargetDebt(t *testing.T) {
	target := 65.0
	targetMet := false
	metrics := coverageMetrics{
		StatementTargetPercent: &target,
		IndustryTargetMet:      &targetMet,
	}
	assessment := passingCoverageAssessment(metrics)
	if !strings.Contains(assessment, "65.00% target remains tracked debt") {
		t.Fatalf("assessment = %q, want configured target", assessment)
	}
}

func TestCoverageParsersRejectMalformedInput(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "invalid.out")
	if err := os.WriteFile(profile, []byte("mode: set\nnot-a-profile-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := parseGoCoverageProfile(profile, nil); err == nil {
		t.Fatal("malformed Go profile unexpectedly passed")
	}
	if _, _, _, err := parseNodeCoverageSummary("no coverage summary"); err == nil {
		t.Fatal("missing Node summary unexpectedly passed")
	}
	if _, err := goModuleName(filepath.Join(t.TempDir(), "missing.mod")); err == nil {
		t.Fatal("missing go.mod unexpectedly passed")
	}
}

func TestRunCoverageModuleExecutesGoCoverageAndWritesEvidence(t *testing.T) {
	workspace := t.TempDir()
	moduleDir := filepath.Join(workspace, "module")
	outDir := filepath.Join(workspace, "artifacts")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":          "module example/covered\n\ngo 1.25\n",
		"covered.go":      "package covered\n\nfunc Add(a, b int) int { return a + b }\n",
		"covered_test.go": "package covered\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"bad sum\") } }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(moduleDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := coverageConfig{}
	cfg.Differential.MinimumStatementPercent = 80
	module := coverageModule{
		TestID: "SVC-TEST-SUITE-001", Name: "covered-go", Kind: "go", Path: "module",
		Packages: []string{"./..."}, MinimumStatementPercent: 100, TargetStatementPercent: 100,
		Purpose: "test purpose", Method: "test method", CriticalCommand: []string{"go", "test", "./..."},
	}
	result := runCoverageModule(workspace, outDir, cfg, module, "", "HEAD", false)
	if result.Status != "PASS" || result.ProfileSHA == "" || result.Metrics.StatementPercent == nil || *result.Metrics.StatementPercent != 100 {
		t.Fatalf("Go coverage result = %#v", result)
	}
	for _, relative := range []string{result.ProfilePath, result.LogPath} {
		if stat, err := os.Stat(filepath.Join(outDir, filepath.FromSlash(relative))); err != nil || stat.Size() == 0 {
			t.Fatalf("coverage evidence %s: stat=%v err=%v", relative, stat, err)
		}
	}
	var logSHA string
	for _, evidence := range result.Evidence {
		if evidence.Path == result.LogPath {
			logSHA = evidence.SHA256
		}
	}
	currentLogSHA, err := fileSHA256(filepath.Join(outDir, filepath.FromSlash(result.LogPath)))
	if err != nil {
		t.Fatal(err)
	}
	if logSHA == "" || logSHA != currentLogSHA {
		t.Fatalf("test log SHA = %q, current SHA = %q", logSHA, currentLogSHA)
	}
}

func TestRunCoverageModuleExecutesNodeMetricGates(t *testing.T) {
	workspace := t.TempDir()
	moduleDir := filepath.Join(workspace, "web")
	outDir := filepath.Join(workspace, "artifacts")
	if err := os.MkdirAll(filepath.Join(moduleDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "scripts", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	reporter, err := os.ReadFile(filepath.Join("..", "..", "node", "test-event-reporter.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "scripts", "node", "test-event-reporter.mjs"), reporter, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "src", "value.mjs"), []byte("export const value = () => 42;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testBody := "import test from 'node:test';\nimport assert from 'node:assert/strict';\nimport { value } from './value.mjs';\ntest('value', () => assert.equal(value(), 42));\n"
	if err := os.WriteFile(filepath.Join(moduleDir, "src", "value.test.mjs"), []byte(testBody), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger := "schema_version: 1\ncases:\n  - canonical_key: js://covered-node/src/value.test.mjs#value\n    module: covered-node\n    language: javascript\n    source: src/value.test.mjs\n    title: value\n    owner: test\n    status: active\n"
	if err := os.WriteFile(filepath.Join(workspace, "tests", "unit-inventory.yaml"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	module := coverageModule{
		TestID: "SVC-TEST-SUITE-001", Name: "covered-node", Kind: "node", Path: "web",
		Build: []string{"node", "--check", "src/value.mjs"}, SourceTestGlobs: []string{"src/*.test.mjs"},
		RuntimeTestGlobs: []string{"src/*.test.mjs"}, Owner: "test",
		MinimumLinePercent: 100, MinimumBranchPercent: 100, MinimumFunctionPercent: 100,
		Purpose: "test purpose", Method: "test method",
	}
	result := runCoverageModule(workspace, outDir, coverageConfig{}, module, "", "HEAD", false)
	if result.Status != "PASS" || result.Metrics.LinePercent == nil || *result.Metrics.LinePercent != 100 {
		t.Fatalf("Node coverage result = %#v", result)
	}
}

func TestValidateCoverageConfigRejectsInvalidPolicies(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "module"), 0o755); err != nil {
		t.Fatal(err)
	}
	valid := func() coverageConfig {
		cfg := coverageConfig{SchemaVersion: 3, RiskThresholds: map[string]float64{"critical": 80, "high": 70, "normal": 60}}
		cfg.Differential.MinimumStatementPercent = 80
		cfg.Modules = []coverageModule{{
			TestID: "SVC-TEST-SUITE-001", Name: "module", Kind: "go", Path: "module",
			Packages: []string{"./..."}, MinimumStatementPercent: 60, TargetStatementPercent: 80,
			Owner: "cloud_platform", DefaultRisk: "normal", Purpose: "purpose", Method: "method",
		}}
		return cfg
	}
	tests := map[string]func(*coverageConfig){
		"schema":           func(cfg *coverageConfig) { cfg.SchemaVersion = 1 },
		"differential":     func(cfg *coverageConfig) { cfg.Differential.MinimumStatementPercent = 0 },
		"risk threshold":   func(cfg *coverageConfig) { cfg.RiskThresholds["critical"] = 79 },
		"missing identity": func(cfg *coverageConfig) { cfg.Modules[0].Name = "" },
		"duplicate name":   func(cfg *coverageConfig) { cfg.Modules = append(cfg.Modules, cfg.Modules[0]) },
		"inactive ID":      func(cfg *coverageConfig) { cfg.Modules[0].TestID = "SVC-NO-SUITE-001" },
		"absolute path":    func(cfg *coverageConfig) { cfg.Modules[0].Path = "/tmp/module" },
		"missing purpose":  func(cfg *coverageConfig) { cfg.Modules[0].Purpose = "" },
		"missing packages": func(cfg *coverageConfig) { cfg.Modules[0].Packages = nil },
		"invalid target":   func(cfg *coverageConfig) { cfg.Modules[0].TargetStatementPercent = 50 },
		"missing owner":    func(cfg *coverageConfig) { cfg.Modules[0].Owner = "" },
		"invalid default risk": func(cfg *coverageConfig) {
			cfg.Modules[0].DefaultRisk = "urgent"
		},
		"invalid package policy": func(cfg *coverageConfig) {
			cfg.Modules[0].PackagePolicies = []coveragePackagePolicy{{Package: "", Risk: "high"}}
		},
		"duplicate package policy": func(cfg *coverageConfig) {
			policy := coveragePackagePolicy{Package: "example/module", Risk: "high", TargetStatementPercent: 70}
			cfg.Modules[0].PackagePolicies = []coveragePackagePolicy{policy, policy}
		},
		"wiring without reason": func(cfg *coverageConfig) {
			cfg.Modules[0].PackagePolicies = []coveragePackagePolicy{{Package: "example/module", Risk: "wiring"}}
		},
		"invalid package threshold": func(cfg *coverageConfig) {
			cfg.Modules[0].PackagePolicies = []coveragePackagePolicy{{
				Package: "example/module", Risk: "high", MinimumStatementPercent: 101, TargetStatementPercent: 70,
			}}
		},
		"package target below risk": func(cfg *coverageConfig) {
			cfg.Modules[0].PackagePolicies = []coveragePackagePolicy{{
				Package: "example/module", Risk: "critical", MinimumStatementPercent: 60, TargetStatementPercent: 70,
			}}
		},
		"invalid critical case": func(cfg *coverageConfig) {
			cfg.Modules[0].CriticalCases = []coverageCriticalCase{{
				TestID: "UNIT-NO-CASE-001", CanonicalKey: "", Purpose: "",
			}}
		},
		"unsupported kind": func(cfg *coverageConfig) { cfg.Modules[0].Kind = "rust" },
		"missing path":     func(cfg *coverageConfig) { cfg.Modules[0].Path = "missing" },
		"node missing globs": func(cfg *coverageConfig) {
			cfg.Modules[0].Kind = "node"
			cfg.Modules[0].Packages = nil
			cfg.Modules[0].MinimumLinePercent = 80
			cfg.Modules[0].MinimumBranchPercent = 80
			cfg.Modules[0].MinimumFunctionPercent = 80
		},
		"node invalid threshold": func(cfg *coverageConfig) {
			cfg.Modules[0].Kind = "node"
			cfg.Modules[0].Packages = nil
			cfg.Modules[0].SourceTestGlobs = []string{"*.test.mjs"}
			cfg.Modules[0].RuntimeTestGlobs = []string{"*.test.mjs"}
			cfg.Modules[0].MinimumLinePercent = 80
			cfg.Modules[0].MinimumBranchPercent = 0
			cfg.Modules[0].MinimumFunctionPercent = 80
		},
	}
	active := map[string]bool{"SVC-TEST-SUITE-001": true}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid()
			mutate(&cfg)
			if err := validateCoverageConfig(workspace, cfg, active); err == nil {
				t.Fatalf("invalid policy unexpectedly passed: %#v", cfg)
			}
		})
	}
	if err := validateCoverageConfig(workspace, valid(), active); err != nil {
		t.Fatalf("valid policy failed: %v", err)
	}
	node := valid()
	node.Modules[0].Kind = "node"
	node.Modules[0].Packages = nil
	node.Modules[0].SourceTestGlobs = []string{"*.test.mjs"}
	node.Modules[0].RuntimeTestGlobs = []string{"*.test.mjs"}
	node.Modules[0].MinimumLinePercent = 80
	node.Modules[0].MinimumBranchPercent = 80
	node.Modules[0].MinimumFunctionPercent = 80
	if err := validateCoverageConfig(workspace, node, active); err != nil {
		t.Fatalf("valid Node policy failed: %v", err)
	}
}

func TestRunTestCoverageRejectsUnknownModuleBeforeExecutingTests(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "unit-unknown-coverage-module"
	defer os.RemoveAll(filepath.Join(workspace, ".artifacts", "test-runs", runID))
	err = runTestCoverage([]string{"--run-id", runID, "--module", "not-managed"})
	if err == nil || !strings.Contains(err.Error(), "unknown coverage module") {
		t.Fatalf("unknown module error = %v", err)
	}
}

func TestRunTestCoverageRejectsInvalidProfiles(t *testing.T) {
	if err := runTestCoverage([]string{"--profile", "unknown"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported profile error = %v", err)
	}
	if err := runTestCoverage([]string{"--profile", "runtime"}); err == nil || !strings.Contains(err.Error(), "--runtime-dir") {
		t.Fatalf("missing runtime directory error = %v", err)
	}
}

func TestValidateRequiredGoCoverageModulesRejectsMissingModule(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Modules = slices.DeleteFunc(cfg.Modules, func(module coverageModule) bool {
		return module.Name == "video-cloud"
	})
	if err := validateRequiredGoCoverageModules(workspace, cfg); err == nil || !strings.Contains(err.Error(), "video-cloud") {
		t.Fatalf("missing module error = %v", err)
	}
	cfg, err = loadCoverageConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Modules {
		if cfg.Modules[i].Name == "video-cloud" {
			cfg.Modules[i].Path = "repos/rtk_cloud_admin"
		}
	}
	if err := validateRequiredGoCoverageModules(workspace, cfg); err == nil || !strings.Contains(err.Error(), "path must be") {
		t.Fatalf("wrong module path error = %v", err)
	}
}

func TestRunTestCoverageWritesPassingReportForSelectedModule(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "unit-passing-coverage-report"
	runDir := filepath.Join(workspace, ".artifacts", "test-runs", runID)
	defer os.RemoveAll(runDir)

	if err := runTestCoverage([]string{"--run-id", runID, "--module", "home-load-runner"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "coverage", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report coverageReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASS" || report.RedactionStatus != "PASS" || len(report.Cases) != 1 {
		t.Fatalf("coverage report = %#v", report)
	}
	if report.Cases[0].Name != "home-load-runner" || report.Cases[0].Status != "PASS" {
		t.Fatalf("selected coverage case = %#v", report.Cases[0])
	}
	markdown, err := os.ReadFile(filepath.Join(runDir, "coverage", "TEST_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "Result: **PASS**") {
		t.Fatalf("coverage Markdown did not record PASS:\n%s", markdown)
	}
}

func TestRunCoverageModuleClassifiesUnsupportedKindAsFailure(t *testing.T) {
	result := runCoverageModule(t.TempDir(), t.TempDir(), coverageConfig{}, coverageModule{
		TestID:  "SVC-TEST-SUITE-001",
		Name:    "unsupported",
		Kind:    "rust",
		Path:    "module",
		Purpose: "verify unsupported runner handling",
		Method:  "request an unsupported coverage kind",
	}, "", "HEAD", false)
	if result.Status != "FAIL" || !strings.Contains(result.Assessment, "unsupported coverage kind") {
		t.Fatalf("unsupported coverage result = %#v", result)
	}
	if result.StartedAt == "" || result.CompletedAt == "" || result.LogPath == "" {
		t.Fatalf("failure result lacks traceability fields: %#v", result)
	}
}

func TestChangedGoLinesHandlesEmptyGitDiff(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedGoLines(workspace, "HEAD", "HEAD", "scripts/go")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("HEAD...HEAD changed lines = %#v", changed)
	}
}

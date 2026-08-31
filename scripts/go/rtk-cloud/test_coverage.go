package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type coverageConfig struct {
	SchemaVersion int `yaml:"schema_version"`
	Differential  struct {
		MinimumStatementPercent float64 `yaml:"minimum_statement_percent"`
	} `yaml:"differential"`
	RiskThresholds map[string]float64 `yaml:"risk_thresholds,omitempty"`
	Modules        []coverageModule   `yaml:"modules"`
}

type coverageModule struct {
	TestID                  string                  `yaml:"test_id"`
	Name                    string                  `yaml:"name"`
	Kind                    string                  `yaml:"kind"`
	Path                    string                  `yaml:"path"`
	Packages                []string                `yaml:"packages,omitempty"`
	Build                   []string                `yaml:"build,omitempty"`
	TestGlobs               []string                `yaml:"test_globs,omitempty"`
	SourceTestGlobs         []string                `yaml:"source_test_globs,omitempty"`
	RuntimeTestGlobs        []string                `yaml:"runtime_test_globs,omitempty"`
	SourceRewrites          []coverageSourceRewrite `yaml:"source_rewrites,omitempty"`
	CriticalCommand         []string                `yaml:"critical_command,omitempty"`
	MinimumStatementPercent float64                 `yaml:"minimum_statement_percent,omitempty"`
	PRMinimumStatement      float64                 `yaml:"pr_minimum_statement_percent,omitempty"`
	TargetStatementPercent  float64                 `yaml:"target_statement_percent,omitempty"`
	MinimumLinePercent      float64                 `yaml:"minimum_line_percent,omitempty"`
	MinimumBranchPercent    float64                 `yaml:"minimum_branch_percent,omitempty"`
	MinimumFunctionPercent  float64                 `yaml:"minimum_function_percent,omitempty"`
	Differential            bool                    `yaml:"differential,omitempty"`
	Owner                   string                  `yaml:"owner,omitempty"`
	DefaultRisk             string                  `yaml:"default_risk,omitempty"`
	CoverPackages           []string                `yaml:"cover_packages,omitempty"`
	PRRequiredEnv           []string                `yaml:"pr_required_env,omitempty"`
	PRRequiredTests         []string                `yaml:"pr_required_tests,omitempty"`
	PackagePolicies         []coveragePackagePolicy `yaml:"package_policies,omitempty"`
	CriticalCases           []coverageCriticalCase  `yaml:"critical_cases,omitempty"`
	Purpose                 string                  `yaml:"purpose"`
	Method                  string                  `yaml:"method"`
}

type coverageSourceRewrite struct {
	FromPrefix string `yaml:"from_prefix"`
	ToPrefix   string `yaml:"to_prefix"`
	Extension  string `yaml:"extension,omitempty"`
}

type coveragePackagePolicy struct {
	Package                 string  `yaml:"package"`
	Risk                    string  `yaml:"risk"`
	MinimumStatementPercent float64 `yaml:"minimum_statement_percent,omitempty"`
	PRMinimumStatement      float64 `yaml:"pr_minimum_statement_percent,omitempty"`
	TargetStatementPercent  float64 `yaml:"target_statement_percent,omitempty"`
	ExclusionReason         string  `yaml:"exclusion_reason,omitempty"`
	Owner                   string  `yaml:"owner,omitempty"`
}

type coverageCriticalCase struct {
	TestID       string `yaml:"test_id"`
	CanonicalKey string `yaml:"canonical_key"`
	Purpose      string `yaml:"purpose"`
}

type coveragePackageResult struct {
	Package                  string   `json:"package"`
	Risk                     string   `json:"risk"`
	Owner                    string   `json:"owner,omitempty"`
	RawStatementPercent      float64  `json:"raw_statement_percent"`
	GovernedStatementPercent *float64 `json:"governed_statement_percent,omitempty"`
	MinimumStatementPercent  *float64 `json:"minimum_statement_percent,omitempty"`
	TargetStatementPercent   *float64 `json:"target_statement_percent,omitempty"`
	CoveredStatements        int      `json:"covered_statements"`
	Statements               int      `json:"statements"`
	Status                   string   `json:"status"`
	Assessment               string   `json:"assessment"`
	ExclusionReason          string   `json:"exclusion_reason,omitempty"`
}

type coverageArtifactEvidence struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type coverageMetrics struct {
	RawStatementPercent      *float64                `json:"raw_statement_percent,omitempty"`
	GovernedStatementPercent *float64                `json:"governed_statement_percent,omitempty"`
	GovernedMinimumPercent   *float64                `json:"governed_minimum_percent,omitempty"`
	StatementPercent         *float64                `json:"statement_percent,omitempty"`
	StatementMinimumPercent  *float64                `json:"statement_minimum_percent,omitempty"`
	StatementTargetPercent   *float64                `json:"statement_target_percent,omitempty"`
	ChangedStatementPercent  *float64                `json:"changed_statement_percent,omitempty"`
	ChangedStatementMinimum  *float64                `json:"changed_statement_minimum_percent,omitempty"`
	ChangedStatements        int                     `json:"changed_statements,omitempty"`
	CoveredChangedStatements int                     `json:"covered_changed_statements,omitempty"`
	UncoveredChanged         []string                `json:"uncovered_changed_statements,omitempty"`
	LinePercent              *float64                `json:"line_percent,omitempty"`
	LineMinimumPercent       *float64                `json:"line_minimum_percent,omitempty"`
	BranchPercent            *float64                `json:"branch_percent,omitempty"`
	BranchMinimumPercent     *float64                `json:"branch_minimum_percent,omitempty"`
	FunctionPercent          *float64                `json:"function_percent,omitempty"`
	FunctionMinimumPercent   *float64                `json:"function_minimum_percent,omitempty"`
	IndustryTargetMet        *bool                   `json:"industry_target_met,omitempty"`
	Packages                 []coveragePackageResult `json:"packages,omitempty"`
}

type coverageCaseResult struct {
	TestID              string                     `json:"test_id"`
	Name                string                     `json:"name"`
	Kind                string                     `json:"kind"`
	Path                string                     `json:"path"`
	Purpose             string                     `json:"purpose"`
	Method              string                     `json:"method"`
	StartedAt           string                     `json:"started_at"`
	CompletedAt         string                     `json:"completed_at"`
	DurationMS          int64                      `json:"duration_ms"`
	Status              string                     `json:"status"`
	Assessment          string                     `json:"assessment"`
	Metrics             coverageMetrics            `json:"metrics"`
	ProfilePath         string                     `json:"profile_path,omitempty"`
	ProfileSHA          string                     `json:"profile_sha256,omitempty"`
	LogPath             string                     `json:"log_path"`
	PackageCoveragePath string                     `json:"package_coverage_path,omitempty"`
	UnitManifestPath    string                     `json:"unit_manifest_path,omitempty"`
	JUnitPath           string                     `json:"junit_path,omitempty"`
	TestEventsPath      string                     `json:"test_events_path,omitempty"`
	SubmoduleCommit     string                     `json:"submodule_commit,omitempty"`
	RuntimeEvidencePath string                     `json:"runtime_evidence_path,omitempty"`
	RuntimeEvidenceSHA  string                     `json:"runtime_evidence_sha256,omitempty"`
	Evidence            []coverageArtifactEvidence `json:"evidence,omitempty"`
	CriticalGate        string                     `json:"critical_gate,omitempty"`
}

type coverageReport struct {
	SchemaVersion   int                  `json:"schema_version"`
	RunID           string               `json:"run_id"`
	Profile         string               `json:"profile"`
	WorkspaceCommit string               `json:"workspace_commit"`
	BaseRef         string               `json:"base_ref,omitempty"`
	HeadRef         string               `json:"head_ref"`
	StartedAt       string               `json:"started_at"`
	CompletedAt     string               `json:"completed_at"`
	DurationMS      int64                `json:"duration_ms"`
	Status          string               `json:"status"`
	Assessment      string               `json:"assessment"`
	RedactionStatus string               `json:"redaction_status"`
	RedactionIssues []string             `json:"redaction_issues,omitempty"`
	Cases           []coverageCaseResult `json:"cases"`
}

var goCoverageLinePattern = regexp.MustCompile(`^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)$`)
var diffHunkPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
var nodeCoverageAllFilesPattern = regexp.MustCompile(`all files\s+\|\s*([0-9.]+)\s*\|\s*([0-9.]+)\s*\|\s*([0-9.]+)\s*\|`)

func runTestCoverage(args []string) error {
	fs := flag.NewFlagSet("test-coverage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runID := fs.String("run-id", "", "artifact run ID; defaults to a UTC timestamp")
	baseRef := fs.String("base-ref", "", "base git ref for differential coverage; omit to skip the differential gate")
	headRef := fs.String("head-ref", "HEAD", "head git ref for differential coverage")
	moduleFilter := fs.String("module", "", "comma-separated coverage module names; default runs all")
	profile := fs.String("profile", "unit", "coverage profile: unit, pr, or runtime")
	runtimeDir := fs.String("runtime-dir", "", "GOCOVERDIR root to aggregate with --profile runtime")
	install := fs.Bool("install", false, "install Node dependencies before JavaScript coverage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *profile {
	case "unit", "pr", "runtime":
	default:
		return fmt.Errorf("unsupported coverage profile %q", *profile)
	}
	if *profile == "runtime" && strings.TrimSpace(*runtimeDir) == "" {
		return errors.New("--runtime-dir is required with --profile runtime")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		return err
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	outDir := filepath.Join(workspace, ".artifacts", "test-runs", *runID, "coverage")
	if err := os.MkdirAll(filepath.Join(outDir, "profiles"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(outDir, "logs"), 0o755); err != nil {
		return err
	}
	started := time.Now().UTC()
	report := coverageReport{
		SchemaVersion: 2,
		RunID:         *runID,
		Profile:       *profile,
		BaseRef:       strings.TrimSpace(*baseRef),
		HeadRef:       strings.TrimSpace(*headRef),
		StartedAt:     started.Format(time.RFC3339),
		Status:        "PASS",
	}
	if commit, commitErr := gitOutput(workspace, "rev-parse", "HEAD"); commitErr == nil {
		report.WorkspaceCommit = strings.TrimSpace(commit)
	}
	selected := map[string]bool{}
	for _, name := range strings.Split(*moduleFilter, ",") {
		if name = strings.TrimSpace(name); name != "" {
			selected[name] = true
		}
	}
	filterActive := len(selected) > 0
	if *profile == "runtime" {
		return runRuntimeCoverage(workspace, outDir, cfg, report, selected, strings.TrimSpace(*runtimeDir))
	}
	for _, module := range cfg.Modules {
		if filterActive && !selected[module.Name] {
			continue
		}
		delete(selected, module.Name)
		result := runCoverageModuleProfile(workspace, outDir, cfg, module, report.BaseRef, report.HeadRef, *profile, *install)
		report.Cases = append(report.Cases, result)
		if result.Status != "PASS" {
			report.Status = "FAIL"
		}
	}
	if len(selected) > 0 {
		names := make([]string, 0, len(selected))
		for name := range selected {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown coverage module(s): %s", strings.Join(names, ", "))
	}
	if err := writeCrossLanguageUnitInventory(outDir, report.Cases); err != nil {
		report.Status = "FAIL"
		report.Assessment = "Cross-language unit inventory is incomplete: " + err.Error()
	}
	report.RedactionStatus = "PASS"
	if issues := findUnredactedFeatureEvidence(outDir); len(issues) > 0 {
		report.RedactionStatus = "FAIL"
		report.RedactionIssues = issues
		report.Status = "FAIL"
	}
	completed := time.Now().UTC()
	report.CompletedAt = completed.Format(time.RFC3339)
	report.DurationMS = completed.Sub(started).Milliseconds()
	if report.Status == "PASS" {
		report.Assessment = "All overall ratchets, JavaScript metric gates, differential coverage, and critical-package policies passed."
	} else if report.RedactionStatus == "FAIL" {
		report.Assessment = "Coverage artifacts contain credential-like material and are unsafe to upload."
	} else {
		report.Assessment = "One or more coverage gates failed; inspect case assessments and raw logs."
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), renderCoverageReport(report), 0o644); err != nil {
		return err
	}
	fmt.Printf("Coverage report: %s\n", filepath.Join(outDir, "test_report.md"))
	if report.Status != "PASS" {
		return exitCode(1)
	}
	return nil
}

func loadCoverageConfig(workspace string) (coverageConfig, error) {
	path := filepath.Join(workspace, "tests", "coverage.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return coverageConfig{}, fmt.Errorf("read coverage config: %w", err)
	}
	var cfg coverageConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return coverageConfig{}, fmt.Errorf("parse coverage config: %w", err)
	}
	if cfg.SchemaVersion != 3 {
		return coverageConfig{}, fmt.Errorf("coverage config schema_version must be 3, got %d", cfg.SchemaVersion)
	}
	if cfg.Differential.MinimumStatementPercent <= 0 || cfg.Differential.MinimumStatementPercent > 100 {
		return coverageConfig{}, errors.New("coverage differential minimum must be in (0,100]")
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return coverageConfig{}, err
	}
	activeIDs := map[string]bool{}
	for _, tc := range catalog.Cases {
		if tc.Status == "active" {
			activeIDs[tc.ID] = true
		}
	}
	if err := validateCoverageConfig(workspace, cfg, activeIDs); err != nil {
		return coverageConfig{}, err
	}
	if err := validateRequiredGoCoverageModules(workspace, cfg); err != nil {
		return coverageConfig{}, err
	}
	return cfg, nil
}

func validateRequiredGoCoverageModules(workspace string, cfg coverageConfig) error {
	required := map[string]string{
		"workspace-tooling":   "scripts/go",
		"workspace-e2e":       "e2e_test",
		"home-load-runner":    "loadtests/home-100k",
		"account-manager":     "repos/rtk_account_manager",
		"billing-service":     "repos/rtk_billing",
		"cloud-admin-backend": "repos/rtk_cloud_admin",
		"cloud-client-golang": "repos/rtk_cloud_client/packages/golang",
		"cloud-frontend":      "repos/rtk_cloud_frontend",
		"cloud-logger":        "repos/rtk_cloud_logger",
		"video-cloud":         "repos/rtk_video_cloud",
		"godaddy-dns-toolkit": "repos/rtk_video_cloud/tools/godaddy-dns",
	}
	configured := make(map[string]coverageModule, len(cfg.Modules))
	for _, module := range cfg.Modules {
		if module.Kind == "go" {
			configured[module.Name] = module
		}
	}
	for name, path := range required {
		module, ok := configured[name]
		if !ok {
			return fmt.Errorf("required Go coverage module %q is missing", name)
		}
		if filepath.Clean(module.Path) != filepath.Clean(path) {
			return fmt.Errorf("required Go coverage module %q path must be %s, got %s", name, path, module.Path)
		}
		if !exists(filepath.Join(workspace, path, "go.mod")) {
			return fmt.Errorf("required Go coverage module %q has no go.mod at %s", name, path)
		}
	}
	return nil
}

func validateCoverageConfig(workspace string, cfg coverageConfig, activeIDs map[string]bool) error {
	if cfg.SchemaVersion != 3 {
		return fmt.Errorf("coverage config schema_version must be 3, got %d", cfg.SchemaVersion)
	}
	if cfg.Differential.MinimumStatementPercent <= 0 || cfg.Differential.MinimumStatementPercent > 100 {
		return errors.New("coverage differential minimum must be in (0,100]")
	}
	for risk, target := range map[string]float64{"critical": 80, "high": 70, "normal": 60} {
		if cfg.RiskThresholds[risk] != target {
			return fmt.Errorf("coverage risk threshold %s must be %.1f", risk, target)
		}
	}
	names := map[string]bool{}
	for index, module := range cfg.Modules {
		prefix := fmt.Sprintf("coverage module %d", index+1)
		if strings.TrimSpace(module.Name) == "" || strings.TrimSpace(module.Path) == "" || strings.TrimSpace(module.TestID) == "" {
			return fmt.Errorf("%s requires name, path, and test_id", prefix)
		}
		if names[module.Name] {
			return fmt.Errorf("duplicate coverage module name %q", module.Name)
		}
		names[module.Name] = true
		if !activeIDs[module.TestID] {
			return fmt.Errorf("%s references missing or inactive catalog ID %q", prefix, module.TestID)
		}
		if filepath.IsAbs(module.Path) || strings.Contains(filepath.ToSlash(module.Path), "../") {
			return fmt.Errorf("%s path must be workspace-relative: %q", prefix, module.Path)
		}
		if strings.TrimSpace(module.Purpose) == "" || strings.TrimSpace(module.Method) == "" {
			return fmt.Errorf("%s requires purpose and method", prefix)
		}
		switch module.Kind {
		case "go":
			if len(module.Packages) == 0 || module.MinimumStatementPercent <= 0 || module.MinimumStatementPercent > 100 {
				return fmt.Errorf("%s Go module requires packages and a statement minimum in (0,100]", prefix)
			}
			if module.TargetStatementPercent < module.MinimumStatementPercent || module.TargetStatementPercent > 100 {
				return fmt.Errorf("%s target must be between its minimum and 100", prefix)
			}
			if strings.TrimSpace(module.Owner) == "" {
				return fmt.Errorf("%s Go module requires owner", prefix)
			}
			if module.DefaultRisk == "" {
				module.DefaultRisk = "normal"
			}
			if !validCoverageRisk(module.DefaultRisk) {
				return fmt.Errorf("%s has invalid default_risk %q", prefix, module.DefaultRisk)
			}
			packageNames := map[string]bool{}
			for _, policy := range module.PackagePolicies {
				if strings.TrimSpace(policy.Package) == "" || !validCoverageRisk(policy.Risk) {
					return fmt.Errorf("%s has invalid package policy for %q", prefix, policy.Package)
				}
				if packageNames[policy.Package] {
					return fmt.Errorf("%s has duplicate package policy %q", prefix, policy.Package)
				}
				packageNames[policy.Package] = true
				if policy.Risk == "wiring" {
					if strings.TrimSpace(policy.ExclusionReason) == "" {
						return fmt.Errorf("%s wiring package %q requires exclusion_reason", prefix, policy.Package)
					}
					continue
				}
				if policy.MinimumStatementPercent < 0 || policy.MinimumStatementPercent > 100 ||
					policy.PRMinimumStatement < 0 || policy.PRMinimumStatement > 100 ||
					policy.TargetStatementPercent <= 0 || policy.TargetStatementPercent > 100 {
					return fmt.Errorf("%s package %q has invalid thresholds", prefix, policy.Package)
				}
				riskTarget := cfg.RiskThresholds[policy.Risk]
				if policy.TargetStatementPercent+0.0001 < riskTarget {
					return fmt.Errorf("%s package %q target %.1f is below %s risk target %.1f", prefix, policy.Package, policy.TargetStatementPercent, policy.Risk, riskTarget)
				}
			}
			for _, critical := range module.CriticalCases {
				if !activeIDs[critical.TestID] || strings.TrimSpace(critical.CanonicalKey) == "" || strings.TrimSpace(critical.Purpose) == "" {
					return fmt.Errorf("%s has invalid critical case %q", prefix, critical.TestID)
				}
			}
		case "node":
			if len(module.SourceTestGlobs) == 0 || len(module.RuntimeTestGlobs) == 0 {
				return fmt.Errorf("%s Node module requires source_test_globs and runtime_test_globs", prefix)
			}
			if strings.TrimSpace(module.Owner) == "" {
				return fmt.Errorf("%s Node module requires owner", prefix)
			}
			for _, threshold := range []float64{module.MinimumLinePercent, module.MinimumBranchPercent, module.MinimumFunctionPercent} {
				if threshold <= 0 || threshold > 100 {
					return fmt.Errorf("%s Node thresholds must be in (0,100]", prefix)
				}
			}
			for _, rewrite := range module.SourceRewrites {
				if rewrite.FromPrefix == "" || rewrite.ToPrefix == "" ||
					filepath.IsAbs(rewrite.FromPrefix) || filepath.IsAbs(rewrite.ToPrefix) {
					return fmt.Errorf("%s has invalid source rewrite", prefix)
				}
			}
			for _, critical := range module.CriticalCases {
				if !activeIDs[critical.TestID] || strings.TrimSpace(critical.CanonicalKey) == "" || strings.TrimSpace(critical.Purpose) == "" {
					return fmt.Errorf("%s has invalid critical case %q", prefix, critical.TestID)
				}
				if !strings.HasPrefix(critical.CanonicalKey, "js://"+module.Name+"/") {
					return fmt.Errorf("%s critical case %q belongs to another module", prefix, critical.TestID)
				}
			}
		default:
			return fmt.Errorf("%s has unsupported kind %q", prefix, module.Kind)
		}
		if !exists(filepath.Join(workspace, module.Path)) {
			return fmt.Errorf("%s path does not exist: %s", prefix, module.Path)
		}
	}
	return nil
}

func runCoverageModule(workspace, outDir string, cfg coverageConfig, module coverageModule, baseRef, headRef string, install bool) coverageCaseResult {
	return runCoverageModuleProfile(workspace, outDir, cfg, module, baseRef, headRef, "unit", install)
}

func runCoverageModuleProfile(workspace, outDir string, cfg coverageConfig, module coverageModule, baseRef, headRef, profile string, install bool) coverageCaseResult {
	started := time.Now().UTC()
	result := coverageCaseResult{
		TestID:    module.TestID,
		Name:      module.Name,
		Kind:      module.Kind,
		Path:      module.Path,
		Purpose:   module.Purpose,
		Method:    module.Method,
		StartedAt: started.Format(time.RFC3339),
		Status:    "FAIL",
	}
	logRel := filepath.ToSlash(filepath.Join("logs", module.Name+".log"))
	result.LogPath = logRel
	logPath := filepath.Join(outDir, filepath.FromSlash(logRel))
	var runErr error
	switch module.Kind {
	case "go":
		runErr = runGoCoverageModuleProfile(workspace, outDir, logPath, cfg, module, baseRef, headRef, profile, &result)
	case "node":
		runErr = runNodeCoverageModule(workspace, outDir, logPath, module, install, &result)
	default:
		runErr = fmt.Errorf("unsupported coverage kind %q", module.Kind)
	}
	if runErr != nil {
		result.Assessment = runErr.Error()
	} else {
		result.Status = "PASS"
		result.Assessment = passingCoverageAssessment(result.Metrics)
	}
	completed := time.Now().UTC()
	result.CompletedAt = completed.Format(time.RFC3339)
	result.DurationMS = completed.Sub(started).Milliseconds()
	return result
}

func passingCoverageAssessment(metrics coverageMetrics) string {
	if metrics.IndustryTargetMet == nil || *metrics.IndustryTargetMet {
		return "PASS: all configured coverage gates passed."
	}
	target := 80.0
	if metrics.StatementTargetPercent != nil {
		target = *metrics.StatementTargetPercent
	}
	return fmt.Sprintf(
		"PASS: enforced ratchet and differential gates passed; the %.2f%% target remains tracked debt.",
		target,
	)
}

func runGoCoverageModule(workspace, outDir, logPath string, cfg coverageConfig, module coverageModule, baseRef, headRef string, result *coverageCaseResult) error {
	return runGoCoverageModuleProfile(workspace, outDir, logPath, cfg, module, baseRef, headRef, "unit", result)
}

func runGoCoverageModuleProfile(workspace, outDir, logPath string, cfg coverageConfig, module coverageModule, baseRef, headRef, profile string, result *coverageCaseResult) error {
	moduleDir := filepath.Join(workspace, module.Path)
	moduleRel := filepath.ToSlash(filepath.Join("modules", module.Name))
	profileRel := filepath.ToSlash(filepath.Join(moduleRel, "coverage.out"))
	profilePath := filepath.Join(outDir, filepath.FromSlash(profileRel))
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		return err
	}
	eventsRel := filepath.ToSlash(filepath.Join(moduleRel, "test-events.json"))
	eventsPath := filepath.Join(outDir, filepath.FromSlash(eventsRel))
	args := []string{"test", "-json", "-count=1"}
	args = append(args, module.Packages...)
	args = append(args, "-coverprofile="+profilePath)
	if profile == "pr" && len(module.CoverPackages) > 0 {
		args = append(args, "-coverpkg="+strings.Join(module.CoverPackages, ","))
	}
	env := map[string]string{"GOWORK": "off"}
	if profile == "pr" {
		for _, name := range module.PRRequiredEnv {
			if strings.TrimSpace(os.Getenv(name)) == "" {
				return fmt.Errorf("PR integration coverage requires environment variable %s", name)
			}
			env[name] = os.Getenv(name)
		}
	}
	commandErr := runCoverageJSONCommand(moduleDir, logPath, eventsPath, env, "go", args...)
	if commandErr != nil && !exists(profilePath) {
		return fmt.Errorf("Go coverage tests failed before producing a profile: %w", commandErr)
	}
	rawTotal, governedTotal, packages, err := analyzeGoCoverageProfile(profilePath, module, profile)
	if err != nil {
		return err
	}
	result.ProfilePath = profileRel
	result.ProfileSHA, err = fileSHA256(profilePath)
	if err != nil {
		return err
	}
	minimum := module.MinimumStatementPercent
	if profile == "pr" && module.PRMinimumStatement > 0 {
		minimum = module.PRMinimumStatement
	}
	result.Metrics.RawStatementPercent = floatPointer(rawTotal)
	result.Metrics.GovernedStatementPercent = floatPointer(governedTotal)
	result.Metrics.GovernedMinimumPercent = floatPointer(minimum)
	result.Metrics.StatementPercent = floatPointer(governedTotal)
	result.Metrics.StatementMinimumPercent = floatPointer(minimum)
	result.Metrics.StatementTargetPercent = floatPointer(module.TargetStatementPercent)
	result.Metrics.Packages = packages
	targetMet := governedTotal+0.0001 >= module.TargetStatementPercent
	result.Metrics.IndustryTargetMet = &targetMet
	manifestRel := filepath.ToSlash(filepath.Join(moduleRel, "unit-manifest.json"))
	junitRel := filepath.ToSlash(filepath.Join(moduleRel, "junit.xml"))
	packageRel := filepath.ToSlash(filepath.Join(moduleRel, "package-coverage.json"))
	units, unitErr := parseGoTestEvents(eventsPath, moduleDir, module)
	if unitErr != nil {
		return unitErr
	}
	validationModule := module
	if profile != "pr" {
		validationModule.PRRequiredTests = nil
	}
	requiredErr := validateRequiredGoTests(validationModule, units)
	if err := writeJSON(filepath.Join(outDir, filepath.FromSlash(manifestRel)), map[string]any{
		"schema_version": 1,
		"module":         module.Name,
		"profile":        profile,
		"tests":          units,
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, filepath.FromSlash(packageRel)), map[string]any{
		"schema_version":   1,
		"module":           module.Name,
		"profile":          profile,
		"raw_percent":      rawTotal,
		"governed_percent": governedTotal,
		"packages":         packages,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, filepath.FromSlash(junitRel)), renderGoJUnit(module.Name, units), 0o644); err != nil {
		return err
	}
	result.UnitManifestPath = manifestRel
	result.JUnitPath = junitRel
	result.TestEventsPath = eventsRel
	result.PackageCoveragePath = packageRel
	var criticalErr error
	if len(module.CriticalCommand) > 0 {
		criticalErr = runCoverageCommand(moduleDir, logPath, map[string]string{"GOWORK": "off"}, module.CriticalCommand[0], module.CriticalCommand[1:]...)
		if criticalErr != nil {
			result.CriticalGate = "FAIL"
		} else {
			result.CriticalGate = "PASS"
		}
	}
	for kind, rel := range map[string]string{
		"coverage-profile": profileRel,
		"go-test-events":   eventsRel,
		"unit-inventory":   manifestRel,
		"junit":            junitRel,
		"package-report":   packageRel,
		"test-log":         result.LogPath,
	} {
		sha, hashErr := fileSHA256(filepath.Join(outDir, filepath.FromSlash(rel)))
		if hashErr != nil {
			return hashErr
		}
		result.Evidence = append(result.Evidence, coverageArtifactEvidence{Kind: kind, Path: rel, SHA256: sha})
	}
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].Path < result.Evidence[j].Path })
	if commit, commitErr := gitOutput(moduleDir, "rev-parse", "HEAD"); commitErr == nil {
		result.SubmoduleCommit = strings.TrimSpace(commit)
	}
	// Always publish the complete evidence set, including when a ratchet fails.
	// This keeps a failing result diagnosable without rerunning the test.
	if commandErr != nil {
		return fmt.Errorf("Go coverage tests failed: %w", commandErr)
	}
	if requiredErr != nil {
		if len(module.CriticalCases) > 0 {
			result.CriticalGate = "FAIL"
		}
		return requiredErr
	}
	if criticalErr != nil {
		return fmt.Errorf("critical package coverage gate failed: %w", criticalErr)
	}
	if len(module.CriticalCases) > 0 {
		result.CriticalGate = "PASS"
	}
	if governedTotal+0.0001 < minimum {
		return fmt.Errorf("governed statement coverage %.2f%% is below %.2f%% ratchet (raw %.2f%%)", governedTotal, minimum, rawTotal)
	}
	for _, item := range packages {
		if item.Status == "FAIL" {
			return fmt.Errorf("package coverage gate failed for %s: %s", item.Package, item.Assessment)
		}
	}
	if module.Differential && strings.TrimSpace(baseRef) != "" {
		changedLines, diffErr := changedGoLines(workspace, baseRef, headRef, module.Path)
		if diffErr != nil {
			return diffErr
		}
		changedPercent, covered, statements, uncovered, diffErr := goDifferentialCoverageDetails(moduleDir, module.Path, profilePath, changedLines)
		if diffErr != nil {
			return diffErr
		}
		if statements > 0 {
			result.Metrics.ChangedStatementPercent = floatPointer(changedPercent)
			result.Metrics.ChangedStatementMinimum = floatPointer(cfg.Differential.MinimumStatementPercent)
			result.Metrics.ChangedStatements = statements
			result.Metrics.CoveredChangedStatements = covered
			result.Metrics.UncoveredChanged = uncovered
			if changedPercent+0.0001 < cfg.Differential.MinimumStatementPercent {
				return fmt.Errorf("differential statement coverage %.2f%% is below %.2f%% (%d/%d statements)", changedPercent, cfg.Differential.MinimumStatementPercent, covered, statements)
			}
		}
	}
	return nil
}

func runNodeCoverageModule(workspace, outDir, logPath string, module coverageModule, install bool, result *coverageCaseResult) error {
	moduleDir := filepath.Join(workspace, module.Path)
	moduleRel := filepath.ToSlash(filepath.Join("modules", module.Name))
	logRel := filepath.ToSlash(filepath.Join(moduleRel, "coverage.log"))
	logPath = filepath.Join(outDir, filepath.FromSlash(logRel))
	result.LogPath = logRel
	eventsRel := filepath.ToSlash(filepath.Join(moduleRel, "test-events.json"))
	eventsPath := filepath.Join(outDir, filepath.FromSlash(eventsRel))
	manifestRel := filepath.ToSlash(filepath.Join(moduleRel, "unit-manifest.json"))
	junitRel := filepath.ToSlash(filepath.Join(moduleRel, "junit.xml"))
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return err
	}
	if install {
		npmCache := filepath.Join(workspace, ".artifacts", "npm-cache")
		if err := os.MkdirAll(npmCache, 0o755); err != nil {
			return err
		}
		if err := runCoverageCommand(moduleDir, logPath, map[string]string{"NPM_CONFIG_CACHE": npmCache}, "npm", "ci"); err != nil {
			return fmt.Errorf("install Node dependencies: %w", err)
		}
	}
	if len(module.Build) > 0 {
		if err := runCoverageCommand(moduleDir, logPath, nil, module.Build[0], module.Build[1:]...); err != nil {
			return fmt.Errorf("build Node module: %w", err)
		}
	}
	testFiles := []string{}
	for _, pattern := range module.RuntimeTestGlobs {
		matches, err := filepath.Glob(filepath.Join(moduleDir, filepath.FromSlash(pattern)))
		if err != nil {
			return fmt.Errorf("expand test glob %q: %w", pattern, err)
		}
		for _, match := range matches {
			rel, err := filepath.Rel(moduleDir, match)
			if err != nil {
				return err
			}
			testFiles = append(testFiles, rel)
		}
	}
	sort.Strings(testFiles)
	if len(testFiles) == 0 {
		return errors.New("Node coverage selected no test files")
	}
	reporter := filepath.Join(workspace, "scripts", "node", "test-event-reporter.mjs")
	args := []string{
		"--test",
		"--experimental-test-coverage",
		"--test-reporter=spec",
		"--test-reporter=" + reporter,
		"--test-reporter-destination=stdout",
		"--test-reporter-destination=" + eventsPath,
	}
	args = append(args, testFiles...)
	commandErr := runCoverageCommand(moduleDir, logPath, nil, "node", args...)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	line, branch, function, err := parseNodeCoverageSummary(string(raw))
	if err != nil {
		return err
	}
	result.Metrics.LinePercent = floatPointer(line)
	result.Metrics.LineMinimumPercent = floatPointer(module.MinimumLinePercent)
	result.Metrics.BranchPercent = floatPointer(branch)
	result.Metrics.BranchMinimumPercent = floatPointer(module.MinimumBranchPercent)
	result.Metrics.FunctionPercent = floatPointer(function)
	result.Metrics.FunctionMinimumPercent = floatPointer(module.MinimumFunctionPercent)
	targetMet := line >= 80 && branch >= 80 && function >= 80
	result.Metrics.IndustryTargetMet = &targetMet
	units, err := parseNodeTestEvents(eventsPath, moduleDir, module)
	if err != nil {
		return err
	}
	commit := ""
	if value, commitErr := gitOutput(moduleDir, "rev-parse", "HEAD"); commitErr == nil {
		commit = strings.TrimSpace(value)
		result.SubmoduleCommit = commit
	}
	manifest := nodeUnitManifest{
		SchemaVersion: 1,
		Module:        module.Name,
		Profile:       "unit",
		Commit:        commit,
		Tests:         units,
	}
	if err := writeJSON(filepath.Join(outDir, filepath.FromSlash(manifestRel)), manifest); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, filepath.FromSlash(junitRel)), renderNodeJUnit(module.Name, units), 0o644); err != nil {
		return err
	}
	result.UnitManifestPath = manifestRel
	result.JUnitPath = junitRel
	result.TestEventsPath = eventsRel
	for kind, rel := range map[string]string{
		"node-test-events": eventsRel,
		"unit-inventory":   manifestRel,
		"junit":            junitRel,
		"coverage-log":     logRel,
	} {
		sha, hashErr := fileSHA256(filepath.Join(outDir, filepath.FromSlash(rel)))
		if hashErr != nil {
			return hashErr
		}
		result.Evidence = append(result.Evidence, coverageArtifactEvidence{Kind: kind, Path: rel, SHA256: sha})
	}
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].Path < result.Evidence[j].Path })
	ledger, err := loadUnitInventory(workspace)
	if err != nil {
		return err
	}
	if err := compareUnitInventory(ledger, []nodeUnitManifest{manifest}); err != nil {
		return err
	}
	if err := validateCriticalNodeTests(module, units); err != nil {
		result.CriticalGate = "FAIL"
		return err
	}
	result.CriticalGate = "PASS"
	if commandErr != nil {
		return fmt.Errorf("Node coverage tests failed: %w", commandErr)
	}
	if line+0.0001 < module.MinimumLinePercent {
		return fmt.Errorf("line coverage %.2f%% is below %.2f%%", line, module.MinimumLinePercent)
	}
	if branch+0.0001 < module.MinimumBranchPercent {
		return fmt.Errorf("branch coverage %.2f%% is below %.2f%%", branch, module.MinimumBranchPercent)
	}
	if function+0.0001 < module.MinimumFunctionPercent {
		return fmt.Errorf("function coverage %.2f%% is below %.2f%%", function, module.MinimumFunctionPercent)
	}
	return nil
}

func runCoverageCommand(dir, logPath string, env map[string]string, name string, args ...string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "$ %s %s\n", name, strings.Join(args, " "))
	fmt.Printf("== coverage: %s ==\n", dir)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), env)
	cmd.Stdin = os.Stdin
	writer := io.MultiWriter(os.Stdout, logFile)
	cmd.Stdout = writer
	cmd.Stderr = writer
	return cmd.Run()
}

func parseGoCoverageProfile(path string, changed map[string]map[int]bool) (percent float64, covered, statements int, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		match := goCoverageLinePattern.FindStringSubmatch(line)
		if match == nil {
			return 0, 0, 0, fmt.Errorf("invalid Go coverage profile line %q", line)
		}
		countStatements, _ := strconv.Atoi(match[6])
		executionCount, _ := strconv.Atoi(match[7])
		if changed != nil {
			startLine, _ := strconv.Atoi(match[2])
			endLine, _ := strconv.Atoi(match[4])
			lines := changed[filepath.ToSlash(match[1])]
			intersects := false
			for lineNo := startLine; lineNo <= endLine; lineNo++ {
				if lines[lineNo] {
					intersects = true
					break
				}
			}
			if !intersects {
				continue
			}
		}
		statements += countStatements
		if executionCount > 0 {
			covered += countStatements
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, 0, err
	}
	if statements == 0 {
		return 0, covered, statements, nil
	}
	return float64(covered) * 100 / float64(statements), covered, statements, nil
}

func changedGoLines(workspace, baseRef, headRef, modulePath string) (map[string]map[int]bool, error) {
	moduleDir := filepath.Join(workspace, filepath.FromSlash(modulePath))
	repositoryRoot, rootErr := gitOutput(moduleDir, "rev-parse", "--show-toplevel")
	if rootErr == nil {
		repositoryRoot = strings.TrimSpace(repositoryRoot)
		workspaceAbs, _ := filepath.Abs(workspace)
		repositoryAbs, _ := filepath.Abs(repositoryRoot)
		if repositoryAbs != workspaceAbs {
			repositoryRel, err := filepath.Rel(workspaceAbs, repositoryAbs)
			if err != nil || strings.HasPrefix(repositoryRel, "..") {
				return nil, fmt.Errorf("resolve submodule repository for %s", modulePath)
			}
			repositoryRel = filepath.ToSlash(repositoryRel)
			if !gitObjectExists(workspace, baseRef+":"+repositoryRel) {
				// A newly introduced submodule has no meaningful predecessor to diff.
				// Its committed module/package baseline gates still apply in this run.
				return map[string]map[int]bool{}, nil
			}
			baseCommit, err := gitOutput(workspace, "rev-parse", baseRef+":"+repositoryRel)
			if err != nil {
				return nil, fmt.Errorf("resolve %s base gitlink: %w", modulePath, err)
			}
			headCommit, err := gitOutput(workspace, "rev-parse", headRef+":"+repositoryRel)
			if err != nil {
				return nil, fmt.Errorf("resolve %s head gitlink: %w", modulePath, err)
			}
			for _, commit := range []string{strings.TrimSpace(baseCommit), strings.TrimSpace(headCommit)} {
				if err := ensureGitCommit(repositoryAbs, commit); err != nil {
					return nil, fmt.Errorf("prepare %s differential commit: %w", modulePath, err)
				}
			}
			innerPath, err := filepath.Rel(repositoryAbs, moduleDir)
			if err != nil {
				return nil, err
			}
			diffArgs := []string{"diff", "--unified=0", "--no-color", strings.TrimSpace(baseCommit), strings.TrimSpace(headCommit)}
			if innerPath != "." {
				diffArgs = append(diffArgs, "--", filepath.ToSlash(innerPath))
			}
			output, err := gitOutput(repositoryAbs, diffArgs...)
			if err != nil {
				return nil, fmt.Errorf("read submodule differential coverage diff: %w", err)
			}
			changed, err := parseChangedGoLines(output)
			if err != nil {
				return nil, err
			}
			prefixed := make(map[string]map[int]bool, len(changed))
			for path, lines := range changed {
				prefixed[filepath.ToSlash(filepath.Join(repositoryRel, path))] = lines
			}
			return prefixed, nil
		}
	}
	output, err := gitOutput(workspace, "diff", "--unified=0", "--no-color", baseRef+"..."+headRef, "--", modulePath)
	if err != nil {
		return nil, fmt.Errorf("read differential coverage diff: %w", err)
	}
	return parseChangedGoLines(output)
}

func gitObjectExists(repository, object string) bool {
	cmd := exec.Command("git", "cat-file", "-e", object)
	cmd.Dir = repository
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func ensureGitCommit(repository, commit string) error {
	if _, err := gitOutput(repository, "cat-file", "-e", commit+"^{commit}"); err == nil {
		return nil
	}
	if _, err := gitOutput(repository, "fetch", "--no-tags", "origin", commit); err != nil {
		return fmt.Errorf("fetch missing git commit %s: %w", commit, err)
	}
	if _, err := gitOutput(repository, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return fmt.Errorf("git commit %s is unavailable after fetch: %w", commit, err)
	}
	return nil
}

func parseChangedGoLines(output string) (map[string]map[int]bool, error) {
	changed := map[string]map[int]bool{}
	currentFile := ""
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
			if strings.HasSuffix(currentFile, ".go") {
				changed[currentFile] = map[int]bool{}
			}
			continue
		}
		if currentFile == "" || !strings.HasSuffix(currentFile, ".go") {
			continue
		}
		match := diffHunkPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		start, _ := strconv.Atoi(match[1])
		count := 1
		if match[2] != "" {
			count, _ = strconv.Atoi(match[2])
		}
		for lineNo := start; lineNo < start+count; lineNo++ {
			changed[currentFile][lineNo] = true
		}
	}
	return changed, scanner.Err()
}

func goDifferentialCoverage(moduleDir, modulePath, profilePath string, changed map[string]map[int]bool) (float64, int, int, error) {
	percent, covered, statements, _, err := goDifferentialCoverageDetails(moduleDir, modulePath, profilePath, changed)
	return percent, covered, statements, err
}

func goDifferentialCoverageDetails(moduleDir, modulePath, profilePath string, changed map[string]map[int]bool) (float64, int, int, []string, error) {
	moduleName, err := goModuleName(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return 0, 0, 0, nil, err
	}
	profileChanged := map[string]map[int]bool{}
	profileToWorkspace := map[string]string{}
	for workspaceFile, lines := range changed {
		rel, err := filepath.Rel(filepath.ToSlash(modulePath), filepath.ToSlash(workspaceFile))
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		profileFile := filepath.ToSlash(filepath.Join(moduleName, rel))
		profileChanged[profileFile] = lines
		profileToWorkspace[profileFile] = workspaceFile
	}
	percent, covered, statements, err := parseGoCoverageProfile(profilePath, profileChanged)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	file, err := os.Open(profilePath)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	defer file.Close()
	uncovered := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		match := goCoverageLinePattern.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		executionCount, _ := strconv.Atoi(match[7])
		if executionCount > 0 {
			continue
		}
		startLine, _ := strconv.Atoi(match[2])
		endLine, _ := strconv.Atoi(match[4])
		lines := profileChanged[filepath.ToSlash(match[1])]
		for lineNo := startLine; lineNo <= endLine; lineNo++ {
			if lines[lineNo] {
				uncovered = append(uncovered, fmt.Sprintf("%s:%s", profileToWorkspace[filepath.ToSlash(match[1])], match[2]))
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, 0, nil, err
	}
	sort.Strings(uncovered)
	return percent, covered, statements, slices.Compact(uncovered), nil
}

func goModuleName(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("go.mod %s has no module directive", path)
}

func parseNodeCoverageSummary(output string) (float64, float64, float64, error) {
	match := nodeCoverageAllFilesPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, 0, 0, errors.New("Node coverage output is missing the all files summary")
	}
	values := make([]float64, 3)
	for index := range values {
		value, err := strconv.ParseFloat(match[index+1], 64)
		if err != nil {
			return 0, 0, 0, err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func floatPointer(value float64) *float64 {
	return &value
}

func renderCoverageReport(report coverageReport) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# Coverage Test Report\n\n")
	fmt.Fprintf(&out, "- Run ID: `%s`\n", report.RunID)
	fmt.Fprintf(&out, "- Profile: `%s`\n", report.Profile)
	fmt.Fprintf(&out, "- Workspace commit: `%s`\n", report.WorkspaceCommit)
	if report.BaseRef != "" {
		fmt.Fprintf(&out, "- Differential range: `%s...%s`\n", report.BaseRef, report.HeadRef)
	}
	fmt.Fprintf(&out, "- Started: %s\n- Completed: %s\n- Duration: %d ms\n", report.StartedAt, report.CompletedAt, report.DurationMS)
	fmt.Fprintf(&out, "- Result: **%s**\n- Assessment: %s\n\n", report.Status, report.Assessment)
	fmt.Fprintf(&out, "- Artifact redaction scan: **%s**\n", report.RedactionStatus)
	if len(report.RedactionIssues) > 0 {
		fmt.Fprintf(&out, "- Unsafe artifacts: `%s`\n", strings.Join(report.RedactionIssues, "`, `"))
	}
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "| Test ID | Module | Purpose | Method | Duration | Coverage | Differential | Result | Assessment |\n")
	fmt.Fprintf(&out, "|---|---|---|---|---:|---|---|---|---|\n")
	for _, item := range report.Cases {
		coverage := "-"
		if item.Metrics.StatementPercent != nil {
			coverage = fmt.Sprintf("%.2f%% ≥ %.2f%%", *item.Metrics.StatementPercent, *item.Metrics.StatementMinimumPercent)
			if item.Metrics.RawStatementPercent != nil && item.Metrics.GovernedStatementPercent != nil {
				coverage = fmt.Sprintf("governed %.2f%% ≥ %.2f%%; raw %.2f%%",
					*item.Metrics.GovernedStatementPercent, *item.Metrics.GovernedMinimumPercent, *item.Metrics.RawStatementPercent)
			}
		} else if item.Metrics.LinePercent != nil {
			coverage = fmt.Sprintf("line %.2f%% / branch %.2f%% / function %.2f%%", *item.Metrics.LinePercent, *item.Metrics.BranchPercent, *item.Metrics.FunctionPercent)
		}
		differential := "-"
		if item.Metrics.ChangedStatementPercent != nil {
			differential = fmt.Sprintf("%.2f%% (%d/%d) ≥ %.2f%%", *item.Metrics.ChangedStatementPercent, item.Metrics.CoveredChangedStatements, item.Metrics.ChangedStatements, *item.Metrics.ChangedStatementMinimum)
			if len(item.Metrics.UncoveredChanged) > 0 {
				differential += "; uncovered: " + strings.Join(item.Metrics.UncoveredChanged, ", ")
			}
		}
		fmt.Fprintf(&out, "| `%s` | %s | %s | %s | %d ms | %s | %s | **%s** | %s |\n",
			item.TestID, item.Name, coverageMarkdownCell(item.Purpose), coverageMarkdownCell(item.Method), item.DurationMS, coverage, differential, item.Status, coverageMarkdownCell(item.Assessment))
	}
	fmt.Fprintf(&out, "\n## Evidence\n\n")
	for _, item := range report.Cases {
		fmt.Fprintf(&out, "- `%s`: `%s`", item.Name, item.LogPath)
		if item.ProfilePath != "" {
			fmt.Fprintf(&out, ", `%s` (SHA-256 `%s`)", item.ProfilePath, item.ProfileSHA)
		}
		for _, path := range []string{item.PackageCoveragePath, item.UnitManifestPath, item.JUnitPath, item.TestEventsPath} {
			if path != "" {
				fmt.Fprintf(&out, ", `%s`", path)
			}
		}
		fmt.Fprintln(&out)
	}
	return []byte(out.String())
}

func coverageMarkdownCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type goCoverageBlock struct {
	File       string
	Statements int
	Count      int
}

type goUnitResult struct {
	CanonicalKey string `json:"canonical_key"`
	TestID       string `json:"test_id,omitempty"`
	Package      string `json:"package"`
	Test         string `json:"test"`
	Subtest      string `json:"subtest,omitempty"`
	Source       string `json:"source,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	Status       string `json:"status"`
}

type goTestEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

type runtimeCoverageAnchor struct {
	SchemaVersion   int    `json:"schema_version"`
	RunID           string `json:"run_id"`
	Module          string `json:"module"`
	WorkspaceCommit string `json:"workspace_commit"`
	ModuleCommit    string `json:"module_commit"`
	GeneratedAt     string `json:"generated_at"`
}

var goTestFunctionPattern = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(`)

func validCoverageRisk(value string) bool {
	switch value {
	case "critical", "high", "normal", "wiring":
		return true
	default:
		return false
	}
}

func runCoverageJSONCommand(dir, logPath, eventsPath string, env map[string]string, name string, args ...string) error {
	for _, path := range []string{logPath, eventsPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	eventsFile, err := os.Create(eventsPath)
	if err != nil {
		return err
	}
	defer eventsFile.Close()
	fmt.Fprintf(logFile, "$ %s %s\n", name, strings.Join(args, " "))
	fmt.Printf("== coverage: %s ==\n", dir)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), env)
	cmd.Stdin = os.Stdin
	// The JSON stream is evidence, not useful console progress. Keeping it out of
	// stdout makes the 10-module matrix readable while preserving every event.
	cmd.Stdout = io.MultiWriter(logFile, eventsFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, logFile)
	err = cmd.Run()
	if err == nil {
		fmt.Printf("== coverage complete: %s ==\n", dir)
	}
	return err
}

func analyzeGoCoverageProfile(path string, module coverageModule, profile string) (float64, float64, []coveragePackageResult, error) {
	blocks, err := readUniqueGoCoverageBlocks(path)
	if err != nil {
		return 0, 0, nil, err
	}
	policyByPackage := map[string]coveragePackagePolicy{}
	for _, policy := range module.PackagePolicies {
		policyByPackage[filepath.ToSlash(policy.Package)] = policy
	}
	type totals struct {
		covered    int
		statements int
	}
	packageTotals := map[string]*totals{}
	rawCovered, rawStatements := 0, 0
	governedCovered, governedStatements := 0, 0
	for _, block := range blocks {
		pkg := filepath.ToSlash(filepath.Dir(block.File))
		item := packageTotals[pkg]
		if item == nil {
			item = &totals{}
			packageTotals[pkg] = item
		}
		item.statements += block.Statements
		rawStatements += block.Statements
		if block.Count > 0 {
			item.covered += block.Statements
			rawCovered += block.Statements
		}
		policy, ok := policyByPackage[pkg]
		risk := module.DefaultRisk
		if ok {
			risk = policy.Risk
		}
		if risk != "wiring" {
			governedStatements += block.Statements
			if block.Count > 0 {
				governedCovered += block.Statements
			}
		}
	}
	rawPercent := coveragePercent(rawCovered, rawStatements)
	governedPercent := coveragePercent(governedCovered, governedStatements)
	names := make([]string, 0, len(packageTotals))
	for name := range packageTotals {
		names = append(names, name)
	}
	sort.Strings(names)
	results := make([]coveragePackageResult, 0, len(names))
	for _, name := range names {
		total := packageTotals[name]
		policy, explicit := policyByPackage[name]
		risk := module.DefaultRisk
		owner := module.Owner
		if explicit {
			risk = policy.Risk
			if policy.Owner != "" {
				owner = policy.Owner
			}
		}
		percent := coveragePercent(total.covered, total.statements)
		item := coveragePackageResult{
			Package:             name,
			Risk:                risk,
			Owner:               owner,
			RawStatementPercent: percent,
			CoveredStatements:   total.covered,
			Statements:          total.statements,
			Status:              "PASS",
			Assessment:          "reporting-only package; no explicit ratchet",
		}
		if risk == "wiring" {
			item.ExclusionReason = policy.ExclusionReason
			item.Assessment = "excluded from governed coverage: " + policy.ExclusionReason
		} else {
			item.GovernedStatementPercent = floatPointer(percent)
			if explicit {
				minimum := policy.MinimumStatementPercent
				if profile == "pr" && policy.PRMinimumStatement > 0 {
					minimum = policy.PRMinimumStatement
				}
				item.MinimumStatementPercent = floatPointer(minimum)
				item.TargetStatementPercent = floatPointer(policy.TargetStatementPercent)
				if percent+0.0001 < minimum {
					item.Status = "FAIL"
					item.Assessment = fmt.Sprintf("%.2f%% is below %.2f%% ratchet", percent, minimum)
				} else if percent+0.0001 < policy.TargetStatementPercent {
					item.Assessment = fmt.Sprintf("ratchet passed; %.2f%% target remains tracked debt", policy.TargetStatementPercent)
				} else {
					item.Assessment = "ratchet and risk target passed"
				}
			}
		}
		results = append(results, item)
	}
	return rawPercent, governedPercent, results, nil
}

func readUniqueGoCoverageBlocks(path string) ([]goCoverageBlock, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	blocks := map[string]goCoverageBlock{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		match := goCoverageLinePattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("invalid Go coverage profile line %q", line)
		}
		statements, _ := strconv.Atoi(match[6])
		count, _ := strconv.Atoi(match[7])
		key := strings.Join(match[1:6], ":")
		block := blocks[key]
		block.File = filepath.ToSlash(match[1])
		block.Statements = statements
		if count > block.Count {
			block.Count = count
		}
		blocks[key] = block
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result := make([]goCoverageBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, block)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].File == result[j].File {
			return result[i].Statements < result[j].Statements
		}
		return result[i].File < result[j].File
	})
	return result, nil
}

func coveragePercent(covered, statements int) float64 {
	if statements == 0 {
		return 0
	}
	return float64(covered) * 100 / float64(statements)
}

func parseGoTestEvents(path, moduleDir string, module coverageModule) ([]goUnitResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	sources, err := discoverGoTestSources(moduleDir)
	if err != nil {
		return nil, err
	}
	started := map[string]string{}
	results := map[string]goUnitResult{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse Go test event: %w", err)
		}
		if event.Test == "" {
			continue
		}
		key := event.Package + "\x00" + event.Test
		if event.Action == "run" {
			started[key] = event.Time
			continue
		}
		status := strings.ToUpper(event.Action)
		if status != "PASS" && status != "FAIL" && status != "SKIP" {
			continue
		}
		testName, subtest := splitGoTestName(event.Test)
		packageName := strings.TrimPrefix(event.Package, moduleImportPrefix(event.Package, moduleDir))
		packageName = strings.TrimPrefix(packageName, "/")
		if packageName == "" {
			packageName = event.Package
		}
		canonical := fmt.Sprintf("go://%s/%s#%s", module.Name, packageName, event.Test)
		results[key] = goUnitResult{
			CanonicalKey: canonical,
			Package:      event.Package,
			Test:         testName,
			Subtest:      subtest,
			Source:       sources[event.Package+"\x00"+testName],
			StartedAt:    started[key],
			CompletedAt:  event.Time,
			DurationMS:   int64(event.Elapsed * 1000),
			Status:       status,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	criticalByKey := map[string]string{}
	for _, critical := range module.CriticalCases {
		criticalByKey[critical.CanonicalKey] = critical.TestID
	}
	units := make([]goUnitResult, 0, len(results))
	for _, item := range results {
		item.TestID = criticalByKey[item.CanonicalKey]
		units = append(units, item)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].CanonicalKey < units[j].CanonicalKey })
	return units, nil
}

func moduleImportPrefix(packageName, moduleDir string) string {
	name, err := goModuleName(filepath.Join(moduleDir, "go.mod"))
	if err != nil || !strings.HasPrefix(packageName, name) {
		return ""
	}
	return name
}

func splitGoTestName(value string) (string, string) {
	test, subtest, found := strings.Cut(value, "/")
	if !found {
		return test, ""
	}
	return test, subtest
}

func discoverGoTestSources(moduleDir string) (map[string]string, error) {
	moduleName, err := goModuleName(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	err = filepath.WalkDir(moduleDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == ".artifacts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relDir, err := filepath.Rel(moduleDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := moduleName
		if relDir != "." {
			pkg += "/" + filepath.ToSlash(relDir)
		}
		source, _ := filepath.Rel(moduleDir, path)
		for _, match := range goTestFunctionPattern.FindAllStringSubmatch(string(raw), -1) {
			result[pkg+"\x00"+match[1]] = filepath.ToSlash(source)
		}
		return nil
	})
	return result, err
}

func validateRequiredGoTests(module coverageModule, units []goUnitResult) error {
	statusByTest := map[string]string{}
	statusByCanonical := map[string]string{}
	for _, unit := range units {
		if unit.Subtest == "" || statusByTest[unit.Test] != "PASS" {
			statusByTest[unit.Test] = unit.Status
		}
		statusByCanonical[unit.CanonicalKey] = unit.Status
	}
	for _, required := range module.PRRequiredTests {
		if statusByTest[required] != "PASS" {
			return fmt.Errorf("required PR integration test %s did not PASS (status %s)", required, statusByTest[required])
		}
	}
	for _, critical := range module.CriticalCases {
		if statusByCanonical[critical.CanonicalKey] != "PASS" {
			return fmt.Errorf("critical test %s (%s) did not PASS", critical.TestID, critical.CanonicalKey)
		}
	}
	return nil
}

func renderGoJUnit(module string, units []goUnitResult) []byte {
	failures, skipped, duration := 0, 0, int64(0)
	for _, unit := range units {
		duration += unit.DurationMS
		switch unit.Status {
		case "FAIL":
			failures++
		case "SKIP":
			skipped++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&out, "<testsuite name=\"%s\" tests=\"%d\" failures=\"%d\" skipped=\"%d\" time=\"%.3f\">\n",
		html.EscapeString(module), len(units), failures, skipped, float64(duration)/1000)
	for _, unit := range units {
		name := unit.Test
		if unit.Subtest != "" {
			name += "/" + unit.Subtest
		}
		fmt.Fprintf(&out, "  <testcase classname=\"%s\" name=\"%s\" time=\"%.3f\">",
			html.EscapeString(unit.Package), html.EscapeString(name), float64(unit.DurationMS)/1000)
		switch unit.Status {
		case "FAIL":
			fmt.Fprint(&out, "<failure message=\"test failed\"/>")
		case "SKIP":
			fmt.Fprint(&out, "<skipped/>")
		}
		fmt.Fprintln(&out, "</testcase>")
	}
	fmt.Fprintln(&out, "</testsuite>")
	return []byte(out.String())
}

func runRuntimeCoverage(workspace, outDir string, cfg coverageConfig, report coverageReport, selected map[string]bool, runtimeRoot string) error {
	started, err := time.Parse(time.RFC3339, report.StartedAt)
	if err != nil {
		started = time.Now().UTC()
	}
	for _, module := range cfg.Modules {
		if module.Kind != "go" || (len(selected) > 0 && !selected[module.Name]) {
			continue
		}
		delete(selected, module.Name)
		moduleRuntime := filepath.Join(runtimeRoot, module.Name)
		anchor, anchorPath, err := validateRuntimeCoverageAnchor(workspace, module, moduleRuntime, report.RunID)
		if err != nil {
			result := coverageCaseResult{
				TestID: module.TestID, Name: module.Name, Kind: module.Kind, Path: module.Path,
				Purpose: module.Purpose, Method: "Aggregate instrumented deployed-process GOCOVERDIR counters.",
				StartedAt: report.StartedAt, Status: "INCOMPLETE", Assessment: err.Error(),
			}
			report.Status = "INCOMPLETE"
			completeRuntimeCoverageCase(&result)
			report.Cases = append(report.Cases, result)
			continue
		}
		dirs, err := runtimeCoverageDirs(moduleRuntime)
		result := coverageCaseResult{
			TestID: module.TestID, Name: module.Name, Kind: module.Kind, Path: module.Path,
			Purpose: module.Purpose, Method: "Aggregate instrumented deployed-process GOCOVERDIR counters.",
			StartedAt: report.StartedAt, Status: "INCOMPLETE",
		}
		if err != nil {
			result.Assessment = err.Error()
			report.Status = "INCOMPLETE"
			completeRuntimeCoverageCase(&result)
			report.Cases = append(report.Cases, result)
			continue
		}
		moduleRel := filepath.ToSlash(filepath.Join("modules", module.Name))
		anchorRel := filepath.ToSlash(filepath.Join(moduleRel, "runtime-evidence.json"))
		anchorRaw, err := os.ReadFile(anchorPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(outDir, filepath.FromSlash(moduleRel)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, filepath.FromSlash(anchorRel)), anchorRaw, 0o644); err != nil {
			return err
		}
		result.RuntimeEvidencePath = anchorRel
		result.RuntimeEvidenceSHA, _ = fileSHA256(filepath.Join(outDir, filepath.FromSlash(anchorRel)))
		result.SubmoduleCommit = anchor.ModuleCommit
		profileRel := filepath.ToSlash(filepath.Join(moduleRel, "coverage.out"))
		profilePath := filepath.Join(outDir, filepath.FromSlash(profileRel))
		if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
			return err
		}
		args := []string{"tool", "covdata", "textfmt", "-i=" + strings.Join(dirs, ","), "-o=" + profilePath}
		if err := runCoverageCommand(workspace, filepath.Join(outDir, filepath.FromSlash(moduleRel), "logs", "runtime.log"), nil, "go", args...); err != nil {
			result.Assessment = "runtime coverage aggregation failed: " + err.Error()
			report.Status = "INCOMPLETE"
			completeRuntimeCoverageCase(&result)
			report.Cases = append(report.Cases, result)
			continue
		}
		raw, governed, packages, err := analyzeGoCoverageProfile(profilePath, module, "runtime")
		if err != nil {
			result.Assessment = err.Error()
			report.Status = "INCOMPLETE"
			completeRuntimeCoverageCase(&result)
			report.Cases = append(report.Cases, result)
			continue
		}
		result.ProfilePath = profileRel
		result.ProfileSHA, _ = fileSHA256(profilePath)
		result.Metrics.RawStatementPercent = floatPointer(raw)
		result.Metrics.GovernedStatementPercent = floatPointer(governed)
		result.Metrics.Packages = packages
		result.Status = "PASS"
		result.Assessment = "runtime coverage counters and metadata aggregated successfully"
		completeRuntimeCoverageCase(&result)
		report.Cases = append(report.Cases, result)
	}
	if len(selected) > 0 {
		names := make([]string, 0, len(selected))
		for name := range selected {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown or non-Go runtime coverage module(s): %s", strings.Join(names, ", "))
	}
	completed := time.Now().UTC()
	report.CompletedAt = completed.Format(time.RFC3339)
	report.DurationMS = completed.Sub(started).Milliseconds()
	report.RedactionStatus = "PASS"
	if issues := findUnredactedFeatureEvidence(outDir); len(issues) > 0 {
		report.RedactionStatus = "FAIL"
		report.RedactionIssues = issues
		report.Status = "FAIL"
	}
	if report.Status == "" {
		report.Status = "PASS"
	}
	if report.Status == "PASS" {
		report.Assessment = "All requested runtime coverage profiles were complete and safely aggregated."
	} else {
		report.Assessment = "One or more runtime coverage profiles were missing, invalid, or unsafe."
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "TEST_REPORT.md"), renderCoverageReport(report), 0o644); err != nil {
		return err
	}
	if report.Status != "PASS" {
		return exitCode(1)
	}
	return nil
}

func completeRuntimeCoverageCase(result *coverageCaseResult) {
	completed := time.Now().UTC()
	result.CompletedAt = completed.Format(time.RFC3339)
	if started, err := time.Parse(time.RFC3339, result.StartedAt); err == nil {
		result.DurationMS = completed.Sub(started).Milliseconds()
	}
}

func runtimeCoverageDirs(root string) ([]string, error) {
	metaDirs := map[string]bool{}
	counterDirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "covmeta.") {
			metaDirs[filepath.Dir(path)] = true
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "covcounters.") {
			counterDirs[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect runtime coverage %s: %w", root, err)
	}
	if len(metaDirs) == 0 {
		return nil, fmt.Errorf("runtime coverage for %s has no covmeta files", root)
	}
	result := make([]string, 0, len(metaDirs))
	for dir := range metaDirs {
		if !counterDirs[dir] {
			return nil, fmt.Errorf("runtime coverage for %s has metadata but no counters in %s", root, dir)
		}
		result = append(result, dir)
	}
	for dir := range counterDirs {
		if !metaDirs[dir] {
			return nil, fmt.Errorf("runtime coverage for %s has counters but no metadata in %s", root, dir)
		}
	}
	sort.Strings(result)
	return result, nil
}

func validateRuntimeCoverageAnchor(workspace string, module coverageModule, runtimeRoot, runID string) (runtimeCoverageAnchor, string, error) {
	path := filepath.Join(runtimeRoot, "coverage-runtime.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return runtimeCoverageAnchor{}, path, fmt.Errorf("runtime coverage anchor for %s: %w", module.Name, err)
	}
	var anchor runtimeCoverageAnchor
	if err := json.Unmarshal(raw, &anchor); err != nil {
		return runtimeCoverageAnchor{}, path, fmt.Errorf("parse runtime coverage anchor for %s: %w", module.Name, err)
	}
	if anchor.SchemaVersion != 1 || anchor.Module != module.Name || anchor.RunID != runID ||
		anchor.WorkspaceCommit == "" || anchor.ModuleCommit == "" || anchor.GeneratedAt == "" {
		return runtimeCoverageAnchor{}, path, fmt.Errorf("runtime coverage anchor for %s is incomplete or does not match run %s", module.Name, runID)
	}
	workspaceCommit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return runtimeCoverageAnchor{}, path, err
	}
	moduleCommit, err := gitOutput(filepath.Join(workspace, module.Path), "rev-parse", "HEAD")
	if err != nil {
		return runtimeCoverageAnchor{}, path, err
	}
	if anchor.WorkspaceCommit != strings.TrimSpace(workspaceCommit) || anchor.ModuleCommit != strings.TrimSpace(moduleCommit) {
		return runtimeCoverageAnchor{}, path, fmt.Errorf("runtime coverage anchor commit mismatch for %s", module.Name)
	}
	return anchor, path, nil
}

func runTestCoverageAggregate(args []string) error {
	flags := flag.NewFlagSet("test-coverage-aggregate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	inputDir := flags.String("input-dir", "", "directory containing downloaded per-module coverage artifacts")
	runID := flags.String("run-id", "", "aggregate artifact run ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inputDir) == "" || strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("--input-dir and --run-id are required")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		return err
	}
	type selectedCase struct {
		result    coverageCaseResult
		report    coverageReport
		sourceDir string
	}
	selected := map[string]selectedCase{}
	err = filepath.WalkDir(*inputDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "results.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var report coverageReport
		if err := json.Unmarshal(raw, &report); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, item := range report.Cases {
			if item.Kind != "go" {
				continue
			}
			current, exists := selected[item.Name]
			if !exists || (report.Profile == "pr" && current.report.Profile != "pr") {
				selected[item.Name] = selectedCase{result: item, report: report, sourceDir: filepath.Dir(path)}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	outDir := filepath.Join(workspace, ".artifacts", "test-runs", *runID, "coverage")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	report := coverageReport{
		SchemaVersion: 2,
		RunID:         *runID,
		Profile:       "aggregate",
		StartedAt:     now.Format(time.RFC3339),
		Status:        "PASS",
		HeadRef:       "HEAD",
	}
	if commit, commitErr := gitOutput(workspace, "rev-parse", "HEAD"); commitErr == nil {
		report.WorkspaceCommit = strings.TrimSpace(commit)
	}
	for _, module := range cfg.Modules {
		if module.Kind != "go" {
			continue
		}
		item, ok := selected[module.Name]
		if !ok {
			report.Status = "FAIL"
			report.Cases = append(report.Cases, coverageCaseResult{
				TestID: module.TestID, Name: module.Name, Kind: "go", Path: module.Path,
				Purpose: module.Purpose, Method: module.Method, StartedAt: report.StartedAt,
				CompletedAt: report.StartedAt, Status: "INCOMPLETE",
				Assessment: "required module result is missing from aggregate inputs",
			})
			continue
		}
		if item.report.WorkspaceCommit != "" && report.WorkspaceCommit != "" && item.report.WorkspaceCommit != report.WorkspaceCommit {
			item.result.Status = "INCOMPLETE"
			item.result.Assessment = "workspace commit does not match aggregate checkout"
		}
		if item.result.Status != "PASS" {
			report.Status = "FAIL"
		}
		if err := copyCoverageCaseEvidence(item.sourceDir, outDir, item.result); err != nil {
			item.result.Status = "INCOMPLETE"
			item.result.Assessment = "copy coverage evidence: " + err.Error()
			report.Status = "FAIL"
		}
		report.Cases = append(report.Cases, item.result)
	}
	report.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	report.DurationMS = time.Since(now).Milliseconds()
	report.RedactionStatus = "PASS"
	if issues := findUnredactedFeatureEvidence(outDir); len(issues) > 0 {
		report.RedactionStatus = "FAIL"
		report.RedactionIssues = issues
		report.Status = "FAIL"
	}
	if report.Status == "PASS" {
		report.Assessment = "All ten governed Go module results, inventories, integration requirements, and evidence passed."
	} else {
		report.Assessment = "One or more governed Go module results or evidence sets are missing or failed."
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "TEST_REPORT.md"), renderCoverageReport(report), 0o644); err != nil {
		return err
	}
	if report.Status != "PASS" {
		return exitCode(1)
	}
	return nil
}

func copyCoverageCaseEvidence(sourceDir, outDir string, result coverageCaseResult) error {
	paths := []string{
		result.ProfilePath,
		result.PackageCoveragePath,
		result.UnitManifestPath,
		result.JUnitPath,
		result.TestEventsPath,
		result.LogPath,
		result.RuntimeEvidencePath,
	}
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		source := filepath.Join(sourceDir, filepath.FromSlash(rel))
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		target := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

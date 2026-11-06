package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Focused evidence deliberately cannot be consumed as a complete coverage
// report. Required CI still owns all overall/package/critical-case gates.
func runPrePRQuick(workspace, base, head, runID, testRun string, selection prePRSelection) error {
	if filepath.Base(runID) != runID || runID == "." || runID == ".." {
		return errors.New("quick run ID must be a single directory name")
	}
	// Git reports physical roots; normalize aliases such as macOS /var and
	// daily-checkout symlinks before comparing roots for differential coverage.
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	cfg, err := prePRLoadCoverageConfig(workspace)
	if err != nil {
		return err
	}
	for _, name := range selection.GoModules {
		index := slices.IndexFunc(cfg.Modules, func(m coverageModule) bool { return m.Name == name && m.Kind == "go" })
		if index < 0 {
			return fmt.Errorf("quick checks have no configured Go module %q", name)
		}
		module := cfg.Modules[index]
		if err := runPrePRQuickGo(workspace, base, head, runID, testRun, module, cfg.Differential.MinimumStatementPercent); err != nil {
			return err
		}
	}
	fmt.Println("Focused local checks passed. Full coverage, critical cases, integration and applicable UI checks remain REQUIRED in CI; this is not a complete pre-PR or deployment GO.")
	return nil
}

func runPrePRQuickGo(workspace, base, head, runID, testRun string, module coverageModule, minimum float64) error {
	moduleDir := filepath.Join(workspace, filepath.FromSlash(module.Path))
	out := filepath.Join(workspace, ".artifacts", "test-runs", runID, "quick", module.Name)
	if err := os.MkdirAll(out, 0700); err != nil {
		return err
	}
	diagnostic := filepath.Join(out, "diagnostic.json")
	if err := os.Remove(diagnostic); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	scratch, err := os.MkdirTemp("", "rtk-prepr-quick-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	config := filepath.Join(scratch, "config")
	if err := os.Mkdir(config, 0700); err != nil {
		return err
	}
	env := map[string]string{"TMPDIR": scratch, "XDG_CONFIG_HOME": config}
	if module.Name != "workspace-tooling" {
		env["GOWORK"] = "off"
	}
	profile := filepath.Join(out, "coverage.out")
	events := filepath.Join(out, "test-events.json")
	log := filepath.Join(out, "test.log")
	// Repeated run IDs never reuse a previous successful profile or event stream.
	for _, path := range []string{profile, events, log} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	args := []string{"test", "-json", "-count=1", "-timeout=10m", "-run=" + testRun, "-covermode=atomic", "-coverprofile=" + profile, "-coverpkg=./...", "./..."}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	defer cancel()
	if err := runGoCoverageShardCommand(ctx, moduleDir, log, events, env, args); err != nil {
		return fmt.Errorf("focused %s tests: %w (log: %s)", module.Name, err, log)
	}
	passed, err := passedPrePRFocusedTests(events)
	if err != nil {
		return err
	}
	if passed == 0 {
		return fmt.Errorf("focused %s checks ran no passing test cases; check --test-run and skipped prerequisites", module.Name)
	}
	changed, err := changedGoLines(workspace, base, head, module.Path)
	if err != nil {
		return err
	}
	percent, covered, total, missing, err := goDifferentialCoverageDetails(moduleDir, module.Path, profile, changed)
	if err != nil {
		return err
	}
	commit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	report := map[string]any{
		"status": "FOCUSED_PASS", "full_coverage": "NOT_RUN", "workspace_commit": strings.TrimSpace(commit),
		"base_ref": base, "head_ref": head, "module": module.Name, "test_run": testRun,
		"duration_ms": time.Since(started).Milliseconds(), "changed_statement_percent": percent,
		"covered_changed_statements": covered, "changed_statements": total, "uncovered_changed_statements": missing,
		"ci_differential_minimum_percent": minimum,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(diagnostic, append(raw, '\n'), 0600); err != nil {
		return err
	}
	fmt.Printf("%s focused differential diagnostic: %.2f%% (%d/%d); full CI minimum %.2f%%. Report: %s\n", module.Name, percent, covered, total, minimum, diagnostic)
	return nil
}

func passedPrePRFocusedTests(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	passed := map[string]bool{}
	parents := map[string]bool{}
	for scanner.Scan() {
		var event goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return 0, err
		}
		if event.Action == "fail" {
			return 0, fmt.Errorf("focused test failure: %s %s", event.Package, event.Test)
		}
		name := event.Test
		for {
			i := strings.LastIndex(name, "/")
			if i < 0 {
				break
			}
			name = name[:i]
			parents[event.Package+"\x00"+name] = true
		}
		if event.Action == "pass" && event.Test != "" {
			passed[event.Package+"\x00"+event.Test] = true
		}
	}
	count := 0
	for test := range passed {
		if !parents[test] {
			count++
		}
	}
	return count, scanner.Err()
}

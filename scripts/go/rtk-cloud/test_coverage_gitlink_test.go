package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDifferentialCoverageUsesExistingGitlinkWithoutParentCommitObject(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	source := filepath.Join(root, "leaf-source")
	for _, repo := range []string{workspace, source} {
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
		runTestCommand(t, repo, "git", "init", "-q")
		runTestCommand(t, repo, "git", "config", "user.email", "coverage@example.test")
		runTestCommand(t, repo, "git", "config", "user.name", "Coverage Test")
	}
	writeTestFile(t, filepath.Join(source, "go.mod"), "module example.test/service\n\ngo 1.25\n")
	writeTestFile(t, filepath.Join(source, "service.go"), "package service\n\nfunc Value() int {\n\treturn 1\n}\n")
	runTestCommand(t, source, "git", "add", "go.mod", "service.go")
	runTestCommand(t, source, "git", "commit", "-q", "-m", "initial leaf")
	runTestCommand(t, workspace, "git", "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "repos/service")
	runTestCommand(t, workspace, "git", "commit", "-q", "-m", "base gitlink")
	base, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(workspace, "repos", "service")
	runTestCommand(t, leaf, "git", "config", "user.email", "coverage@example.test")
	runTestCommand(t, leaf, "git", "config", "user.name", "Coverage Test")
	// User-level diff preferences must not suppress the parser's a/b prefixes.
	runTestCommand(t, leaf, "git", "config", "diff.noprefix", "true")
	writeTestFile(t, filepath.Join(leaf, "service.go"), "package service\n\nfunc Value() int {\n\treturn 2\n}\n")
	runTestCommand(t, leaf, "git", "add", "service.go")
	runTestCommand(t, leaf, "git", "commit", "-q", "-m", "changed leaf statement")
	runTestCommand(t, workspace, "git", "add", "repos/service")
	runTestCommand(t, workspace, "git", "commit", "-q", "-m", "updated gitlink")
	if _, err := gitOutput(workspace, "cat-file", "-e", strings.TrimSpace(base)+":repos/service"); err == nil {
		t.Fatal("fixture unexpectedly copies leaf commit objects into the parent repository")
	}
	exists, err := gitTreeEntryExists(workspace, strings.TrimSpace(base), "repos/service")
	if err != nil || !exists {
		t.Fatal("real base gitlink was not found in the tree", err)
	}
	changed, err := changedGoLines(workspace, strings.TrimSpace(base), "HEAD", "repos/service")
	if err != nil || !changed["repos/service/service.go"][4] {
		t.Fatal("existing leaf statement silently skipped differential coverage", changed, err)
	}
	profile := filepath.Join(root, "coverage.out")
	writeTestFile(t, profile, "mode: set\nexample.test/service/service.go:4.1,4.12 1 0\n")
	percent, covered, statements, uncovered, err := goDifferentialCoverageDetails(leaf, "repos/service", profile, changed)
	if err != nil || percent != 0 || covered != 0 || statements != 1 || len(uncovered) != 1 || uncovered[0] != "repos/service/service.go:4" {
		t.Fatal("changed uncovered statement became a vacuous pass", percent, covered, statements, uncovered, err)
	}
	if _, err := changedGoLines(workspace, "missing-base-ref", "HEAD", "repos/service"); err == nil {
		t.Fatal("unavailable base became a new-submodule exemption")
	}
	if exists, err := gitTreeEntryExists(workspace, "HEAD", "repos/absent"); err != nil || exists {
		t.Fatal("absent base tree entry not distinguished", exists, err)
	}
}

func TestDifferentialCoverageDeduplicatesCrossPackageBlocksAndUncoveredSources(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.test/module\n\ngo 1.25\n")
	profile := filepath.Join(dir, "coverage.out")
	writeTestFile(t, profile, "mode: set\n"+
		"example.test/module/pkg/a.go:10.1,12.2 2 0\n"+
		"example.test/module/pkg/a.go:14.1,14.8 1 0\n"+
		"example.test/module/pkg/a.go:10.1,12.2 2 1\n"+
		"example.test/module/pkg/a.go:10.1,12.2 2 0\n"+
		"example.test/module/pkg/a.go:14.1,14.8 1 0\n")
	changed := map[string]map[int]bool{"repos/service/pkg/a.go": {11: true, 14: true}}
	percent, covered, statements, uncovered, err := goDifferentialCoverageDetails(dir, "repos/service", profile, changed)
	if err != nil || covered != 2 || statements != 3 || math.Abs(percent-200.0/3) > 0.0001 || len(uncovered) != 1 || uncovered[0] != "repos/service/pkg/a.go:14" {
		t.Fatal("duplicate instrumentation distorted the differential gate", percent, covered, statements, uncovered, err)
	}
	writeTestFile(t, profile, "mode: set\nexample.test/module/pkg/a.go:10.1,12.2 2 1\nexample.test/module/pkg/a.go:10.1,12.2 3 1\n")
	if _, _, _, err := parseGoCoverageProfile(profile, nil); err == nil {
		t.Fatal("contradictory duplicate block counts accepted")
	}
}

// This opt-in read-only check re-evaluates an already completed, anchored PR
// profile after coverage-runner repairs; it never substitutes a unit profile or
// reruns/skips the original PostgreSQL and required-test evidence.
func TestReuseAnchoredBillingPRCoverageDifferential(t *testing.T) {
	profile := os.Getenv("RTK_BILLING_PR_COVERAGE_REUSE_PROFILE")
	if profile == "" {
		t.Skip("set RTK_BILLING_PR_COVERAGE_REUSE_PROFILE to re-evaluate retained PR evidence")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	base, head := os.Getenv("RTK_BILLING_PR_COVERAGE_REUSE_BASE"), os.Getenv("RTK_BILLING_PR_COVERAGE_REUSE_HEAD")
	if base == "" || head == "" {
		t.Fatal("explicit source anchors required for evidence reuse")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(profile))), "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var previous coverageReport
	if err := json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	if previous.Profile != "pr" || previous.Status != "PASS" || previous.RedactionStatus != "PASS" || len(previous.Cases) != 1 {
		t.Fatal("only completed, redacted Billing PR evidence can be reused")
	}
	module := previous.Cases[0]
	if module.Name != "billing-service" || module.CriticalGate != "PASS" {
		t.Fatal("original PostgreSQL/critical test gate not qualified")
	}
	leaf := filepath.Join(workspace, "repos", "rtk_billing")
	current, err := gitOutput(leaf, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(current) != module.SubmoduleCommit {
		t.Fatal("physical Billing source differs from tested evidence", err)
	}
	if dirty, err := gitOutput(leaf, "status", "--porcelain"); err != nil || strings.TrimSpace(dirty) != "" {
		t.Fatal("Billing source must remain clean for retained evidence reuse", err)
	}
	anchored, err := gitOutput(workspace, "rev-parse", head+":repos/rtk_billing")
	if err != nil || strings.TrimSpace(anchored) != module.SubmoduleCommit {
		t.Fatal("head gitlink differs from tested evidence", err)
	}
	sha, err := fileSHA256(profile)
	if err != nil || sha != module.ProfileSHA {
		t.Fatal("retained coverage profile changed", err)
	}
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedGoLines(workspace, base, head, "repos/rtk_billing")
	if err != nil {
		t.Fatal(err)
	}
	percent, covered, statements, _, err := goDifferentialCoverageDetails(leaf, "repos/rtk_billing", profile, changed)
	if err != nil || statements == 0 || percent < cfg.Differential.MinimumStatementPercent {
		t.Fatal("anchored differential gate not satisfied", percent, covered, statements, err)
	}
	t.Logf("Retained Billing PR profile %s: %.4f%% (%d/%d), source %s, anchors %s..%s, SHA256 %s", profile, percent, covered, statements, module.SubmoduleCommit, base, head, sha)
}

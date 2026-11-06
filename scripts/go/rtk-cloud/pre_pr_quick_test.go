package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Runs real Go coverage against two changed functions. The focused test covers
// only one: quick publication must describe that gap without claiming full CI.
func TestRunPrePRQuickProducesFocusedEvidence(t *testing.T) {
	for _, mode := range []string{"pass", "failure", "no-match", "skipped", "policy-failure"} {
		t.Run(mode, func(t *testing.T) {
			workspace := newPrePRTestWorkspace(t)
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			t.Setenv("GOWORK", "off")
			body := `if Add(2,3) != 5 { t.Fatal("sum") }`
			if mode == "failure" {
				body = `t.Fatal("deliberate focused failure")`
			}
			if mode == "skipped" {
				body = `t.Skip("unavailable")`
			}
			files := map[string]string{
				".gitignore":               ".artifacts/\n",
				"scripts/go/go.mod":        "module example/quick\n\ngo 1.25\n",
				"scripts/go/value.go":      "package quick\nfunc Add(a,b int) int { return a+b }\nfunc Sub(a,b int) int { return a-b }\n",
				"scripts/go/value_test.go": "package quick\nimport \"testing\"\nfunc TestAdd(t *testing.T) {" + body + "}\n",
			}
			for name, contents := range files {
				path := filepath.Join(workspace, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"add", "."}, {"commit", "-m", "focused fixture"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = workspace
				if raw, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v: %s", err, raw)
				}
			}
			oldMatrix, oldConfig := prePRRunMatrix, prePRLoadCoverageConfig
			t.Cleanup(func() { prePRRunMatrix, prePRLoadCoverageConfig = oldMatrix, oldConfig })
			policy := false
			prePRRunMatrix = func(args []string) error {
				if !reflect.DeepEqual(args, []string{"--policy-only"}) {
					t.Fatalf("quick policy args=%v", args)
				}
				policy = true
				if mode == "policy-failure" {
					return errors.New("invalid early policy")
				}
				return nil
			}
			prePRLoadCoverageConfig = func(string) (coverageConfig, error) {
				cfg := coverageConfig{Modules: []coverageModule{{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: []string{"./..."}, CoverPackages: []string{"./..."}}}}
				cfg.Differential.MinimumStatementPercent = 70
				return cfg, nil
			}
			run := "^TestAdd$"
			if mode == "no-match" {
				run = "^TestAbsent$"
			}
			err := runPrePR([]string{"--base", "HEAD~1", "--mode", "quick", "--test-run", run, "--run-id", "focused-fixture"})
			if mode != "pass" {
				if err == nil {
					t.Fatal("incomplete quick check passed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !policy {
				t.Fatal("policy omitted")
			}
			paths, err := filepath.Glob(filepath.Join(workspace, ".artifacts/test-runs/focused-fixture/quick/*/diagnostic.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("diagnostic paths=%v err=%v", paths, err)
			}
			raw, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Status       string   `json:"status"`
				FullCoverage string   `json:"full_coverage"`
				Differential float64  `json:"changed_statement_percent"`
				Missing      []string `json:"uncovered_changed_statements"`
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			if report.Status != "FOCUSED_PASS" || report.FullCoverage != "NOT_RUN" || report.Differential != 50 || len(report.Missing) == 0 {
				t.Fatalf("misleading focused evidence: %s", raw)
			}
		})
	}
}

func TestRunPrePRQuickRejectsAmbiguousSelection(t *testing.T) {
	workspace := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	for _, args := range [][]string{{"--mode", "unknown"}, {"--mode", "quick"}, {"--mode", "quick", "--test-run", "["}, {"--mode", "quick", "--test-run", "TestParent/Missing"}, {"--mode", "quick", "--test-run", "Test", "--matrix=false"}} {
		err := runPrePR(append([]string{"--base", "HEAD", "--dry-run"}, args...))
		if err == nil {
			t.Fatalf("invalid quick args accepted: %v", args)
		}
	}
	if err := runPrePR([]string{"--base", "HEAD", "--mode", "full", "--test-run", "Test", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "quick") {
		t.Fatalf("ignored quick-only selection: %v", err)
	}
}

func TestPrePRFocusedPassRequiresPassingLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	for _, tc := range []struct {
		child string
		want  int
	}{{"skip", 0}, {"pass", 1}} {
		raw := `{"Action":"` + tc.child + `","Package":"example","Test":"TestParent/child"}` + "\n" + `{"Action":"pass","Package":"example","Test":"TestParent"}` + "\n"
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := passedPrePRFocusedTests(path)
		if err != nil || got != tc.want {
			t.Fatalf("child=%s passed=%d want=%d err=%v", tc.child, got, tc.want, err)
		}
	}
}

func TestRunPrePRQuickRejectsNodeSelection(t *testing.T) {
	workspace := newPrePRTestWorkspaceWithScript(t, `#!/bin/sh
printf '%s\n' 'policy=true' 'go_modules=[]' 'node_modules=["cloud-admin-web"]' 'account_manager_postgres=false' 'billing_postgres=false' 'video_cloud_postgres_emqx=false'
`)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	err := runPrePR([]string{"--base", "HEAD", "--mode", "quick", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "Go changes only") {
		t.Fatalf("Node checks silently omitted: %v", err)
	}
}

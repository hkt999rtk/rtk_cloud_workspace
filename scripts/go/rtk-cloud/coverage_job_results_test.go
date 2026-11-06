package main

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCoverageJobResultsRejectIncompleteSelectedChecks(t *testing.T) {
	workspace, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github/workflows/go-coverage-governance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	aggregate := workflow.Jobs["aggregate"]
	if aggregate.If != "always() && (needs.changes.result != 'success' || needs.changes.outputs.run_coverage != 'false')" {
		t.Fatalf("aggregate can skip selector failures: %s", aggregate.If)
	}
	if len(aggregate.Steps) < 3 || aggregate.Steps[1].Env["NEEDS_JSON"] != "${{ toJSON(needs) }}" || aggregate.Steps[1].Run != "scripts/ci/check-coverage-job-results.sh" {
		t.Fatal("result gate must run immediately after checkout with actual needs context")
	}
	run := aggregate.Steps[1].Run
	flags := map[string]string{"policy": "policy", "run_unit": "unit", "run_node": "node", "account_manager_postgres": "account-manager-postgres", "billing_postgres": "billing-postgres", "video_cloud_postgres_emqx": "video-cloud-postgres-emqx"}
	type job struct {
		Result  string            `json:"result,omitempty"`
		Outputs map[string]string `json:"outputs,omitempty"`
	}
	base := func() map[string]job {
		outputs := map[string]string{"run_coverage": "true"}
		needs := map[string]job{}
		for flag, name := range flags {
			outputs[flag] = "false"
			needs[name] = job{Result: "skipped"}
		}
		outputs["run_unit"] = "true"
		needs["unit"] = job{Result: "success"}
		needs["changes"] = job{Result: "success", Outputs: outputs}
		return needs
	}
	check := func(t *testing.T, needs map[string]job, pass bool) {
		t.Helper()
		raw, err := json.Marshal(needs)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", run)
		cmd.Dir = workspace
		cmd.Env = withEnv(os.Environ(), map[string]string{"NEEDS_JSON": string(raw)})
		out, err := cmd.CombinedOutput()
		if (err == nil) != pass {
			t.Fatalf("gate pass=%v want %v: %v: %s", err == nil, pass, err, out)
		}
	}
	t.Run("selective success with unselected skipped", func(t *testing.T) { check(t, base(), true) })
	t.Run("full success", func(t *testing.T) {
		needs := base()
		for flag, name := range flags {
			needs["changes"].Outputs[flag] = "true"
			needs[name] = job{Result: "success"}
		}
		check(t, needs, true)
	})
	for flag, name := range flags {
		for _, result := range []string{"failure", "cancelled", "skipped", "", "unknown"} {
			t.Run(flag+" selected "+result, func(t *testing.T) {
				needs := base()
				needs["changes"].Outputs[flag] = "true"
				needs[name] = job{Result: result}
				check(t, needs, false)
			})
		}
		t.Run(flag+" missing selected job", func(t *testing.T) {
			needs := base()
			needs["changes"].Outputs[flag] = "true"
			delete(needs, name)
			check(t, needs, false)
		})
		for _, value := range []string{"", "unknown"} {
			t.Run(flag+" invalid selector "+value, func(t *testing.T) { needs := base(); needs["changes"].Outputs[flag] = value; check(t, needs, false) })
		}
		t.Run(flag+" missing selector", func(t *testing.T) { needs := base(); delete(needs["changes"].Outputs, flag); check(t, needs, false) })
		t.Run(flag+" unknown unselected result", func(t *testing.T) {
			needs := base()
			needs["changes"].Outputs[flag] = "false"
			needs[name] = job{Result: "unknown"}
			check(t, needs, false)
		})
	}
	for _, result := range []string{"failure", "cancelled", "skipped", "", "unknown"} {
		t.Run("selector "+result, func(t *testing.T) {
			needs := base()
			changes := needs["changes"]
			changes.Result = result
			needs["changes"] = changes
			check(t, needs, false)
		})
	}
	t.Run("missing changes", func(t *testing.T) { needs := base(); delete(needs, "changes"); check(t, needs, false) })
	t.Run("unknown coverage selector", func(t *testing.T) {
		needs := base()
		needs["changes"].Outputs["run_coverage"] = "unknown"
		check(t, needs, false)
	})
	t.Run("malformed context", func(t *testing.T) {
		cmd := exec.Command("bash", "-c", run)
		cmd.Dir = workspace
		cmd.Env = withEnv(os.Environ(), map[string]string{"NEEDS_JSON": "{"})
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("malformed needs accepted: %s", out)
		}
	})
}

func TestCoverageJobResultsScriptChangeSelectsItsChecks(t *testing.T) {
	selector, err := os.ReadFile(filepath.Join("..", "..", "ci", "select-coverage-jobs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := newPrePRTestWorkspaceWithScript(t, string(selector))
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workspace, "scripts/ci/check-coverage-job-results.sh"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "coverage result gate")
	cmd := exec.Command("bash", "scripts/ci/select-coverage-jobs.sh", base, "HEAD", "pull_request")
	cmd.Dir = workspace
	cmd.Env = withoutEnvironmentKey(os.Environ(), "GITHUB_OUTPUT")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("selector: %v: %s", err, out)
	}
	got, err := parsePrePRSelection(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Policy || !slices.Equal(got.GoModules, []string{"workspace-tooling"}) || !strings.Contains(string(out), "run_unit=true\n") || !strings.Contains(string(out), "run_coverage=true\n") {
		t.Fatalf("gate-only change skipped its checks: %s", out)
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRunTestMatrixUsesExtendedWorkspaceTestTimeout(t *testing.T) {
	workspace := t.TempDir()
	var got []string
	checked := false
	err := runTestMatrixWithChecks(workspace, false, func(path string, policyOnly bool) error {
		if path != workspace || policyOnly {
			t.Fatalf("policy input = %q, %t", path, policyOnly)
		}
		checked = true
		return nil
	}, func(path, name string, args ...string) error {
		if !checked || path != filepath.Join(workspace, "scripts", "go") {
			t.Fatalf("baseline must run after policy checks in scripts/go: %q", path)
		}
		got = append([]string{name}, args...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "test", "-count=1", "-timeout=20m", "./..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace baseline command = %q, want %q", got, want)
	}
}

func TestTestMatrixPolicyOnlyAndPolicyFailureNeverRunBaseline(t *testing.T) {
	policyError := errors.New("inventory is stale")
	for _, tc := range []struct {
		name       string
		policyOnly bool
		err        error
	}{
		{name: "coverage owns baseline", policyOnly: true},
		{name: "policy error fails early", err: policyError},
		{name: "policy-only still validates", policyOnly: true, err: policyError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := runTestMatrixWithChecks(t.TempDir(), tc.policyOnly, func(_ string, policyOnly bool) error {
				called = true
				if policyOnly != tc.policyOnly {
					t.Fatal("policy-only flag not passed to metadata validation")
				}
				return tc.err
			}, func(string, string, ...string) error {
				t.Fatal("baseline must not run")
				return nil
			})
			if !called || !errors.Is(err, tc.err) {
				t.Fatalf("policy called = %t, error = %v", called, err)
			}
		})
	}
}

func TestWorkspaceBaselineDisablesAmbientGoWorkspace(t *testing.T) {
	// Inspect the real subprocess environment without loading this repository or
	// executing another copy of the test suite.
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "unrelated.go.work"))
	if err := runWorkspaceBaselineCmd(t.TempDir(), "sh", "-c", `test "$GOWORK" = off`); err != nil {
		t.Fatalf("baseline did not isolate the module dependency graph: %v", err)
	}
}

func TestWorkspaceBaselineCoverageRejectsNarrowerOrDifferentScope(t *testing.T) {
	valid := coverageModule{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: []string{"./..."}}
	if err := validateWorkspaceBaselineCoverage(coverageConfig{Modules: []coverageModule{valid}}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*coverageModule){
		func(m *coverageModule) { m.Name = "other" },
		func(m *coverageModule) { m.Kind = "javascript" },
		func(m *coverageModule) { m.Path = "other" },
		func(m *coverageModule) { m.Packages = []string{"./rtk-cloud"} },
		func(m *coverageModule) { m.Packages = nil },
	} {
		module := valid
		mutate(&module)
		if err := validateWorkspaceBaselineCoverage(coverageConfig{Modules: []coverageModule{module}}); err == nil {
			t.Fatalf("accepted incomplete baseline replacement: %+v", module)
		}
	}
	if err := validateWorkspaceBaselineCoverage(coverageConfig{}); err == nil {
		t.Fatal("accepted missing baseline replacement")
	}
}

func TestCoverageWorkflowDelegatesBaselineOnlyToRequiredSelectedCoverage(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "go-coverage-governance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	var selected, aggregate string
	for _, step := range workflow.Jobs["policy"].Steps {
		if step.Name == "Validate catalog, policy, inventory, and generated docs" {
			selected = step.Env["BASELINE_COVERAGE_SELECTED"]
			if !strings.Contains(step.Run, `if [ "$BASELINE_COVERAGE_SELECTED" = true ]; then`) || !strings.Contains(step.Run, "--policy-only") {
				t.Fatal("policy must retain baseline unless selected coverage owns it")
			}
		}
	}
	for _, condition := range []string{"needs.changes.outputs.run_unit == 'true'", "needs.changes.outputs.run_coverage == 'true'", "contains(fromJSON(needs.changes.outputs.go_modules), 'workspace-tooling')", "contains(fromJSON(needs.changes.outputs.coverage_modules), 'workspace-tooling')"} {
		if !strings.Contains(selected, condition) {
			t.Errorf("baseline substitution lacks %s", condition)
		}
	}
	for _, step := range workflow.Jobs["aggregate"].Steps {
		if step.Name == "Require selected checks to succeed" {
			aggregate = step.Env["SELECTED_CHECKS_PASSED"]
			if step.Run != `test "$SELECTED_CHECKS_PASSED" = true` {
				t.Fatal("aggregate must fail when a selected prerequisite failed or was skipped")
			}
		}
	}
	for _, job := range []string{"policy", "unit", "node", "account-manager-postgres", "billing-postgres", "video-cloud-postgres-emqx"} {
		if !strings.Contains(aggregate, "needs."+job+".result == 'success'") {
			t.Errorf("aggregate does not require %s to succeed when selected", job)
		}
	}
}

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func billingPrivateWireWorkflowStep(t *testing.T) billingUnitWorkflowStep {
	t.Helper()
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
				billingUnitWorkflowStep `yaml:",inline"`
				WorkingDirectory        string `yaml:"working-directory"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	start, coverage, wire, cleanup := -1, -1, -1, -1
	var result billingUnitWorkflowStep
	for i, step := range workflow.Jobs["unit"].Steps {
		switch step.Name {
		case "Start isolated Billing unit PostgreSQL fixture":
			start = i
		case "Run governed unit coverage":
			coverage = i
		case "Verify Billing Logger private retention wire":
			wire, result = i, step.billingUnitWorkflowStep
			if step.If != "matrix.module == 'billing-service'" || step.Shell != "bash" || step.WorkingDirectory != "repos/rtk_billing" {
				t.Fatal("private wire acceptance must use the Billing-only unit fixture and owning module")
			}
			if _, replacesFixture := step.Env["TEST_DATABASE_URL"]; replacesFixture {
				t.Fatal("wire acceptance must inherit the validated owned fixture DSN")
			}
		case "Stop isolated Billing unit PostgreSQL fixture":
			cleanup = i
		}
	}
	if start < 0 || coverage <= start || wire <= coverage || cleanup <= wire {
		t.Fatal("private wire test must run between governed coverage and owned fixture cleanup")
	}
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(result.Run)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("wire workflow shell syntax: %v\n%s", err, output)
	}
	return result
}

func TestBillingPrivateWireWorkflowRequiresActualTaggedPassEvidence(t *testing.T) {
	step := billingPrivateWireWorkflowStep(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("wire acceptance regression requires jq, matching its CI runner prerequisite")
	}
	const mainTest = "TestRawRetentionActualLoggerPgPrivateWire"
	for _, scenario := range []string{"passed", "main-skipped", "main-missing", "branch-missing", "branch-skipped", "child-failed", "missing-dsn", "missing-owner"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			root := filepath.Join(dir, "workspace")
			runner := filepath.Join(dir, "runner")
			billing := filepath.Join(root, "repos", "rtk_billing")
			for _, path := range []string{bin, runner, billing, filepath.Join(root, "repos", "rtk_cloud_logger")} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var events strings.Builder
			for _, name := range []string{mainTest, mainTest + "/completed", mainTest + "/aborted", mainTest + "/recovery-fenced"} {
				if scenario == "main-missing" && name == mainTest || scenario == "branch-missing" && name == mainTest+"/aborted" {
					continue
				}
				action := "pass"
				if scenario == "main-skipped" && name == mainTest || scenario == "branch-skipped" && name == mainTest+"/recovery-fenced" {
					action = "skip"
				}
				raw, err := json.Marshal(goTestEvent{Action: action, Test: name})
				if err != nil {
					t.Fatal(err)
				}
				events.Write(raw)
				events.WriteByte('\n')
			}
			eventsPath := filepath.Join(dir, "events.json")
			if err := os.WriteFile(eventsPath, []byte(events.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			goLog := filepath.Join(dir, "go.log")
			// Compile selection and workspace boundaries are checked by a strict
			// Go fake; the workflow executes its real jq acceptance predicate.
			mockGo := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_GO_LOG"
if [ "$1" = work ]; then
  [ "$*" = "work init $GITHUB_WORKSPACE/repos/rtk_billing $GITHUB_WORKSPACE/repos/rtk_cloud_logger" ] || exit 91
  [ "$GOWORK" = off ] || exit 92
  case "$PWD" in "$RUNNER_TEMP"/rtk-billing-logger-wire.*) ;; *) exit 93 ;; esac
  printf 'go 1.26.3\n' > go.work
else
  [ "$*" = 'test -json -count=1 -tags=billing_logger_integration -run ^TestRawRetentionActualLoggerPgPrivateWire$ -timeout=180s ./internal/api' ] || exit 94
  [ "$PWD" -ef "$GITHUB_WORKSPACE/repos/rtk_billing" ] || exit 95
  case "$GOWORK" in "$RUNNER_TEMP"/rtk-billing-logger-wire.*/go.work) ;; *) exit 96 ;; esac
  [ -f "$GOWORK" ] || exit 97
  [ "$TEST_DATABASE_URL" = 'postgres://fixture:fixture@127.0.0.1:63422/wire?sslmode=disable' ] || exit 98
  [ "$MOCK_SCENARIO" != child-failed ] || exit 23
  cat "$MOCK_WIRE_EVENTS"
fi
`
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(mockGo), 0o755); err != nil {
				t.Fatal(err)
			}
			dsn := "postgres://fixture:fixture@127.0.0.1:63422/wire?sslmode=disable"
			owner := "owned-fixture-id"
			if scenario == "missing-dsn" {
				dsn = ""
			}
			if scenario == "missing-owner" {
				owner = ""
			}
			command := exec.Command("bash", "-c", step.Run)
			command.Dir = billing
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_WORKSPACE="+root, "RUNNER_TEMP="+runner,
				"GITHUB_RUN_ID=424242", "GITHUB_RUN_ATTEMPT=7",
				"TEST_DATABASE_URL="+dsn, "BILLING_UNIT_POSTGRES_CONTAINER_ID="+owner,
				"MOCK_GO_LOG="+goLog, "MOCK_WIRE_EVENTS="+eventsPath, "MOCK_SCENARIO="+scenario,
			)
			output, err := command.CombinedOutput()
			if (err == nil) != (scenario == "passed") {
				t.Fatalf("actual wire shell accepted=%v for %s: %v\n%s", err == nil, scenario, err, output)
			}
			if scenario == "missing-dsn" || scenario == "missing-owner" {
				if _, err := os.Stat(goLog); !os.IsNotExist(err) {
					t.Fatalf("missing fixture input must fail before compiling: %v", err)
				}
			}
			matches, err := filepath.Glob(filepath.Join(runner, "rtk-billing-logger-wire.*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary Go workspace must be removed after success or failure: %v, %v", matches, err)
			}
			if _, err := os.Stat(filepath.Join(billing, "go.work")); !os.IsNotExist(err) {
				t.Fatal("wire test must not create a workspace in the leaf checkout")
			}
		})
	}
}

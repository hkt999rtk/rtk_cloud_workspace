package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeCoverageWorkflowKeepsSharedClusterGuardrails(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "go-runtime-coverage-nightly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"mode:",
		"options: [preflight, run]",
		"video-cloud-staging-lke",
		"group: staging-mutating-tests",
		"RUNTIME_COVERAGE_NIGHTLY_ENABLED",
		"RUNTIME_COVERAGE_SHARED_CLUSTER: \"1\"",
		"--preflight --plan --apply --deploy --artifacts",
		"runtime-coverage-k8s.sh cleanup",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("runtime workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{"--dns", "cloud_env/staging/lke\n"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("runtime workflow contains forbidden value %q", forbidden)
		}
	}
	feature, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "feature-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feature), "group: staging-mutating-tests") {
		t.Fatal("feature qualification does not share the staging mutation lock")
	}
}

func TestRuntimeCoveragePreflightWrongConfirmationIsBlocked(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "preflight.json")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	command.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=unit-preflight",
		"RUNTIME_COVERAGE_MODE=run",
		"RUNTIME_COVERAGE_CONFIRM=wrong-stack",
		"CLOUD_STAGING_LKE_CLUSTER_LABEL=video-cloud-staging-lke",
		"RUNTIME_COVERAGE_PREFLIGHT_REPORT="+report,
	)
	if err := command.Run(); err == nil {
		t.Fatal("preflight accepted a wrong confirmation")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status   string   `json:"status"`
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "BLOCKED" || !strings.Contains(strings.Join(parsed.Failures, "\n"), "requires confirmation") {
		t.Fatalf("preflight report = %#v", parsed)
	}
}

func TestRuntimeCleanupWritesResidualAndStagingAnchorReport(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"cleanup-report.json",
		"residual_namespaces",
		"residual_pvcs",
		"residual_pods",
		"staging deployment UID or image changed",
		"rtk-cloud-run-id=$run_id",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("cleanup gate missing %q", required)
		}
	}
}

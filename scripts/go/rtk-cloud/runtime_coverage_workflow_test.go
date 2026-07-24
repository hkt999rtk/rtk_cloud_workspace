package main

import (
	"encoding/json"
	"fmt"
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
		"runs-on: ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_LABEL: ubuntu-24.04",
		"--preflight --plan --apply --deploy --artifacts",
		"Prepare run-scoped Clip credentials",
		"exec deployment/video-cloud-api -- /app/admin-token --ttl 1h",
		"VIDEO_CLOUD_LOAD_CLIP_USER_PRIVATE_KEY=$user_private",
		"VIDEO_CLOUD_LOAD_CLIP_SERVER_PUBLIC_KEY=$server_public",
		"runtime-coverage-k8s.sh cleanup",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("runtime workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"--dns",
		"cloud_env/staging/lke\n",
		"jarvis-macos",
		"secrets.VIDEO_CLOUD_ADMIN_TOKEN",
		"secrets.CLIP_USER_PRIVATE_KEY_PATH",
		"secrets.CLIP_SERVER_PUBLIC_KEY_PATH",
	} {
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
		"RUNTIME_COVERAGE_RUNNER_LABEL=ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_OS=Linux",
		"RUNTIME_COVERAGE_RUNNER_ARCH=X64",
		"GITHUB_ACTIONS=true",
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
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

func TestRuntimeCoveragePreflightWrongRunnerArchitectureIsBlocked(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "preflight.json")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	command.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=unit-preflight",
		"RUNTIME_COVERAGE_MODE=preflight",
		"CLOUD_STAGING_LKE_CLUSTER_LABEL=video-cloud-staging-lke",
		"RUNTIME_COVERAGE_RUNNER_LABEL=ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_OS=Linux",
		"RUNTIME_COVERAGE_RUNNER_ARCH=X64",
		"GITHUB_ACTIONS=true",
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=ARM64",
		"RUNTIME_COVERAGE_PREFLIGHT_REPORT="+report,
	)
	if err := command.Run(); err == nil {
		t.Fatal("preflight accepted a wrong runner architecture")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status string `json:"status"`
		Runner struct {
			Label        string `json:"label"`
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"runner"`
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "BLOCKED" ||
		parsed.Runner.Label != "ubuntu-24.04" ||
		parsed.Runner.OS != "Linux" ||
		parsed.Runner.Architecture != "ARM64" ||
		!strings.Contains(strings.Join(parsed.Failures, "\n"), "runner architecture must be X64") {
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

func TestRuntimeSnapshotHandlesKubernetesPayloadBeyondArgumentLimit(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	deployments := filepath.Join(root, "deployments.json")
	pods := filepath.Join(root, "pods.json")
	if err := os.WriteFile(deployments, []byte(`{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"api","uid":"deployment-uid"},"spec":{"template":{"spec":{"containers":[{"image":"example/api:main"}]}}}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	podItems := make([]map[string]any, 0, 18000)
	for index := 0; index < cap(podItems); index++ {
		podItems = append(podItems, map[string]any{
			"status": map[string]any{
				"containerStatuses": []map[string]string{{
					"imageID": fmt.Sprintf("docker-pullable://example/api@sha256:%064x", index),
				}},
			},
		})
	}
	podJSON, err := json.Marshal(map[string]any{"items": podItems})
	if err != nil {
		t.Fatal(err)
	}
	if len(podJSON) < 2*1024*1024 {
		t.Fatalf("fixture must exceed a typical ARG_MAX, got %d bytes", len(podJSON))
	}
	if err := os.WriteFile(pods, podJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(bin, "kubectl")
	fakeKubectl := `#!/usr/bin/env bash
case " $* " in
  *" get deployments "*) cat "$FAKE_DEPLOYMENTS" ;;
  *" get pods "*) cat "$FAKE_PODS" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(kubectl, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"), "snapshot")
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_WORKSPACE="+root,
		"RUNTIME_COVERAGE_RUN_ID=runtime-large-snapshot",
		"RUNTIME_COVERAGE_STACK=coverage-large-snapshot",
		"KUBECONFIG="+kubeconfig,
		"FAKE_DEPLOYMENTS="+deployments,
		"FAKE_PODS="+pods,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("snapshot failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".artifacts", "runtime-coverage", "runtime-large-snapshot", "staging-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Deployments  []any    `json:"deployments"`
		ImageDigests []string `json:"image_digests"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Deployments) != 1 || len(snapshot.ImageDigests) != len(podItems) {
		t.Fatalf("snapshot counts = deployments:%d digests:%d", len(snapshot.Deployments), len(snapshot.ImageDigests))
	}
}

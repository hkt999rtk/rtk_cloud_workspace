package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStagingE2EResetDeletesWorkloadsByDefault(t *testing.T) {
	workspace, envRoot := makeStagingResetTestEnv(t)
	outDir := filepath.Join(t.TempDir(), "out")
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("CLOUD_PROVIDER", "lke")
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	t.Setenv("CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT", fakeStagingPhaseCommand(t, logPath, "reset"))
	t.Setenv("CLOUD_STAGING_E2E_PROVISION_SCRIPT", fakeStagingPhaseCommand(t, logPath, "provision"))
	t.Setenv("CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT", fakeStagingPhaseDataSetupCommand(t, logPath))
	t.Setenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT", fakeStagingPhaseMQTTCommand(t, logPath))
	t.Setenv("CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT", fakeStagingPhaseMQTTLogVerifyCommand(t, logPath))
	t.Setenv("CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT", fakeStagingPhaseBillingVerifyCommand(t, logPath))

	if err := runStagingE2ETest([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--run",
		"--confirm", "video-cloud-staging",
		"--out-dir", outDir,
		"--steps", "reset,provision,data,mqtt,runtime-logs",
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "reset DESTRUCTIVE=1") {
		t.Fatalf("full E2E reset should delete workload resources by default, got:\n%s", log)
	}
	if strings.Contains(log, "reset ARGS=--workspace "+workspace+" --env-root "+envRoot+" --yes --purge-storage") {
		t.Fatalf("full E2E reset should not purge storage by default, got:\n%s", log)
	}
}

func TestRunStagingE2EResetRejectsMissingEmailDeliveryBeforeMutation(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	for _, key := range accountManagerEmailSecretKeys {
		t.Setenv(key, "")
	}
	logPath := filepath.Join(t.TempDir(), "commands.log")
	outDir := filepath.Join(t.TempDir(), "out")
	t.Setenv("CLOUD_PROVIDER", "lke")
	t.Setenv("CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT", fakeStagingPhaseCommand(t, logPath, "reset"))

	err := runStagingE2ETest([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--run",
		"--confirm", "video-cloud-staging",
		"--out-dir", outDir,
		"--steps", "reset",
	})
	if err == nil || !strings.Contains(err.Error(), "staging reset blocked before deleting workloads") {
		t.Fatalf("missing email delivery error = %v", err)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatalf("reset created output before email preflight: %v", statErr)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("reset command ran before email preflight: %v", statErr)
	}
}

func TestRunRemoveK8sPreservesStorageByDefault(t *testing.T) {
	workspace, envRoot := makeStagingResetTestEnv(t)
	kubectlLog := fakeKubectlForStagingReset(t)
	t.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", "1")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig.yaml"))

	if err := runRemoveK8s([]string{"--workspace", workspace, "--env-root", envRoot, "--yes"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, kubectlLog)
	if strings.Contains(log, "delete namespace") {
		t.Fatalf("default reset must preserve namespaces/PVC storage, got:\n%s", log)
	}
	if strings.Contains(log, "delete pvc") {
		t.Fatalf("default reset must not delete PVCs, got:\n%s", log)
	}
	if !strings.Contains(log, "delete deployment,statefulset,daemonset,job,cronjob --all --ignore-not-found=true") {
		t.Fatalf("default reset should delete workload resources, got:\n%s", log)
	}
}

func TestRunRemoveK8sPlanPrintsCleanupScopeWithoutConfirmation(t *testing.T) {
	workspace, envRoot := makeStagingResetTestEnv(t)
	t.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", "1")
	stdout, stderr, err := captureOutput(func() error {
		return runRemoveK8s([]string{"--workspace", workspace, "--env-root", envRoot, "--plan", "--purge-storage"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"cloud-remove-k8s plan",
		"mode: destructive namespace cleanup",
		"purge_storage: true",
		"delete workloads, PVCs, then namespace video-cloud-staging-platform",
		"delete workloads, PVCs, then namespace video-cloud-staging-video-cloud",
		"delete workloads, PVCs, then namespace video-cloud-staging-ingress",
		"delete workloads, PVCs, then namespace video-cloud-staging-secrets",
		"delete workloads, PVCs, then namespace video-cloud-staging-logger",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("plan missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunRemoveK8sPurgeStorageDeletesPVCAndNamespaces(t *testing.T) {
	workspace, envRoot := makeStagingResetTestEnv(t)
	kubectlLog := fakeKubectlForStagingReset(t)
	t.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", "1")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig.yaml"))

	if err := runRemoveK8s([]string{"--workspace", workspace, "--env-root", envRoot, "--yes", "--purge-storage"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, kubectlLog)
	if !strings.Contains(log, "delete pvc --all --ignore-not-found=true") {
		t.Fatalf("purge reset should delete PVCs, got:\n%s", log)
	}
	workloadDelete := strings.Index(log, "delete deployment,statefulset,daemonset,job,cronjob --all --ignore-not-found=true")
	pvcDelete := strings.Index(log, "delete pvc --all --ignore-not-found=true")
	if workloadDelete < 0 || pvcDelete < 0 || workloadDelete > pvcDelete {
		t.Fatalf("purge reset should delete workloads before PVCs, got:\n%s", log)
	}
	if !strings.Contains(log, "delete namespace video-cloud-staging-platform --ignore-not-found=true") {
		t.Fatalf("purge reset should delete staging namespaces, got:\n%s", log)
	}
	if !strings.Contains(log, "delete namespace video-cloud-staging-logger --ignore-not-found=true") {
		t.Fatalf("purge reset should delete logger namespace, got:\n%s", log)
	}
}

func captureOutput(fn func() error) (string, string, error) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return "", "", err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return "", "", err
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	runErr := fn()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdout, readStdoutErr := io.ReadAll(stdoutR)
	stderr, readStderrErr := io.ReadAll(stderrR)
	_ = stdoutR.Close()
	_ = stderrR.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	if readStdoutErr != nil {
		return "", "", readStdoutErr
	}
	if readStderrErr != nil {
		return "", "", readStderrErr
	}
	return string(stdout), string(stderr), runErr
}

func TestResolveLKEImagesUsesExistingEnvRootManifest(t *testing.T) {
	_, envRoot := makeStagingResetTestEnv(t)
	clearLKEImageEnvForTest(t)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json"), `{
  "env": {
    "LKE_POSTGRES_IMAGE": "postgres:16-alpine",
    "LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/rtk/video-cloud:manifest",
    "LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/rtk/account-manager:manifest",
    "LKE_CLOUD_ADMIN_IMAGE": "registry.example.test/rtk/cloud-admin:manifest",
    "LKE_FRONTEND_IMAGE": "registry.example.test/rtk/frontend:manifest",
    "LKE_CLOUD_LOGGER_IMAGE": "registry.example.test/rtk/cloud-logger:manifest"
  }
}`)

	if err := resolveLKEImagesIfNeeded(t.TempDir(), envRoot); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("LKE_VIDEO_CLOUD_IMAGE"); got != "registry.example.test/rtk/video-cloud:manifest" {
		t.Fatalf("expected video image from existing manifest, got %q", got)
	}
}

func TestResolveLKEImagesRejectsStaleManifestWhenStackPinsImage(t *testing.T) {
	_, envRoot := makeStagingResetTestEnv(t)
	clearLKEImageEnvForTest(t)
	appendTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "LKE_ACCOUNT_MANAGER_IMAGE=registry.example.test/rtk/account-manager:new\n")
	manifest := filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json")
	writeTestFile(t, manifest, `{
  "env": {
    "LKE_POSTGRES_IMAGE": "postgres:16-alpine",
    "LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/rtk/video-cloud:manifest",
    "LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/rtk/account-manager:old",
    "LKE_CLOUD_ADMIN_IMAGE": "registry.example.test/rtk/cloud-admin:manifest",
    "LKE_FRONTEND_IMAGE": "registry.example.test/rtk/frontend:manifest",
    "LKE_CLOUD_LOGGER_IMAGE": "registry.example.test/rtk/cloud-logger:manifest"
  }
}`)

	err := resolveLKEImagesIfNeeded(t.TempDir(), envRoot)
	if err == nil {
		t.Fatal("expected stale manifest mismatch error")
	}
	if !strings.Contains(err.Error(), "LKE image artifact mismatch for LKE_ACCOUNT_MANAGER_IMAGE") || !strings.Contains(err.Error(), manifest) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunStagingProvisionPlanReportsExistingLKEImageArtifact(t *testing.T) {
	workspace, envRoot := makeStagingResetTestEnv(t)
	clearLKEImageEnvForTest(t)
	manifest := filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json")
	writeTestFile(t, manifest, `{
  "env": {
    "LKE_POSTGRES_IMAGE": "postgres:16-alpine",
    "LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/rtk/video-cloud:manifest",
    "LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/rtk/account-manager:manifest",
    "LKE_CLOUD_ADMIN_IMAGE": "registry.example.test/rtk/cloud-admin:manifest",
    "LKE_FRONTEND_IMAGE": "registry.example.test/rtk/frontend:manifest",
    "LKE_CLOUD_LOGGER_IMAGE": "registry.example.test/rtk/cloud-logger:manifest"
  }
}`)

	out := captureStdout(t, func() {
		if err := runStagingProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--plan"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "image_resolve: existing artifact ("+manifest+")") {
		t.Fatalf("expected existing image artifact in plan, got:\n%s", out)
	}
}

func clearLKEImageEnvForTest(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LKE_VIDEO_CLOUD_IMAGE",
		"LKE_ACCOUNT_MANAGER_IMAGE",
		"LKE_CLOUD_ADMIN_IMAGE",
		"LKE_FRONTEND_IMAGE",
		"LKE_CLOUD_LOGGER_IMAGE",
	} {
		t.Setenv(key, "")
	}
}

func makeStagingResetTestEnv(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
AUTH_TOKEN_BASE_URL=https://admin.video-cloud-staging.realtekconnect.com
SENDMAIL_HTTP_BASE_URL=https://sm.realtekconnect.com
SENDMAIL_HTTP_BEARER_TOKEN=test-token
SENDMAIL_HTTP_TIMEOUT=15s
`)
	return workspace, envRoot
}

func fakeKubectlForStagingReset(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
} >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeStagingPhaseCommand(t *testing.T, logPath, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf '%s ARGS=%s\n' "` + name + `" "$*"
  printf '%s DESTRUCTIVE=%s\n' "` + name + `" "${CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET:-}"
} >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeStagingPhaseDataSetupCommand(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setup-data")
	script := `#!/usr/bin/env bash
set -euo pipefail
out_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [[ -z "$out_dir" ]]; then
  echo "missing --out-dir" >&2
  exit 1
fi
mkdir -p "$out_dir" "$out_dir/bind-validation"
printf '{}\n' > "$out_dir/users.json"
printf '{}\n' > "$out_dir/bind.json"
printf '{"overall":"pass"}\n' > "$out_dir/bind-validation/summary.json"
printf '{"users_file":"%s","device_bind_file":"%s","summary_file":"%s","bind_validation_dir":"%s"}\n' "$out_dir/users.json" "$out_dir/bind.json" "$out_dir/summary.json" "$out_dir/bind-validation" > "$out_dir/summary.json"
printf 'setup-data ARGS=%s\n' "$*" >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeStagingPhaseMQTTCommand(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mqtt-test")
	script := `#!/usr/bin/env bash
set -euo pipefail
out_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$out_dir"
printf '{"overall":"pass"}\n' > "$out_dir/results.json"
printf 'mqtt-test ARGS=%s\n' "$*" >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeStagingPhaseMQTTLogVerifyCommand(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mqtt-log-verify")
	script := `#!/usr/bin/env bash
set -euo pipefail
out_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$out_dir"
printf '{"overall":"pass"}\n' > "$out_dir/summary.json"
printf 'mqtt-log-verify ARGS=%s\n' "$*" >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeStagingPhaseBillingVerifyCommand(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "billing-verify")
	script := `#!/usr/bin/env bash
set -euo pipefail
out_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$out_dir"
printf '{"overall":"pass"}\n' > "$out_dir/summary.json"
printf 'billing-verify ARGS=%s\n' "$*" >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendTestFile(t *testing.T, path string, body string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

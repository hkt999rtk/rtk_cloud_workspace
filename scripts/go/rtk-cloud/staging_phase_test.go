package main

import (
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

	if err := runStagingE2ETest([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--run",
		"--confirm", "video-cloud-staging",
		"--out-dir", outDir,
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
	if !strings.Contains(log, "delete namespace video-cloud-staging-platform --ignore-not-found=true") {
		t.Fatalf("purge reset should delete staging namespaces, got:\n%s", log)
	}
}

func makeStagingResetTestEnv(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
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

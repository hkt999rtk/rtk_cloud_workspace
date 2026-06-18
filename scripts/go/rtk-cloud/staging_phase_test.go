package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

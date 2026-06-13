package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunProvisionLKEApplyUsesKubectl(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if count := strings.Count(log, "ARGS apply -f -"); count != len(lkeNamespaces(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}))+1 {
		t.Fatalf("unexpected kubectl apply count in log:\n%s", log)
	}
	if !strings.Contains(log, "kind: Namespace") || !strings.Contains(log, "kind: ConfigMap") {
		t.Fatalf("expected namespace and configmap manifests, got:\n%s", log)
	}
}

func TestRunProvisionLKEPlanWithoutStackUsesProviderEnv(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	t.Setenv("CLOUD_PROVIDER", "lke")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--plan"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunProvisionLKEDeployRequiresImages(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"})
	if err == nil || !strings.Contains(err.Error(), "LKE deploy requires container image environment variables") {
		t.Fatalf("expected image requirement error, got %v", err)
	}
}

func TestRunDeployLKEVideoOnlyUsesVideoImage(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")

	if err := runDeploy([]string{"--workspace", workspace, "--env-root", envRoot, "--video-only"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "ARGS version --client") {
		t.Fatalf("expected preflight kubectl version, got:\n%s", log)
	}
	if !strings.Contains(log, "name: video-cloud-api") {
		t.Fatalf("expected video-cloud deployment manifest, got:\n%s", log)
	}
	if strings.Contains(log, "name: account-manager") || strings.Contains(log, "name: cloud-admin") {
		t.Fatalf("video-only deploy should not apply account-manager/admin manifests:\n%s", log)
	}
}

func TestRunRemoveAllVMLKERemovesNamespaces(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runRemoveAllVM([]string{"--workspace", workspace, "--env-root", envRoot, "--yes"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "ARGS delete namespace --ignore-not-found") {
		t.Fatalf("expected namespace delete, got:\n%s", log)
	}
	if strings.Contains(log, "/linode/instances") {
		t.Fatalf("LKE removal should not call Linode VM APIs:\n%s", log)
	}
}

func makeLKETestEnv(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte(`CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspace, envRoot
}

func fakeKubectl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "config" && "${2:-}" == "current-context" ]]; then
  printf 'test-context\n'
  exit 0
fi
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
  if [[ "${1:-}" == "apply" ]]; then
    cat
    printf '\n---\n'
  fi
} >> "` + logPath + `"
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	return logPath
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

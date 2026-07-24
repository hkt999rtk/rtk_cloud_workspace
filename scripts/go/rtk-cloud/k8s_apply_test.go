package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubectlApplyRetriesTransientAPITimeout(t *testing.T) {
	testKubectlRetriesTransientAPITimeout(t, func() error {
		return kubectlApply("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: test\n")
	})
}

func TestRunKubectlRetriesTransientAPITimeout(t *testing.T) {
	testKubectlRetriesTransientAPITimeout(t, func() error {
		return runKubectl("get", "nodes")
	})
}

func TestWaitForKubernetesAPIReadyPollsUntilReady(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	statePath := filepath.Join(dir, "readyz-attempt")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf 'args=%s\n' "$*" >> "` + logPath + `"
if [[ "$*" == *"get --raw=/readyz"* ]]; then
  attempt=0
  if [[ -f "` + statePath + `" ]]; then
    attempt="$(cat "` + statePath + `")"
  fi
  attempt=$((attempt + 1))
  printf '%s' "$attempt" > "` + statePath + `"
  if [[ "$attempt" -eq 1 ]]; then
    echo 'Unable to connect to the server: dial tcp 192.0.2.1:443: i/o timeout' >&2
    exit 1
  fi
  echo ok
  exit 0
fi
if [[ "$*" == *"get nodes -o name"* ]]; then
  echo node/lke-test-1
  exit 0
fi
echo "unexpected kubectl args: $*" >&2
exit 2
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	// Process startup can take longer under the coverage/race matrix even though
	// the fake kubectl itself is deterministic.
	t.Setenv("RTK_CLOUD_KUBE_API_READY_TIMEOUT", "5s")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_POLL", "1ms")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", "3")

	if err := waitForKubernetesAPIReady(); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "get --raw=/readyz") != 4 {
		t.Fatalf("expected readyz to be polled until three stable successes, got:\n%s", log)
	}
}

func TestKubernetesKubectlArgsAddsKubeconfigAndRequestTimeout(t *testing.T) {
	t.Setenv("RTK_CLOUD_KUBECONFIG", "/tmp/kubernetes.yaml")
	t.Setenv("RTK_CLOUD_KUBECTL_REQUEST_TIMEOUT", "7s")

	got := strings.Join(lkeKubectlArgs("get", "nodes"), " ")
	want := "--kubeconfig /tmp/kubernetes.yaml --request-timeout=7s get nodes"
	if got != want {
		t.Fatalf("lkeKubectlArgs got %q want %q", got, want)
	}
}

func testKubectlRetriesTransientAPITimeout(t *testing.T, run func() error) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	statePath := filepath.Join(dir, "attempt")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
attempt=0
if [[ -f "` + statePath + `" ]]; then
  attempt="$(cat "` + statePath + `")"
fi
attempt=$((attempt + 1))
printf '%s' "$attempt" > "` + statePath + `"
printf 'attempt=%s args=%s\n' "$attempt" "$*" >> "` + logPath + `"
if [[ "$attempt" -eq 1 ]]; then
  echo 'Unable to connect to the server: dial tcp 192.0.2.1:443: i/o timeout' >&2
  exit 1
fi
echo 'namespace/test configured'
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "2")
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_DELAY", "1ms")

	if err := run(); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "attempt=") != 2 {
		t.Fatalf("expected two kubectl attempts, got:\n%s", log)
	}
}

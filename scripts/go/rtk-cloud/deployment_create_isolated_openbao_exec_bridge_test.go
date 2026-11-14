package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// installIsolatedOpenBaoExecBridge only forwards the selected OpenBao exec to
// the exact labeled test container. Other kubectl operations use the existing
// isolated adapter. It never reports workload readiness or creates receipts.
func installIsolatedOpenBaoExecBridge(t *testing.T, kubeconfigPath, namespace, containerID, owner string) {
	t.Helper()
	fallback := os.Getenv("RTK_CLOUD_KUBECTL")
	if !filepath.IsAbs(fallback) || !filepath.IsAbs(kubeconfigPath) || namespace == "" || owner == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(containerID) {
		t.Fatal("isolated OpenBao bridge requires exact test Kubernetes and container ownership")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("isolated bridge source path unavailable")
	}
	bridge := filepath.Join(filepath.Dir(source), "testdata", "isolated_openbao_exec_bridge.py")
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "kubectl-openbao-bridge")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec \"$RTK_TEST_BRIDGE_PYTHON\" \"$RTK_TEST_BRIDGE_SCRIPT\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_TEST_BRIDGE_PYTHON", python)
	t.Setenv("RTK_TEST_BRIDGE_SCRIPT", bridge)
	t.Setenv("RTK_TEST_BRIDGE_DOCKER", docker)
	t.Setenv("RTK_TEST_BRIDGE_FALLBACK", fallback)
	t.Setenv("RTK_TEST_BRIDGE_KUBECONFIG", kubeconfigPath)
	t.Setenv("RTK_TEST_BRIDGE_CONTAINER", containerID)
	t.Setenv("RTK_TEST_BRIDGE_OWNER", owner)
	t.Setenv("RTK_TEST_BRIDGE_NAMESPACE", namespace)
	t.Setenv("RTK_CLOUD_KUBECTL", launcher)
}

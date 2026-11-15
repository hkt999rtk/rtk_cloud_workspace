package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedMigrationWrapperForwardsOnlyKnownFoundation(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("isolated-selected"), false); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fallback := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\nprintf foundation-forwarded\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", fallback)
	image := "ghcr.io/example/test@sha256:" + strings.Repeat("a", 64)
	selected := map[string]string{"pki-migrate": image, "pki-grants": image, "pki-create-database-logins": image, "account-manager-migrate": image}
	objects, stateRoot := t.TempDir(), t.TempDir()
	if err := os.Chmod(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	installIsolatedMigrationController(t, store, objects, "/usr/bin/true", strings.Repeat("a", 64), "isolated-owner", "video-cloud-dev", selected, stateRoot)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(os.Getenv("RTK_CLOUD_KUBECTL"), append([]string{"--kubeconfig", store.KubeconfigPath(), "--request-timeout=90s"}, args...)...)
		out, err := cmd.Output()
		return string(out), err
	}
	for _, args := range [][]string{
		{"--namespace", "video-cloud-dev-platform", "rollout", "status", "Deployment/redis", "--timeout=37s"},
		{"--namespace", "video-cloud-dev-platform", "rollout", "status", "StatefulSet/postgresql", "--timeout=37s"},
		{"--namespace", "video-cloud-dev-platform", "wait", "--for=jsonpath={.status.phase}=Bound", "PersistentVolumeClaim/redis-data", "--timeout=37s"},
	} {
		if out, err := run(args...); err != nil || out != "foundation-forwarded" {
			t.Fatal("known foundation operation did not reach original controller", err)
		}
	}
	for _, args := range [][]string{
		{"--namespace", "video-cloud-dev-platform", "rollout", "status", "StatefulSet/foreign", "--timeout=37s"},
		{"--namespace", "video-cloud-dev-foreign", "rollout", "status", "Deployment/redis", "--timeout=37s"},
		{"--namespace", "video-cloud-dev-platform", "wait", "--for=condition=complete", "Job/foreign", "--timeout=37s"},
	} {
		if _, err := run(args...); err == nil {
			t.Fatal("unknown workload operation was accepted")
		}
	}
	intent := filepath.Join(stateRoot, "intent-original-uid")
	if err := os.WriteFile(intent, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", fallback)
	restarted, _ := installIsolatedMigrationController(t, store, objects, "/usr/bin/true", strings.Repeat("a", 64), "isolated-owner", "video-cloud-dev", selected, stateRoot)
	if raw, err := os.ReadFile(intent); err != nil || string(raw) != "original" || restarted.root != stateRoot {
		t.Fatal("reconstructed controller lost the original intent", err)
	}
	original := map[string]string{"RTK_FRESH_EXPECTED_KUBECONFIG": store.KubeconfigPath()}
	observed := map[string]string{"RTK_FRESH_EXPECTED_KUBECONFIG": store.KubeconfigPath(), "RTK_CLOUD_KUBECTL": os.Getenv("RTK_CLOUD_KUBECTL")}
	for _, key := range []string{"RTK_TEST_JOB_FALLBACK", "RTK_TEST_JOB_KUBECONFIG", "RTK_TEST_JOB_BRIDGE", "RTK_TEST_JOB_CAPABILITY", "RTK_TEST_JOB_PLATFORM_NAMESPACE"} {
		observed[key] = os.Getenv(key)
	}
	foundation := &isolatedOpenBaoFirstController{cfg: deploymentConfig{Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}, foundationNetwork: strings.Repeat("a", 64), foundationOwner: "isolated-owner"}
	if !isolatedProofBridgeMatches(observed, original, fallback, objects, foundation, restarted) {
		t.Fatal("original bridge identity was rejected")
	}
	for _, key := range []string{"RTK_CLOUD_KUBECTL", "RTK_TEST_JOB_KUBECONFIG", "RTK_TEST_JOB_CAPABILITY", "RTK_TEST_JOB_BRIDGE", "RTK_TEST_JOB_FALLBACK"} {
		old := observed[key]
		observed[key] = "foreign"
		if isolatedProofBridgeMatches(observed, original, fallback, objects, foundation, restarted) {
			t.Fatal("foreign bridge component accepted", key)
		}
		observed[key] = old
	}
	foundation.foundationOwner = "foreign-owner"
	if isolatedProofBridgeMatches(observed, original, fallback, objects, foundation, restarted) {
		t.Fatal("foreign network owner accepted")
	}
}

func TestIsolatedMigrationControllerRootRejectsUnsafeWithoutRemovingIntent(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(root, "intent-original")
	if err := os.WriteFile(intent, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if !isolatedMigrationControllerRootValid(root) {
		t.Fatal("private fixture root rejected")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if isolatedMigrationControllerRootValid(root) {
		t.Fatal("unsafe fixture root accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "foreign-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if isolatedMigrationControllerRootValid(link) {
		t.Fatal("symlink fixture root accepted")
	}
	if raw, err := os.ReadFile(intent); err != nil || string(raw) != "original" {
		t.Fatal("rejected root inspection changed original intent", err)
	}
}

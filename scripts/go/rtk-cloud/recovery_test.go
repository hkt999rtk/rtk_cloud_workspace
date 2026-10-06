package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryCommandRouting(t *testing.T) {
	for _, command := range []string{"backup", "restore"} {
		if _, ok := commands[command]; !ok {
			t.Fatal("command not registered")
		}
		args := []string{command, "status", "--environment", "prod"}
		got, err := normalizeEnvironmentArgs(args)
		if err != nil || len(got) != len(args) || got[2] != "--environment" {
			t.Fatal("environment rewritten")
		}
	}
}

func TestRecoveryLegacyRuntimeCopyIsRetiredWithoutReadingOrWritingState(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(tmp, "source"), filepath.Join(tmp, "target")
	for _, dir := range []string{"env", "state/openbao", "services/video-cloud"} {
		if err = os.MkdirAll(filepath.Join(source, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"env/stack.env": "CLOUD_STACK_NAME=video-cloud-staging\n", "state/provider-preflight.env": "PROVIDER=lke\n", "state/openbao/unseal-key": "fixture-secret", "services/video-cloud/video-cloud.env": "PASSWORD=fixture-secret\n"}
	for path, value := range files {
		if err = os.WriteFile(filepath.Join(source, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := func() ([]byte, error) {
		return exec.Command("bash", filepath.Join(workspace, "scripts/restore-staging-runtime.sh"), "--source-runtime", source, "--target-runtime", target).CombinedOutput()
	}
	if b, err := command(); err == nil || !strings.Contains(string(b), "retired") || strings.Contains(string(b), "fixture-secret") {
		t.Fatal("legacy operational-state copying accepted or exposed source material")
	} else if status, ok := err.(*exec.ExitError); !ok || status.ExitCode() != 2 {
		t.Fatal("retired copier did not return migration/input exit code")
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("retired copier created a target")
	}
	for path, value := range files {
		if got, err := os.ReadFile(filepath.Join(source, path)); err != nil || string(got) != value {
			t.Fatal("retired copier changed its source")
		}
	}
}

func TestRecoveryMutationGuard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	if err := os.Mkdir(filepath.Join(root, "staging"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staging", "recovery-state.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"deployment", "provision", "deploy", "staging-reset-k8s", "test-live"} {
		if recoveryMutationGuard([]string{command}) == nil {
			t.Fatalf("%s not blocked", command)
		}
	}
	for _, command := range []string{"backup", "restore", "docs-check", "contracts-check"} {
		if err := recoveryMutationGuard([]string{command}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"postgres-backup", "plan"}, {"postgres-backup", "status"},
		{"postgres-backup", "prune", "--dry-run"},
		{"postgres-backup", "prune", "--dry-run=false", "--dry-run=true"},
		{"postgres-backup-worker", "run"}, // worker enforces the cluster lock
	} {
		if err := recoveryMutationGuard(args); err != nil {
			t.Fatalf("safe inspection/worker rejected: %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"postgres-backup", "run"}, {"postgres-backup", "configure"},
		{"postgres-backup", "prune"},
		{"postgres-backup", "prune", "--dry-run=true", "--dry-run=false"},
		{"postgres-restore", "drill"},
	} {
		if err := recoveryMutationGuard(args); err == nil {
			t.Fatalf("mutation allowed during maintenance: %v", args)
		}
	}
}

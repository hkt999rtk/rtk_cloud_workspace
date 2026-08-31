package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func TestRecoveryCLIOfflinePlanAndValidation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(root, "secrets"))
	t.Setenv("RTK_BACKUP_ACCESS_KEY_ID", "")
	t.Setenv("RTK_BACKUP_SECRET_ACCESS_KEY", "")
	// Only this fixture can be executed: no real kubectl or operator check.
	kubectl := filepath.Join(root, "kubectl-fixture")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\ncase \"$*\" in\n*' create -f -'*) cat >/dev/null;;\n*' delete configmap '*) exit 0;;\n*) exit 1;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	check := []recovery.Check{{ID: "offline", Argv: []string{filepath.Join(root, "never-execute")}}}
	cfg := recovery.Config{Version: 1, Environment: "staging", Stack: "stack", Directory: filepath.Join(root, "backups"), TimeoutSeconds: 1, MaxArchiveBytes: 1 << 20, Recipients: []string{identity.Recipient().String()}, Namespaces: []string{"platform"}, LockNamespace: "platform", Remote: recovery.Remote{Endpoint: "https://backup.example.invalid", Bucket: "backups", Region: "test", Prefix: "staging/core"}, Workloads: []recovery.Workload{{Namespace: "platform", Kind: "deployment", Name: "api", Role: "application"}}, Components: []recovery.Component{{ID: "globals", Kind: "postgres", Namespace: "platform", Pod: "postgres-0", User: "postgres", Database: "@globals"}, {ID: "db", Kind: "postgres", Namespace: "platform", Pod: "postgres-0", User: "postgres", Database: "accounts"}, {ID: "bao", Kind: "volume", Namespace: "platform", PVC: "bao", Purpose: "openbao-file", Image: "helper@sha256:" + strings.Repeat("a", 64)}, {ID: "runtime", Kind: "secretstore", Paths: []string{"runtime"}}}, PreflightChecks: check, StartupChecks: check, QuiescenceChecks: check, RecoveryChecks: check, HealthChecks: check, ExternalDependencies: []string{"objects and escrow"}}
	config := filepath.Join(root, "config.json")
	if err := recovery.WriteJSON(config, cfg); err != nil {
		t.Fatal(err)
	}
	base := []string{"--environment", "staging", "--config", config}
	for _, command := range []string{"backup", "restore"} {
		if err := runRecovery(command, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := runBackup(append([]string{"plan"}, base...)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"unknown"}, {"plan", "--unknown"}, {"plan", "unexpected"}, {"plan"}, {"plan", "--environment", "staging", "--config", "missing"}, {"plan", "--environment", "prod", "--config", config}, {"create", "--environment", "staging", "--config", config}} {
		if runBackup(args) == nil {
			t.Fatalf("accepted invalid CLI request %v", args)
		}
	}
	for _, tc := range []struct{ command, action string }{{"backup", "preflight"}, {"backup", "status"}, {"backup", "create"}, {"backup", "verify"}, {"backup", "resume"}, {"backup", "abort"}, {"backup", "fetch"}, {"backup", "upload"}, {"restore", "apply"}, {"restore", "retry"}, {"restore", "inspect"}} {
		args := append([]string{tc.action}, base...)
		args = append(args, "--confirm-environment", "staging", "--confirm-stack", "stack")
		if runRecovery(tc.command, args) == nil {
			t.Fatalf("operation %s unexpectedly succeeded without external fixtures", tc.action)
		}
	}
	for _, extra := range [][]string{{"--id", "backup", "--file", "/unselected.age"}, {"--id", "backup"}} {
		args := append([]string{"upload"}, base...)
		args = append(args, "--confirm-environment", "staging", "--confirm-stack", "stack")
		args = append(args, extra...)
		if runBackup(args) == nil {
			t.Fatal("unavailable/foreign upload accepted")
		}
	}
	// A real encrypted archive exercises the read-only inspect path offline.
	capture := filepath.Join(root, "capture")
	if err := os.Mkdir(capture, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "fixture.data"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "identity")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cfg.Directory, "fixture.age")
	m := recovery.Manifest{Version: 1, Scope: "core", ID: "fixture", Environment: cfg.Environment, Stack: cfg.Stack, Components: cfg.Components, ConfigurationSHA256: recovery.InventoryFingerprint(cfg)}
	if err := recovery.Pack(capture, archive, m, cfg.Recipients); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"inspect"}, base...)
	args = append(args, "--file", archive, "--identity", key)
	if err := runRestore(args); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{`{`, `{}`} {
		if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if runBackup(append([]string{"plan"}, base...)) == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if err := recovery.WriteJSON(config, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.Directory, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if runBackup(append([]string{"plan"}, base...)) == nil {
		t.Fatal("backup inside Git accepted")
	}
}

func TestRecoveryMutationGuardReadOnlyAndFilesystemErrors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(root, "missing"))
	if err := recoveryMutationGuard([]string{"deployment"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"secrets", "verify"}, {"secrets", "inventory"}, {"secrets", "plan"}, {"deployment", "--help"}, {"deployment", "-h"}} {
		if err := recoveryMutationGuard(args); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".ignored"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "prod"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := recoveryMutationGuard([]string{"deployment"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prod", "recovery-command.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if recoveryMutationGuard([]string{"deployment"}) == nil {
		t.Fatal("command lock ignored")
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(root, "file"))
	if recoveryMutationGuard([]string{"deployment"}) == nil {
		t.Fatal("invalid SecretStore ignored")
	}
}

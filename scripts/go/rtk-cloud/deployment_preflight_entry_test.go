package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentPreflightEntryRejectsFastBeforePrivateStore(t *testing.T) {
	// A guard read would fail on this file instead of reaching flag rejection.
	configRoot := filepath.Join(t.TempDir(), "invalid-private-root")
	if err := os.WriteFile(configRoot, []byte("isolated guard barrier"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	for _, option := range []string{"--fast", "-fast", "--fast=true", "--fast=false", "-fast=false"} {
		for _, entry := range []struct {
			name string
			args []string
		}{
			{"direct-first", []string{"deployment", "preflight", option, "--environment", "dev"}},
			{"direct-last", []string{"deployment", "preflight", "--environment", "dev", option}},
			{"facade-before-phase", []string{"deployment", "check", option, "--environment", "dev", "--phase", "pre-deploy"}},
			{"facade-after-phase", []string{"deployment", "check", "--environment", "dev", "--phase", "pre-deploy", option}},
		} {
			t.Run(entry.name+"/"+option, func(t *testing.T) {
				_, diagnostics, err := captureOutput(func() error { return run(entry.args) })
				if err != exitCode(2) || !strings.Contains(diagnostics, "flag provided but not defined: -fast") {
					t.Fatalf("removed option reached private state or was accepted: err=%v diagnostics=%s", err, diagnostics)
				}
			})
		}
	}
}

func TestDeploymentPreflightEntryRetainsMaintenanceFence(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	configRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	t.Setenv("PATH", t.TempDir())
	environmentRoot := filepath.Join(configRoot, "dev")
	if err := os.Mkdir(environmentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(environmentRoot, "recovery-command.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err := captureOutput(func() error {
		return run([]string{"deployment", "preflight", "--workspace", workspace, "--environment", "dev"})
	})
	if err != exitCode(1) || !strings.Contains(diagnostics, "environment recovery/maintenance fence blocks this operation") {
		t.Fatalf("valid preflight bypassed the maintenance fence: err=%v diagnostics=%s", err, diagnostics)
	}
	entries, err := os.ReadDir(environmentRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != "recovery-command.lock" {
		t.Fatalf("fenced preflight created private deployment state: entries=%v err=%v", entries, err)
	}
}

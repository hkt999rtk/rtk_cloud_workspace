package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentPreflightRuntimeSnapshotIdentityAndParsing(t *testing.T) {
	for _, tc := range []struct {
		name, contents, want string
	}{
		{"missing stack", "CLOUD_REGION=us-sea\n", "FAIL runtime-identity"},
		{"wrong stack", "CLOUD_STACK_NAME=video-cloud-dev\n", "FAIL runtime-identity"},
		{"malformed", "private-placeholder-without-assignment\n", "FAIL runtime-snapshot"},
		{"duplicate", "private-placeholder=one\nprivate-placeholder=two\n", "FAIL runtime-snapshot"},
		{"valid", "CLOUD_STACK_NAME=video-cloud-staging\n", "PASS runtime-identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			cfg, err := resolveDeploymentConfig(workspace, "staging", "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
			writeTestFile(t, path, tc.contents)
			var out bytes.Buffer
			reporter := &deploymentPreflightReporter{out: &out}
			validateDeploymentRuntimeSnapshot(cfg, "acceptance", reporter)
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "private-placeholder") {
				t.Fatalf("unexpected/redaction-unsafe output: %s", out.String())
			}
			if after := readTestFile(t, path); after != tc.contents {
				t.Fatal("preflight rewrote the runtime snapshot")
			}
		})
	}
}

func TestDeploymentPreflightRuntimeConsoleDriftUsesRecordedValues(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "staging", "environment.env"), "GOOGLE_LOGIN_ENABLED=true\nTEST_LAB_ENABLED=true\n")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	// These process inputs must not conceal missing/old recorded settings.
	t.Setenv("GOOGLE_LOGIN_ENABLED", "true")
	t.Setenv("TEST_LAB_ENABLED", "true")
	path := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	before := "CLOUD_STACK_NAME=video-cloud-staging\nGOOGLE_LOGIN_ENABLED=false\n"
	writeTestFile(t, path, before)
	for _, operation := range []string{"acceptance", "provision"} {
		t.Run(operation, func(t *testing.T) {
			var out bytes.Buffer
			reporter := &deploymentPreflightReporter{out: &out}
			validateDeploymentRuntimeSnapshot(cfg, operation, reporter)
			want := "WARN runtime-config"
			if operation == "acceptance" {
				want = "FAIL runtime-config"
			}
			if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "GOOGLE_LOGIN_ENABLED, TEST_LAB_ENABLED") {
				t.Fatalf("drift not disclosed: %s", out.String())
			}
			if got := len(reporter.failed) > 0; got != (operation == "acceptance") {
				t.Fatalf("wrong operation verdict: %s", out.String())
			}
		})
	}
	if after := readTestFile(t, path); after != before {
		t.Fatal("preflight changed the restored deployment")
	}
}

func TestDeploymentPreflightNewEnvironmentNeedsNoRuntimeSnapshot(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reporter := &deploymentPreflightReporter{out: &out}
	validateDeploymentRuntimeSnapshot(cfg, "provision", reporter)
	if len(reporter.failed) != 0 || !strings.Contains(out.String(), "no restored stack") {
		t.Fatalf("bootstrap was blocked: %s", out.String())
	}
	if _, err := os.Stat(cfg.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatal("read-only preflight materialized a runtime")
	}
}

func TestDeploymentPreflightAggregatesConsoleConflictAndIndependentBlockers(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	makeIsolatedTestSecretStore(t, "staging")
	appendFile(t, filepath.Join(workspace, "cloud_env", "staging", "environment.env"), "GOOGLE_LOGIN_ENABLED=true\n")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_LOGIN_ENABLED", "false")
	checks := defaultDeploymentPreflightChecks()
	checks.lookPath = func(string) (string, error) { return "/fake/tool", nil }
	checks.validateDNS = func(deploymentConfig) error { return errors.New("DNS fixture unavailable") }
	checks.validateLKEState = func(deploymentConfig) error { return errors.New("provider fixture unavailable") }
	var out bytes.Buffer
	if err := runDeploymentPreflightWithChecks(cfg, "provision", checks, &out); err == nil {
		t.Fatal("conflicting release inputs passed")
	}
	for _, want := range []string{"FAIL console-config", "GOOGLE_LOGIN_ENABLED", "FAIL credential:dns", "FAIL environment-safety"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("independent blocker %q not collected: %s", want, out.String())
		}
	}
	if _, err := os.Stat(cfg.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatal("preflight materialized runtime before rejecting the inputs")
	}
}

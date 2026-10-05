package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentCheckPhasesSeparateUpgradePrerequisitesFromLiveHealth(t *testing.T) {
	for _, phase := range []string{deploymentCheckPreDeploy, deploymentCheckPostDeploy} {
		t.Run(phase, func(t *testing.T) {
			deps, _ := facadeFixture(t, true)
			preflightCalled, runtimeCalled := false, false
			deps.preflight = func(_ context.Context, _ deploymentConfig, _ io.Writer) error {
				preflightCalled = true
				// The old TLS-terminating route has a safe deployment-owned
				// migration plan, so it is deployable despite old live health.
				return nil
			}
			deps.runtime = func(secretStore, time.Time) error {
				runtimeCalled = true
				return errors.New("public CertIssuer ingress terminates caller TLS; direct Service client mTLS requires SSL passthrough")
			}
			base := deps.collect
			deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allowWrites bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
				if phase == deploymentCheckPreDeploy && (!o.readOnly || allowWrites) {
					t.Fatal("pre-deploy allowed provider writes")
				}
				return base(ctx, cfg, path, o, allowWrites, emit)
			}
			path := filepath.Join(t.TempDir(), "report.json")
			err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--phase", phase, "--report", path}, io.Discard, io.Discard, deps)
			report := readFacadeReport(t, path)
			if report.Phase != phase {
				t.Fatalf("reported phase=%q", report.Phase)
			}
			if phase == deploymentCheckPreDeploy {
				if err != nil || !preflightCalled || runtimeCalled || report.Overall != "PASS" || !report.ReadOnly {
					t.Fatalf("err=%v preflight=%t runtime=%t report=%+v", err, preflightCalled, runtimeCalled, report)
				}
				for _, check := range report.Checks {
					if check.ID == "pki.runtime" || check.ID == "secrets.bindings" {
						t.Fatalf("pre-deploy ran live health: %+v", check)
					}
				}
				if !strings.Contains(report.Scope, "current runtime health and write permissions unverified") {
					t.Fatalf("ambiguous scope: %s", report.Scope)
				}
			} else if err == nil || preflightCalled || !runtimeCalled || reportCheck(t, report, "pki.runtime").Status != "FAIL" {
				t.Fatalf("err=%v preflight=%t runtime=%t report=%+v", err, preflightCalled, runtimeCalled, report)
			}
		})
	}
}

func TestDeploymentPreDeployPrerequisitesAndUnsafeMigrationBlock(t *testing.T) {
	for _, cause := range []string{"required command is not available", "CertIssuer route is owned by another deployment and cannot be safely migrated"} {
		t.Run(cause, func(t *testing.T) {
			deps, _ := facadeFixture(t, false)
			deps.preflight = func(context.Context, deploymentConfig, io.Writer) error { return errors.New(cause) }
			deps.bindings = func(secretStore) error { t.Fatal("pre-deploy read live bindings"); return nil }
			deps.runtime = func(secretStore, time.Time) error { t.Fatal("pre-deploy read live PKI health"); return nil }
			path := filepath.Join(t.TempDir(), "report.json")
			err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--phase", deploymentCheckPreDeploy, "--report", path}, io.Discard, io.Discard, deps)
			if err == nil {
				t.Fatal("pre-deploy passed failed prerequisite")
			}
			report := readFacadeReport(t, path)
			if check := reportCheck(t, report, "deployment.preflight"); check.Status != "FAIL" || check.Message != cause {
				t.Fatalf("%+v", check)
			}
		})
	}
}

func TestDeploymentCheckPhaseSelectionAndInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--phase", "pre-deploy"},
		{"--phase=pre-deploy", "--phase", "pre-deploy", "--fast"},
	} {
		o, err := parseDeploymentCheckOptions(append([]string{"--environment", "staging"}, args...), io.Discard)
		if err != nil || o.phase != deploymentCheckPreDeploy || !o.qualification.readOnly {
			t.Fatalf("options=%+v err=%v", o, err)
		}
	}
	defaultOptions, err := parseDeploymentCheckOptions([]string{"--environment", "staging"}, io.Discard)
	if err != nil || defaultOptions.phase != deploymentCheckPostDeploy {
		t.Fatalf("default=%+v err=%v", defaultOptions, err)
	}
	for _, args := range [][]string{
		{"--phase", "invalid"},
		{"--phase=pre-deploy", "--phase=post-deploy"},
		{"--phase=pre-deploy", "--read-only=false"},
		{"--phase=pre-deploy", "--create-missing-object-storage-bucket"},
		{"--phase=pre-deploy", "--grant-object-storage-bucket-access"},
		{"--phase=pre-deploy", "--require-pki-migration"},
		{"--phase=pre-deploy", "--require-product-pki"},
	} {
		if _, err := parseDeploymentCheckOptions(append([]string{"--environment", "staging"}, args...), io.Discard); err == nil {
			t.Fatalf("accepted invalid combination: %v", args)
		}
	}
}

func TestDeploymentCheckPurposeAppearsBeforeHelpErrorsOrExternalWork(t *testing.T) {
	t.Setenv("RTK_CLOUD_CHECK_BANNER_PHASE", "")
	for _, args := range [][]string{{"--help"}, {"--phase", "pre-deploy", "--help"}, {"--phase", "pre-deploy"}, {"--phase", "bad"}} {
		var output bytes.Buffer
		_ = runDeploymentCheckWithDependencies(context.Background(), args, &output, &output, deploymentCheckDependencies{})
		if !strings.Contains(output.String(), "check") || !strings.Contains(output.String(), "pre-deploy") && !strings.Contains(output.String(), "post-deploy") {
			t.Fatalf("description missing before help/error: %q", output.String())
		}
	}
	for _, phase := range []string{deploymentCheckPreDeploy, deploymentCheckPostDeploy} {
		var output bytes.Buffer
		deps := deploymentCheckDependencies{guard: func([]string) error {
			if !strings.Contains(output.String(), "("+phase+")") {
				t.Fatal("external work began before phase description")
			}
			return errors.New("stop after observing banner")
		}}
		_ = runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--phase", phase}, &output, &output, deps)
	}
}

func TestDeploymentPhaseScriptsPrintBeforeBuildAndEnforceTheirPurpose(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	goPath := filepath.Join(bin, "go")
	if err := os.WriteFile(goPath, []byte(`#!/bin/sh
printf 'BUILD_STARTED\n'
cat > "$3" <<'CHECKER'
#!/bin/sh
printf 'CHECK_ARGS:%s\n' "$*"
exit "${FAKE_PHASE_EXIT:-0}"
CHECKER
chmod +x "$3"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_CHECK_SCRIPT_PHASE", "")
	t.Setenv("RTK_CLOUD_CHECK_SCRIPT_READ_ONLY", "")
	for _, tc := range []struct {
		script, phase string
		readOnly      bool
	}{
		{"check-deployment-preflight.sh", deploymentCheckPreDeploy, true},
		{"check-deployment-health.sh", deploymentCheckPostDeploy, true},
		{"check-deployment-credentials.sh", deploymentCheckPostDeploy, false},
	} {
		t.Run(tc.script, func(t *testing.T) {
			path := filepath.Join(workspace, "scripts", tc.script)
			for _, status := range []string{"0", "1", "2"} {
				t.Setenv("FAKE_PHASE_EXIT", status)
				output, err := exec.Command("bash", path, "--environment", "staging", "--phase", tc.phase).CombinedOutput()
				if err != nil {
					var failed *exec.ExitError
					if !errors.As(err, &failed) || failed.ExitCode() != int(status[0]-'0') {
						t.Fatalf("status=%s err=%v output=%s", status, err, output)
					}
				} else if status != "0" {
					t.Fatalf("lost exit %s", status)
				}
				text := string(output)
				if !strings.Contains(text, "check") || strings.Index(text, "BUILD_STARTED") < strings.Index(text, "("+tc.phase+")") {
					t.Fatalf("build started before description: %s", text)
				}
				if !strings.Contains(text, "--phase "+tc.phase) || strings.Count(text, "--phase ") != 1 || (tc.readOnly && !strings.Contains(text, "--read-only")) {
					t.Fatalf("wrong purpose forwarded: %s", text)
				}
			}
			other := deploymentCheckPreDeploy
			if tc.phase == other {
				other = deploymentCheckPostDeploy
			}
			invalid := [][]string{{"--phase", other}, {"--phase=" + other}, {"--phase=" + tc.phase, "--phase=" + other}, {"-phase=" + other}}
			if tc.readOnly {
				invalid = append(invalid, []string{"--read-only=false"}, []string{"-read-only=false"}, []string{"--read-only=0"}, []string{"--read-only=FALSE"})
			}
			for _, args := range invalid {
				output, err := exec.Command("bash", append([]string{path}, args...)...).CombinedOutput()
				var failed *exec.ExitError
				if !errors.As(err, &failed) || failed.ExitCode() != 2 || !strings.Contains(string(output), "check") || strings.Contains(string(output), "BUILD_STARTED") {
					t.Fatalf("args=%v err=%v output=%s", args, err, output)
				}
			}
		})
	}
}

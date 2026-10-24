package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func cleanupDiagnosticFixture(t *testing.T, environment string) (map[string]string, deploymentplan.Object, environmentBootstrapCleanupBinding) {
	t.Helper()
	stack := "video-cloud-" + environment
	env := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": stack + "-video-cloud", "name": "pki-controller"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"serviceAccountName": "pki-controller", "automountServiceAccountToken": false, "containers": []any{map[string]any{"name": "pki-controller"}}}}}}
	binding := environmentBootstrapCleanupBinding{Version: 1, Environment: environment, SessionID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", NamespaceUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", SecretUID: "cccccccc-cccc-cccc-cccc-cccccccccccc", PolicySHA256: strings.Repeat("d", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	return env, object, binding
}

func TestCreateNormalTailCleanupDiagnosticsKeepEveryValidationBoundary(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"owner", "uids", "map", "ca", "controller", "identity"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				env, object, binding := cleanupDiagnosticFixture(t, environment)
				want := ""
				switch mode {
				case "owner":
					delete(env, "CLOUD_ENV_NAME") // The actual failed product control-render input.
					want = "exact bootstrap authorization owner required"
				case "uids":
					binding.SecretUID = "never-public-private-owner"
					want = "original canonical session and resource UIDs required"
				case "map":
					binding.ConfigMapName = "never-public-private-map"
					want = "cleanup map must belong to the exact renewed session"
				case "ca":
					binding.KubernetesCA = "never-public-private-certificate"
					want = "verified Kubernetes CA binding required"
				case "controller":
					deploymentplan.Map(object["metadata"])["name"] = "never-public-private-controller"
					want = "cleanup belongs only to the environment controller"
				case "identity":
					durablePod(object)["serviceAccountName"] = "never-public-private-account"
					want = "controller cleanup requires its exact dedicated ServiceAccount and projected token"
				}
				before := deploymentplan.Digest(object)
				_, err := applyEnvironmentBootstrapCleanup(env, object, binding)
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.bindings.bootstrap" || blocker.Cause != want || blocker.Next == "" || strings.Contains(err.Error(), "never-public") || deploymentplan.Digest(object) != before {
					t.Fatal("cleanup validation lost its safe typed diagnosis or changed rejected desired input", err)
				}
			})
		}
	}
}

func TestCreateNormalTailCleanupFailureReachesCanonicalReportAndLog(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"cleanup", "unclassified"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				store, _ := newSecretStore("", environment)
				path := filepath.Join(store.Root, "deployment", "reports", "cleanup-failure.json")
				now := time.Now().UTC()
				admitted, deadline := now.Add(-time.Minute), now.Add(time.Minute)
				calls := 0
				deps.ContinueCreate = func(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) (bool, error) {
					calls++
					env, object, binding := cleanupDiagnosticFixture(t, cfg.Environment)
					delete(env, "CLOUD_ENV_NAME")
					_, failure := applyEnvironmentBootstrapCleanup(env, object, binding)
					if failure == nil {
						t.Fatal("original missing selected environment was accepted")
					}
					if mode == "unclassified" {
						failure = errors.New("never-public-token=synthetic-secret\n-----BEGIN PRIVATE KEY-----")
					}
					failure = fmt.Errorf("never-public subprocess envelope: %w", failure)
					report.Scope, report.Decision, report.ReusedPreflight = "original-create-normal-tail", "NO-GO", true
					report.ExecutionAdmittedAt, report.ExecutionDeadline = &admitted, &deadline
					report.Gates = append([]deploymentplan.GateResult(nil), c.Proof.Qualification.Gates...)
					report.Gates = append(report.Gates, createNormalTailProgramFailure(failure, report, now))
					return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
				}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "create", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--report", path}, &out, &diagnostics, deps)
				if err != exitCode(1) || calls != 1 || *writes != 0 {
					t.Fatal("failed cleanup reached writes, disappeared or bypassed the public create path", err, calls, *writes)
				}
				raw, readErr := os.ReadFile(path)
				var report unifiedDeploymentReport
				if readErr != nil || json.Unmarshal(raw, &report) != nil || report.Decision != "NO-GO" || report.Completed || len(report.Gates) != len(c.Proof.Qualification.Gates)+1 {
					t.Fatal("canonical failure report was lost", readErr)
				}
				row := report.Gates[len(report.Gates)-1]
				if !strings.Contains(row.Next, "same admitted plan") || report.ExecutionDeadline == nil || !report.ExecutionDeadline.Equal(deadline) {
					t.Fatal("diagnostics changed the admitted recovery contract", row)
				}
				logs, globErr := filepath.Glob(filepath.Join(store.Root, "deployment", "logs", "create-*.log"))
				if globErr != nil || len(logs) != 1 {
					t.Fatal("canonical attempt log is missing", globErr)
				}
				logRaw, logErr := os.ReadFile(logs[0])
				for _, output := range []string{path, logs[0]} {
					info, statErr := os.Stat(output)
					if statErr != nil || info.Mode().Perm() != 0600 {
						t.Fatal("diagnostic evidence is not a private canonical file", statErr)
					}
				}
				all := out.String() + diagnostics.String() + string(raw) + string(logRaw)
				if logErr != nil || strings.Contains(all, "never-public") || strings.Contains(all, "PRIVATE KEY") || !strings.Contains(string(logRaw), "report=") {
					t.Fatal("diagnostics exposed raw private errors or lost the saved report", logErr)
				}
				if mode == "cleanup" {
					if row.ID != "deployment.bindings.bootstrap" || row.Cause != "exact bootstrap authorization owner required" || !strings.Contains(out.String(), row.Cause) || !strings.Contains(string(logRaw), row.Cause) {
						t.Fatal("the exact safe cleanup cause did not reach display and canonical evidence", row)
					}
				} else if row.ID != "deployment.execution" || row.Cause != "execution stopped; private execution evidence is retained when created" {
					t.Fatal("ordinary private errors crossed the sanitized boundary", row)
				}
			})
		}
	}
}

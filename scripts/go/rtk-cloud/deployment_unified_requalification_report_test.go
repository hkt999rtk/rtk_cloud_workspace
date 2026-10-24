package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestUnifiedExecutionReportsSavedQualificationAndCurrentFailure(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"safe-blocker", "unclassified"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				checkCalls, accepted := 0, false
				cause, next := "original database schema prerequisite is unavailable", "verify the original database and complete its explicit schema prerequisite before repeating preflight"
				resource := "Deployment/video-cloud-" + environment + "-billing/payment-simulator"
				originalChecks := deps.Checks
				deps.LiveChecks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					checks := originalChecks(c)
					checks["deployment.compatibility"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						checkCalls++
						if mode == "unclassified" {
							return deploymentplan.GateEvidence{}, errors.New("never-public database password")
						}
						return deploymentplan.GateEvidence{}, fmt.Errorf("never-public subprocess payload: %w", &deploymentplan.Blocker{ID: "deployment.compatibility.schema", Resource: resource, Cause: cause, Next: next})
					}
					return checks
				}
				deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					t.Fatal("execution reran full preflight")
					return nil
				}
				deps.Accept = func(context.Context, *compiledEnvironmentDeployment) error {
					accepted = true
					return nil
				}
				store, err := newSecretStore("", environment)
				if err != nil {
					t.Fatal(err)
				}
				reportPath := filepath.Join(store.Root, "deployment", "reports", "requalification.json")
				var out, diagnostics bytes.Buffer
				err = runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--report", reportPath}, &out, &diagnostics, deps)
				if err != exitCode(1) || checkCalls != 1 || *writes != 0 || accepted {
					t.Fatal("failed current-state execution check reached writes or acceptance", err, checkCalls, *writes, accepted)
				}
				raw, err := os.ReadFile(reportPath)
				var report unifiedDeploymentReport
				if err != nil || json.Unmarshal(raw, &report) != nil || report.Decision != "NO-GO" || report.Completed || report.Plan == nil || report.Plan.PlanSHA256 != c.Plan.SHA256() || len(report.Gates) != 10 || !report.ReusedPreflight {
					t.Fatal("saved qualification or current check report was lost", err, string(raw))
				}
				failureID := "deployment.execution.recheck.compatibility"
				failureRows, savedCompatibilityRows := 0, 0
				for _, row := range report.Gates {
					if row.ID == failureID {
						failureRows++
						if row.Status != "FAIL" || row.Next == "" || row.Impact == "" || row.Recheck == "" {
							t.Fatal("latest failed gate lost actionable metadata", row)
						}
						if mode == "safe-blocker" && (row.Cause != cause+" ("+resource+")" || row.Next != next) {
							t.Fatal("sanitized native blocker cause, owner or repair was discarded", row)
						}
						if row.EvidenceSource != "execution" || mode == "safe-blocker" && row.CheckID != "deployment.compatibility.schema" {
							t.Fatal("current failure was not distinguished from saved qualification", row)
						}
						if mode == "unclassified" && row.Cause != "required immediate execution evidence could not be verified" {
							t.Fatal("unclassified private error crossed the report boundary", row)
						}
					} else if row.Status != "PASS" {
						t.Fatal("unaffected gate outcome changed", row)
					} else if row.ID == "deployment.compatibility" {
						savedCompatibilityRows++
						if row.EvidenceSHA256 != deploymentplan.Digest("isolated fixture evidence") || row.EvidenceSource != "preflight" {
							t.Fatal("original qualification evidence changed", row)
						}
					}
				}
				if failureRows != 1 || savedCompatibilityRows != 1 {
					t.Fatal("saved proof or current failure was duplicated or lost", report.Gates)
				}
				finalStart := strings.LastIndex(out.String(), "plan-sha256:")
				if finalStart < 0 || !strings.Contains(out.String()[finalStart:], "[FAIL] "+failureID) || strings.Contains(out.String()[finalStart:], "[OK] deployment.compatibility") {
					t.Fatal("final display lost the actual current failure", out.String())
				}
				logs, err := filepath.Glob(filepath.Join(store.Root, "deployment", "logs", "upgrade-*.log"))
				if err != nil || len(logs) != 1 {
					t.Fatal("attempt log is missing", logs, err)
				}
				logRaw, err := os.ReadFile(logs[0])
				public := out.String() + diagnostics.String() + string(raw) + string(logRaw)
				if err != nil || strings.Contains(public, "never-public") || strings.Contains(public, "private completed-resource and recovery evidence retained") || !strings.Contains(string(logRaw), `"id":"`+failureID+`","status":"FAIL"`) {
					t.Fatal("attempt diagnosis leaked raw errors or fabricated a retained journal", err, public)
				}
			})
		}
	}
}

func TestUnifiedExecutionErrorsRetainSavedProofAndSuccessfulCurrentChecks(t *testing.T) {
	c, deps, _ := unifiedCLIProfile(t, "dev")
	deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
		checks := map[string]deploymentplan.GateCheck{}
		for _, id := range deploymentplan.RequiredGateIDs(false) {
			calls := 0
			checks[id] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
				calls++
				return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest(fmt.Sprintf("current check %d", calls))}, nil
			}
		}
		return checks
	}
	deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, _ string, qualify func(context.Context, *deploymentplan.Plan) error, _ func(context.Context, *compiledEnvironmentDeployment) error) error {
		if err := qualify(ctx, c.Plan); err != nil {
			return err
		}
		return &deploymentplan.Blocker{ID: "deployment.admission", Resource: "Deployment/video-cloud-dev-video-cloud/original-api", Cause: "admission result differs from the approved resource body", Next: "qualify the exact admission mutation before execution"}
	}
	store, _ := newSecretStore("", "dev")
	reportPath := filepath.Join(store.Root, "deployment", "reports", "admission.json")
	var out, diagnostics bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", "dev", "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--report", reportPath}, &out, &diagnostics, deps)
	if err != exitCode(1) {
		t.Fatal("native admission failure disappeared", err)
	}
	raw, _ := os.ReadFile(reportPath)
	var report unifiedDeploymentReport
	if json.Unmarshal(raw, &report) != nil || report.Completed || report.Decision != "NO-GO" || len(report.Gates) != 12 || !report.ReusedPreflight {
		t.Fatal("native execution failure report changed", string(raw))
	}
	for _, row := range report.Gates[:6] {
		if row.Status != "PASS" || row.EvidenceSHA256 != deploymentplan.Digest("isolated fixture evidence") || row.EvidenceSource != "preflight" {
			t.Fatal("report changed saved successful qualification evidence", row)
		}
	}
	for _, row := range report.Gates[6:11] {
		if row.Status != "PASS" || !strings.HasPrefix(row.ID, "deployment.execution.recheck.") || row.EvidenceSHA256 != deploymentplan.Digest("current check 1") || row.EvidenceSource != "execution" {
			t.Fatal("current check evidence was repeated or lost", row)
		}
	}
	if row := report.Gates[11]; row.ID != "deployment.admission" || !strings.Contains(row.Cause, "original-api") || row.Next != "qualify the exact admission mutation before execution" {
		t.Fatal("safe actual execution error metadata was lost", row)
	}
}

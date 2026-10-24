package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func fixtureLiveChecks(calls map[string]int) map[string]deploymentplan.GateCheck {
	checks := map[string]deploymentplan.GateCheck{}
	for _, id := range []string{"deployment.target", "deployment.execution", "deployment.pki", "deployment.compatibility", "deployment.network"} {
		checks[id] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			calls[id]++
			return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("current " + id)}, nil
		}
	}
	return checks
}

func TestDeploymentConsumesQualifiedProofWithoutCompilationOrWholeQualification(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, environment)
			c.Proof.ValidUntil = time.Now().Add(2 * time.Minute)
			originalProof := c.Proof.SHA256()
			deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
				t.Fatal("deployment recompiled the target")
				return nil, nil
			}
			deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				t.Fatal("deployment repeated complete preflight")
				return nil
			}
			loads, accepts := 0, 0
			deps.LoadQualified = func(_ context.Context, cfg deploymentConfig, operation, sha string) (*compiledEnvironmentDeployment, error) {
				loads++
				if cfg.Environment != environment || operation != "upgrade" || sha != c.Plan.SHA256() {
					t.Fatal("qualified evidence selected wrong scope")
				}
				return c, nil
			}
			calls := map[string]int{}
			deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				return fixtureLiveChecks(calls)
			}
			originalExecute := deps.Execute
			deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, recheck func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
				deadline, exists := ctx.Deadline()
				if !exists || !deadline.After(c.Proof.ValidUntil) {
					t.Fatal("execution timeout was shortened to qualification expiry")
				}
				return originalExecute(ctx, c, sha, recheck, accept)
			}
			deps.Accept = func(context.Context, *compiledEnvironmentDeployment) error { accepts++; return nil }
			store, _ := newSecretStore("", environment)
			reportPath := filepath.Join(store.Root, "deployment", "reports", "consumed.json")
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--report", reportPath}, &out, &diagnostics, deps)
			if err != nil || loads != 1 || accepts != 1 || *writes != 1 || len(calls) != 5 {
				t.Fatal("qualified execution omitted current checks or acceptance", err, loads, accepts, *writes, calls)
			}
			for id, count := range calls {
				if count != 1 {
					t.Fatal("current check repeated", id, count)
				}
			}
			raw, _ := os.ReadFile(reportPath)
			var report unifiedDeploymentReport
			if json.Unmarshal(raw, &report) != nil || !report.Completed || !report.ReusedPreflight || report.QualificationSHA256 != originalProof || len(report.Gates) != 11 || report.EvidenceValidUntil == nil || !report.EvidenceValidUntil.Equal(c.Proof.ValidUntil) || report.ExecutionAdmittedAt == nil || report.ExecutionDeadline == nil || !report.ExecutionAdmittedAt.Before(c.Proof.ValidUntil) || !report.ExecutionDeadline.After(c.Proof.ValidUntil) {
				t.Fatal("report lost reused qualification", string(raw))
			}
			for i, row := range report.Gates {
				want := "execution"
				if i < 6 {
					want = "preflight"
				}
				if row.Status != "PASS" || row.EvidenceSource != want {
					t.Fatal("saved results misrepresented as new qualification", row)
				}
			}
			if strings.Contains(out.String(), "deployment.compile") || strings.Contains(out.String(), "deployment.release.resolve") || strings.Contains(out.String(), "deployment.qualification") {
				t.Fatal("execution announced whole preflight", out.String())
			}
		})
	}
}

func TestQualifiedExecutionRejectsMissingExpiredAndInterruptedPrerequisites(t *testing.T) {
	for _, mode := range []string{"missing-proof", "expired", "wrong-plan", "missing-live-check", "live-failed", "proof-mutated", "plan-mutated", "skipped-live", "skipped-acceptance", "acceptance-failed"} {
		t.Run(mode, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, "dev")
			calls := map[string]int{}
			deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				checks := fixtureLiveChecks(calls)
				switch mode {
				case "missing-live-check":
					delete(checks, "deployment.network")
				case "live-failed":
					checks["deployment.execution"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						return deploymentplan.GateEvidence{}, errors.New("never-public provider credential")
					}
				case "proof-mutated":
					checks["deployment.target"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						c.Proof.ValidUntil = c.Proof.ValidUntil.Add(time.Hour)
						return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("changed")}, nil
					}
				case "plan-mutated":
					checks["deployment.target"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						c.Plan.ConfigurationSHA256 = deploymentplan.Digest("changed")
						return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("changed")}, nil
					}
				}
				return checks
			}
			switch mode {
			case "missing-proof":
				c.Proof = nil
			case "expired":
				c.Proof.ValidUntil = time.Now().Add(-time.Second)
			case "wrong-plan":
				c.Proof.Qualification.PlanSHA256 = deploymentplan.Digest("different")
			case "skipped-live":
				deps.Execute = func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error {
					return nil
				}
			case "skipped-acceptance":
				deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, _ string, live func(context.Context, *deploymentplan.Plan) error, _ func(context.Context, *compiledEnvironmentDeployment) error) error {
					return live(ctx, c.Plan)
				}
			case "acceptance-failed":
				deps.Accept = func(context.Context, *compiledEnvironmentDeployment) error {
					return errors.New("never-public PKI diagnostic")
				}
			}
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", "dev", "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
			if err != exitCode(1) || strings.Contains(out.String(), "completed=true") || strings.Contains(out.String()+diagnostics.String(), "never-public") {
				t.Fatal("incomplete or private failure became completion", mode, err, out.String())
			}
			if mode != "acceptance-failed" && *writes != 0 {
				t.Fatal("writes preceded prerequisite checks", mode, *writes)
			}
			if (mode == "missing-proof" || mode == "expired" || mode == "wrong-plan") && len(calls) != 0 {
				t.Fatal("invalid saved evidence reached live checks", calls)
			}
		})
	}
}

func TestQualifiedExecutionCompletesAfterEvidenceExpiryWithinCallerTimeout(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, environment)
			deps.LoadQualified = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
				c.Proof.ValidUntil = time.Now().Add(100 * time.Millisecond)
				return c, nil
			}
			calls := map[string]int{}
			deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				return fixtureLiveChecks(calls)
			}
			originalExecute := deps.Execute
			var executionCtx context.Context
			deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, live func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
				executionCtx = ctx
				deadline, _ := ctx.Deadline()
				if deadline.Before(time.Now().Add(89 * time.Minute)) {
					t.Fatal("90m execution was capped by preflight or PKI duration")
				}
				timer := time.NewTimer(time.Until(c.Proof.ValidUntil) + 10*time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal("admitted execution was cancelled at evidence expiry", ctx.Err())
				}
				return originalExecute(ctx, c, sha, live, accept)
			}
			accepts := 0
			deps.Accept = func(ctx context.Context, c *compiledEnvironmentDeployment) error {
				accepts++
				if time.Now().Before(c.Proof.ValidUntil) || ctx.Err() != nil {
					t.Fatal("test did not complete acceptance after evidence expiry")
				}
				return nil
			}
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--timeout", "90m"}, &out, &diagnostics, deps)
			if err != nil || *writes != 1 || accepts != 1 || len(calls) != 5 || !strings.Contains(out.String(), "completed=true") {
				t.Fatal("freshly admitted execution failed after expiry", err, out.String(), calls)
			}
			if _, err := admittedEnvironmentExecution(executionCtx, c); err == nil {
				t.Fatal("finished attempt left an active admission token")
			}
			if _, _, err := admitQualifiedEnvironmentExecution(executionCtx, c, time.Now().UTC()); err == nil {
				t.Fatal("expired evidence was accepted for another attempt")
			}
		})
	}
}

func TestQualifiedExecutionRechecksFreshnessImmediatelyBeforeAdmission(t *testing.T) {
	c, deps, writes := unifiedCLIProfile(t, "dev")
	deps.LoadQualified = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
		c.Proof.ValidUntil = time.Now().Add(100 * time.Millisecond)
		return c, nil
	}
	checksConstructed := false
	deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
		checksConstructed = true
		time.Sleep(time.Until(c.Proof.ValidUntil) + time.Millisecond)
		return fixtureLiveChecks(map[string]int{})
	}
	deps.Execute = func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error {
		t.Fatal("expiry before admission reached execution")
		return nil
	}
	var out, diagnostics bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", "dev", "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
	if err != exitCode(1) || *writes != 0 || !checksConstructed || !strings.Contains(out.String(), "qualification evidence has expired") || !strings.Contains(out.String(), "valid_until=") || !strings.Contains(out.String(), "checked_at=") {
		t.Fatal("admission used freshness from the earlier load", err, out.String())
	}
}

func TestQualifiedExecutionKeepsCallerTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"timeout", "cancelled", "live-timeout"} {
		t.Run(mode, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, "dev")
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			originalExecute := deps.Execute
			deps.Execute = func(ctx context.Context, _ *compiledEnvironmentDeployment, _ string, _ func(context.Context, *deploymentplan.Plan) error, _ func(context.Context, *compiledEnvironmentDeployment) error) error {
				if mode == "cancelled" {
					cancel()
				}
				<-ctx.Done()
				return nil // A misbehaving adapter cannot claim completion.
			}
			if mode == "live-timeout" {
				deps.Execute = originalExecute
				deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					checks := fixtureLiveChecks(map[string]int{})
					checks["deployment.target"] = func(ctx context.Context, _ *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						<-ctx.Done()
						return deploymentplan.GateEvidence{}, ctx.Err()
					}
					return checks
				}
			}
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(parent, "upgrade", []string{"--environment", "dev", "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256(), "--timeout", "50ms"}, &out, &diagnostics, deps)
			want := "reached its --timeout or earlier caller deadline"
			if mode == "cancelled" {
				want = "deployment execution was cancelled"
			}
			if err != exitCode(1) || *writes != 0 || !strings.Contains(out.String(), want) || strings.Contains(out.String(), "completed=true") {
				t.Fatal("interrupted execution claimed completion", err, out.String())
			}
		})
	}
}

func TestAdmittedProofFencesKeepImmutableIdentityAfterEvidenceExpiry(t *testing.T) {
	for _, mode := range []string{"valid", "proof-mutated", "proof-replaced", "plan-mutated", "plan-replaced", "generation-mutated", "pointer-invalidated", "attempt-ended", "different-deployment"} {
		t.Run(mode, func(t *testing.T) {
			c, qualification, at := completePreflightProofFixture(t, "dev")
			proof, err := saveEnvironmentPreflightProof(c, qualification, at)
			if err != nil {
				t.Fatal(err)
			}
			c.Proof = proof
			parent, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
			defer cancel()
			ctx, admission, err := admitQualifiedEnvironmentExecution(parent, c, at.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			defer admission.active.Store(false)
			afterExpiry := proof.ValidUntil.Add(time.Second)
			if err := validateEnvironmentPreflightProof(*proof, c.Configuration, c.Plan, afterExpiry); err == nil {
				t.Fatal("new attempt accepted expired proof")
			}
			switch mode {
			case "proof-mutated":
				c.Proof.ValidUntil = c.Proof.ValidUntil.Add(time.Hour)
			case "proof-replaced":
				replacement := *c.Proof
				c.Proof = &replacement
			case "plan-mutated":
				c.Plan.ConfigurationSHA256 = deploymentplan.Digest("changed")
			case "plan-replaced":
				replacement := *c.Plan
				c.Plan = &replacement
			case "generation-mutated":
				c.PreflightGeneration = deploymentplan.Digest("changed")
			case "pointer-invalidated":
				if err := invalidateEnvironmentPreflightProof(c.Configuration); err != nil {
					t.Fatal(err)
				}
			case "attempt-ended":
				admission.active.Store(false)
			case "different-deployment":
				replacement := *c
				c = &replacement
			}
			err = validateEnvironmentExecutionProof(ctx, c, c.Configuration, c.Plan, afterExpiry)
			if err == nil {
				err = verifyEnvironmentActivePreflightProof(c)
			}
			if (err == nil) != (mode == "valid") {
				t.Fatal("admitted fence lost immutable/current-pointer protection", mode, err)
			}
		})
	}
}

func TestCompilationRejectsInvalidEvidenceValidityBeforeImageResolution(t *testing.T) {
	for _, ttl := range []string{"", "0", "invalid"} {
		t.Run(ttl, func(t *testing.T) {
			cfg := deploymentConfig{Environment: "dev", Workspace: "/synthetic/missing-workspace", Values: map[string]string{"DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": ttl}}
			_, err := compileEnvironmentDeployment(context.Background(), cfg, "upgrade")
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.preflight.validity" {
				t.Fatal("invalid TTL reached release/configuration resolution", err)
			}
		})
	}
}

func TestPreflightRunsQualificationOnceAndCannotReportUnpublishedEvidenceAsGO(t *testing.T) {
	for _, mode := range []string{"success", "invalidate-failed", "publish-failed", "publish-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			_, deps, writes := unifiedCLIProfile(t, "dev")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := map[string]int{}
			deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				checks := fixtureLiveChecks(calls)
				checks["deployment.release"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					calls["deployment.release"]++
					return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("published image")}, nil
				}
				return checks
			}
			deps.LiveChecks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
				t.Fatal("preflight invoked deployment checks")
				return nil
			}
			deps.LoadQualified = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
				t.Fatal("preflight consumed old result")
				return nil, nil
			}
			if mode == "invalidate-failed" {
				deps.InvalidateProof = func(deploymentConfig) (string, error) { return "", errors.New("never-public store path") }
			}
			publish := deps.PublishProof
			deps.PublishProof = func(ctx context.Context, c *compiledEnvironmentDeployment, q deploymentplan.Qualification) error {
				if mode == "publish-failed" {
					return errors.New("never-public artifact write")
				}
				if mode == "publish-cancelled" {
					cancel()
				}
				return publish(ctx, c, q)
			}
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(ctx, "preflight", []string{"--environment", "dev"}, &out, &diagnostics, deps)
			if (err == nil) != (mode == "success") || *writes != 0 || strings.Contains(out.String()+diagnostics.String(), "never-public") {
				t.Fatal("qualification publication failure lost", mode, err, out.String())
			}
			if mode != "invalidate-failed" {
				for id, count := range calls {
					if count != 1 {
						t.Fatal("qualification repeated", id, count)
					}
				}
				if len(calls) != 6 {
					t.Fatal("incomplete preflight", calls)
				}
			}
		})
	}
}

func TestPreflightGateEvidenceBindsCheckedResponsesWithoutPromotingMissingEvidence(t *testing.T) {
	c, _, _ := unifiedCLIProfile(t, "dev")
	var hashes []string
	for _, reversed := range []bool{false, true} {
		e, err := checkedPreflightGate(context.Background(), "deployment.pki", c.Plan, func(ctx context.Context, _ *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			values := []string{"never-public secret body", "native registry proof"}
			if reversed {
				values[0], values[1] = values[1], values[0]
			}
			for _, v := range values {
				capturePreflightGateResponse(ctx, []byte(v))
			}
			return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("validated")}, nil
		})
		if err != nil || e.SHA256 == deploymentplan.Digest("validated") || len(e.SHA256) != 64 {
			t.Fatal("observed proof unbound", e, err)
		}
		hashes = append(hashes, e.SHA256)
	}
	if hashes[0] != hashes[1] {
		t.Fatal("concurrent response ordering changed proof")
	}
	e, err := checkedPreflightGate(context.Background(), "deployment.pki", c.Plan, func(ctx context.Context, _ *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
		capturePreflightGateResponse(ctx, []byte("never-public"))
		return deploymentplan.GateEvidence{}, nil
	})
	if err != nil || e.SHA256 != "" {
		t.Fatal("response hash fabricated absent verifier evidence", e, err)
	}
}

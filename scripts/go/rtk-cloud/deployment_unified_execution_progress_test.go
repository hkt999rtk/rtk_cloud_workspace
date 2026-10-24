package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestUnifiedExecutionReceivesLiveProgressContextAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, failed := range []bool{false, true} {
			t.Run(environment+map[bool]string{true: "/failure", false: "/success"}[failed], func(t *testing.T) {
				c, deps, _ := unifiedCLIProfile(t, environment)
				deps.Execute = func(ctx context.Context, compiled *compiledEnvironmentDeployment, _ string, recheck func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
					if _, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); !ok {
						t.Fatal("execution lost live heartbeat context")
					}
					if err := recheck(ctx, compiled.Plan); err != nil {
						return err
					}
					backend := environmentKubernetesBackend{}
					backend.ExecutionProgress(ctx, "configuration", "fence")
					backend.ExecutionProgressResult(ctx, "configuration", "fence", true)
					backend.ExecutionProgress(ctx, "configuration", "apply")
					backend.ExecutionProgressResult(ctx, "configuration", "apply", true)
					backend.ExecutionProgress(ctx, "", "verification")
					backend.ExecutionProgressResult(ctx, "", "verification", !failed)
					if failed {
						return errors.New("private-execution-canary")
					}
					return accept(ctx, compiled)
				}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", "video-cloud-" + environment, "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
				if (err != nil) != failed {
					t.Fatal("progress changed execution outcome", err)
				}
				for _, stage := range []struct{ id, task string }{
					{"deployment.execution.configuration.fence", "Recheck every stage dependency"},
					{"deployment.execution.configuration.apply", "Apply resources in dependency order"},
					{"deployment.execution.verification", "Verify updated resources; deployment is not complete yet"},
				} {
					if !strings.Contains(out.String(), "[RUNNING] "+stage.id+"\n  "+stage.task+"\n") {
						t.Fatal("execution did not use the shared two-line presentation", stage.id, out.String())
					}
				}
				if strings.Contains(out.String(), "task=") || strings.ContainsAny(out.String(), "\x1b\r") {
					t.Fatal("plain deployment output retained task prefixes or terminal controls", out.String())
				}
				if strings.Contains(out.String(), "[OK]") || strings.Contains(out.String(), "[FAIL] deployment.execution.verification") != failed {
					t.Fatal("deployment verification exposed a success row or omitted its failure", out.String())
				}
				if strings.Contains(out.String()+diagnostics.String(), "private-execution-canary") {
					t.Fatal("execution progress leaked private errors")
				}
			})
		}
	}
}

func TestExecutionResultUsesTerminalColors(t *testing.T) {
	_, output, ctx := terminalProgressFixture(120, true)
	backend := environmentKubernetesBackend{}
	backend.ExecutionProgress(ctx, "workload", "fence")
	backend.ExecutionProgressResult(ctx, "workload", "fence", true)
	backend.ExecutionProgress(ctx, "workload", "apply")
	backend.ExecutionProgressResult(ctx, "workload", "apply", false)
	for _, block := range []string{
		"[RUNNING] deployment.execution.workload.fence\n  \x1b[36mRecheck every stage dependency\x1b[0m\n",
		"[RUNNING] deployment.execution.workload.apply\n  \x1b[36mApply resources in dependency order\x1b[0m\n",
	} {
		if !strings.Contains(output.String(), block) {
			t.Fatal("deployment tasks lost their indented cyan presentation", output.String())
		}
	}
	if strings.Contains(output.String(), "[OK]") || !strings.Contains(output.String(), "\x1b[31m[FAIL]\x1b[0m deployment.execution.workload.apply") {
		t.Fatal("deployment phase showed success or omitted its red failure", output.String())
	}
}

func TestExecutionRolloutProgressRestoresActualVerificationStage(t *testing.T) {
	var out bytes.Buffer
	err := runEnvironmentProgress(context.Background(), &out, "deployment.execution.verification", func(ctx context.Context) error {
		progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		started := progress.started
		resource := "StatefulSet/video-cloud-dev-platform/openbao"
		environmentDeploymentResourceProgress(ctx, 1, 3, resource, "retain", "read")
		progress.checked, progress.total = 2, 3
		restore := environmentExecutionWaitProgress(ctx, "storage")
		if progress.stage != "deployment.execution.storage.rollout" {
			t.Fatal("wait still reported previous network or verification stage")
		}
		beforeRestore := out.String()
		restore(nil)
		if progress.stage != "deployment.execution.verification" || progress.started != started {
			t.Fatal("wait lost the calling verification stage")
		}
		if out.String() != beforeRestore || progress.resource != resource || progress.action != "retain" || progress.step != "read" || progress.checked != 2 || progress.total != 3 || progress.completed != 1 || progress.resources != 3 {
			t.Fatal("wait pretended to restart verification or lost its resource progress", out.String())
		}
		environmentKubernetesBackend{}.ExecutionProgress(ctx, "private-phase-canary", "private-step-canary")
		return nil
	})
	if err != nil || strings.Contains(out.String(), "private-") || strings.Contains(out.String(), "[PASS]") {
		t.Fatal("progress fabricated success or exposed unmaintained labels")
	}
	environmentExecutionWaitProgress(context.Background(), "storage")(nil)
}

func TestExecutionWaitKeepsFailureVisibleWithoutResumingApply(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			err := runEnvironmentProgress(ctx, &out, "deployment.execution.workload.apply", func(ctx context.Context) error {
				progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
				restore := environmentExecutionWaitProgress(ctx, "workload")
				failure := errors.New("private-wait-output")
				if cancelled {
					cancel()
					failure = context.Canceled
				}
				restore(failure)
				if progress.stage != "deployment.execution.workload.rollout" || progress.status != "FAIL" {
					t.Fatal("failed rollout silently resumed applying resources")
				}
				return failure
			})
			if err == nil || !strings.Contains(out.String(), "[FAIL] deployment.execution.workload.rollout") || strings.Contains(out.String(), "private-wait-output") {
				t.Fatal("failed wait lost its visible failure or exposed the private error", err, out.String())
			}
		})
	}
}

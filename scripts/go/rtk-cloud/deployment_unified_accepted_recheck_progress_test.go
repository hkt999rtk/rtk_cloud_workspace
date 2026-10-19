package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestAcceptedEvidenceFailureReportsRecoveryWithoutFailingAcquiredLease(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, deps, _ := unifiedCLIProfile(t, environment)
			deps.Execute = func(ctx context.Context, _ *compiledEnvironmentDeployment, _ string, _ func(context.Context, *deploymentplan.Plan) error, _ func(context.Context, *compiledEnvironmentDeployment) error) error {
				backend := environmentKubernetesBackend{}
				backend.ExecutionProgress(ctx, "", "lease")
				backend.ExecutionProgressResult(ctx, "", "lease", true)
				backend.ExecutionProgress(ctx, "", "recovery")
				backend.ExecutionProgressResult(ctx, "", "recovery", false)
				backend.ExecutionProgress(ctx, "", "cleanup")
				backend.ExecutionProgressResult(ctx, "", "cleanup", true)
				return &deploymentplan.Blocker{ID: "deployment.recovery", Cause: "preserved execution evidence cannot be revalidated", Next: "inspect the private journal and qualify actual state"}
			}
			var out, diagnostics bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", "video-cloud-" + environment, "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
			if err == nil || !strings.Contains(out.String(), "[FAIL] deployment.execution.recovery") || !strings.Contains(out.String(), "[FAIL] deployment.recovery") || !strings.Contains(out.String(), "Inspect preserved execution evidence") {
				t.Fatal("recovery failure lacks its actual stage and safe diagnosis", out.String(), diagnostics.String())
			}
			for _, block := range []string{
				"[RUNNING] deployment.execution.lease\n  Check or acquire exclusive deployment ownership\n",
				"[RUNNING] deployment.execution.recovery\n  Inspect preserved execution evidence and verify an accepted retained target\n",
				"[RUNNING] deployment.execution.cleanup\n  Release execution ownership and archive the local lock\n",
			} {
				if !strings.Contains(out.String(), block) {
					t.Fatal("deployment recovery or cleanup lost the shared presentation", out.String())
				}
			}
			if strings.Contains(out.String(), "[FAIL] deployment.execution.lease") || strings.Contains(out.String(), "[FAIL] deployment.execution.cleanup") || strings.Contains(out.String(), "completed=true") {
				t.Fatal("evidence failure misreported ownership or completion", out.String())
			}
		})
	}
}

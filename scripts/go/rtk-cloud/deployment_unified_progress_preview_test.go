package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Opt-in local presentation preview: it calls only the UI helpers, no compiler,
// backend, environment store, provider, preflight or deployment operation.
func TestEnvironmentProgressTerminalPreview(t *testing.T) {
	if os.Getenv("RTK_PROGRESS_PREVIEW") != "1" {
		t.Skip("local terminal preview only")
	}
	fmt.Fprintln(os.Stdout, "LOCAL UI PREVIEW — simulated work; no environment operations")
	err := runEnvironmentProgress(context.Background(), os.Stdout, "deployment.compile", func(ctx context.Context) error {
		end := environmentDeploymentOperation(ctx, "inventory", "Namespace//video-cloud-example-application")
		time.Sleep(250 * time.Millisecond)
		end()
		environmentDeploymentProgress(ctx, "deployment.execution.admission")
		environmentDeploymentAdmissionProgress(ctx, 0, 12)
		for checked := 1; checked <= 12; checked++ {
			time.Sleep(80 * time.Millisecond)
			environmentDeploymentAdmissionProgress(ctx, checked, 12)
		}
		environmentDeploymentProgress(ctx, "deployment.execution.workload.apply")
		for index := 0; index < 3; index++ {
			id := fmt.Sprintf("Deployment/video-cloud-example-application/service-%d", index)
			environmentDeploymentResourceProgress(ctx, index, 3, id, "update", "read")
			time.Sleep(120 * time.Millisecond)
			environmentDeploymentResourceProgress(ctx, index, 3, id, "update", "complete")
		}
		environmentDeploymentProgress(ctx, "deployment.execution.workload.rollout")
		environmentDeploymentRolloutProgress(ctx, "Deployment/video-cloud-example-application/service-2", 0, 1, 1, 300)
		time.Sleep(250 * time.Millisecond)
		environmentDeploymentProgress(ctx, "deployment.execution.acceptance")
		output := environmentProgressResultOutput(ctx, os.Stdout)
		fmt.Fprintln(output, "[LOCAL SAMPLE] Checking public endpoint readiness")
		time.Sleep(250 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"BLOCKED", "FAIL", "ERROR"} {
		fmt.Fprintf(os.Stdout, "%s LOCAL UI status sample\n", deploymentStatusLabel(os.Stdout, status))
	}
	fmt.Fprintln(os.Stdout, "LOCAL UI PREVIEW FINISHED — no deployment was executed")
}

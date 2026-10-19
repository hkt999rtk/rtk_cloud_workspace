package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Phase and step labels are maintained IDs, never resource payloads or errors.
func environmentExecutionStage(phase, step string) string {
	stage := "deployment.execution"
	switch phase {
	case "namespace", "storage", "bootstrap", "configuration", "migration", "workload", "routing", "retirement":
		stage += "." + phase
	}
	switch step {
	case "qualification", "admission", "lease", "recovery", "fence", "apply", "rollout", "verification", "acceptance", "ownership", "journal", "cleanup":
		return stage + "." + step
	default:
		return ""
	}
}

func (b environmentKubernetesBackend) ExecutionProgress(ctx context.Context, phase, step string) {
	if stage := environmentExecutionStage(phase, step); stage != "" {
		environmentDeploymentProgress(ctx, stage)
	}
}

func (b environmentKubernetesBackend) ExecutionProgressResult(ctx context.Context, phase, step string, passed bool) {
	if stage := environmentExecutionStage(phase, step); stage != "" {
		status := "FAIL"
		if passed && ctx.Err() == nil {
			status = "PASS"
		}
		environmentDeploymentProgressResult(ctx, stage, status)
	}
}

func (b environmentKubernetesBackend) ExecutionResourceProgress(ctx context.Context, index, total int, resource deploymentplan.Resource, step string) {
	if step == "wait" && resource.Action != "retire" {
		switch resource.Desired["kind"] {
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "PersistentVolumeClaim":
		default:
			return
		}
	}
	environmentDeploymentResourceProgress(ctx, index, total, resource.ID, resource.Action, step)
}

func environmentExecutionWaitProgress(ctx context.Context, phase string) func(error) {
	progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress)
	if !ok {
		return func(error) {}
	}
	progress.mu.Lock()
	previous := progress.stage
	started := progress.started
	resource, action, step := progress.resource, progress.action, progress.step
	checked, total := progress.checked, progress.total
	progress.mu.Unlock()
	stage := environmentExecutionStage(phase, "rollout")
	environmentDeploymentProgress(ctx, stage)
	return func(err error) {
		environmentDeploymentProgressDone(ctx, stage, err)
		if err == nil && ctx.Err() == nil {
			// Rollout temporarily changes the display, while the resource's
			// original apply or verification stage continues. Restore its
			// details without emitting another start or resetting its clock.
			progress.mu.Lock()
			progress.resource, progress.action, progress.step = resource, action, step
			progress.checked, progress.total = checked, total
			progress.mu.Unlock()
			environmentDeploymentProgressResume(ctx, previous, started)
		}
	}
}

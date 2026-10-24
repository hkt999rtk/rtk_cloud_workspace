package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Full workload updates use the same original-share recovery as preparation
// stages. The maintained target and native OnDelete restart checks stay intact.
func createNormalTailWorkloadBackend(cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, c *compiledEnvironmentDeployment) environmentCreateKubernetesBackend {
	native := c.Backend
	native.OpenBaoReplacementReady = func(ctx context.Context, controller, pod deploymentplan.Object) error {
		if err := h.Execution.Check(ctx); err != nil {
			return err
		}
		if err := validateOpenBaoPodOwner(controller, pod); err != nil {
			return err
		}
		runtime := environmentCreateBootstrapRuntime{Execution: h.Execution, ExecutionSHA256: h.Execution.Journal.PlanSHA256, Backend: native}
		if err := unsealEnvironmentCreateOpenBaoRestart(ctx, cfg, store, runtime, pod); err != nil {
			return err
		}
		return h.Execution.Check(ctx)
	}
	return environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: h.Execution, cfg: cfg, store: store, stagePlanSHA256: c.Plan.SHA256()}
}

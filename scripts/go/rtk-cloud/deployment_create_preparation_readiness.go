package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Readiness of an original retained enrollment owner is completion of its
// native producer, not a second enrollment after its permanent consumer starts.
// This is scoped to the authenticated original fresh-product program and its
// actual durable outputs. Ordinary services retain native rollout acceptance.
func completedFreshPreparationOwners(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, live []deploymentplan.Object) (map[string]string, error) {
	e := runtime.Execution
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil, nil
	}
	program, err := readFreshProductProgram(e.Store, e)
	if err != nil {
		return nil, err
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(e.Store, l)
	if err != nil {
		return nil, err
	}
	if _, _, err := acceptedCreateStage(e.Store, e, "kubernetes/fresh-product-enrollment-closed"); err != nil {
		return nil, err
	}
	if err := verifyFreshProductOriginalOutputs(ctx, cfg, runtime, l, installed); err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, object := range live {
		owners[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	return authenticatedFreshPreparationOwners(ctx, cfg, runtime, owners, live)
}

// This in-memory view selects runtime health only. Never persist it or replace
// the frozen plan/journal: the ordinary executor still verifies every original
// resource body and UID, including the retained preparation owners.
func freshPreparationRuntimePlan(plan *deploymentplan.Plan, owners map[string]string) (*deploymentplan.Plan, error) {
	if len(owners) == 0 {
		return plan, nil
	}
	target := *plan
	target.Resources = nil
	for _, resource := range plan.Resources {
		if uid := owners[resource.ID]; uid != "" {
			if resource.Action != "retain" || resource.Desired["kind"] != "Deployment" || !environmentBootstrapOwnerUUID.MatchString(uid) || resource.UID != uid {
				return nil, environmentCreateBootstrapBlocker("completed preparation is not its original retained workload owner: " + resource.ID)
			}
			continue
		}
		target.Resources = append(target.Resources, resource)
	}
	return &target, nil
}

// Both preflight dependency admission and deployment use this exact adapter.
func newEnvironmentCreateStageReadinessBackend(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, stage string, plan *deploymentplan.Plan, native environmentKubernetesBackend, live []deploymentplan.Object) (environmentCreateKubernetesBackend, error) {
	b := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store, stagePlanSHA256: plan.SHA256(), templateOnly: stage == createRuntimeHostStage || stage == custodyTokenAccessStage}
	if stage == "workload" || stage == createWorkloadImageForwardStage {
		runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
		if ok {
			if runtime.Execution != e || e == nil || e.Store != store {
				return b, environmentCreateBootstrapBlocker("preparation readiness lost its original create runtime")
			}
			var err error
			b.completedPreparations, err = completedFreshPreparationOwners(ctx, cfg, runtime, live)
			if err != nil {
				return b, err
			}
			if _, err = freshPreparationRuntimePlan(plan, b.completedPreparations); err != nil {
				return b, err
			}
		} else if e != nil && createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") != nil {
			return b, environmentCreateBootstrapBlocker("fresh preparation readiness requires its original runtime")
		}
	}
	return b, nil
}

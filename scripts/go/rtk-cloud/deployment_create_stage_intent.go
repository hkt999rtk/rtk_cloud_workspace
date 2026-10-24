package main

import (
	"encoding/json"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The parent journal binds the exact child plan through its digest, without
// duplicating Secret bodies or private native export responses. The ordinary
// planner retains its original bounded 0600 resource plan for guarded recovery.
func environmentCreateKubernetesStageIntent(plan *deploymentplan.Plan) (json.RawMessage, error) {
	if plan == nil || !environmentPreflightDigest.MatchString(plan.SHA256()) {
		return nil, environmentCreateBootstrapBlocker("exact child resource plan is required for original parent intent")
	}
	resources := make([]deploymentplan.Object, 0, len(plan.Resources))
	for _, resource := range plan.Resources {
		resources = append(resources, deploymentplan.Object{
			"resource_id": resource.ID, "phase": resource.Phase, "action": resource.Action, "uid": resource.UID,
			"desired_sha256": deploymentplan.Digest(resource.Desired), "baseline_sha256": deploymentplan.Digest(resource.Baseline),
		})
	}
	return json.Marshal(deploymentplan.Object{"plan_sha256": plan.SHA256(), "inputs": plan.Inputs, "resources": resources})
}

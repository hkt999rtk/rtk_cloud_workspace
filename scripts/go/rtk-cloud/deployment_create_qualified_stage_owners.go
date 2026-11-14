package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateQualifiedStageKey struct{}
type environmentCreateQualifiedStage struct {
	Execution *environmentCreateExecution
	Stage     string
	Plan      *deploymentplan.Plan
}

// The already-fenced full target carries explicit original ownership, including
// controller-created claims without stack labels. Scope that authority to this
// exact execution, stage and input before maintained custody projection.
func environmentCreateQualifiedStageOwners(ctx context.Context, e *environmentCreateExecution, stage string, definitions []deploymentplan.Definition) (map[string]string, error) {
	qualified, ok := ctx.Value(environmentCreateQualifiedStageKey{}).(environmentCreateQualifiedStage)
	if !ok {
		return nil, nil
	}
	if e == nil || qualified.Execution != e || qualified.Stage != stage || qualified.Plan == nil || environmentCreateStageBindingsSHA(stage, environmentCreateDefinitions(qualified.Plan)) != environmentCreateStageBindingsSHA(stage, definitions) {
		return nil, environmentCreateBlocker("deployment.create.ownership", "child stage differs from its qualified original target ownership")
	}
	owners := map[string]string{}
	for _, resource := range qualified.Plan.Resources {
		if resource.UID != "" {
			owners[resource.ID] = resource.UID
		}
	}
	return owners, nil
}

package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// verifyFailedForwardDefinitions reproduces the immutable candidate using its
// captured baselines rather than current live state, whose already-created
// Jobs would otherwise change a retry's create actions into retain actions.
func verifyFailedForwardDefinitions(original *deploymentplan.Plan, definitions []deploymentplan.Definition) error {
	bad := func() error {
		return createProviderRecoveryBlock("repair renderer no longer reproduces its exact qualified stage")
	}
	if original == nil || len(original.Resources) == 0 || len(definitions) == 0 {
		return bad()
	}
	var captured []deploymentplan.Object
	for _, resource := range original.Resources {
		if resource.Baseline == nil {
			if resource.Action != "create" {
				return bad()
			}
			continue
		}
		if resource.UID == "" {
			return bad()
		}
		body := deploymentplan.Clone(resource.Baseline)
		meta := deploymentplan.Map(body["metadata"])
		if meta == nil {
			return bad()
		}
		meta["uid"] = resource.UID
		captured = append(captured, body)
	}
	rendered, err := deploymentplan.Compile(original.Inputs, definitions, captured)
	if err != nil || rendered.SHA256() != original.SHA256() {
		return bad()
	}
	return nil
}

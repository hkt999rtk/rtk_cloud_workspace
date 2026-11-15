package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// Verify the current renderer against the immutable original stage and the
// admitted mount successor without changing either plan or definitions.
func verifyInitializerMountForwardDefinitions(original *deploymentplan.Plan, defs []deploymentplan.Definition, update *deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("application renderer differs from the qualified private state successor")
	}
	if original == nil || update == nil || len(update.Resources) != 1 || update.Resources[0].Action != "update" || update.Resources[0].Baseline == nil || update.Resources[0].UID == "" {
		return bad()
	}
	target := update.Resources[0]
	var source deploymentplan.Object
	for _, r := range original.Resources {
		if r.ID == target.ID {
			if source != nil {
				return bad()
			}
			source = r.Desired
		}
	}
	if source == nil {
		return bad()
	}
	copied := make([]deploymentplan.Definition, len(defs))
	found := false
	for i, d := range defs {
		copied[i] = d
		copied[i].Object = deploymentplan.Clone(d.Object)
		if deploymentplan.ID(d.Object) != target.ID {
			continue
		}
		if found {
			return bad()
		}
		found = true
		candidate := copied[i]
		candidate.Object = deploymentplan.Clone(candidate.Object)
		candidate.Dependencies = nil
		deploymentplan.Map(candidate.Object["metadata"])["uid"] = target.UID
		baseline := deploymentplan.Clone(target.Baseline)
		deploymentplan.Map(baseline["metadata"])["uid"] = target.UID
		rebuilt, err := deploymentplan.Compile(update.Inputs, []deploymentplan.Definition{candidate}, []deploymentplan.Object{baseline})
		if err != nil || rebuilt.SHA256() != update.SHA256() {
			return bad()
		}
		copied[i].Object = deploymentplan.Clone(source)
	}
	if !found {
		return bad()
	}
	if verifyFailedForwardDefinitions(original, copied) != nil {
		return bad()
	}
	return nil
}

package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// verifyInitializerForwardDefinitions proves that the current renderer still
// reproduces the frozen stage except for the independently admitted command.
// Neither the frozen plan nor caller-owned definitions are modified.
func verifyInitializerForwardDefinitions(original *deploymentplan.Plan, definitions []deploymentplan.Definition, update *deploymentplan.Plan, initializer string) error {
	bad := func() error {
		return createProviderRecoveryBlock("application renderer differs from the qualified initializer successor")
	}
	if original == nil || update == nil || len(update.Resources) != 1 || update.Resources[0].Action != "update" || initializer == "" {
		return bad()
	}
	target := update.Resources[0]
	var old deploymentplan.Object
	for _, resource := range original.Resources {
		if resource.ID == target.ID {
			if old != nil || resource.Desired["kind"] != "Deployment" {
				return bad()
			}
			old = resource.Desired
		}
	}
	if old == nil || target.Desired["kind"] != "Deployment" {
		return bad()
	}
	oldPod := environmentPKIPod(old)
	if oldPod == nil {
		return bad()
	}
	oldInit := managedNamedField(oldPod["initContainers"], initializer)
	if oldInit == nil || len(certIssuerObjectList(oldInit["command"])) == 0 {
		return bad()
	}
	cloned := make([]deploymentplan.Definition, len(definitions))
	found := false
	for i, def := range definitions {
		cloned[i] = def
		cloned[i].Object = deploymentplan.Clone(def.Object)
		if deploymentplan.ID(def.Object) != target.ID {
			continue
		}
		if found || target.UID == "" || target.Baseline == nil {
			return bad()
		}
		found = true
		// Recompile the isolated target with the captured baseline so native
		// ownership labels are checked exactly as in the accepted update.
		candidate := cloned[i]
		candidate.Object = deploymentplan.Clone(cloned[i].Object)
		candidate.Dependencies = nil
		deploymentplan.Map(candidate.Object["metadata"])["uid"] = target.UID
		baseline := deploymentplan.Clone(target.Baseline)
		deploymentplan.Map(baseline["metadata"])["uid"] = target.UID
		rebuilt, err := deploymentplan.Compile(update.Inputs, []deploymentplan.Definition{candidate}, []deploymentplan.Object{baseline})
		if err != nil || rebuilt.SHA256() != update.SHA256() {
			return bad()
		}
		pod := environmentPKIPod(cloned[i].Object)
		if pod == nil {
			return bad()
		}
		init := managedNamedField(pod["initContainers"], initializer)
		if init == nil || deploymentplan.Digest(init["command"]) == deploymentplan.Digest(oldInit["command"]) {
			return bad()
		}
		init["command"] = oldInit["command"]
	}
	if !found {
		return bad()
	}
	if err := verifyFailedForwardDefinitions(original, cloned); err != nil {
		return bad()
	}
	return nil
}

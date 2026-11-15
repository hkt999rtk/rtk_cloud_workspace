package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
)

// compileFailedPrivateStateForward only compiles an exact same-owner native
// successor. The caller authenticates all source journals, fences the parent,
// verifies the exact admitted q1, and re-observes live state before execution.
// A nil admitted object requests the update-only first pass; the complete
// retain plan must be built using the verified server-side dry-run result.
func compileFailedPrivateStateForward(original *deploymentplan.Plan, source deploymentplan.Journal, oldRetain *deploymentplan.Plan, oldJournal deploymentplan.Journal, previous *deploymentplan.Plan, previousJournal deploymentplan.Journal, corrected deploymentplan.Definition, live []deploymentplan.Object, admitted deploymentplan.Object, updateStage, retainStage string) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		return nil, nil, createProviderRecoveryBlock("private state successor requires the exact recorded owner, failed initializer and native renderer")
	}
	if original == nil || oldRetain == nil || previous == nil || original.Operation != "scoped" || oldRetain.Operation != "scoped" || previous.Operation != "scoped" || len(original.Resources) == 0 || len(original.Resources) != len(oldRetain.Resources) || len(previous.Resources) != 1 || source.PlanSHA256 != original.SHA256() || oldJournal.PlanSHA256 != oldRetain.SHA256() || previousJournal.PlanSHA256 != previous.SHA256() || source.Failed == "" || oldJournal.Failed == "" || source.InFlight != "" || oldJournal.InFlight != "" || previousJournal.InFlight != "" || source.Accepted || oldJournal.Accepted || previousJournal.Accepted || source.AcceptanceStarted || oldJournal.AcceptanceStarted || previousJournal.AcceptanceStarted || len(source.Completed) != len(original.Resources) || len(source.Owners) != len(original.Resources) || len(oldJournal.Completed) != len(oldRetain.Resources) || len(oldJournal.Owners) != len(oldRetain.Resources) || len(previousJournal.Owners) != 1 || len(previousJournal.Completed) > 1 || !secretEnvironmentPattern.MatchString(updateStage) || !secretEnvironmentPattern.MatchString(retainStage) || updateStage == retainStage {
		return bad()
	}
	if original.Environment != oldRetain.Environment || original.Stack != oldRetain.Stack || original.ConfigurationSHA256 != oldRetain.ConfigurationSHA256 || original.ReleaseSHA256 != oldRetain.ReleaseSHA256 || original.Environment != previous.Environment || original.Stack != previous.Stack || original.ConfigurationSHA256 != previous.ConfigurationSHA256 || original.ReleaseSHA256 != previous.ReleaseSHA256 {
		return bad()
	}
	prior := previous.Resources[0]
	targetID := oldJournal.Failed
	if prior.ID != targetID || prior.Action != "update" || prior.Desired["kind"] != "Deployment" || prior.UID == "" || previousJournal.Owners[targetID] != prior.UID || source.Owners[targetID] != prior.UID || deploymentplan.ID(corrected.Object) != targetID || corrected.Object["kind"] != "Deployment" || corrected.Retain || corrected.Retire {
		return bad()
	}
	if len(previousJournal.Completed) == 0 {
		if previousJournal.Failed != targetID {
			return bad()
		}
	} else if previousJournal.Completed[0] != targetID || (previousJournal.Failed != "" && previousJournal.Failed != targetID) {
		return bad()
	}
	inventory := make(map[string]deploymentplan.Object, len(live))
	for _, o := range live {
		id := deploymentplan.ID(o)
		if inventory[id] != nil {
			return bad()
		}
		inventory[id] = o
	}
	var target deploymentplan.Resource
	found, originalFailed := false, false
	for i, old := range original.Resources {
		if old.ID == source.Failed {
			originalFailed = true
		}
		retained := oldRetain.Resources[i]
		uid := source.Owners[old.ID]
		current := inventory[old.ID]
		if source.Completed[i] != old.ID || oldJournal.Completed[i] != old.ID || retained.ID != old.ID || uid == "" || oldJournal.Owners[old.ID] != uid || retained.Action != "retain" || retained.UID != uid || old.Action == "retire" || retained.Phase != old.Phase || !slices.Equal(retained.Dependencies, old.Dependencies) || deploymentplan.Digest(retained.Desired) != deploymentplan.Digest(old.Desired) || current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != uid {
			return bad()
		}
		expected := old.Desired
		if old.ID == targetID {
			if found || old.Desired["kind"] != "Deployment" || corrected.Phase != old.Phase {
				return bad()
			}
			target = old
			found = true
			expected = prior.Desired
		}
		if !deploymentplan.Matches(expected, current) {
			return bad()
		}
	}
	if !found || !originalFailed || !deploymentplan.Matches(target.Desired, prior.Baseline) {
		return bad()
	}
	priorExpected := deploymentplan.Clone(target.Desired)
	if !projectDeviceMemoryTrustInitializer(priorExpected) || deploymentplan.Digest(priorExpected) != deploymentplan.Digest(prior.Desired) {
		return bad()
	}
	expected := deploymentplan.Clone(prior.Desired)
	changed, err := projectDeviceMemoryTrustPrivateSubPath(expected)
	if err != nil || !changed {
		return bad()
	}
	def := corrected
	def.Object = deploymentplan.Clone(corrected.Object)
	deploymentplan.Map(def.Object["metadata"])["uid"] = prior.UID
	def.Dependencies = nil
	in := original.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(updateStage, []deploymentplan.Definition{def})
	update, err := deploymentplan.Compile(in, []deploymentplan.Definition{def}, live)
	if err != nil {
		return nil, nil, err
	}
	if len(update.Resources) != 1 || update.Resources[0].Action != "update" || update.Resources[0].UID != prior.UID || deploymentplan.Digest(update.Resources[0].Desired) != deploymentplan.Digest(expected) {
		return bad()
	}
	if admitted == nil {
		return update, nil, nil
	}
	if deploymentplan.ID(admitted) != targetID || deploymentplan.String(deploymentplan.Map(admitted["metadata"])["uid"]) != prior.UID || !deploymentplan.Matches(expected, admitted) {
		return bad()
	}
	future := make([]deploymentplan.Object, 0, len(live))
	for _, o := range live {
		c := deploymentplan.Clone(o)
		if deploymentplan.ID(c) == targetID {
			c = deploymentplan.Clone(admitted)
		}
		future = append(future, c)
	}
	defs := make([]deploymentplan.Definition, 0, len(original.Resources))
	for _, old := range original.Resources {
		d := deploymentplan.Clone(old.Desired)
		if old.ID == targetID {
			d = deploymentplan.Clone(expected)
		}
		deploymentplan.Map(d["metadata"])["uid"] = source.Owners[old.ID]
		defs = append(defs, deploymentplan.Definition{Object: d, Phase: old.Phase, Retain: true, Dependencies: slices.Clone(old.Dependencies)})
	}
	retainInputs := original.Inputs
	retainInputs.BindingsSHA256 = environmentCreateStageBindingsSHA(retainStage, defs)
	next, err := deploymentplan.Compile(retainInputs, defs, future)
	if err != nil {
		return nil, nil, err
	}
	if len(next.Resources) != len(original.Resources) {
		return bad()
	}
	for i, r := range next.Resources {
		if r.ID != original.Resources[i].ID || r.Action != "retain" || r.UID != source.Owners[r.ID] {
			return bad()
		}
	}
	return update, next, nil
}

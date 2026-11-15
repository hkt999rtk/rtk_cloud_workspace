package main

import (
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// compileFailedInitializerForward builds only a native update and a proposed
// retained successor. The caller must authenticate both source journals, keep
// the parent execution fence, dry-run the update, and re-observe its actual
// accepted UID and body before admitting or executing the retained successor.
// The synthesized future inventory is a planning baseline, never live evidence.
func compileFailedInitializerForward(original *deploymentplan.Plan, originalJournal deploymentplan.Journal, retained *deploymentplan.Plan, retainedJournal deploymentplan.Journal, corrected deploymentplan.Definition, live []deploymentplan.Object, admitted deploymentplan.Object, initializer, updateStage, retainStage string) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		return nil, nil, createProviderRecoveryBlock("initializer repair requires the exact failed owners and a command-only renderer change")
	}
	if original == nil || retained == nil || original.Operation != "scoped" || retained.Operation != "scoped" || len(original.Resources) == 0 || len(original.Resources) != len(retained.Resources) || originalJournal.PlanSHA256 != original.SHA256() || retainedJournal.PlanSHA256 != retained.SHA256() || originalJournal.Failed == "" || retainedJournal.Failed == "" || originalJournal.InFlight != "" || retainedJournal.InFlight != "" || originalJournal.Accepted || retainedJournal.Accepted || originalJournal.AcceptanceStarted || retainedJournal.AcceptanceStarted || len(originalJournal.Completed) != len(original.Resources) || len(retainedJournal.Completed) != len(retained.Resources) || len(originalJournal.Owners) != len(original.Resources) || len(retainedJournal.Owners) != len(retained.Resources) || !secretEnvironmentPattern.MatchString(initializer) || !secretEnvironmentPattern.MatchString(updateStage) || !secretEnvironmentPattern.MatchString(retainStage) || updateStage == retainStage || retained.Environment != original.Environment || retained.Stack != original.Stack || retained.ConfigurationSHA256 != original.ConfigurationSHA256 || retained.ReleaseSHA256 != original.ReleaseSHA256 {
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
	targetID := retainedJournal.Failed
	if deploymentplan.ID(corrected.Object) != targetID || corrected.Object["kind"] != "Deployment" || corrected.Retain || corrected.Retire {
		return bad()
	}
	var target deploymentplan.Resource
	found, originalFailed := false, false
	for i, old := range original.Resources {
		if old.ID == originalJournal.Failed {
			originalFailed = true
		}
		forward := retained.Resources[i]
		uid := originalJournal.Owners[old.ID]
		current := inventory[old.ID]
		if originalJournal.Completed[i] != old.ID || retainedJournal.Completed[i] != old.ID || forward.ID != old.ID || uid == "" || retainedJournal.Owners[old.ID] != uid || forward.Action != "retain" || forward.UID != uid || old.Action == "retire" || forward.Phase != old.Phase || !slices.Equal(forward.Dependencies, old.Dependencies) || deploymentplan.Digest(forward.Desired) != deploymentplan.Digest(old.Desired) || current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != uid || !deploymentplan.Matches(old.Desired, current) {
			return bad()
		}
		if old.ID == targetID {
			if found || old.Desired["kind"] != "Deployment" || corrected.Phase != old.Phase {
				return bad()
			}
			target = old
			found = true
		}
	}
	if !found || !originalFailed {
		return bad()
	}
	def := corrected
	def.Object = deploymentplan.Clone(corrected.Object)
	deploymentplan.Map(def.Object["metadata"])["uid"] = originalJournal.Owners[targetID]
	// This single-resource native child authenticates the omitted original
	// dependencies through the source journals and parent fence.
	def.Dependencies = nil
	updateInputs := original.Inputs
	updateInputs.BindingsSHA256 = environmentCreateStageBindingsSHA(updateStage, []deploymentplan.Definition{def})
	update, err := deploymentplan.Compile(updateInputs, []deploymentplan.Definition{def}, live)
	if err != nil {
		return nil, nil, err
	}
	if len(update.Resources) != 1 || update.Resources[0].Action != "update" || update.Resources[0].UID != originalJournal.Owners[targetID] {
		return bad()
	}
	oldBody := deploymentplan.Clone(target.Desired)
	newBody := update.Resources[0].Desired
	oldPod, newPod := environmentPKIPod(oldBody), environmentPKIPod(newBody)
	if oldPod == nil || newPod == nil {
		return bad()
	}
	oldInits, newInits := certIssuerObjectList(oldPod["initContainers"]), certIssuerObjectList(newPod["initContainers"])
	var oldInit, newInit deploymentplan.Object
	for _, raw := range oldInits {
		c := deploymentplan.Map(raw)
		if c["name"] == initializer {
			if oldInit != nil {
				return bad()
			}
			oldInit = c
		}
	}
	for _, raw := range newInits {
		c := deploymentplan.Map(raw)
		if c["name"] == initializer {
			if newInit != nil {
				return bad()
			}
			newInit = c
		}
	}
	if oldInit == nil || newInit == nil || len(certIssuerObjectList(newInit["command"])) == 0 || deploymentplan.Digest(oldInit["command"]) == deploymentplan.Digest(newInit["command"]) {
		return bad()
	}
	oldInit["command"] = newInit["command"]
	if deploymentplan.Digest(oldBody) != deploymentplan.Digest(newBody) {
		return bad()
	}
	// A first pass renders only the update. A complete successor plan must be
	// bound to the exact server-side admitted object, including API defaults.
	if admitted == nil {
		return update, nil, nil
	}
	if deploymentplan.ID(admitted) != targetID || deploymentplan.String(deploymentplan.Map(admitted["metadata"])["uid"]) != originalJournal.Owners[targetID] || !deploymentplan.Matches(newBody, admitted) {
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
		desired := deploymentplan.Clone(old.Desired)
		if old.ID == targetID {
			desired = deploymentplan.Clone(newBody)
		}
		deploymentplan.Map(desired["metadata"])["uid"] = originalJournal.Owners[old.ID]
		defs = append(defs, deploymentplan.Definition{Object: desired, Phase: old.Phase, Retain: true, Dependencies: slices.Clone(old.Dependencies)})
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
		if r.ID != original.Resources[i].ID || r.Action != "retain" || r.UID != originalJournal.Owners[r.ID] {
			return bad()
		}
	}
	return update, next, nil
}

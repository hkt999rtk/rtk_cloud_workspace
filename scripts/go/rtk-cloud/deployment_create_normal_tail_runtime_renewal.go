package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
)

// The completed typed renewal supplies only accepted temporary control owners.
// Permanent outputs and the original enrollment placement remain unchanged.
func runtimeRenewalCorrections(source createNormalTailRuntimeSource, previous *deploymentplan.Plan, changes []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	r := source.SessionRenewal
	if r == nil {
		if len(source.SessionRenewalResources) != 0 {
			return nil, productSessionRenewalBlock("orphan runtime dependencies")
		}
		return changes, nil
	}
	if r.Source.OriginalForwardPlanSHA256 != previous.SHA256() || r.Source.OriginalForwardJournalSHA256 != source.PreviousJournalSHA256 || deploymentplan.Digest(r.Original.Outputs) != deploymentplan.Digest(r.Installed.Outputs) || r.Original.Ceremony.BootstrapSessionID == r.Installed.Ceremony.BootstrapSessionID || len(r.Original.Outputs) != 5 || len(source.SessionRenewalResources) == 0 {
		return nil, productSessionRenewalBlock("runtime linked source")
	}
	outputs := map[string]environmentCreateProductIdentity{}
	for _, o := range r.Original.Outputs {
		outputs[o.ResourceID] = environmentCreateProductIdentity{ID: o.ResourceID, Subject: o.Subject}
	}
	consumers := map[string]int{}
	byID := map[string]deploymentplan.Definition{}
	for _, old := range previous.Resources {
		object := deploymentplan.Clone(old.Desired)
		if err := productSessionForwardACKInPlace(object, outputs, consumers, r.Original.Ceremony, r.Installed.Ceremony); err != nil {
			return nil, err
		}
		byID[old.ID] = deploymentplan.Definition{Object: object, Phase: old.Phase, Dependencies: old.Dependencies}
	}
	for id := range outputs {
		if consumers[id] != 1 {
			return nil, productSessionRenewalBlock("runtime unique product ACK consumer")
		}
	}
	for _, d := range changes {
		byID[deploymentplan.ID(d.Object)] = d
	}
	seen := map[string]bool{}
	for _, dep := range source.SessionRenewalResources {
		if seen[dep.ID] || !environmentBootstrapOwnerUUID.MatchString(dep.UID) || dep.Action != "retain" || dep.Baseline == nil || deploymentplan.Map(dep.Baseline["metadata"])["uid"] != dep.UID || !deploymentplan.Matches(dep.Desired, dep.Baseline) {
			return nil, productSessionRenewalBlock("runtime accepted dependency")
		}
		seen[dep.ID] = true
		d := deploymentplan.Definition{Object: deploymentplan.Clone(dep.Desired), Phase: dep.Phase}
		if old, ok := byID[dep.ID]; ok {
			d.Phase, d.Dependencies = old.Phase, old.Dependencies
			if dep.ID == "Deployment/"+previous.Stack+"-video-cloud/pki-controller" {
				d.Object = deploymentplan.Clone(old.Object)
				if _, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(source.Configuration), d.Object, r.Installed.Ceremony.CleanupBinding); err != nil {
					return nil, err
				}
			}
		}
		byID[dep.ID] = d
	}
	if !seen["Deployment/"+previous.Stack+"-video-cloud/pki-controller"] || !seen["ConfigMap/"+previous.Stack+"-video-cloud/"+r.Installed.Ceremony.CleanupBinding.ConfigMapName] {
		return nil, productSessionRenewalBlock("runtime cleanup dependencies")
	}
	var result []deploymentplan.Definition
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}
func runtimeRenewalDependency(source createNormalTailRuntimeSource, id string) *deploymentplan.Resource {
	for i := range source.SessionRenewalResources {
		if source.SessionRenewalResources[i].ID == id {
			return &source.SessionRenewalResources[i]
		}
	}
	return nil
}
func runtimeRenewalAdditional(source createNormalTailRuntimeSource, previous *deploymentplan.Plan) []deploymentplan.Resource {
	known := map[string]bool{}
	for _, r := range previous.Resources {
		known[r.ID] = true
	}
	var additional []deploymentplan.Resource
	for _, r := range source.SessionRenewalResources {
		if !known[r.ID] {
			additional = append(additional, r)
		}
	}
	sort.Slice(additional, func(i, j int) bool { return additional[i].ID < additional[j].ID })
	return additional
}
func validateRuntimeRenewalRetained(dep deploymentplan.Resource, desired deploymentplan.Object, got deploymentplan.Resource) error {
	action := "retain"
	if !deploymentplan.Matches(desired, dep.Baseline) {
		action = "update"
	}
	if got.Action != action || got.UID != dep.UID || got.Baseline == nil || deploymentplan.Map(got.Baseline["metadata"])["uid"] != dep.UID || deploymentplan.Map(got.Baseline["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(dep.Desired, got.Baseline) || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(deploymentplan.Projection(got.Baseline)) {
		return productSessionRenewalBlock("runtime accepted dependency baseline")
	}
	return nil
}

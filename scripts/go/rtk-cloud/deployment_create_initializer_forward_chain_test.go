package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestInitializerSuccessorPreservesAllOriginalOwnersAndBodies(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectEnvironmentCustodyResources(cfg.Values, "dev", binding, strings.Repeat("b", 64), defs)
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentplan.Clone(createRecipeObject(t, projected, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki"))
	init := managedNamedField(environmentPKIPod(target)["initContainers"], "initialize-trust-state")
	cmd := certIssuerObjectList(init["command"])
	paths := deviceTrustPathInRecipe.FindAllString(deploymentplan.String(cmd[2]), -1)
	init["command"] = []any{"/bin/sh", "-ec", deviceNativeTrustPreparation(paths[0])}
	other := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "another", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"v": "original"}}
	id, otherID := deploymentplan.ID(target), deploymentplan.ID(other)
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: p.Stack, Operation: "scoped"}
	original := &deploymentplan.Plan{Inputs: inputs, Resources: []deploymentplan.Resource{{ID: id, Desired: target, Action: "create"}, {ID: otherID, Desired: other, Action: "create"}}}
	origJ := deploymentplan.Journal{PlanSHA256: original.SHA256(), Failed: id, Completed: []string{id, otherID}, Owners: map[string]string{id: "target-uid", otherID: "other-uid"}}
	updated := deploymentplan.Clone(target)
	if !projectDeviceMemoryTrustInitializer(updated) {
		t.Fatal("failed to build exact successor")
	}
	update := &deploymentplan.Plan{Inputs: inputs, Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: "target-uid", Baseline: deploymentplan.Clone(target), Desired: updated}}}
	uJ := deploymentplan.Journal{PlanSHA256: update.SHA256(), Accepted: true, Owners: map[string]string{id: "target-uid"}}
	retain := &deploymentplan.Plan{Inputs: inputs, Resources: []deploymentplan.Resource{{ID: id, Action: "retain", UID: "target-uid", Desired: deploymentplan.Clone(updated)}, {ID: otherID, Action: "retain", UID: "other-uid", Desired: deploymentplan.Clone(other)}}}
	rJ := deploymentplan.Journal{PlanSHA256: retain.SHA256(), Accepted: true, Owners: map[string]string{id: "target-uid", otherID: "other-uid"}}
	if err := verifyInitializerSuccessorPlans(original, origJ, update, uJ, retain, rJ); err != nil {
		t.Fatal("valid chain rejected", err)
	}
	// The API may add Kubernetes defaults to the observed original baseline;
	// desired successor content remains exactly the qualified one-command change.
	defaulted := *update
	defaulted.Resources = append([]deploymentplan.Resource(nil), update.Resources...)
	defaulted.Resources[0].Baseline = deploymentplan.Clone(target)
	deploymentplan.Map(defaulted.Resources[0].Baseline["spec"])["revisionHistoryLimit"] = float64(10)
	defaultedJournal := uJ
	defaultedJournal.PlanSHA256 = defaulted.SHA256()
	if err := verifyInitializerSuccessorPlans(original, origJ, &defaulted, defaultedJournal, retain, rJ); err != nil {
		t.Fatal("Kubernetes defaulted original baseline rejected", err)
	}
	// The target is identified by the original failed journal and exact
	// memory-only transformation, not by one hard-coded workload name.
	renamedOriginal := *original
	renamedOriginal.Resources = append([]deploymentplan.Resource(nil), original.Resources...)
	renamedTarget := deploymentplan.Clone(target)
	deploymentplan.Map(renamedTarget["metadata"])["name"] = "another-device-api"
	renamedID := deploymentplan.ID(renamedTarget)
	renamedOriginal.Resources[0].ID, renamedOriginal.Resources[0].Desired = renamedID, renamedTarget
	renamedJournal := origJ
	renamedJournal.PlanSHA256, renamedJournal.Failed = renamedOriginal.SHA256(), renamedID
	renamedJournal.Completed = []string{renamedID, otherID}
	renamedJournal.Owners = map[string]string{renamedID: "target-uid", otherID: "other-uid"}
	renamedUpdate := *update
	renamedUpdate.Resources = append([]deploymentplan.Resource(nil), update.Resources...)
	renamedUpdate.Resources[0].ID = renamedID
	renamedUpdate.Resources[0].Baseline = deploymentplan.Clone(renamedTarget)
	renamedUpdate.Resources[0].Desired = deploymentplan.Clone(renamedTarget)
	if !projectDeviceMemoryTrustInitializer(renamedUpdate.Resources[0].Desired) {
		t.Fatal("renamed successor not transformed")
	}
	renamedUpdateJournal := uJ
	renamedUpdateJournal.PlanSHA256, renamedUpdateJournal.Owners = renamedUpdate.SHA256(), map[string]string{renamedID: "target-uid"}
	renamedRetain := *retain
	renamedRetain.Resources = append([]deploymentplan.Resource(nil), retain.Resources...)
	renamedRetain.Resources[0].ID, renamedRetain.Resources[0].Desired = renamedID, deploymentplan.Clone(renamedUpdate.Resources[0].Desired)
	renamedRetainJournal := rJ
	renamedRetainJournal.PlanSHA256, renamedRetainJournal.Owners = renamedRetain.SHA256(), map[string]string{renamedID: "target-uid", otherID: "other-uid"}
	if err := verifyInitializerSuccessorPlans(&renamedOriginal, renamedJournal, &renamedUpdate, renamedUpdateJournal, &renamedRetain, renamedRetainJournal); err != nil {
		t.Fatal("non-default owner rejected", err)
	}
	bad := *retain
	bad.Resources = append([]deploymentplan.Resource(nil), retain.Resources...)
	bad.Resources[1].Desired = deploymentplan.Clone(other)
	deploymentplan.Map(bad.Resources[1].Desired["data"])["v"] = "changed"
	badJ := rJ
	badJ.PlanSHA256 = bad.SHA256()
	if verifyInitializerSuccessorPlans(original, origJ, update, uJ, &bad, badJ) == nil {
		t.Fatal("unrelated resource change accepted")
	}
	bad = *retain
	bad.Resources = append([]deploymentplan.Resource(nil), retain.Resources...)
	bad.Resources[0].UID = "other-owner"
	badJ = rJ
	badJ.PlanSHA256 = bad.SHA256()
	if verifyInitializerSuccessorPlans(original, origJ, update, uJ, &bad, badJ) == nil {
		t.Fatal("replacement UID accepted")
	}
	wrong := *update
	wrong.Resources = append([]deploymentplan.Resource(nil), update.Resources...)
	wrong.Resources[0].Desired = deploymentplan.Clone(updated)
	deploymentplan.Map(wrong.Resources[0].Desired["metadata"])["labels"] = deploymentplan.Object{"unrelated": "changed"}
	wrongJ := uJ
	wrongJ.PlanSHA256 = wrong.SHA256()
	if verifyInitializerSuccessorPlans(original, origJ, &wrong, wrongJ, retain, rJ) == nil {
		t.Fatal("unrelated update accepted")
	}
}

package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSessionApplicationSuccessorsContinueOnlySameRecordedOwners(t *testing.T) {
	object := func(name, sid string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": "n"}, "spec": deploymentplan.Object{"session": sid}}
	}
	old, next, latest, core := object("app", "old"), object("app", "next"), object("app", "latest"), object("core", "next")
	id, coreID := deploymentplan.ID(old), deploymentplan.ID(core)
	p0 := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, Action: "create", Desired: old}}}
	original := []serviceSessionRecordedPlan{{Plan: p0, Journal: deploymentplan.Journal{PlanSHA256: p0.SHA256(), Completed: []string{id}, Owners: map[string]string{id: "uid"}}, SelectedIDs: map[string]bool{id: true}}}
	step := func(before, after deploymentplan.Object) serviceSessionRecordedPlan {
		p := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, UID: "uid", Action: "update", Baseline: before, Desired: after}, {ID: coreID, UID: "core-uid", Action: "update", Baseline: core, Desired: core}}}
		return serviceSessionRecordedPlan{Plan: p, Journal: deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id, coreID}, Owners: map[string]string{id: "uid", coreID: "core-uid"}, Accepted: true}}
	}
	sources := []serviceSessionRecordedPlan{step(old, next), step(next, latest)}
	got, err := appendSessionApplicationSuccessors(original, sources, nil)
	if err != nil || len(got) != 3 || got[0].SelectedIDs[id] || got[1].SelectedIDs[id] || !got[2].SelectedIDs[id] || got[2].SelectedIDs[coreID] || !original[0].SelectedIDs[id] {
		t.Fatal("latest application owner was not selected", err)
	}
	partial := step(old, next)
	partial.Journal.Accepted = false
	partial.Journal.Completed = nil
	partial.Journal.Failed = id
	partial.Journal.Owners = map[string]string{id: "uid"}
	got, err = appendSessionApplicationSuccessors(original, []serviceSessionRecordedPlan{partial}, nil)
	if err != nil || !got[1].SelectedIDs[id] {
		t.Fatal("recorded failed owner not followed", err)
	}
	partial.Journal.Owners = map[string]string{}
	got, err = appendSessionApplicationSuccessors(original, []serviceSessionRecordedPlan{partial}, nil)
	if err != nil || !got[0].SelectedIDs[id] || got[1].SelectedIDs[id] {
		t.Fatal("unwritten owner replaced predecessor", err)
	}
	retained := step(old, old)
	retained.Plan.Resources[0].Action = "retain"
	retained.Journal.PlanSHA256 = retained.Plan.SHA256()
	got, err = appendSessionApplicationSuccessors(original, []serviceSessionRecordedPlan{retained}, nil)
	if err != nil || !got[0].SelectedIDs[id] || got[1].SelectedIDs[id] {
		t.Fatal("no-write retain changed application authority", err)
	}
	retained.Plan.Resources[0].Desired = next
	retained.Journal.PlanSHA256 = retained.Plan.SHA256()
	if _, err := appendSessionApplicationSuccessors(original, []serviceSessionRecordedPlan{retained}, nil); err == nil {
		t.Fatal("retained mutation accepted")
	}
	for _, mode := range []string{"in-flight", "wrong-uid", "wrong-baseline", "wrong-action"} {
		t.Run(mode, func(t *testing.T) {
			bad := step(old, next)
			switch mode {
			case "in-flight":
				bad.Journal.Accepted = false
				bad.Journal.InFlight = id
			case "wrong-uid":
				bad.Journal.Owners[id] = "other"
			case "wrong-baseline":
				bad.Plan.Resources[0].Baseline = latest
				bad.Journal.PlanSHA256 = bad.Plan.SHA256()
			case "wrong-action":
				bad.Plan.Resources[0].Action = "retire"
				bad.Journal.PlanSHA256 = bad.Plan.SHA256()
			}
			if _, err := appendSessionApplicationSuccessors(original, []serviceSessionRecordedPlan{bad}, nil); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

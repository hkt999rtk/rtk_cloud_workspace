package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceSessionCorePendingOwnerRequiresRecordedUIDAndBaseline(t *testing.T) {
	id, uid := "Deployment/test/core", "11111111-1111-4111-8111-111111111111"
	before := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "core", "namespace": "test"}, "spec": deploymentplan.Object{"value": "old"}}
	after := deploymentplan.Clone(before)
	deploymentplan.Map(after["spec"])["value"] = "new"
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: uid, Baseline: before, Desired: after}}}
	pending := serviceSessionRecordedPlan{Stage: "pending", Plan: plan, Journal: deploymentplan.Journal{Failed: id, Owners: map[string]string{id: uid}}}
	base := serviceSessionRecordedPlan{Stage: "base"}
	latest, ok := followServiceSessionCoreOwner(base, id, uid, before, after, []serviceSessionRecordedPlan{pending}, map[string]int{"base": 1, "pending": 2}, 1)
	if !ok || latest.Stage != "pending" || !latest.SelectedIDs[id] {
		t.Fatal("recorded pending owner was not selected")
	}
	pending.Journal.Owners[id] = "22222222-2222-4222-8222-222222222222"
	if _, ok := followServiceSessionCoreOwner(base, id, uid, before, after, []serviceSessionRecordedPlan{pending}, map[string]int{"base": 1, "pending": 2}, 1); ok {
		t.Fatal("accepted replacement UID")
	}
	pending.Journal.Owners[id] = uid
	plan.Resources[0].Baseline = deploymentplan.Clone(after)
	if _, ok := followServiceSessionCoreOwner(base, id, uid, before, after, []serviceSessionRecordedPlan{pending}, map[string]int{"base": 1, "pending": 2}, 1); ok {
		t.Fatal("accepted unrelated baseline")
	}
}

func TestServiceSessionCoreFollowsAddedConfigMapKeyWhenOldBodyStillMatches(t *testing.T) {
	id, uid := "ConfigMap/test/trust", "11111111-1111-4111-8111-111111111111"
	before := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "trust", "namespace": "test"}, "data": deploymentplan.Object{"root.pem": "root"}}
	after := deploymentplan.Clone(before)
	deploymentplan.Map(after["data"])["bootstrap-session"] = "new"
	if !deploymentplan.Matches(before, after) {
		t.Fatal("fixture must exercise subset matching")
	}
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: uid, Baseline: before, Desired: after}}}
	pending := serviceSessionRecordedPlan{Stage: "pending", Plan: plan, Journal: deploymentplan.Journal{Failed: id, Owners: map[string]string{id: uid}}}
	latest, ok := followServiceSessionCoreOwner(serviceSessionRecordedPlan{Stage: "base"}, id, uid, before, after, []serviceSessionRecordedPlan{pending}, map[string]int{"base": 1, "pending": 2}, 1)
	if !ok || latest.Stage != "pending" {
		t.Fatal("added key caused old owner selection")
	}
}

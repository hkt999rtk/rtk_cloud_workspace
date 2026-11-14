package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestServiceSessionOwnerSelectionUsesLatestRecordedSameUID(t *testing.T) {
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	obj := func(name, value string) deploymentplan.Object {
		env := []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": "11111111-1111-4111-8111-111111111111"}, deploymentplan.Object{"name": "RECIPE", "value": value}}
		pod := deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "env": env}}}
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": pod}}}
	}

	a0, b0 := obj("a", "old"), obj("b", "keep")
	idA, idB := deploymentplan.ID(a0), deploymentplan.ID(b0)
	p0 := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: in, Resources: []deploymentplan.Resource{{ID: idA, Action: "create", Phase: "workload", Desired: a0}, {ID: idB, Action: "create", Phase: "workload", Desired: b0}}}
	j0 := deploymentplan.Journal{PlanSHA256: p0.SHA256(), Completed: []string{idA, idB}, Owners: map[string]string{idA: "uid-a", idB: "uid-b"}}
	a1 := obj("a", "one")
	p1 := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: in, Resources: []deploymentplan.Resource{{ID: idA, Action: "update", UID: "uid-a", Phase: "workload", Baseline: a0, Desired: a1}}}
	j1 := deploymentplan.Journal{PlanSHA256: p1.SHA256(), Failed: idA, Owners: map[string]string{idA: "uid-a"}}
	a2 := obj("a", "two")
	p2 := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: in, Resources: []deploymentplan.Resource{{ID: idA, Action: "update", UID: "uid-a", Phase: "workload", Baseline: a1, Desired: a2}}}
	j2 := deploymentplan.Journal{PlanSHA256: p2.SHA256(), Completed: []string{idA}, Owners: map[string]string{idA: "uid-a"}}
	sources := []serviceSessionRecordedPlan{{Plan: p0, Journal: j0}, {Plan: p1, Journal: j1}, {Plan: p2, Journal: j2}}
	got, err := selectLatestServiceSessionOwners(sources)
	if err != nil || len(got) != 3 || !got[0].SelectedIDs[idB] || got[0].SelectedIDs[idA] || len(got[1].SelectedIDs) != 0 || !got[2].SelectedIDs[idA] {
		t.Fatal("wrong latest selection", err)
	}
	j2.Owners = map[string]string{idA: "replacement"}
	if _, err = selectLatestServiceSessionOwners([]serviceSessionRecordedPlan{{Plan: p0, Journal: j0}, {Plan: p1, Journal: j1}, {Plan: p2, Journal: j2}}); err == nil {
		t.Fatal("replacement accepted")
	}
	j2.Owners = map[string]string{idA: "uid-a"}
	bad := *p2
	bad.Resources = append([]deploymentplan.Resource(nil), p2.Resources...)
	bad.Resources[0].Baseline = b0
	j2.PlanSHA256 = bad.SHA256()
	if _, err = selectLatestServiceSessionOwners([]serviceSessionRecordedPlan{{Plan: p0, Journal: j0}, {Plan: p1, Journal: j1}, {Plan: &bad, Journal: j2}}); err == nil {
		t.Fatal("discontinuous source accepted")
	}
}

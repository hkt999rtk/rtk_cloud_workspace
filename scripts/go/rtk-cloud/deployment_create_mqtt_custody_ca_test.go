package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestIssuerCredentialOwnersRequireAcceptedCoreBaseline(t *testing.T) {
	const uid = "346e6133-4e39-4036-9230-ac1a2ccb473a"
	old := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": "video-cloud-dev-account-manager"}, "data": deploymentplan.Object{"bundle": "service-trust"}}
	newBody := deploymentplan.Clone(old)
	deploymentplan.Map(newBody["data"])["bundle"] = "service-trust-with-crl"
	id := deploymentplan.ID(old)
	oldPlan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: id, Action: "retain", UID: uid, Desired: old}}}
	newPlan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: uid, Baseline: old, Desired: newBody}}}
	journal := func(p *deploymentplan.Plan) deploymentplan.Journal {
		return deploymentplan.Journal{PlanSHA256: p.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{id}, Owners: map[string]string{id: uid}}
	}
	live := deploymentplan.Clone(newBody)
	deploymentplan.Map(live["metadata"])["uid"] = uid
	read := func(_ context.Context, id string) (deploymentplan.Object, error) { return live, nil }
	verify := func() error {
		return verifyIssuerCredentialOwners(t.Context(), read, oldPlan, journal(oldPlan), newPlan, journal(newPlan))
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(live["data"])["bundle"] = "unaccepted-change"
	if err := verify(); err == nil {
		t.Fatal("accepted live drift from the latest accepted credential owner")
	}
	deploymentplan.Map(live["data"])["bundle"] = "service-trust-with-crl"
	newPlan.Resources[0].Baseline = newBody
	if err := verify(); err == nil {
		t.Fatal("accepted an update with no chain to the original credential owner")
	}
}

func TestIssuerCredentialOwnersAcceptOnlyRecordedTransportSuccessor(t *testing.T) {
	const uid = "346e6133-4e39-4036-9230-ac1a2ccb473a"
	old := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "trust", "namespace": "ns"}, "data": deploymentplan.Object{"ca": "old"}}
	next := deploymentplan.Clone(old)
	deploymentplan.Map(next["data"])["ca"] = "new"
	id := deploymentplan.ID(old)
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: id, Action: "retain", UID: uid, Desired: old}}}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{id}, Owners: map[string]string{id: uid}}
	core := &deploymentplan.Plan{}
	coreJournal := deploymentplan.Journal{PlanSHA256: core.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	live := deploymentplan.Clone(next)
	deploymentplan.Map(live["metadata"])["uid"] = uid
	read := func(context.Context, string) (deploymentplan.Object, error) { return live, nil }
	successor := []activeTransportRenewalResource{{ID: id, UID: uid, Before: old, Desired: next}}
	if err := verifyIssuerCredentialOwners(t.Context(), read, plan, j, core, coreJournal, successor); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(live["data"])["ca"] = "unrecorded"
	if err := verifyIssuerCredentialOwners(t.Context(), read, plan, j, core, coreJournal, successor); err == nil {
		t.Fatal("accepted unrecorded trust")
	}
	deploymentplan.Map(live["data"])["ca"] = "new"
	successor[0].UID = "foreign"
	if err := verifyIssuerCredentialOwners(t.Context(), read, plan, j, core, coreJournal, successor); err == nil {
		t.Fatal("accepted a different owner")
	}
	successor[0].UID = uid
	managed := deploymentplan.Clone(next)
	deploymentplan.Map(managed["data"])["ca"] = "managed"
	deploymentplan.Map(live["data"])["ca"] = "managed"
	updated := []activeTransportRenewalResource{{ID: id, UID: uid, Before: next, Desired: managed}}
	if err := verifyIssuerCredentialOwners(t.Context(), read, plan, j, core, coreJournal, successor, updated); err != nil {
		t.Fatal("recorded managed write was not chained to the successor", err)
	}
	updated[0].Before = old
	if err := verifyIssuerCredentialOwners(t.Context(), read, plan, j, core, coreJournal, successor, updated); err == nil {
		t.Fatal("accepted a managed write with a foreign baseline")
	}
}

func TestMQTTCustodyCAStagePreservesOriginalOwnersAndTokens(t *testing.T) {
	const stack = "video-cloud-dev"
	namespace := stack + "-video-cloud"
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped",
		ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	defs := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "mqtt-trust", "namespace": namespace}}, Phase: "configuration"}}
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{}}}}
		pod := environmentPKIPod(object)
		pod["containers"] = []any{deploymentplan.Object{"name": "app", "image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), "command": []any{"/app/" + strings.TrimPrefix(name, "video-cloud-"), "--deployment-bootstrap-trust"}}}
		pod["volumes"] = []any{deploymentplan.Object{"name": "pki-custody-auth", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{
			deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"path": "kube.jwt"}},
			deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}},
		}}}}
		defs = append(defs, deploymentplan.Definition{Object: object, Phase: "workload"})
	}
	old, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: old.SHA256(), Failed: "Deployment/" + namespace + "/video-cloud-api", Owners: map[string]string{}}
	var live []deploymentplan.Object
	for _, r := range old.Resources {
		object := deploymentplan.Clone(r.Desired)
		uid := "original-" + r.ID
		deploymentplan.Map(object["metadata"])["uid"] = uid
		j.Completed = append(j.Completed, r.ID)
		j.Owners[r.ID] = uid
		live = append(live, object)
	}
	plan, err := compileMQTTCustodyStage(old, j, live, old.ReleaseSHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range plan.Resources {
		want := "retain"
		expected := deploymentplan.Clone(old.Resources[i].Desired)
		if r.Desired["kind"] == "Deployment" {
			want = "update"
			pod := environmentPKIPod(expected)
			volume := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
			projection := deploymentplan.Map(volume["projected"])
			ca := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(projection["sources"])[1])["configMap"])
			deploymentplan.Map(certIssuerObjectList(ca["items"])[0])["mode"] = float64(0444)
		}
		if r.Action != want || r.UID != j.Owners[r.ID] || deploymentplan.Digest(r.Desired) != deploymentplan.Digest(expected) {
			t.Fatalf("unexpected change for %s", r.ID)
		}
	}
	var afterCA []deploymentplan.Object
	for _, r := range plan.Resources {
		object := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(object["metadata"])["uid"] = r.UID
		afterCA = append(afterCA, object)
	}
	j.PlanSHA256 = plan.SHA256()
	j.Failed = "Deployment/" + namespace + "/video-cloud-logingester"
	newImage := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("d", 64)
	next, err := compileMQTTCustodyStage(plan, j, afterCA, strings.Repeat("e", 64), newImage)
	if err != nil {
		t.Fatal(err)
	}
	for i, resource := range next.Resources {
		want := "retain"
		body := deploymentplan.Clone(plan.Resources[i].Desired)
		if resource.ID == j.Failed {
			want = "update"
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(body)["containers"])[0])
			container["image"] = newImage
		}
		if resource.Action != want || resource.UID != j.Owners[resource.ID] || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(body) {
			t.Fatalf("unexpected trust recovery change for %s", resource.ID)
		}
	}
	deploymentplan.Map(live[1]["metadata"])["uid"] = "replacement"
	j.PlanSHA256 = old.SHA256()
	j.Failed = "Deployment/" + namespace + "/video-cloud-api"
	if _, err := compileMQTTCustodyStage(old, j, live, old.ReleaseSHA256, ""); err == nil {
		t.Fatal("accepted a replacement workload owner")
	}
}

package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceSessionBatchesSeparateRecordedPreviousSessions(t *testing.T) {
	a := "11111111-1111-4111-8111-111111111111"
	b := "22222222-2222-4222-8222-222222222222"
	next := "33333333-3333-4333-8333-333333333333"
	object := func(kind, name, sid string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "owner", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": sid}}}}}}}}
	}
	defs := []deploymentplan.Definition{{Phase: "workload", Object: object("Deployment", "a", a)}, {Phase: "workload", Object: object("Deployment", "b", b)}, {Phase: "workload", Object: object("StatefulSet", "openbao", a)}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	p, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Owners: map[string]string{}}
	var live []deploymentplan.Object
	for _, r := range p.Resources {
		o := deploymentplan.Clone(r.Desired)
		uid := "uid-" + deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
		deploymentplan.Map(o["metadata"])["uid"] = uid
		j.Owners[r.ID] = uid
		j.Completed = append(j.Completed, r.ID)
		live = append(live, o)
	}
	source := []serviceSessionRecordedPlan{{Plan: p, Journal: j}}
	got, err := compileServiceSessionBatches(source, live, []string{a, b}, next)
	if err != nil || len(got) != 2 || got[0].Plan == nil || got[1].Plan == nil || len(got[0].Plan.Resources) != 1 || len(got[1].Plan.Resources) != 1 || len(got[0].Pending) != 1 || got[0].Pending[0].Reason != "non-deployment" || len(got[1].Pending) != 0 {
		t.Fatal("did not separate both original sessions and pending StatefulSet", err, got)
	}
	if got[0].Stage == got[1].Stage || got[0].Plan.Resources[0].ID == got[1].Plan.Resources[0].ID {
		t.Fatal("session batches overlap")
	}
	qualification := secondSessionAdmission{Environment: "dev", SourceJournal: environmentCreateJournal{Stack: "video-cloud-dev"}, Renewal: secondSessionQualification{OriginalState: managedSessionState{Session: a}, Intent: createServiceSessionIntent{SessionID: b}, RenewedIntent: createServiceSessionIntent{SessionID: next}}, Batches: got}
	if !validServiceSessionBatches(qualification) {
		t.Fatal("valid same-owner SID batches were rejected")
	}
	modified := *got[0].Plan
	modified.Resources = append([]deploymentplan.Resource(nil), got[0].Plan.Resources...)
	modified.Resources[0].Desired = deploymentplan.Clone(modified.Resources[0].Desired)
	deploymentplan.Map(modified.Resources[0].Desired["metadata"])["name"] = "changed-owner"
	qualification.Batches = append([]serviceSessionBatch(nil), got...)
	qualification.Batches[0].Plan = &modified
	if validServiceSessionBatches(qualification) {
		t.Fatal("changed admitted owner accepted")
	}
	if _, err := compileServiceSessionBatches(source, live, []string{a, a}, next); err == nil {
		t.Fatal("duplicate prior session accepted")
	}
	foreign := deploymentplan.Clone(live[0])
	deploymentplan.Map(foreign["metadata"])["name"] = "unrecorded"
	if _, err := compileServiceSessionBatches(source, append(live, foreign), []string{a, b}, next); err == nil {
		t.Fatal("unrecorded owner accepted")
	}
	job := object("Job", "unverified-job", a)
	job["apiVersion"] = "batch/v1"
	deploymentplan.Map(job["metadata"])["uid"] = "44444444-4444-4444-8444-444444444444"
	job["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	unverified := serviceSessionHistoricalJob{ID: deploymentplan.ID(job), UID: "44444444-4444-4444-8444-444444444444"}
	if _, err := compileServiceSessionBatches(source, append(live, job), []string{a, b}, next, []serviceSessionHistoricalJob{unverified}); err == nil {
		t.Fatal("unverified historical Job was accepted")
	}
}

func TestServiceSessionBatchesKeepIndependentlyRecordedReleaseOwners(t *testing.T) {
	old := "11111111-1111-4111-8111-111111111111"
	next := "22222222-2222-4222-8222-222222222222"
	input := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	create := func(name, release string) (*deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Object) {
		t.Helper()
		object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "owner", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old}}}}}}}}
		in := input
		in.ReleaseSHA256 = release
		p, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: object, Phase: "workload"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := p.Resources[0]
		live := deploymentplan.Clone(r.Desired)
		uid := "uid-" + name
		deploymentplan.Map(live["metadata"])["uid"] = uid
		return p, deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{r.ID}, Owners: map[string]string{r.ID: uid}}, live
	}
	a, aj, al := create("a", strings.Repeat("b", 64))
	b, bj, bl := create("b", strings.Repeat("d", 64))
	batches, err := compileServiceSessionBatches([]serviceSessionRecordedPlan{{Plan: a, Journal: aj}, {Plan: b, Journal: bj}}, []deploymentplan.Object{al, bl}, []string{old}, next)
	if err != nil || len(batches) != 1 || batches[0].Plan == nil || len(batches[0].Plan.Resources) != 2 {
		t.Fatal("separately recorded release owners could not continue", err)
	}
	b.ConfigurationSHA256 = strings.Repeat("e", 64)
	bj.PlanSHA256 = b.SHA256()
	if _, err := compileServiceSessionBatches([]serviceSessionRecordedPlan{{Plan: a, Journal: aj}, {Plan: b, Journal: bj}}, []deploymentplan.Object{al, bl}, []string{old}, next); err == nil {
		t.Fatal("changed original configuration binding was accepted")
	}
}

package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSecondSessionAdmissionFreezesOriginalLineage(t *testing.T) {
	store, e, source, _ := freshStageFixture(t)
	digest := deploymentplan.Digest("evidence")
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "second-session", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": "fixture"}}, "restartPolicy": "Never"}}}}
	candidate, err := deploymentplan.Compile(source.Inputs, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := secondSessionQualification{ParentSHA: e.Journal.PlanSHA256, OriginalReview: digest, SourcePlanSHA: digest, SourceJournalSHA: digest, PreviousPlanSHA: digest, PreviousJournalSHA: digest, ClientsSHA: digest, KeySHA: digest, CSRSHA: digest, PreviousCertSHA: digest, OpenBaoTrustSHA: digest, Intent: createServiceSessionIntent{SessionID: "01010101-0202-4303-8404-050505050505"}}
	r.CustodyOwner = "23456:23457"
	r.PreviousReview, r.PreviousBindingSHA = digest, digest
	r.PreviousCredentialPlanSHA, r.PreviousCredentialJournalSHA = digest, digest
	r.PreviousCustodyPlanSHA, r.PreviousCustodyJournalSHA = digest, digest
	r.PreviousCustodyID, r.PreviousCustodyUID = "Deployment/dev/custody", "44444444-4444-4444-8444-444444444444"
	r.Intent.Namespaces = []string{"video-cloud-dev-video-cloud"}
	r.PreviousCredentialUIDs = map[string]string{"video-cloud-dev-video-cloud": "33333333-3333-4333-8333-333333333333"}
	r.SourceStage, r.PreviousStage = "kubernetes/service-session", "previous-session"
	r.SourceJobID, r.PreviousJobID = "Job/dev/original", "Job/dev/previous"
	r.SourceJobUID, r.PreviousJobUID = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	r.PreviousState = managedSessionState{Session: r.Intent.SessionID, Issuer: r.Intent.IssuerID, Root: r.Intent.RootSHA, Expires: time.Now().Add(-time.Hour)}
	r.BindingSHA = deploymentplan.Digest(r)
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = sessionRenewalUUID(r.ParentSHA, r.BindingSHA, r.Intent.SessionID)
	r.Candidate = candidate
	var snapshot environmentCreateJournal
	if err = json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	q := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), SourceJournal: snapshot, SourceJournalSHA256: deploymentplan.Digest(snapshot), QualifiedAt: now, ExecutionDeadline: now.Add(time.Hour), Renewal: r}
	if err = saveSecondSessionAdmission(store, e, q); err != nil {
		t.Fatal(err)
	}
	if err = saveSecondSessionAdmission(store, e, q); err != nil {
		t.Fatal("idempotent retry", err)
	}
	review := deploymentplan.Digest(q)
	got, err := loadSecondSessionAdmission(store, e, review)
	if err != nil || deploymentplan.Digest(got) != review {
		t.Fatal("load", err)
	}
	q.Renewal.KeySHA = deploymentplan.Digest("foreign")
	if validSecondSessionAdmission(store, e, q) {
		t.Fatal("accepted changed key binding")
	}
	q = got
	q.Renewal.RenewedIntent.SessionID = "11111111-2222-4333-8444-555555555555"
	if validSecondSessionAdmission(store, e, q) {
		t.Fatal("accepted unrelated successor")
	}
	q = got
	q.ExecutionDeadline = q.QualifiedAt
	if validSecondSessionAdmission(store, e, q) {
		t.Fatal("accepted exhausted deadline")
	}
}

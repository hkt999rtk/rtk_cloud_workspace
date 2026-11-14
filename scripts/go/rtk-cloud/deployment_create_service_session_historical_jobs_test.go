package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceSessionHistoricalJobsRequireExactCompletedLineage(t *testing.T) {
	store, e, template, backend := freshStageFixture(t)
	originalSID, previousSID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	stage := func(name string) (*deploymentplan.Plan, deploymentplan.Journal) {
		sid := originalSID
		if name == "previous-job" {
			sid = previousSID
		}
		body := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": "fixture", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": sid}}}}, "restartPolicy": "Never"}}}}
		defs := []deploymentplan.Definition{{Object: body, Phase: "bootstrap"}}
		in := template.Inputs
		if name == "original-job" {
			in.ReleaseSHA256 = deploymentplan.Digest("original-historical-release")
		}
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(name, defs)
		p, err := deploymentplan.Compile(in, defs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, name, p, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
		j, ok, err := p.ReadExecution(store.Root)
		if err != nil || !ok || !j.Accepted {
			t.Fatal("missing original accepted Job", err)
		}
		backend.objects[p.Resources[0].ID]["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		return p, j
	}
	original, originalJournal := stage("original-job")
	previous, previousJournal := stage("previous-job")
	candidateBody := deploymentplan.Clone(previous.Resources[0].Desired)
	deploymentplan.Map(candidateBody["metadata"])["name"] = "next-job"
	candidate, err := deploymentplan.Compile(template.Inputs, []deploymentplan.Definition{{Object: candidateBody, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := deploymentplan.Digest("evidence")
	r := secondSessionQualification{ParentSHA: e.Journal.PlanSHA256, OriginalReview: digest, PreviousReview: digest, PreviousBindingSHA: digest, SourceStage: "kubernetes/original-job", SourcePlanSHA: original.SHA256(), SourceJournalSHA: deploymentplan.Digest(originalJournal), SourceJobID: original.Resources[0].ID, SourceJobUID: originalJournal.Owners[original.Resources[0].ID], PreviousStage: "previous-job", PreviousPlanSHA: previous.SHA256(), PreviousJournalSHA: deploymentplan.Digest(previousJournal), PreviousJobID: previous.Resources[0].ID, PreviousJobUID: previousJournal.Owners[previous.Resources[0].ID], PreviousCredentialPlanSHA: digest, PreviousCredentialJournalSHA: digest, PreviousCustodyPlanSHA: digest, PreviousCustodyJournalSHA: digest, PreviousCustodyID: "Deployment/test/custody", PreviousCustodyUID: "44444444-4444-4444-8444-444444444444", ClientsSHA: digest, KeySHA: digest, CSRSHA: digest, PreviousCertSHA: digest, OpenBaoTrustSHA: digest, CustodyOwner: "23456:23457", Intent: createServiceSessionIntent{SessionID: "01010101-0202-4303-8404-050505050505", Namespaces: []string{e.Journal.Stack + "-video-cloud"}}}
	r.PreviousCredentialUIDs = map[string]string{r.Intent.Namespaces[0]: "33333333-3333-4333-8333-333333333333"}
	r.Intent.SessionID = previousSID
	r.Intent.Subjects = []string{"service:openbao", "service:future"}
	r.OriginalState.Session = originalSID
	r.OriginalState.Installed = []string{"service:openbao"}
	r.PreviousState = managedSessionState{Session: r.Intent.SessionID, Issuer: r.Intent.IssuerID, Root: r.Intent.RootSHA, Expires: time.Now().Add(-time.Hour)}
	r.BindingSHA = secondSessionBinding(&r)
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = sessionRenewalUUID(r.ParentSHA, r.BindingSHA, r.Intent.SessionID)
	r.Candidate = candidate
	var snapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	q := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), SourceJournal: snapshot, SourceJournalSHA256: deploymentplan.Digest(snapshot), QualifiedAt: now, ExecutionDeadline: now.Add(time.Hour), Renewal: r}
	jobs, err := verifyServiceSessionHistoricalJobs(t.Context(), store, e, backend, q)
	if err != nil || len(jobs) != 2 {
		t.Fatal("accepted original and predecessor Jobs not verified", err)
	}
	owner := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "owner", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "owner", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": originalSID}}}}}}}}
	defs := []deploymentplan.Definition{{Object: owner, Phase: "workload"}}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("owner", defs)
	ownerPlan, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "owner", ownerPlan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	ownerJournal, ok, err := ownerPlan.ReadExecution(store.Root)
	if err != nil || !ok {
		t.Fatal("missing accepted owner", err)
	}
	objects := []deploymentplan.Object{backend.objects[r.SourceJobID], backend.objects[r.PreviousJobID], backend.objects[ownerPlan.Resources[0].ID]}
	sources := []serviceSessionRecordedPlan{{Plan: ownerPlan, Journal: ownerJournal}}
	if _, err := compileServiceSessionBatches(sources, objects, []string{originalSID, previousSID}, r.RenewedIntent.SessionID); err == nil {
		t.Fatal("unrecorded original Jobs were silently ignored")
	}
	batches, err := compileServiceSessionBatches(sources, objects, []string{originalSID, previousSID}, r.RenewedIntent.SessionID, jobs)
	if err != nil || len(batches) != 2 || batches[0].Plan == nil || len(batches[0].Plan.Resources) != 1 || batches[1].Plan != nil {
		t.Fatal("verified historical Jobs changed workload candidates", err)
	}
	host := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": e.Journal.Stack + "-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao-pki", "env": []any{deploymentplan.Object{"name": "OPENBAO_PKI_HOST_RENEWAL_TLS_CA", "value": "/custom/service-trust/root.pem"}}, "volumeMounts": []any{deploymentplan.Object{"name": "trust", "mountPath": "/custom/service-trust", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "custom-service-trust"}}}}}}}
	hostDefs := []deploymentplan.Definition{{Object: host, Phase: "workload"}}
	hostInputs := template.Inputs
	hostInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("original-host", hostDefs)
	hostPlan, err := deploymentplan.Compile(hostInputs, hostDefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "original-host", hostPlan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	if id, err := serviceSessionTrustMapID(host); err != nil || id != "ConfigMap/"+e.Journal.Stack+"-secrets/custom-service-trust" {
		t.Fatal("did not use configured trust volume", err, id)
	}
	unsafeHost := deploymentplan.Clone(host)
	unsafeMount := managedNamedField(environmentPKIPod(unsafeHost)["containers"], "openbao-pki")
	deploymentplan.Map(certIssuerObjectList(unsafeMount["volumeMounts"])[0])["subPath"] = "root.pem"
	if _, err := serviceSessionTrustMapID(unsafeHost); err == nil {
		t.Fatal("subPath projection cannot receive a new key")
	}
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "custom-service-trust", "namespace": e.Journal.Stack + "-secrets"}, "data": deploymentplan.Object{"root.pem": "original-root", "unrelated": "preserve"}}
	trustDefs := []deploymentplan.Definition{{Object: trust, Phase: "configuration"}}
	trustInputs := template.Inputs
	trustInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("service-trust", trustDefs)
	trustPlan, err := deploymentplan.Compile(trustInputs, trustDefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "service-trust", trustPlan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	batches[0].Pending = []serviceSessionPendingOwner{{ID: "StatefulSet/" + e.Journal.Stack + "-secrets/openbao", Reason: "non-deployment"}}
	q.Batches = batches
	q.Reference, err = prepareServiceSessionReference(t.Context(), store, e, backend, originalSID, previousSID, r.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("prepare original ConfigMap reference", err)
	}
	changed := q
	ref := *q.Reference
	plan := *q.Reference.Plan
	plan.Resources = append([]deploymentplan.Resource(nil), plan.Resources...)
	plan.Resources[0].Desired = deploymentplan.Clone(plan.Resources[0].Desired)
	deploymentplan.Map(plan.Resources[0].Desired["data"])["root.pem"] = "changed-root"
	ref.Plan = &plan
	changed.Reference = &ref
	if validSecondSessionAdmission(store, e, changed) {
		t.Fatal("ConfigMap reference qualification changed existing root data")
	}
	if err := saveSecondSessionAdmission(store, e, q); err != nil {
		t.Fatal("cannot save the exact admitted workload batches", err)
	}
	review := deploymentplan.Digest(q)
	ctx, cancel := context.WithDeadline(t.Context(), q.ExecutionDeadline)
	defer cancel()
	mints, acknowledgements := 0, 0
	ackComplete := false
	adapters := serviceSessionBatchExecution{Backend: backend, Ready: func(context.Context) error { return nil }, VerifyHistory: func(context.Context) error { return verifyServiceSessionReferenceSource(store, e, q) }, Mint: func(context.Context) error { mints++; return nil }, Pending: func(ctx context.Context, _ serviceSessionBatch) error {
		return executeServiceSessionReference(ctx, store, e, backend, q, io.Discard)
	}, Accept: check, VerifyACKs: func(context.Context) (managedSessionState, error) {
		acknowledgements++
		state := managedSessionState{Session: r.RenewedIntent.SessionID, Issuer: r.RenewedIntent.IssuerID, Root: r.RenewedIntent.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(time.Hour), Subjects: r.RenewedIntent.Subjects}
		if ackComplete {
			state.Installed = []string{"service:openbao"}
		}
		return state, nil
	}, Clock: time.Now}
	adapters.Ready = func(context.Context) error { return errors.New("original OpenBao host handoff is not ready") }
	if err := executeServiceSessionBatches(ctx, store, e, review, adapters, io.Discard); err == nil || mints != 0 {
		t.Fatal("session was minted before the original OpenBao host was ready", err)
	}
	adapters.Ready = func(context.Context) error { return nil }
	backend.failure, backend.failed = "after-write-workload", false
	if err := executeServiceSessionBatches(ctx, store, e, review, adapters, io.Discard); err == nil || acknowledgements != 0 {
		t.Fatal("interrupted batch incorrectly reported ACK completion", err)
	}
	load := func(secretStore, *environmentCreateExecution) (*secondSessionAdmission, string, error) {
		return &q, review, nil
	}
	resumed, resumedReview, found, err := resumeServiceSessionBatchesQualificationWith(ctx, store, e, backend, adapters.Ready, now, q.ExecutionDeadline, load)
	if err != nil || !found || resumedReview != review || deploymentplan.Digest(resumed) != review {
		t.Fatal("preflight did not reuse the admitted interrupted batch", err)
	}
	id := ownerPlan.Resources[0].ID
	saved := deploymentplan.Clone(backend.objects[id])
	deploymentplan.Map(backend.objects[id]["metadata"])["uid"] = "55555555-5555-4555-8555-555555555555"
	if _, _, _, err := resumeServiceSessionBatchesQualificationWith(ctx, store, e, backend, adapters.Ready, now, q.ExecutionDeadline, load); err == nil {
		t.Fatal("preflight accepted a replacement owner on retry")
	}
	backend.objects[id] = saved
	if _, _, _, err := resumeServiceSessionBatchesQualificationWith(ctx, store, e, backend, adapters.Ready, now, q.ExecutionDeadline.Add(time.Hour), load); err == nil {
		t.Fatal("preflight changed the admitted deadline")
	}
	backend.failure, backend.failed = "", false
	if err := executeServiceSessionBatches(ctx, store, e, review, adapters, io.Discard); err == nil {
		t.Fatal("batch completed without the existing OpenBao ACK")
	}
	if completed, err := completedServiceSessionBatch(store, e, q, review); err != nil || completed {
		t.Fatal("missing ACK was recorded as completion", err)
	}
	ackComplete = true
	if err := executeServiceSessionBatches(ctx, store, e, review, adapters, io.Discard); err != nil || mints != 3 || acknowledgements != 2 {
		t.Fatal("same admitted batch did not resume", err)
	}
	if completed, err := completedServiceSessionBatch(store, e, q, review); err != nil || !completed {
		t.Fatal("actual acknowledged batch completion was not persisted", err)
	}
	if _, _, resumed, err := resumeServiceSessionBatchesQualificationWith(ctx, store, e, backend, adapters.Ready, now, q.ExecutionDeadline, load); err != nil || resumed {
		t.Fatal("completed batch was replayed instead of allowing a newly qualified successor", err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-batches-"+review)
	var receipt serviceSessionBatchReceipt
	if entry == nil || json.Unmarshal(entry.Receipt, &receipt) != nil {
		t.Fatal("missing persisted acknowledgement receipt")
	}
	receipt.ObservedAt = q.QualifiedAt
	if validServiceSessionBatchReceipt(store, e, q, review, receipt) {
		t.Fatal("accepted ACK timestamp before the native child plans completed")
	}
	if err := executeServiceSessionBatches(ctx, store, e, review, adapters, io.Discard); err != nil || mints != 3 || acknowledgements != 3 {
		t.Fatal("completed batch replayed the renewal", err)
	}
	written, ok, err := batches[0].Plan.ReadExecution(store.Root)
	if err != nil || !ok || !written.Accepted || written.Owners[ownerPlan.Resources[0].ID] != ownerJournal.Owners[ownerPlan.Resources[0].ID] {
		t.Fatal("retry changed the recorded owner", err)
	}
	actualTrust := backend.objects[trustPlan.Resources[0].ID]
	trustData := deploymentplan.Map(actualTrust["data"])
	if trustData["root.pem"] != "original-root" || trustData["unrelated"] != "preserve" || trustData[serviceSessionReferenceKey] != r.RenewedIntent.SessionID || backend.writes[trustPlan.Resources[0].ID] != 2 {
		t.Fatal("reference update changed other ConfigMap data or was replayed")
	}
	job := backend.objects[r.SourceJobID]
	job["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
	if _, err := verifyServiceSessionHistoricalJobs(t.Context(), store, e, backend, q); err == nil {
		t.Fatal("unfinished original Job was accepted")
	}
	job["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	deploymentplan.Map(job["metadata"])["uid"] = "55555555-5555-4555-8555-555555555555"
	if _, err := verifyServiceSessionHistoricalJobs(t.Context(), store, e, backend, q); err == nil {
		t.Fatal("replacement original Job was accepted")
	}
}

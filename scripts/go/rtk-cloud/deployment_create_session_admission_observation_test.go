package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSessionAdmissionObservationAuthenticatesEachAncestorOnce(t *testing.T) {
	f := newServiceSessionNativeMintFixture(t)
	q, review := f.q, f.review
	reviews := []string{review}
	ids := serviceSessionBatchPrevious(q)
	var history []secondSessionAdmission
	for n := 0; n < 5; n++ {
		intent := mustProviderRecoveryJSON(sessionLineageIntent(review, q))
		key := sessionLineagePrefix + review
		at := time.Now().UTC()
		if err := f.e.BeginEntry(key, "session-lineage", intent, at); err != nil {
			t.Fatal(err)
		}
		if err := f.e.RecordReceipt(key, intent); err != nil {
			t.Fatal(err)
		}
		if err := f.e.CompleteEntry(key, at); err != nil {
			t.Fatal(err)
		}
		history = append(history, q)
		ids = append(ids, q.Renewal.RenewedIntent.SessionID)
		var next secondSessionAdmission
		if err := json.Unmarshal(mustProviderRecoveryJSON(q), &next); err != nil {
			t.Fatal(err)
		}
		next.SourceJournal = f.e.Journal
		next.SourceJournalSHA256 = deploymentplan.Digest(next.SourceJournal)
		next.QualifiedAt = time.Now().UTC()
		r := &next.Renewal
		r.PreviousReview, r.PreviousBindingSHA = review, q.Renewal.BindingSHA
		r.Intent = q.Renewal.RenewedIntent
		r.PreviousState.Session = r.Intent.SessionID
		r.PreviousState.Expires = next.QualifiedAt.Add(-time.Minute)
		r.BindingSHA = secondSessionBinding(r)
		r.RenewedIntent = r.Intent
		r.RenewedIntent.SessionID = sessionRenewalUUID(r.ParentSHA, r.BindingSHA, r.Intent.SessionID)
		next.Batches = nil
		for i, id := range ids {
			batch := serviceSessionBatch{Previous: id, Stage: "service-session-forward-" + deploymentplan.Digest([]string{id, r.RenewedIntent.SessionID})[:16]}
			if i == 0 {
				batch.Pending = f.q.Batches[0].Pending
			}
			next.Batches = append(next.Batches, batch)
		}
		next.Reference.Stage = "service-session-reference-" + deploymentplan.Digest(r.RenewedIntent.SessionID)[:16]
		deploymentplan.Map(next.Reference.Plan.Resources[0].Desired["data"])[serviceSessionReferenceKey] = r.RenewedIntent.SessionID
		next.Reference.Plan.BindingsSHA256 = environmentCreateStageBindingsSHA(next.Reference.Stage, []deploymentplan.Definition{{Phase: "configuration", Object: next.Reference.Plan.Resources[0].Desired}})
		next.RequiredACK = serviceSessionRequiredACKs(next, history)
		if err := saveSecondSessionAdmission(f.store, f.e, next); err != nil {
			t.Fatalf("ancestor fixture %d: %v", n, err)
		}
		q, review = next, deploymentplan.Digest(next)
		reviews = append(reviews, review)
	}
	reads := map[string]int{}
	read := func(id string) (secondSessionAdmission, error) {
		reads[id]++
		return readSecondSessionAdmissionRecord(f.store, f.e, id)
	}
	load := secondSessionAdmissionReader(f.store, f.e, read)
	first, err := load(review)
	if err != nil || deploymentplan.Digest(first) != review {
		t.Fatal("original ancestor observation", err)
	}
	first.Renewal.KeySHA = deploymentplan.Digest("caller-mutation")
	second, err := load(review)
	if err != nil || deploymentplan.Digest(second) != review {
		t.Fatal("caller changed authenticated snapshot", err)
	}
	for _, id := range reviews {
		if reads[id] != 1 {
			t.Fatalf("same observation repeated ancestor authentication: reads=%d unique=%d", reads[id], len(reviews))
		}
	}
	// A new observation reads originals again; a prior success cannot hide drift.
	for id := range reads {
		delete(reads, id)
	}
	if _, err := secondSessionAdmissionReader(f.store, f.e, read)(review); err != nil {
		t.Fatal(err)
	}
	for _, id := range reviews {
		if reads[id] != 1 {
			t.Fatal("observation reused a prior read")
		}
	}
	badAncestor := reviews[0]
	broken := secondSessionAdmissionReader(f.store, f.e, func(id string) (secondSessionAdmission, error) {
		v, err := readSecondSessionAdmissionRecord(f.store, f.e, id)
		if id == badAncestor {
			v.Renewal.CSRSHA = deploymentplan.Digest("changed-ancestor")
		}
		return v, err
	})
	if _, err := broken(review); err == nil {
		t.Fatal("ancestor digest mismatch accepted")
	}
	originalStack := f.e.Journal.Stack
	f.e.Journal.Stack = "foreign"
	if _, err := load(review); err == nil {
		t.Fatal("cached admission hid owner/journal drift")
	}
	f.e.Journal.Stack = originalStack
	intent := mustProviderRecoveryJSON(sessionLineageIntent(review, q))
	if err := f.e.BeginEntry(sessionLineagePrefix+review, "session-lineage", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := load(review); err == nil {
		t.Fatal("observation survived a genuine journal write")
	}
	if _, err := newSecondSessionAdmissionReader(f.store, f.e)(review); err != nil {
		t.Fatal("fresh observation rejected original admission after journal advance", err)
	}
}

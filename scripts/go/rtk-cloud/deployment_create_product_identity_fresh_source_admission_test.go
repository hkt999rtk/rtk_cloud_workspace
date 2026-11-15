package main

import (
	"context"
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

// The successor is genuinely qualified and admitted, but has not run. Reading
// the completed source does not grant its pending replacement any authority.
func TestFreshProductResumeSourceAdmissionSurvivesPendingSuccessor(t *testing.T) {
	f := newBootstrapCleanupLineageFixtureWithLifetime(t, 30*time.Second, true)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	source, err := freshProductResumeSourceAdmission(f.store, f.e, f.review)
	if err != nil || deploymentplan.Digest(source) != f.review {
		t.Fatal("original completed source rejected", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	state, _, err := readManagedSessionState(t.Context(), f.cfg, f.runtime, f.service.BootstrapSessionID)
	if err != nil {
		t.Fatal(err)
	}
	at := state.Expires.Add(time.Second)
	reads := nativeSecondSessionReads(f.cfg, f.store, f.e, f.native)
	reads.clock = nil // The qualifier's existing controlled clock; no wall wait.
	// The external fixture keeps JSON observations. Model the SQL other-active
	// projection at that SAME controlled clock; leave every saved proof and
	// native subject/issuance/ACK field untouched.
	nativeState := reads.state
	ids := []string{f.q.Renewal.OriginalState.Session, f.q.Renewal.Intent.SessionID, f.q.Renewal.RenewedIntent.SessionID}
	reads.state = func(ctx context.Context, id string) (managedSessionState, error) {
		observed, err := nativeState(ctx, id)
		if err != nil {
			return observed, err
		}
		observed.OtherActive = 0
		for _, other := range ids {
			if other == id {
				continue
			}
			state, err := nativeState(ctx, other)
			if err != nil {
				return observed, err
			}
			if state.Status == "active" && state.Expires.After(at) {
				observed.OtherActive++
			}
		}
		return observed, nil
	}
	next, err := prepareSecondSessionCandidateWithReads(t.Context(), f.cfg, f.store, f.e, reads, f.parent, f.service, f.service.Root, at)
	if err != nil {
		t.Fatal("genuine next native qualification", err)
	}
	var snapshot environmentCreateJournal
	if json.Unmarshal(mustProviderRecoveryJSON(f.e.Journal), &snapshot) != nil {
		t.Fatal("copy original journal")
	}
	admitted := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: f.store.Environment, ParentOwnerSHA256: deploymentplan.Digest(f.e.owner), SourceJournal: snapshot, SourceJournalSHA256: deploymentplan.Digest(snapshot), QualifiedAt: at, ExecutionDeadline: f.q.ExecutionDeadline, Renewal: *next}
	if err := saveSecondSessionAdmission(f.store, f.e, admitted); err != nil {
		t.Fatal("save qualified pending successor", err)
	}
	nextReview := deploymentplan.Digest(admitted)
	if err := beginSessionLineage(f.store, f.e, admitted, nextReview, at); err != nil {
		t.Fatal("admit pending successor", err)
	}
	if done, _ := completedServiceSessionBatch(f.store, f.e, admitted, nextReview); done {
		t.Fatal("pending successor must not have native completion evidence")
	}
	before = createBootstrapStateSnapshot(t, f.store.Root)
	source, err = freshProductResumeSourceAdmission(f.store, f.e, f.review)
	if err != nil || deploymentplan.Digest(source) != f.review {
		t.Fatal("completed original programme source was lost behind pending successor", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	for _, review := range []string{nextReview, strings.Repeat("f", 64), ""} {
		if _, err := freshProductResumeSourceAdmission(f.store, f.e, review); err == nil {
			t.Fatal("incomplete or foreign source accepted", review)
		}
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

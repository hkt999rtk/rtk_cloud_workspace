package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLExecutionContractServiceExecutedTailBindsEveryOriginalReceipt(t *testing.T) {
	f := newCRLExecutorFixture(t)
	state := f.state(t)
	// Use the production consumer locations for the actual publication run. Its
	// resulting receipts, rather than handcrafted completed entries, are the
	// positive source for the historical-tail validator below.
	for i, target := range f.artifact.Plan.Targets {
		claim := state.Objects[target.ClaimID]
		delete(state.Objects, target.ClaimID)
		deploymentplan.Map(claim["metadata"])["name"] = environmentCreateServiceOwnerClaim(target.Container)
		target.ClaimID = deploymentplan.ID(claim)
		state.Objects[target.ClaimID] = claim
		for _, path := range target.Paths {
			delete(state.Caches, target.PodName+":"+path)
		}
		target.Paths = []string{environmentCreateServiceState + "/private/crl/listener-root.json", environmentCreateServiceState + "/private/crl/outbound-root.json"}
		for _, path := range target.Paths {
			state.Caches[target.PodName+":"+path] = f.artifact.Plan.Snapshot.Prior
		}
		f.artifact.Plan.Targets[i] = target
	}
	f.writeState(t, state)
	source := f.execution.Journal
	sourceRaw := mustProviderRecoveryJSON(source)
	if err := executeCreateRootCRLRecovery(t.Context(), f.cfg, f.store, f.execution, f.backend, f.artifact, io.Discard); err != nil {
		t.Fatal("actual Service CRL publication", err)
	}
	current := f.execution.Journal
	if !rootCRLJournalTailValid(current, f.artifact, source, true) || !rootCRLJournalTailValid(current, f.artifact, source, false) {
		t.Fatal("genuine completed publication receipts rejected")
	}
	if len(current.Entries) != len(source.Entries)+5 {
		t.Fatal("expected complete actual publication tail")
	}
	for _, mode := range []string{"parent-receipt", "child-receipt", "changed-intent", "wrong-phase", "reordered-write", "outside-admission", "pre-qualification", "missing-completion-time", "pending-with-receipt", "allocated-with-completion", "unknown-write", "wrong-consumer-claim", "wrong-consumer-path", "partial-native-reconcile", "empty-tail"} {
		t.Run(mode, func(t *testing.T) {
			var journal environmentCreateJournal
			if err := json.Unmarshal(mustProviderRecoveryJSON(current), &journal); err != nil {
				t.Fatal(err)
			}
			var a createRootCRLRecoveryArtifact
			if err := json.Unmarshal(mustProviderRecoveryJSON(f.artifact), &a); err != nil {
				t.Fatal(err)
			}
			offset := len(source.Entries)
			complete := true
			switch mode {
			case "parent-receipt":
				journal.Entries[offset].Receipt = json.RawMessage(`{"completed":true}`)
			case "child-receipt":
				journal.Entries[offset+1].Receipt = json.RawMessage(`{"imported":true}`)
			case "changed-intent":
				journal.Entries[offset+2].IntentSHA256 = deploymentplan.Digest("foreign")
			case "wrong-phase":
				journal.Entries[offset+2].Phase = "other"
			case "reordered-write":
				journal.Entries[offset+1], journal.Entries[offset+2] = journal.Entries[offset+2], journal.Entries[offset+1]
			case "outside-admission":
				journal.Entries[offset].StartedAt = a.ValidUntil
			case "pre-qualification":
				journal.Entries[offset+1].StartedAt = a.QualifiedAt.Add(-time.Second)
			case "missing-completion-time":
				journal.Entries[offset+1].CompletedAt = time.Time{}
			case "pending-with-receipt":
				journal.Entries[offset+1].State = "pending"
			case "allocated-with-completion":
				journal.Entries[offset+1].State = "allocated"
			case "unknown-write":
				journal.Entries = append(journal.Entries, journal.Entries[offset+1])
				complete = false
			case "wrong-consumer-claim":
				a.Plan.Targets[0].ClaimID = "PersistentVolumeClaim/foreign"
			case "wrong-consumer-path":
				a.Plan.Targets[1].Paths[0] = "/foreign"
			case "partial-native-reconcile":
				journal.Entries = journal.Entries[:offset+2]
				journal.Entries[offset+1].State = "pending"
				journal.Entries[offset+1].Receipt = nil
				journal.Entries[offset+1].CompletedAt = time.Time{}
				complete = false
			case "empty-tail":
				journal.Entries = journal.Entries[:offset]
			}
			got := rootCRLJournalTailValid(journal, a, source, complete)
			if got != (mode == "partial-native-reconcile") {
				t.Fatal("historical receipt classification", mode, got)
			}
		})
	}
	if string(mustProviderRecoveryJSON(source)) != string(sourceRaw) {
		t.Fatal("historical source mutated")
	}
	// Lease availability is a native read: it accepts absence and refuses an
	// occupied current repair without changing the existing Lease.
	review := deploymentplan.Digest(f.artifact.Plan)
	if err := checkCreateRootCRLLeaseAvailable(t.Context(), f.execution, f.backend, rootCRLLeasePlan(f.cfg, review)); err != nil {
		t.Fatal("unoccupied repair lease rejected", err)
	}
	lease, err := f.backend.AcquireExecution(t.Context(), rootCRLLeasePlan(f.cfg, review))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCreateRootCRLLeaseAvailable(t.Context(), f.execution, f.backend, rootCRLLeasePlan(f.cfg, strings.Repeat("a", 64))); err == nil {
		t.Fatal("occupied original lease ignored")
	}
	if err := lease.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

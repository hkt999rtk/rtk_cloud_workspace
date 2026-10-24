package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the historical admission producer separately from the long native
// recovery chain. The suffix below is an in-memory selector input only: it is
// never stored as accepted child evidence or passed to the full source loader.
func TestHistoricalRecoveryAdmissionPhaseFeedsExactCoreCheckpoint(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	f.record(t, "bounded-bootstrap-default", map[string]string{"operation": "observe"}, map[string]string{"observed": "original"})
	before := deploymentplan.Digest(f.e.Journal)
	oldEntries := len(f.e.Journal.Entries)
	review, artifact := f.admitRecovery(t, "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1", map[string]string{"bounded-selector-input": deploymentplan.Digest("original")})
	entry := f.e.Journal.Entries[len(f.e.Journal.Entries)-1]
	intent := map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": before}
	if len(f.e.Journal.Entries) != oldEntries+1 || entry.Key != "openbao-recovery-admission-"+review || entry.Phase != "recovery" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.IntentSHA256 != deploymentplan.Digest(intent) {
		t.Fatal("historical admission did not use the actual recovery phase and exact original intent")
	}
	if f.e.Journal.Entries[oldEntries-1].Phase != "bootstrap" {
		t.Fatal("recovery admission changed the ordinary bootstrap producer default")
	}
	loaded, source, err := loadAdmittedCreateRecovery(f.store, f.e, review)
	if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(artifact) || deploymentplan.Digest(source) != before {
		t.Fatal("actual admission archive, held parent and original source differ", err)
	}
	if err := f.e.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	var checkpoint environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(f.e.Journal), &checkpoint); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	phases := []string{"openbao-host-ack-forward-" + review, "openbao-unseal", "managed-application-owners-forward-" + review, "native-app-routing-profile", "bootstrap", "managed-complete-issuer"}
	keys := []string{"kubernetes/" + phases[0], "openbao-managed-unseal-11111111-1111-4111-8111-111111111111", "kubernetes/" + phases[2], "kubernetes/" + phases[3], "native-app-routing-profile-proof", "kubernetes/managed-complete-issuer"}
	for i, key := range keys {
		suffix := environmentCreateJournalEntry{Key: key, Phase: phases[i], State: "completed", StartedAt: now, CompletedAt: now, IntentSHA256: deploymentplan.Digest(key), Receipt: mustProviderRecoveryJSON(map[string]string{"selector_input": key})}
		if i == len(keys)-1 {
			suffix.State = "pending"
			suffix.CompletedAt = time.Time{}
			suffix.Receipt = nil
		}
		checkpoint.Entries = append(checkpoint.Entries, suffix)
	}
	if managedCorePartialInitialReview(checkpoint) != review {
		t.Fatal("actual admission cannot select the exact stopped Core suffix")
	}
	for _, mode := range []string{"bootstrap-admission", "pending-admission", "foreign-admission", "prior-pending-alias", "prior-missing-receipt", "pending-ack", "missing-unseal", "pending-apps", "pending-routing", "missing-routing-proof", "completed-core", "extra-tail"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			if err := json.Unmarshal(mustProviderRecoveryJSON(checkpoint), &changed); err != nil {
				t.Fatal(err)
			}
			start := len(changed.Entries) - 7
			switch mode {
			case "bootstrap-admission":
				changed.Entries[start].Phase = "bootstrap"
			case "pending-admission":
				changed.Entries[start].State = "pending"
				changed.Entries[start].CompletedAt = time.Time{}
				changed.Entries[start].Receipt = nil
			case "foreign-admission":
				changed.Entries[start].Key = "unrelated-admission-" + review
			case "prior-pending-alias":
				changed.Entries[start-1].State = "pending"
				changed.Entries[start-1].CompletedAt = time.Time{}
				changed.Entries[start-1].Receipt = nil
			case "prior-missing-receipt":
				changed.Entries[start-1].Receipt = nil
			case "pending-ack":
				changed.Entries[start+1].State = "pending"
			case "missing-unseal":
				changed.Entries[start+2].Key = "openbao-managed-unseal-foreign"
			case "pending-apps":
				changed.Entries[start+3].State = "pending"
			case "pending-routing":
				changed.Entries[start+4].State = "pending"
			case "missing-routing-proof":
				changed.Entries[start+5].Receipt = nil
			case "completed-core":
				changed.Entries[start+6].State = "completed"
				changed.Entries[start+6].CompletedAt = now
				changed.Entries[start+6].Receipt = mustProviderRecoveryJSON(map[string]string{"unexpected": "accepted"})
			case "extra-tail":
				changed.Entries = append(changed.Entries, changed.Entries[start+6])
			}
			if managedCorePartialInitialReview(changed) != "" {
				t.Fatal("selector accepted an incomplete or foreign source", mode)
			}
		})
	}
	if writes != deploymentplan.Digest(f.native.writes) || f.native.held {
		t.Fatal("selector changed native owners or lease")
	}
	assertRecoveryContractReadOnly(t, f.store, snapshot)
	if err := f.e.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

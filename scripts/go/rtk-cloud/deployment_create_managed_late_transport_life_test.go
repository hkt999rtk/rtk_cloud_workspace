package main

import (
	"strings"
	"testing"
	"time"
)

func TestManagedLateTransportFiniteLifeKeepsOrdinaryFloorAndSameKeyPins(t *testing.T) {
	_, _, old, key, issued := activeTransportFixture(t)
	current, err := renewActiveTransportSameKey(old, key, issued)
	if err != nil {
		t.Fatal(err)
	}
	now := issued.Add(22*time.Hour + 30*time.Minute)
	if verifyActiveTransportSameKey(old, current, key, now) == nil {
		t.Fatal("ordinary two-hour floor weakened")
	}
	if err := verifyActiveTransportSameKeyForMinimum(old, current, key, now, time.Hour); err != nil {
		t.Fatal("valid signed same-key material rejected before fixed operation cutoff", err)
	}
	for _, minimum := range []time.Duration{0, -time.Minute, 2 * time.Hour} {
		if verifyActiveTransportSameKeyForMinimum(old, current, key, now, minimum) == nil {
			t.Fatal("invalid/insufficient life admitted")
		}
	}
	changed := current
	changed.Identities = map[string]environmentCreateTransportIdentity{}
	for name, identity := range current.Identities {
		changed.Identities[name] = identity
	}
	leaf := changed.Identities["account-manager"]
	leaf.Key = old.Identities["pki-controller"].Key
	changed.Identities["account-manager"] = leaf
	if verifyActiveTransportSameKeyForMinimum(old, changed, key, now, time.Hour) == nil {
		t.Fatal("contextual lifetime waived original private-key binding")
	}
	if verifyActiveTransportSameKeyForMinimum(old, current, key, issued.Add(24*time.Hour), time.Minute) == nil {
		t.Fatal("expired certificate admitted")
	}
}
func TestManagedLateTransportScopeRequiresActualAdmittedHistory(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	if _, selected, err := managedLatePostMintTransportMinimum(store, parent); selected || err != nil {
		t.Fatal("ordinary environment selected post scope")
	}
	owner := environmentCreateOwner{PlanSHA256: parent, OwnerToken: "isolated", Journal: "deployment/create/isolated/journal.json"}
	journal := environmentCreateJournal{PlanSHA256: parent, OwnerToken: owner.OwnerToken}
	now := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 13; i++ {
		journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "unrelated", Phase: "bootstrap", State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: mustProviderRecoveryJSON(map[string]string{"real": "isolated"})})
	}
	review := strings.Repeat("b", 64)
	journal.Entries[4].Key = "kubernetes/service-session-original-key-renewal-" + review
	journal.Entries[5].Key = "openbao-recovery-admission-" + strings.Repeat("c", 64)
	journal.Entries[9].Key = "kubernetes/service-cleanup-binding-renewal-" + review
	lateSeedForwardWrite(t, store, "deployment/.execution-active/owner.json", owner)
	lateSeedForwardWrite(t, store, owner.Journal, journal)
	if _, selected, err := managedLatePostMintTransportMinimum(store, parent); !selected || err == nil {
		t.Fatal("literal fake completed tail became transport authority")
	}
	if _, selected, err := managedLatePostMintTransportMinimum(store, strings.Repeat("d", 64)); selected || err != nil {
		t.Fatal("foreign parent inherited contextual lifetime")
	}
}

package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestJournalMembershipAcceptsReceiptEncodingChanges(t *testing.T) {
	t.Run("openbao", func(t *testing.T) {
		now := time.Now().UTC()
		owner := environmentCreateOwner{Environment: "dev", Stack: "stack", PlanSHA256: "parent", OwnerToken: "owner"}
		before := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: owner.Environment, Stack: owner.Stack, PlanSHA256: owner.PlanSHA256, OwnerToken: owner.OwnerToken, Entries: []environmentCreateJournalEntry{{Key: "existing", Phase: "bootstrap", State: "completed", StartedAt: now, CompletedAt: now, Receipt: json.RawMessage(`{"x":1}`)}}}
		current := before
		current.Entries = append([]environmentCreateJournalEntry(nil), before.Entries...)
		for _, tc := range []struct {
			receipt string
			want    bool
		}{{" {\n\"x\": 1\n}", true}, {`{"x":2}`, false}, {`{"x":`, false}} {
			current.Entries[0].Receipt = json.RawMessage(tc.receipt)
			if got := createOpenBaoACKCompletionMembership(before, current, owner); got != tc.want {
				t.Errorf("receipt %q: got %v want %v", tc.receipt, got, tc.want)
			}
		}
	})
	t.Run("mqtt", func(t *testing.T) {
		a, source, before := mqttRootCRLCompletionFixture(t)
		evidence, err := mqttRootCRLCompletionRecordedEvidence(a, source, before)
		if err != nil {
			t.Fatal(err)
		}
		index := len(source.Entries)
		before.Entries[index].State = "allocated"
		before.Entries[index].Receipt = mustProviderRecoveryJSON(mqttRootCRLCompletionReceipt(a, evidence))
		for _, tc := range []struct {
			suffix string
			want   bool
		}{{"\n", true}, {"x", false}} {
			current := cloneMQTTCompletionJournal(t, before)
			current.Entries[index].Receipt = append(current.Entries[index].Receipt, tc.suffix...)
			if got := mqttRootCRLCompletionMembership(a, source, before, current); got != tc.want {
				t.Errorf("suffix %q: got %v want %v", tc.suffix, got, tc.want)
			}
		}
	})
	t.Run("app", func(t *testing.T) {
		a, source, _, before, evidence, _ := appCompletionNegativeFixture(t)
		for _, tc := range []struct {
			suffix string
			want   bool
		}{{"\n", true}, {"x", false}} {
			current := cloneAppCompletionNegativeJournal(t, before)
			current.Entries[372].Receipt = append(current.Entries[372].Receipt, tc.suffix...)
			if got := managedLateAppCompletionMembership(a, source, before, current, evidence); got != tc.want {
				t.Errorf("suffix %q: got %v want %v", tc.suffix, got, tc.want)
			}
		}
	})
}

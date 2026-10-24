package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLatePostNativeHeld400Fixture(t *testing.T) *environmentCreateExecution {
	t.Helper()
	e, _ := managedLateTerminalHeld396Fixture(t)
	at := e.Journal.Entries[395].StartedAt.Add(time.Second)
	review := managedLateExpiredRenewalReview(e.Journal)
	for i, key := range []string{"bootstrap-renewed-cleanup-custody-" + review, "bootstrap-copy-retire-namespace-a-11111111-1111-4111-8111-111111111111", "bootstrap-copy-retire-namespace-b-11111111-1111-4111-8111-111111111111", "bootstrap-copy-retire-namespace-c-11111111-1111-4111-8111-111111111111"} {
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", StartedAt: at.Add(time.Duration(i) * time.Second), CompletedAt: at.Add(time.Duration(i+1) * time.Second), IntentSHA256: strings.Repeat("e", 64), Receipt: []byte(`{"isolated":true}`)})
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestManagedLatePostNativeHeld400KeepsActualFenceAndPureHistorySeparate(t *testing.T) {
	e := managedLatePostNativeHeld400Fixture(t)
	before := deploymentplan.Digest([]any{e.Journal, e.owner})
	if !managedLateTerminalSourceSelected(e.Journal) || !managedLatePostNativeCleanupSelected(e.Journal) || managedLateTerminalFinalizerSelected(e.Journal) {
		t.Fatal("post-native400 reused the pre-native396 selector")
	}
	view, err := managedLateTerminalPureSourceView(context.Background(), e)
	if err != nil || len(view.Journal.Entries) != 392 || checkManagedLatePureSourceExecution(context.Background(), view) != nil || view.Check(context.Background()) == nil {
		t.Fatal("held400 could not authenticate read-only392 while retaining strict actual fence", err)
	}
	if err := view.BeginEntry("forbidden-history-write", "bootstrap", []byte(`{}`), time.Now()); err == nil {
		t.Fatal("pure history gained mutation authority")
	}
	if e.Check(context.Background()) != nil || before != deploymentplan.Digest([]any{e.Journal, e.owner}) {
		t.Fatal("pure400 ancestry rewrote parent/source")
	}
}

func TestManagedLatePostNativeSelectorRejectsUnknownOrPartialOutcome(t *testing.T) {
	for _, mode := range []string{"missing-copy", "extra-tail", "custody-key", "pending-custody", "pending-copy", "missing-receipt", "copy-key", "completed-finalizer", "unknown-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			e := managedLatePostNativeHeld400Fixture(t)
			j := e.Journal
			j.Entries = append([]environmentCreateJournalEntry(nil), j.Entries...)
			switch mode {
			case "missing-copy":
				j.Entries = j.Entries[:399]
			case "extra-tail":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unknown"})
			case "custody-key":
				j.Entries[396].Key = "foreign"
			case "pending-custody":
				j.Entries[396].State = "pending"
			case "pending-copy":
				j.Entries[398].State = "pending"
			case "missing-receipt":
				j.Entries[398].Receipt = nil
			case "copy-key":
				j.Entries[398].Key = "foreign"
			case "completed-finalizer":
				j.Entries[395].State = "completed"
			case "unknown-predecessor":
				j.Entries[392].State = "pending"
			}
			if managedLatePostNativeCleanupSelected(j) || managedLateTerminalSourceSelected(j) {
				t.Fatal("partial/unknown post-native outcome selected")
			}
		})
	}
}

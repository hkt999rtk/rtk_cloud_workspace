package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateTerminalHeld396Fixture(t *testing.T) (*environmentCreateExecution, *environmentCreateExecution) {
	t.Helper()
	_, e, _, _, _ := managedLateSourceWriteFixture(t)
	e.owner.Schema = environmentCreateOwnerSchema
	e.Journal.Schema = environmentCreateJournalSchema
	review := strings.Repeat("b", 64)
	now := time.Now().UTC().Add(-time.Minute)
	for i := len(e.Journal.Entries); i < 396; i++ {
		at := now.Add(time.Duration(i) * time.Millisecond)
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-terminal-%03d", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("c", 64), Receipt: json.RawMessage(`{"isolated":true}`)})
	}
	e.Journal.Entries[374].Key = "openbao-recovery-admission-" + review
	e.Journal.Entries[387].Key = "kubernetes/managed-late-log-stop-" + review
	for i, key := range []string{"kubernetes/managed-late-log-existing-ack-", "kubernetes/managed-late-log-retire-", "kubernetes/managed-late-log-restore-", "managed-late-log-verified-"} {
		e.Journal.Entries[388+i].Key = key + review
	}
	e.Journal.Entries[392].Key = "openbao-host-ack-verified-" + strings.Repeat("d", 64)
	e.Journal.Entries[392].Phase = "openbao-host-ack-verification"
	e.Journal.Entries[393].Key = "bootstrap-copy-retire-original-a"
	e.Journal.Entries[394].Key = "bootstrap-copy-retire-original-b"
	e.Journal.Entries[395] = environmentCreateJournalEntry{Key: "service-session-finalize", Phase: "bootstrap", State: "pending", StartedAt: now.Add(time.Second), IntentSHA256: strings.Repeat("e", 64)}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	managedLateSourceReuseNegativeWrite(t, e.Store, "deployment/.execution-active/owner.json", mustProviderRecoveryJSON(e.owner))
	if err := e.Check(context.Background()); err != nil {
		t.Fatal("isolated actual396 canonical fence refused", err)
	}
	view, err := managedLateTerminalPureSourceView(context.Background(), e)
	if err != nil {
		t.Fatal("held396 did not authorize its exact pure392 view", err)
	}
	return e, view
}

func TestManagedLateTerminalPureViewKeepsCanonical396FenceAndRejectsWrites(t *testing.T) {
	actual, view := managedLateTerminalHeld396Fixture(t)
	raw, err := os.ReadFile(actual.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	ownerRaw, err := os.ReadFile(filepath.Join(actual.Store.Root, "deployment/.execution-active/owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest([]any{actual.Journal, actual.owner})
	if len(view.Journal.Entries) != 392 || len(actual.Journal.Entries) != 396 || view.Check(context.Background()) == nil {
		t.Fatal("pure392 bypassed global canonical396 check")
	}
	if err := checkManagedLatePureSourceExecution(context.Background(), view); err != nil {
		t.Fatal("pure source check did not authenticate actual held396", err)
	}
	if err := view.BeginEntry("forbidden-projected-write", "bootstrap", json.RawMessage(`{"forbidden":true}`), time.Now().UTC()); err == nil {
		t.Fatal("projected historical view gained BeginEntry authority")
	}
	if err := view.RecordReceipt("service-session-finalize", json.RawMessage(`{"forbidden":true}`)); err == nil {
		t.Fatal("projected historical view gained receipt authority")
	}
	if err := actual.Check(context.Background()); err != nil {
		t.Fatal("pure reads changed held actual396", err)
	}
	after, _ := os.ReadFile(actual.JournalPath)
	afterOwner, _ := os.ReadFile(filepath.Join(actual.Store.Root, "deployment/.execution-active/owner.json"))
	if !bytes.Equal(raw, after) || !bytes.Equal(ownerRaw, afterOwner) || deploymentplan.Digest([]any{actual.Journal, actual.owner}) != before {
		t.Fatal("pure history check rewrote canonical source or owner")
	}
}

func TestManagedLateTerminalPureViewRefusesProjectionAndHeldParentDrift(t *testing.T) {
	for _, mode := range []string{"view-source", "view-metadata", "view-lease", "view-owner", "view-root", "view-config-root", "view-environment", "view-directory", "view-journal-path", "view-running-lock", "missing-link", "foreign-parent", "nested-parent", "source-pin", "view-pin", "canonical-journal", "canonical-owner", "released-lock", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			actual, original := managedLateTerminalHeld396Fixture(t)
			view := *original
			if err := json.Unmarshal(mustProviderRecoveryJSON(original.Journal), &view.Journal); err != nil {
				t.Fatal(err)
			}
			link := *original.latePureSource
			view.latePureSource = &link
			view.Journal.Entries = slices.Clone(view.Journal.Entries)
			ctx := context.Background()
			switch mode {
			case "view-source":
				view.Journal.Entries[380].IntentSHA256 = strings.Repeat("f", 64)
			case "view-metadata":
				view.Journal.Stack = "foreign"
			case "view-lease":
				view.Journal.KubernetesLeases = append(view.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/foreign/foreign"})
			case "view-owner":
				view.owner.OwnerToken = "foreign"
			case "view-root":
				view.Store.Root += "-foreign"
			case "view-config-root":
				view.Store.ConfigRoot += "-foreign"
			case "view-environment":
				view.Store.Environment = "staging"
			case "view-directory":
				view.Directory += "-foreign"
			case "view-journal-path":
				view.JournalPath += "-foreign"
			case "view-running-lock":
				view.running = nil
			case "missing-link":
				view.latePureSource = nil
			case "foreign-parent":
				view.latePureSource.parent = &environmentCreateExecution{}
			case "nested-parent":
				actual.latePureSource = &managedLatePureSourceView{parent: actual}
			case "source-pin":
				view.latePureSource.parentSHA = strings.Repeat("f", 64)
			case "view-pin":
				view.latePureSource.viewSHA = strings.Repeat("f", 64)
			case "canonical-journal":
				managedLateSourceReuseNegativeWrite(t, actual.Store, actual.owner.Journal, []byte(`{"replaced":true}`))
			case "canonical-owner":
				managedLateSourceReuseNegativeWrite(t, actual.Store, "deployment/.execution-active/owner.json", []byte(`{"replaced":true}`))
			case "released-lock":
				if err := actual.running.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := checkManagedLatePureSourceExecution(ctx, &view); err == nil {
				t.Fatal("unheld/changed pure history view accepted")
			}
		})
	}
}

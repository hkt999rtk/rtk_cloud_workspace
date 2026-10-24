package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateCompletedTailAuditFixture(t *testing.T) (environmentCreateBootstrapRuntime, *createManagedLateRemaining) {
	t.Helper()
	runtime, _, _ := managedLateResidualCompletedHandoffFixture(t)
	e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
	name := "pki-export-account-manager-" + e.Journal.PlanSHA256[:12]
	keys := []string{"kubernetes/server-custody-account-manager", "server-export-account-manager", "retire-" + name, "kubernetes/retire-" + name}
	start := time.Now().UTC().Add(-time.Minute)
	for i, key := range keys {
		phase := "bootstrap"
		if strings.HasPrefix(key, "kubernetes/") {
			phase = strings.TrimPrefix(key, "kubernetes/")
		}
		at := start.Add(time.Duration(i) * time.Second)
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, Phase: phase, State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: strings.Repeat("e", 64), Receipt: []byte(`{"completed":"isolated-normal-stage"}`)})
	}
	for i := range 2 {
		e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-account-manager/rtk-deployment-execution", UID: []string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i], Holder: "isolated-accepted-normal-child"})
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if !managedLateCompletedTailSelected(e.Journal) {
		t.Fatal("literal completed428 fixture was not routed")
	}
	return runtime, r
}

func TestManagedLateCompletedTailAuditPureHistoryUsesCompletedProofAndOriginalLeases(t *testing.T) {
	runtime, archived := managedLateCompletedTailAuditFixture(t)
	e := runtime.Execution
	before := deploymentplan.Digest([]any{e.Journal, e.owner})
	rawBefore, err := os.ReadFile(e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	proofBefore, err := os.ReadFile(filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256)))
	if err != nil {
		t.Fatal(err)
	}
	view, err := managedLateCompletedTailPureSourceView(context.Background(), e, archived)
	if err != nil || view == nil || len(view.Journal.Entries) != 392 || len(view.Journal.KubernetesLeases) != archived.FinalizationAuthority.SourceLeases || len(e.Journal.KubernetesLeases) != archived.FinalizationAuthority.SourceLeases+2 || deploymentplan.Digest(view.Journal) != archived.CurrentSourceSHA {
		t.Fatal("completed428 could not prove the immutable392 parsing view", err)
	}
	if view.Check(context.Background()) == nil || checkManagedLatePureSourceExecution(context.Background(), view) != nil || e.Check(context.Background()) != nil {
		t.Fatal("completed pure view confused actual fence and historical parsing")
	}
	if err := view.BeginEntry("forbidden-completed-history-write", "bootstrap", []byte(`{}`), time.Now()); err == nil {
		t.Fatal("completed history view gained write authority")
	}
	if before != deploymentplan.Digest([]any{e.Journal, e.owner}) {
		t.Fatal("source view rewrote completed396 or accepted normal tail")
	}
	rawAfter, _ := os.ReadFile(e.JournalPath)
	proofAfter, _ := os.ReadFile(filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256)))
	if !bytes.Equal(rawBefore, rawAfter) || !bytes.Equal(proofBefore, proofAfter) {
		t.Fatal("pure completed-tail check republished history")
	}
}

func TestManagedLateCompletedTailAuditRejectsUnprovedCompletedSource(t *testing.T) {
	for _, mode := range []string{"missing-tail", "extra-tail", "foreign-tail-key", "pending-tail", "missing-tail-receipt", "pending396", "late396", "missing-one-retirement", "changed-captured-lease", "changed392", "foreign-owner", "wrong-archived-source", "missing-proof", "changed-proof", "changed-fence", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			runtime, old := managedLateCompletedTailAuditFixture(t)
			e := runtime.Execution
			archived := *old
			ctx := context.Background()
			switch mode {
			case "missing-tail":
				e.Journal.Entries = e.Journal.Entries[:427]
			case "extra-tail":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[427])
			case "foreign-tail-key":
				e.Journal.Entries[427].Key = "kubernetes/foreign-stage"
			case "pending-tail":
				e.Journal.Entries[427].State = "pending"
				e.Journal.Entries[427].CompletedAt = time.Time{}
				e.Journal.Entries[427].Receipt = nil
			case "missing-tail-receipt":
				e.Journal.Entries[425].Receipt = nil
			case "pending396":
				e.Journal.Entries[395].State = "pending"
				e.Journal.Entries[395].CompletedAt = time.Time{}
				e.Journal.Entries[395].Receipt = nil
			case "late396":
				e.Journal.Entries[395].CompletedAt = old.ResidualAuthority.Deadline.Add(time.Second)
			case "missing-one-retirement":
				e.Journal.Entries[423].State = "pending"
				e.Journal.Entries[423].CompletedAt = time.Time{}
				e.Journal.Entries[423].Receipt = nil
			case "changed-captured-lease":
				e.Journal.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
				e.Journal.KubernetesLeases[0].UID = "44444444-4444-4444-8444-444444444444"
			case "changed392":
				e.Journal.Entries[391].Receipt = []byte(`{"changed":true}`)
			case "foreign-owner":
				archived.OwnerSHA = strings.Repeat("f", 64)
			case "wrong-archived-source":
				archived.CurrentSourceSHA = strings.Repeat("e", 64)
			case "missing-proof":
				if err := os.Remove(filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256))); err != nil {
					t.Fatal(err)
				}
			case "changed-proof":
				if err := os.WriteFile(filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256)), []byte(`{"changed":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-fence":
				managedLateSourceReuseNegativeWrite(t, e.Store, "deployment/.execution-active/owner.json", []byte(`{"replaced":true}`))
			case "cancelled":
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				stop()
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := managedLateCompletedTailPureSourceView(ctx, e, &archived); err == nil {
				t.Fatal("unproved completed428 source gained historical capability", mode)
			}
		})
	}
}

func TestManagedLateCompletedTailAuditRoutingEnvelopeNeverGrantsResumeAuthority(t *testing.T) {
	runtime, _ := managedLateCompletedTailAuditFixture(t)
	e := runtime.Execution
	if !managedLateCompletedTailSelected(e.Journal) {
		t.Fatal("fixture lost its routing envelope")
	}
	// A genuinely completed historical chain alone is insufficient for this
	// new resume profile: the selected remaining21 paired grant is mandatory.
	if _, err := loadManagedLateCompletedTailSource(context.Background(), deploymentConfig{Environment: "dev"}, e.Store, e); err == nil {
		t.Fatal("routing envelope bypassed the required remaining21 authority")
	}
	if err := verifyManagedLateCompletedTailSource(context.Background(), deploymentConfig{Environment: "dev"}, e.Store, e, runtime.ServiceSessionRecovery); err == nil {
		t.Fatal("unproved runtime marker granted completed-tail authority")
	}
}

func TestManagedLateCompletedTailAuditRestoresOnlyCompletedCeremonyWithoutNativeBudget(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-ceremony", "expired-overall", "source-drift", "missing-proof"} {
		t.Run(mode, func(t *testing.T) {
			runtime, archived := managedLateCompletedTailAuditFixture(t)
			e, r := runtime.Execution, runtime.ServiceSessionRecovery
			view, err := managedLateCompletedTailPureSourceView(context.Background(), e, archived)
			if err != nil {
				t.Fatal(err)
			}
			proofPath := filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256))
			raw, err := os.ReadFile(proofPath)
			if err != nil {
				t.Fatal(err)
			}
			r.completedTail = &createManagedLateCompletedTail{SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), ProofSHA: environmentPreflightBytesSHA256(raw), ViewSHA: deploymentplan.Digest(view.Journal), AuthoritySHA: deploymentplan.Digest(archived.ResidualAuthority), LateReview: archived.OriginalReview, SourceLeases: len(e.Journal.KubernetesLeases), OverallDeadline: archived.ResidualAuthority.OverallDeadline, view: view}
			runtime.Backend.Store = e.Store
			switch mode {
			case "foreign-ceremony":
				r.Ceremony.BootstrapSessionID = "44444444-4444-4444-8444-444444444444"
			case "expired-overall":
				r.completedTail.OverallDeadline = time.Now().Add(-time.Second)
			case "source-drift":
				e.Journal.Entries[427].Receipt = []byte(`{"changed":true}`)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "missing-proof":
				if err := os.Remove(proofPath); err != nil {
					t.Fatal(err)
				}
			}
			before := deploymentplan.Digest(e.Journal)
			if _, err := managedLateActiveBudget(context.Background()); err == nil {
				t.Fatal("fixture accidentally supplied a new native budget")
			}
			got, err := restoreManagedLateRemainingCeremony(context.Background(), deploymentConfig{Environment: "dev"}, e.Store, runtime)
			if mode == "valid" {
				if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(archived.Ceremony) || !r.Renewal {
					t.Fatal("genuine completed tail required another native budget or changed its ceremony", err)
				}
			} else if err == nil {
				t.Fatal("unproved completed ceremony restored", mode)
			}
			if before != deploymentplan.Digest(e.Journal) {
				t.Fatal("ceremony restoration replayed or rewrote native history")
			}
		})
	}
}

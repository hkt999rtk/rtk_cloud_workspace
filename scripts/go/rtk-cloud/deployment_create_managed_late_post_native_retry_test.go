package main

import (
	"context"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLatePostNativeRetryFixture(t *testing.T, age time.Duration) (*createManagedLateRemaining, createFoundationRecoveryArtifact) {
	t.Helper()
	r, a, _ := managedLatePostNativeAuditFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	r.FinalizationAuthority.StartedAt = now.Add(-70 * time.Minute)
	r.FinalizationAuthority.OverallDeadline = now.Add(20 * time.Minute)
	r.FinalizationAuthority.Deadline = managedLateFinalizationDeadline(r.FinalizationAuthority.StartedAt, r.FinalizationAuthority.OverallDeadline, r.LeafNotAfter, r.SessionExpires)
	r.TerminalFinalizerCompletion.OriginalAuthoritySHA = deploymentplan.Digest(r.FinalizationAuthority)
	terminalStart := now.Add(-55 * time.Minute)
	a.Plan.ParentJournalSHA = r.TerminalFinalizerCompletion.SourceSHA
	a.Plan.Materials[managedLateRemainingMaterial+"terminal-completion"] = deploymentplan.Digest(r.TerminalFinalizerCompletion)
	a.QualifiedAt, a.ValidUntil = terminalStart.Add(-time.Minute), terminalStart.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	var err error
	r.TerminalAuthority, err = newManagedLateTerminalFinalizerAuthority(r, a, now.Add(35*time.Minute), terminalStart, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	r.PostNativeCleanup.TerminalAuthoritySHA = deploymentplan.Digest(r.TerminalAuthority)
	r.PostNativeCleanup.Native.FinalizedAt = terminalStart.Add(5 * time.Minute)
	start := now.Add(-age)
	a.Plan.ParentJournalSHA = r.PostNativeCleanup.SourceSHA
	a.Plan.Materials[managedLateRemainingMaterial+"post-native-cleanup"] = deploymentplan.Digest(r.PostNativeCleanup)
	a.QualifiedAt, a.ValidUntil = start.Add(-time.Minute), start.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	r.PostNativeAuthority, err = newManagedLatePostNativeCleanupAuthority(r, a, now.Add(35*time.Minute), start, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	return r, a
}

func TestManagedLatePostNativeRetryUsesOriginalPositiveRemainder(t *testing.T) {
	for _, mode := range []string{"late-valid", "expired", "caller-short", "changed-authority", "ordinary-still-reserves-fifteen"} {
		t.Run(mode, func(t *testing.T) {
			age := 25 * time.Minute
			if mode == "expired" {
				age = 31 * time.Minute
			}
			r, _ := managedLatePostNativeRetryFixture(t, age)
			before := deploymentplan.Digest(r.PostNativeAuthority)
			cutoff := r.PostNativeAuthority.OverallDeadline
			if mode == "caller-short" {
				cutoff = r.PostNativeAuthority.Deadline.Add(-time.Second)
			}
			if mode == "changed-authority" {
				r.PostNativeAuthority.SourceSHA = r.OwnerSHA
			}
			ctx, cancel := context.WithDeadline(context.Background(), cutoff)
			defer cancel()
			materials := managedLateBudgetMaterials()
			materials[managedLateRemainingMaterial+"post-native-policy"] = managedLatePostNativeCleanupPolicySHA()
			phase, stop, err := beginManagedLatePostNativeCleanupBudget(ctx, materials, r)
			pass := mode == "late-valid" || mode == "ordinary-still-reserves-fifteen"
			if (err == nil) != pass {
				t.Fatalf("retry acceptance differs: %v", err)
			}
			if !pass {
				return
			}
			defer stop()
			if time.Until(r.PostNativeAuthority.Deadline) >= managedLateFinalizerLimit || verifyManagedLateFinalizerBudget(phase) != nil {
				t.Fatal("already admitted local cleanup unnecessarily requires a new fifteen-minute reserve")
			}
			if got, _ := phase.Deadline(); !got.Equal(r.PostNativeAuthority.Deadline) || deploymentplan.Digest(r.PostNativeAuthority) != before {
				t.Fatal("retry moved the recorded clock")
			}
			if mode == "ordinary-still-reserves-fifteen" {
				deadline, _ := phase.Deadline()
				ordinary := managedLateRuntimeBudget{StartedAt: deadline.Add(-managedLateNativeLimit), Deadline: deadline, Overall: ctx}
				ordinaryCtx := context.WithValue(phase, managedLateBudgetKey{}, ordinary)
				if verifyManagedLateFinalizerBudget(ordinaryCtx) == nil {
					t.Fatal("ordinary native finalizer lost its reserve")
				}
			}
		})
	}
}

func TestManagedLatePostNativeRetryDoesNotReplaceFirstAdmissionReserve(t *testing.T) {
	r, a, now := managedLatePostNativeAuditFixture(t)
	rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
	if _, err := newManagedLatePostNativeCleanupAuthority(r, a, now.Add(14*time.Minute), now, rawSHA); err == nil {
		t.Fatal("first admission accepted less than fifteen minutes")
	}
	phase, err := newManagedLatePostNativeCleanupAuthority(r, a, now.Add(15*time.Minute), now, rawSHA)
	if err != nil || !phase.Deadline.Equal(now.Add(15*time.Minute)) {
		t.Fatal("exact first reserve refused", err)
	}
	store, parent, _, _ := terminalWitnessFixture(t)
	raw := mustProviderRecoveryJSON(phase)
	if err := writeManagedLatePostNativeCleanupPair(store, parent, raw); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedLatePostNativeCleanupPair(store, parent, mustProviderRecoveryJSON(deploymentplan.Object{"reset": true})); err == nil {
		t.Fatal("retry replaced an admitted pair")
	}
	got, err := readManagedLatePostNativeCleanupPair(store, parent)
	if err != nil || string(got) != string(raw) {
		t.Fatal("failed retry changed original pair", err)
	}
}

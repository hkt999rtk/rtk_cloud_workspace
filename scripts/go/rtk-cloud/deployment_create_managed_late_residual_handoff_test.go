package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These contexts model cancellation independently of the elapsed phase deadline.
// Production keeps using standard contexts and performs both fence checks.
type managedLateHandoffDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c managedLateHandoffDeadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

type managedLateHandoffClosingContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *managedLateHandoffClosingContext) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

// Every authority is constructed and archived at its own historical time. The
// actual contexts below can therefore expire immediately without wall sleeps.
func managedLateResidualCompletedHandoffFixture(t *testing.T) (environmentCreateBootstrapRuntime, managedLateRuntimeBudget, []byte) {
	t.Helper()
	store, parent, proposalRel, approvalRel, proposal := managedLateResidualApprovalAuditFixture(t)
	base := filepath.Join("deployment/create", parent, "bootstrap/service")
	read := func(rel string, out any) {
		t.Helper()
		if err := createOpenBaoACKCompletionRead(store, rel, out); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, value any) string {
		t.Helper()
		raw := mustProviderRecoveryJSON(value)
		path := filepath.Join(store.Root, rel)
		if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return environmentPreflightBytesSHA256(raw)
	}
	e := &environmentCreateExecution{Store: store}
	read(filepath.Join(base, "managed-late-residual-copy-cleanup-source-journal.json"), &e.Journal)
	read(filepath.Join(base, "managed-late-residual-copy-cleanup-source-owner.json"), &e.owner)
	e.JournalPath, e.Directory = filepath.Join(store.Root, e.owner.Journal), filepath.Dir(filepath.Join(store.Root, e.owner.Journal))
	now := time.Now().UTC().Truncate(time.Second)
	origin := now.Add(-8 * time.Hour)
	e.Journal.AdmittedAt, e.owner.StartedAt = origin.Add(-3*time.Hour), origin.Add(-3*time.Hour)
	for i := range 392 {
		e.Journal.Entries[i].StartedAt = origin.Add(-150 * time.Minute).Add(time.Duration(i) * time.Millisecond)
		e.Journal.Entries[i].CompletedAt = e.Journal.Entries[i].StartedAt.Add(time.Millisecond)
	}
	e.Journal.KubernetesLeases = make([]deploymentplan.LeaseOwner, 3)
	r, seedArtifact, _ := managedLateRemainingNegativeFixture(t)
	r.ParentSHA, r.OwnerSHA, r.SourceLeases = parent, deploymentplan.Digest(e.owner), 0
	r.Ceremony.Root.Environment = "dev"
	r.Ceremony.Intermediate.ID = "99999999-1111-4111-8111-111111111111"
	r.NotBefore, r.LeafNotAfter, r.SessionExpires = origin.Add(-115*time.Minute), origin.Add(7*time.Hour), origin.Add(7*time.Hour)
	r.StaleSeedsSHA = proposal.StaleSeedsSHA
	pending := &e.Journal.Entries[395]
	pending.Key, pending.Phase, pending.State = "service-session-finalize", "bootstrap", "pending"
	pending.StartedAt, pending.CompletedAt, pending.Receipt = origin.Add(40*time.Minute), time.Time{}, nil
	pending.IntentSHA256 = activeTransportJournalDigest(deploymentplan.Object{"session_id": r.SessionID, "issuer_id": r.Ceremony.Intermediate.ID, "cleanup_binding": r.Ceremony.CleanupBinding})
	for i := 392; i < 400; i++ {
		if i == 395 {
			continue
		}
		at := origin.Add(time.Duration(30+i-392) * time.Minute)
		if i >= 396 {
			at = origin.Add(time.Duration(66+i-396) * time.Minute)
		}
		e.Journal.Entries[i].StartedAt, e.Journal.Entries[i].CompletedAt = at, at.Add(time.Second)
	}
	prefix := func(n, leases int) string {
		j := e.Journal
		j.Entries = j.Entries[:n]
		j.KubernetesLeases = j.KubernetesLeases[:leases]
		return deploymentplan.Digest(j)
	}
	r.SourceSHA = prefix(388, 0)
	artifact := func(source string, at time.Time) createFoundationRecoveryArtifact {
		t.Helper()
		var a createFoundationRecoveryArtifact
		if err := json.Unmarshal(mustProviderRecoveryJSON(seedArtifact), &a); err != nil {
			t.Fatal(err)
		}
		a.Plan.ParentSHA, a.Plan.ParentOwnerSHA, a.Plan.ParentJournalSHA = parent, r.OwnerSHA, source
		a.QualifiedAt, a.ValidUntil = at.Add(-time.Minute), at.Add(14*time.Minute)
		for key, value := range managedLateRemainingPins(r) {
			a.Plan.Materials[managedLateRemainingMaterial+key] = value
		}
		managedLateRemainingNegativeBind(t, r, &a)
		return a
	}
	persist := func(a createFoundationRecoveryArtifact) {
		write(filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json"), a)
		write(filepath.Join("deployment/create/openbao-recovery", r.Review, managedLateRemainingFilename), r)
	}
	rawSHA := func(a createFoundationRecoveryArtifact) string {
		return environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
	}
	var err error
	a := artifact(r.SourceSHA, origin.Add(-100*time.Minute))
	r.Authority, err = newManagedLateRemainingAuthority(r, a, origin.Add(-10*time.Minute), origin.Add(-100*time.Minute), rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	write(managedLateRemainingAuthorityPath(parent), r.Authority)
	r.CleanupOnly, r.InstalledAt, r.HelperObservationSHA = true, origin.Add(-80*time.Minute), strings.Repeat("4", 64)
	r.CleanupSourceSHA, r.CurrentSourceSHA = prefix(389, 1), prefix(389, 1)
	a = artifact(r.CurrentSourceSHA, origin.Add(-50*time.Minute))
	r.CleanupAuthority, err = newManagedLateInstalledCleanupAuthority(r, a, origin.Add(40*time.Minute), origin.Add(-50*time.Minute), rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	write(managedLateInstalledCleanupPath(parent), r.CleanupAuthority)
	r.FinalizationOnly, r.LogProofSHA, r.CurrentSourceSHA = true, strings.Repeat("6", 64), prefix(392, 3)
	a = artifact(r.CurrentSourceSHA, origin)
	r.FinalizationAuthority, err = newManagedLateFinalizationAuthority(r, a, origin.Add(90*time.Minute), origin, rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	write(managedLateFinalizationAuthorityPath(parent), r.FinalizationAuthority)
	evidence := map[string]string{}
	for _, key := range []string{"owners", "all-volumes", "clients", "host-servers", "api-servers", "ca-registry", "current-crls", "sealed-session", "revocation", "service-crl", "service-crl-acks", "copies", "key", "csr"} {
		evidence[key] = strings.Repeat("d", 64)
	}
	r.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{Schema: managedLateTerminalFinalizerSchema, SourceSHA: prefix(396, 3), PrefixSHA: r.CurrentSourceSHA, ProofSHA: strings.Repeat("b", 64), PendingSHA: deploymentplan.Digest(*pending), OriginalAuthoritySHA: deploymentplan.Digest(r.FinalizationAuthority), NativeEvidence: evidence}
	a = artifact(r.TerminalFinalizerCompletion.SourceSHA, origin.Add(55*time.Minute))
	r.TerminalAuthority, err = newManagedLateTerminalFinalizerAuthority(r, a, origin.Add(145*time.Minute), origin.Add(55*time.Minute), rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	write(managedLateTerminalFinalizerPath(parent), r.TerminalAuthority)
	write(managedLateTerminalFinalizerAdmissionPath(parent), r.TerminalAuthority)
	native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: r.SessionID, LeafSHA256: r.Fingerprint, CRLSHA256: strings.Repeat("a", 64), CleanupSHA256: strings.Repeat("b", 64), FinalizedAt: origin.Add(65 * time.Minute)}
	r.PostNativeCleanup = &createManagedLatePostNativeCleanup{Schema: managedLatePostNativeCleanupSchema, SourceSHA: prefix(400, 3), TerminalSourceSHA: r.TerminalFinalizerCompletion.SourceSHA, TerminalAuthoritySHA: deploymentplan.Digest(r.TerminalAuthority), CustodyEntrySHA: deploymentplan.Digest(e.Journal.Entries[396]), Native: native, TombstoneSHA: strings.Repeat("c", 64), NativeEvidence: evidence}
	a = artifact(r.PostNativeCleanup.SourceSHA, origin.Add(90*time.Minute))
	r.PostNativeAuthority, err = newManagedLatePostNativeCleanupAuthority(r, a, origin.Add(180*time.Minute), origin.Add(90*time.Minute), rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	proposal.PostSHA = write(managedLatePostNativeCleanupPath(parent, false), r.PostNativeAuthority)
	write(managedLatePostNativeCleanupPath(parent, true), r.PostNativeAuthority)
	proposal.TerminalSHA = write(managedLateTerminalFinalizerPath(parent), r.TerminalAuthority)
	proposal.Source, proposal.CleanupDeadline, proposal.OverallDeadline = r.PostNativeCleanup.SourceSHA, r.PostNativeAuthority.Deadline, r.TerminalAuthority.OverallDeadline
	proposal.JournalRawSHA = write(filepath.Join(base, "managed-late-residual-copy-cleanup-source-journal.json"), e.Journal)
	proposal.OwnerRawSHA = write(filepath.Join(base, "managed-late-residual-copy-cleanup-source-owner.json"), e.owner)
	for _, name := range []string{managedLateRemainingAuthorityFilename, managedLateInstalledCleanupFilename, managedLateFinalizationAuthorityFilename} {
		raw, readErr := os.ReadFile(filepath.Join(store.Root, base, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		proposal.Historical[name] = environmentPreflightBytesSHA256(raw)
	}
	proposal.Remaining = filepath.Join("deployment/create/openbao-recovery", r.PostNativeAuthority.Review, managedLateRemainingFilename)
	remainingRaw, err := os.ReadFile(filepath.Join(store.Root, proposal.Remaining))
	if err != nil {
		t.Fatal(err)
	}
	proposal.RemainingSHA = environmentPreflightBytesSHA256(remainingRaw)
	proposal.FiveACKProof = proposal.Remaining + "#/HistoricalSeedCleanup"
	proposal.CreatedAt = now.Add(-81 * time.Minute)
	proposal.FailureDiagnosticSHA = write(proposal.FailureDiagnostic, map[string]any{"authority": false, "parent_sha256": parent, "failed_report_raw_sha256": proposal.FailureReportSHA, "fixed_cleanup_deadline": proposal.CleanupDeadline.Format(time.RFC3339Nano), "journal_entries": 400})
	proposalSHA := write(proposalRel, proposal)
	write(approvalRel, managedLateResidualApprovalRecord{Schema: "rtk-cloud.residual-copy-cleanup-operator-approval/v1", Environment: "dev", Parent: parent, Source: proposal.Source, Proposal: proposalSHA, Decision: "approved", ApprovedAt: now.Add(-80 * time.Minute), HumanMessage: "approve exact isolated expired-phase handoff fixture", CleanupMinutes: 60, CallerMinutes: 90})
	approval, err := loadManagedLateResidualCleanupApproval(store, parent)
	if err != nil || approval == nil {
		t.Fatal("isolated explicit approval", err)
	}
	r.ResidualCleanup = &createManagedLateResidualCleanup{Approval: approval, PostNativeSHA: deploymentplan.Digest(r.PostNativeCleanup), OldPostNativeAuthoritySHA: deploymentplan.Digest(r.PostNativeAuthority), CredentialMode: "non-expiring-kubernetes-token", CredentialSHA: strings.Repeat("9", 64), NativeEvidence: map[string]string{}}
	for key, value := range evidence {
		r.ResidualCleanup.NativeEvidence[key] = value
	}
	r.ResidualCleanup.NativeEvidence["current-credential"] = managedLateResidualCredentialSHA(r.ResidualCleanup)
	started := now.Add(-70 * time.Minute)
	a = artifact(r.PostNativeCleanup.SourceSHA, started)
	r.ResidualAuthority, err = newManagedLateResidualCleanupAuthority(r, a, now.Add(20*time.Minute), started, rawSHA(a))
	if err != nil {
		t.Fatal(err)
	}
	persist(a)
	write(managedLateResidualCleanupPath(parent, true), r.ResidualAuthority)
	write(managedLateResidualCleanupPath(parent, false), r.ResidualAuthority)
	for i, object := range approval.Objects {
		at := now.Add(-60 * time.Minute).Add(time.Duration(i) * time.Second)
		intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID, Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "absent": true})})
	}
	entry := &e.Journal.Entries[395]
	entry.State, entry.CompletedAt, entry.Receipt = "completed", now.Add(-11*time.Minute), mustProviderRecoveryJSON(deploymentplan.Object{"native_finalization": native, "custody_tombstone_sha256": r.PostNativeCleanup.TombstoneSHA, "copies_absent": true})
	completed := createManagedLateTerminalFinalizerCompleted{Schema: managedLateTerminalFinalizerSchema, ParentSHA: parent, Review: r.TerminalAuthority.Review, AuthoritySHA: deploymentplan.Digest(r.TerminalAuthority), SourceSHA: r.TerminalAuthority.SourceSHA, PendingSHA: r.TerminalFinalizerCompletion.PendingSHA, EntrySHA: deploymentplan.Digest(entry), PostNativeReview: r.PostNativeAuthority.Review, PostNativeAuthoritySHA: deploymentplan.Digest(r.PostNativeAuthority), ResidualReview: r.ResidualAuthority.Review, ResidualAuthoritySHA: deploymentplan.Digest(r.ResidualAuthority), CompletedAt: entry.CompletedAt}
	write(managedLateTerminalFinalizerCompletedPath(parent), completed)
	write(e.owner.Journal, e.Journal)
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	write("deployment/.execution-active/owner.json", e.owner)
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		t.Fatal(err)
	}
	e.running, err = deploymentplan.LockExecutionOwner(fence, true)
	if err != nil {
		t.Fatal(err)
	}
	e.runningInfo, err = e.running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := &createManagedLateSessionRecovery{Review: r.OriginalReview, NewSessionID: r.SessionID}
	if err := validateManagedLateTerminalFinalizerCompletion(store, e, late); err != nil {
		t.Fatal("isolated complete residual proof", err)
	}
	overall, stop := context.WithDeadline(context.Background(), r.ResidualAuthority.OverallDeadline)
	t.Cleanup(stop)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: r.ResidualAuthority.Deadline, Overall: overall, Limit: managedLateResidualCleanupLimit, ResidualAuthority: r.ResidualAuthority}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: parent, ServiceSessionRecovery: &createServiceSessionRecovery{ManagedLateSession: late, ManagedLateRemaining: r, Ceremony: r.Ceremony}}
	return runtime, budget, mustProviderRecoveryJSON(e.Journal)
}

func TestManagedLateResidualExpiredCleanupHandoffRequiresExistingCompletedProof(t *testing.T) {
	runtime, budget, originalJournal := managedLateResidualCompletedHandoffFixture(t)
	e := runtime.Execution
	proofPath := filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256))
	originalProof, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"completed", "qualified-tail-completed", "missing-proof", "changed-proof", "pending", "late-completion", "shorter-child", "cancelled-child", "expired-overall", "overall-cancelled-during-proof"} {
		t.Run(mode, func(t *testing.T) {
			var j environmentCreateJournal
			if err := json.Unmarshal(originalJournal, &j); err != nil {
				t.Fatal(err)
			}
			e.Journal = j
			defer func() {
				e.Journal = environmentCreateJournal{}
				_ = json.Unmarshal(originalJournal, &e.Journal)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(proofPath, originalProof, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			b := budget
			if mode == "pending" {
				e.Journal.Entries[395].State = "pending"
			}
			if mode == "late-completion" {
				e.Journal.Entries[395].CompletedAt = b.Deadline.Add(time.Second)
			}
			if mode == "qualified-tail-completed" {
				at := time.Now().Add(-time.Second)
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/workload", Phase: "kubernetes", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("a", 64), Receipt: []byte(`{"accepted":true}`)})
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-proof" {
				if err := os.Remove(proofPath); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-proof" {
				var proof createManagedLateTerminalFinalizerCompleted
				_ = json.Unmarshal(originalProof, &proof)
				proof.EntrySHA = strings.Repeat("f", 64)
				if err := os.WriteFile(proofPath, mustProviderRecoveryJSON(proof), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired-overall" {
				expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
				b.Overall = expired
			}
			deadline := b.Deadline
			if mode == "shorter-child" {
				deadline = deadline.Add(-time.Second)
			}
			// Establish the expired phase from the genuine overall context before
			// installing the final-check cancellation observer in the budget.
			phase, stop := context.WithDeadline(b.Overall, deadline)
			defer stop()
			var closing *managedLateHandoffClosingContext
			if mode == "overall-cancelled-during-proof" {
				live, cancel := context.WithCancel(b.Overall)
				defer cancel()
				closing = &managedLateHandoffClosingContext{Context: live, cancel: cancel}
				b.Overall = closing
			}
			ctx := context.WithValue(phase, managedLateBudgetKey{}, b)
			if mode == "cancelled-child" {
				cancelled, cancel := context.WithCancel(context.WithValue(b.Overall, managedLateBudgetKey{}, b))
				cancel()
				ctx = managedLateHandoffDeadlineContext{Context: cancelled, deadline: b.Deadline}
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			proofBefore, _ := os.ReadFile(proofPath)
			got, err := managedLateContextAfterFinalization(ctx, runtime)
			pass := mode == "completed" || mode == "qualified-tail-completed"
			if (err == nil) != pass || pass && got != b.Overall {
				t.Fatal("expired phase handoff acceptance differs", err)
			}
			if mode == "cancelled-child" && !errors.Is(err, context.Canceled) {
				t.Fatal("exact-deadline explicit cancellation did not reach cancellation guard", err)
			}
			if closing != nil && (closing.checks != 3 || !errors.Is(err, context.Canceled)) {
				t.Fatal("closing overall fence did not reject cancellation after durable proof", closing.checks, err)
			}
			after, _ := os.ReadFile(e.JournalPath)
			proofAfter, _ := os.ReadFile(proofPath)
			if !bytes.Equal(before, after) || !bytes.Equal(proofBefore, proofAfter) {
				t.Fatal("read-only expired handoff wrote parent or completion proof")
			}
			if mode == "missing-proof" {
				if _, err := os.Lstat(proofPath); !os.IsNotExist(err) {
					t.Fatal("missing proof regenerated")
				}
			}
		})
	}
}

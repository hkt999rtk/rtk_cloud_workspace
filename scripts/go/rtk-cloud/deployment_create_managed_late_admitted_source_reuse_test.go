package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This fixture proves only the fresh admission envelope and cache mechanics.
// It deliberately lacks real historical ancestry: full phase validation must
// refuse it rather than treating a structurally valid envelope as authority.
func managedLateAdmittedReuseFixture(t *testing.T) (secretStore, *environmentCreateExecution, deploymentConfig, *createManagedLateSessionRecovery, environmentCreateJournal) {
	t.Helper()
	store, e, cfg, paths, _ := managedLateSourceWriteFixture(t)
	invalidateManagedLateSourceReuse(e)
	e.owner.Schema = environmentCreateOwnerSchema
	e.Journal.Schema = environmentCreateJournalSchema
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	late := &createManagedLateSessionRecovery{ParentSHA: source.PlanSHA256, SourceSHA: deploymentplan.Digest(source), SourceOwnerSHA: deploymentplan.Digest(e.owner), Mode: managedLateSessionACKReady}
	late.ExpiredRenewal = &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema, ParentSHA: late.ParentSHA, SourceSHA: late.SourceSHA, SourceOwnerSHA: late.SourceOwnerSHA, PriorLate: &createManagedLateSessionRecovery{OpenBaoTLSRepair: &createOpenBaoTLSRootCRLArtifact{Plan: createOpenBaoTLSRootCRLPlan{Configuration: cfg}}}}
	now := time.Now().UTC()
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: late.ParentSHA, ParentOwnerSHA: late.SourceOwnerSHA, ParentJournalSHA: late.SourceSHA, CheckerSHA: strings.Repeat("1", 64), Materials: map[string]string{"accepted-service-core-late-session-source-descriptor": managedLateSessionSourceDigest(late)}}, QualifiedAt: now.Add(-2 * time.Minute), ValidUntil: now.Add(13 * time.Minute)}
	late.Review = deploymentplan.Digest(a.Plan)
	late.ExpiredRenewal.Review = late.Review
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: late.Review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: late.Review})
	}
	entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + late.Review, Phase: "recovery", State: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now.Add(-30 * time.Second), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": late.SourceSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review, "audit_sha256": strings.Repeat("2", 64)})}
	e.Journal.Entries = append(slices.Clone(source.Entries), entry)
	base := filepath.Join("deployment/create/openbao-recovery", late.Review)
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": source, "source-owner.json": e.owner, "managed-late-session-source.json": late} {
		managedLateSourceReuseNegativeWrite(t, store, filepath.Join(base, name), mustProviderRecoveryJSON(value))
	}
	managedLateSourceReuseNegativeWrite(t, store, paths["owner"], mustProviderRecoveryJSON(e.owner))
	managedLateSourceReuseNegativeWrite(t, store, paths["source"], mustProviderRecoveryJSON(e.Journal))
	if !managedLateAdmissionMatches(a, source, entry, late) || e.Check(context.Background()) != nil {
		t.Fatal("isolated completed375 admission envelope refused")
	}
	return store, e, cfg, late, source
}

func TestManagedLateAdmittedSourceReuseNineHistoricalReadsDeriveOncePerPhase(t *testing.T) {
	store, e, cfg, late, source := managedLateAdmittedReuseFixture(t)
	cap, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
	if err != nil {
		t.Fatal(err)
	}
	if cap.matches(store, e) {
		t.Fatal("provisional capture granted unvalidated actual375 authority")
	}
	pure := *e
	pure.Journal, pure.lateSourceReuse = source, cap
	derivations, freshReads := 0, 0
	read := func() {
		var history map[string]string
		hit, err := readManagedLateSourceReuse(store, &pure, cfg, "isolated-successful-expired-history", &history)
		if err != nil {
			t.Fatal(err)
		}
		if !hit {
			derivations++
			history = map[string]string{"validated-pure-history": "original374"}
			if err := rememberManagedLateSourceReuse(store, &pure, cfg, "isolated-successful-expired-history", history); err != nil {
				t.Fatal(err)
			}
		}
		history["validated-pure-history"] = "caller-only-mutation"
		freshReads++ // runtime/child/native boundaries remain outside this memo
	}
	for range 9 {
		read()
	}
	if derivations != 1 || freshReads != 9 {
		t.Fatalf("nine pre-seed pure source reads derived %d times; fresh boundaries %d", derivations, freshReads)
	}
	e.lateSourceReuse = cap
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if managedLateSourceReuseActive(&pure) {
		t.Fatal("parent save left shared old-phase authority active")
	}
	next, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
	if err != nil || next == cap {
		t.Fatal("new phase did not capture fresh current bytes", err)
	}
	pure.lateSourceReuse = next
	read()
	if derivations != 2 {
		t.Fatal("new phase reused a pre-mutation historical result")
	}
}

func TestManagedLateAdmittedSourceReuseRefusesDriftAndUnknownCurrentProgress(t *testing.T) {
	for _, mode := range []string{"pending-admission", "wrong-review", "changed-receipt", "changed-owner", "changed-prefix", "unknown-suffix", "missing-source", "foreign-store", "missing-history-link"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, late, _ := managedLateAdmittedReuseFixture(t)
			selected := store
			switch mode {
			case "pending-admission":
				e.Journal.Entries[374].State = "pending"
			case "wrong-review":
				late.Review = strings.Repeat("3", 64)
			case "changed-receipt":
				e.Journal.Entries[374].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review, "audit_sha256": strings.Repeat("2", 64), "extra": "unknown"})
			case "changed-owner":
				e.owner.StartedAt = time.Now().UTC()
			case "changed-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("3", 64)
			case "unknown-suffix":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unknown-native-outcome", Phase: "bootstrap", State: "pending", StartedAt: time.Now().UTC()})
			case "missing-source":
				late.SourceSHA = strings.Repeat("3", 64)
			case "foreign-store":
				selected.Environment = "staging"
			case "missing-history-link":
				late.ExpiredRenewal.PriorLate = nil
			}
			// These fixture-only writes mirror the changed current snapshot so
			// rejection must still come from exact authority validation.
			managedLateSourceReuseNegativeWrite(t, store, "deployment/.execution-active/owner.json", mustProviderRecoveryJSON(e.owner))
			managedLateSourceReuseNegativeWrite(t, store, e.owner.Journal, mustProviderRecoveryJSON(e.Journal))
			if _, err := captureManagedLateAdmittedSourceReuse(context.Background(), selected, e, late); err == nil {
				t.Fatal("changed or unknown admitted source acquired even provisional reuse", mode)
			}
		})
	}
}

func TestManagedLateAdmittedSourceReuseRequiresFullHistoryBeforeCurrentAuthority(t *testing.T) {
	store, e, _, late, _ := managedLateAdmittedReuseFixture(t)
	before := deploymentplan.Digest(e.Journal)
	if err := beginManagedLateAdmittedSourcePhase(context.Background(), store, e, late); err == nil || managedLateSourceReuseActive(e) {
		t.Fatal("synthetic admission without genuine App/Core/CA history became current authority")
	}
	if deploymentplan.Digest(e.Journal) != before || e.Check(context.Background()) != nil {
		t.Fatal("failed full history validation rewrote the held current parent")
	}
}

func TestManagedLateAdmittedSourceReuseMatchesOnlyExactCommittedSnapshot(t *testing.T) {
	store, e, _, late, source := managedLateAdmittedReuseFixture(t)
	cap, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the post-validation representation in isolation. The production
	// constructor sets these fields only after full RuntimeProgress succeeds.
	cap.currentSHA = deploymentplan.Digest(e.Journal)
	cap.admissionReview, cap.descriptorSHA = late.Review, managedLateSessionSourceDigest(late)
	if !cap.matches(store, e) {
		t.Fatal("exact admitted snapshot refused after commit")
	}
	for _, length := range []int{370, 374} {
		pure := *e
		pure.Journal = source
		pure.Journal.Entries = slices.Clone(source.Entries[:length])
		if !cap.matches(store, &pure) {
			t.Fatal("authenticated historical view lost after admitted commit", length)
		}
	}
	for _, mode := range []string{"extra-entry", "changed-admission", "lease", "parent", "owner", "completed"} {
		t.Run(mode, func(t *testing.T) {
			changed := *e
			changed.Journal = e.Journal
			changed.Journal.Entries = slices.Clone(e.Journal.Entries)
			switch mode {
			case "extra-entry":
				changed.Journal.Entries = append(changed.Journal.Entries, environmentCreateJournalEntry{Key: "unknown"})
			case "changed-admission":
				changed.Journal.Entries[374].IntentSHA256 = strings.Repeat("3", 64)
			case "lease":
				changed.Journal.KubernetesLeases = append(slices.Clone(changed.Journal.KubernetesLeases), deploymentplan.LeaseOwner{UID: "foreign"})
			case "parent":
				changed.Journal.OwnerToken = "foreign"
			case "owner":
				changed.owner.OwnerToken = "foreign"
			case "completed":
				changed.Journal.CompletedAt = time.Now().UTC()
			}
			if cap.matches(store, &changed) {
				t.Fatal("different current progress inherited a committed source phase", mode)
			}
		})
	}
	e.lateSourceReuse = cap
	managedLateSourceReuseNegativeWrite(t, store, "deployment/rollback/"+strings.Repeat("4", 64)+"/journal.json", []byte(`{"mutable-child":"changed"}`))
	if err := checkManagedLateSourceReuse(e); err == nil || managedLateSourceReuseActive(e) {
		t.Fatal("fresh child-journal addition escaped the phase input manifest")
	}
}

func TestManagedLateAdmittedSourceReuseEndsBeforeFinalizationReaders(t *testing.T) {
	store, e, _, late, _ := managedLateAdmittedReuseFixture(t)
	cap, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
	if err != nil {
		t.Fatal(err)
	}
	e.lateSourceReuse = cap
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-host-ack-verified-" + late.OriginalRepairReview})
	// This grants no finalization authority. It merely drops the optimization;
	// those callers retain their original independent bounded proof readers.
	if err := beginManagedLateAdmittedSourcePhase(context.Background(), store, e, late); err != nil || managedLateSourceReuseActive(e) {
		t.Fatal("pre-finalization scope was extended into later completion history", err)
	}
}

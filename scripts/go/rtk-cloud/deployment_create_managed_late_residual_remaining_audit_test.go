package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualRemainingAuditFixture(t *testing.T) (environmentCreateBootstrapRuntime, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	runtime, _, _ := managedLateResidualCompletedHandoffFixture(t)
	e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
	e.Journal.Entries = append([]environmentCreateJournalEntry(nil), e.Journal.Entries[:403]...)
	e.Journal.Entries[395].State, e.Journal.Entries[395].CompletedAt, e.Journal.Entries[395].Receipt = "pending", time.Time{}, nil
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	old := r.ResidualAuthority
	now := time.Now().UTC().Truncate(time.Second)
	approval := &createManagedLateResidualRemainingApproval{
		ProposalSHA: strings.Repeat("1", 64), ApprovalSHA: strings.Repeat("2", 64), ParentSHA: r.ParentSHA,
		SourceSHA: deploymentplan.Digest(e.Journal), SourceRawSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(e.Journal)),
		OriginalSourceSHA: r.ResidualCleanup.Approval.SourceSHA, OwnerSHA: r.OwnerSHA,
		OriginalAuthoritySHA: deploymentplan.Digest(old), OldAuthorityRawSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(old)),
		CompletedSHA: deploymentplan.Digest(e.Journal.Entries[400:403]), CleanupLimit: 60 * time.Minute, OverallLimit: 90 * time.Minute, ApprovedAt: now.Add(-4 * time.Minute),
		Completed: slices.Clone(r.ResidualCleanup.Approval.Objects[:3]), Objects: slices.Clone(r.ResidualCleanup.Approval.Objects[3:]),
	}
	slices.SortFunc(approval.Objects, func(a, b createManagedLateResidualCleanupObject) int {
		return strings.Compare(a.ResourceID, b.ResourceID)
	})
	approval.InventorySHA = deploymentplan.Digest(approval.Objects)
	r.ResidualRemaining = &createManagedLateResidualRemaining{Approval: approval, OriginalAuthoritySHA: deploymentplan.Digest(old), NativeEvidence: map[string]string{}}
	for key, value := range r.ResidualCleanup.NativeEvidence {
		r.ResidualRemaining.NativeEvidence[key] = value
	}
	var a createFoundationRecoveryArtifact
	if err := createOpenBaoACKCompletionRead(e.Store, filepath.Join("deployment/create/openbao-recovery", old.Review, "qualification.json"), &a); err != nil {
		t.Fatal(err)
	}
	a.Plan.ParentJournalSHA, a.Plan.CheckerSHA = approval.SourceSHA, strings.Repeat("f", 64)
	a.QualifiedAt, a.ValidUntil = now.Add(-3*time.Minute), now.Add(12*time.Minute)
	a.Plan.Materials[managedLateRemainingMaterial+"residual-remaining"] = deploymentplan.Digest(r.ResidualRemaining)
	a.Plan.Materials[managedLateRemainingMaterial+"residual-remaining-policy"] = managedLateResidualRemainingPolicySHA()
	managedLateRemainingNegativeBind(t, r, &a)
	started := now.Add(-2 * time.Minute)
	phase, err := newManagedLateResidualRemainingAuthority(r, a, started.Add(90*time.Minute), started, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal("new21 phase did not preserve the expired original grant", err)
	}
	r.ResidualRemainingAuthority = phase
	if !now.After(old.Deadline) || !now.After(r.LeafNotAfter) || !managedLateResidualRemainingProgressValid(e.Journal, r) {
		t.Fatal("fixture lacks expired historical grant/leaf and genuine source403")
	}
	return runtime, a, started
}

func managedLateResidualRemainingAuditCompleted(t *testing.T, runtime environmentCreateBootstrapRuntime) environmentCreateJournal {
	t.Helper()
	r := runtime.ServiceSessionRecovery.ManagedLateRemaining
	j := runtime.Execution.Journal
	j.Entries = append([]environmentCreateJournalEntry(nil), j.Entries...)
	for i, object := range r.ResidualRemaining.Approval.Objects {
		at := r.ResidualRemainingAuthority.StartedAt.Add(time.Duration(i+1) * time.Second)
		intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
		j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID, Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "absent": true})})
	}
	return j
}

func TestManagedLateResidualRemainingAuditMixedOldThreeNewTwentyOne(t *testing.T) {
	runtime, _, _ := managedLateResidualRemainingAuditFixture(t)
	r := runtime.ServiceSessionRecovery.ManagedLateRemaining
	completed := managedLateResidualRemainingAuditCompleted(t, runtime)
	if !managedLateResidualRemainingRetirementsComplete(completed, r) || managedLateResidualRetirementsComplete(completed, r.ResidualCleanup.Approval, r.ResidualAuthority.StartedAt, r.ResidualAuthority.Deadline) {
		t.Fatal("mixed grant completion lost distinct old3/new21 windows")
	}
	oldRaw := mustProviderRecoveryJSON(r.ResidualAuthority)
	for _, mode := range []string{"genuine", "completed-parent-normal-tail", "changed-captured-lease", "missing21", "foreign-uid", "foreign-resource", "pending", "future-within-grant", "new-before-admission", "new-after-deadline", "replayed-old-copy", "duplicate-new", "old-after-original-deadline", "old-intent-drift", "wrong-source403", "wrong-completed3", "missing-new-authority"} {
		t.Run(mode, func(t *testing.T) {
			var j environmentCreateJournal
			if err := json.Unmarshal(mustProviderRecoveryJSON(completed), &j); err != nil {
				t.Fatal(err)
			}
			copy := *r
			p := *r.ResidualRemaining
			approval := *p.Approval
			p.Approval, copy.ResidualRemaining = &approval, &p
			entry := &j.Entries[403]
			switch mode {
			case "completed-parent-normal-tail":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "kubernetes/workload", Phase: "kubernetes", State: "completed", StartedAt: time.Now().UTC().Add(-time.Second), CompletedAt: time.Now().UTC(), Receipt: []byte(`{"accepted":true}`)})
				j.KubernetesLeases = append(j.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/isolated/rtk-deployment-execution", UID: "22222222-2222-4222-8222-222222222222", Holder: "genuine-later-workload"})
				j.CompletedAt = time.Now().UTC()
			case "changed-captured-lease":
				j.KubernetesLeases[0].UID = "33333333-3333-4333-8333-333333333333"
			case "missing21":
				j.Entries = j.Entries[:423]
			case "foreign-uid", "foreign-resource":
				var receipt deploymentplan.Object
				_ = json.Unmarshal(entry.Receipt, &receipt)
				if mode == "foreign-uid" {
					receipt["uid"] = approval.Completed[0].UID
				} else {
					receipt["resource_id"] = approval.Completed[0].ResourceID
				}
				entry.Receipt = mustProviderRecoveryJSON(receipt)
			case "pending":
				entry.State, entry.CompletedAt, entry.Receipt = "pending", time.Time{}, nil
			case "future-within-grant":
				entry.CompletedAt = time.Now().UTC().Add(time.Minute)
			case "new-before-admission":
				entry.StartedAt = copy.ResidualRemainingAuthority.StartedAt.Add(-time.Nanosecond)
			case "new-after-deadline":
				entry.CompletedAt = copy.ResidualRemainingAuthority.Deadline.Add(time.Nanosecond)
			case "replayed-old-copy":
				j.Entries[403] = j.Entries[400]
			case "duplicate-new":
				j.Entries[404] = j.Entries[403]
			case "old-after-original-deadline":
				j.Entries[400].CompletedAt = copy.ResidualAuthority.Deadline.Add(time.Second)
				prefix := j
				prefix.Entries = prefix.Entries[:403]
				approval.SourceSHA, approval.CompletedSHA = deploymentplan.Digest(prefix), deploymentplan.Digest(j.Entries[400:403])
			case "old-intent-drift":
				j.Entries[400].IntentSHA256 = strings.Repeat("a", 64)
			case "wrong-source403":
				approval.SourceSHA = strings.Repeat("b", 64)
			case "wrong-completed3":
				approval.CompletedSHA = strings.Repeat("c", 64)
			case "missing-new-authority":
				copy.ResidualRemainingAuthority = nil
			}
			if managedLateResidualRemainingRetirementsComplete(j, &copy) != (mode == "genuine" || mode == "completed-parent-normal-tail") {
				t.Fatal("mixed receipt windows or exact UID/source partition were waived")
			}
		})
	}
	if !bytes.Equal(oldRaw, mustProviderRecoveryJSON(r.ResidualAuthority)) {
		t.Fatal("new21 proof rewrote old24 authority")
	}
}

func TestManagedLateResidualRemainingAuditClockAndOldWire(t *testing.T) {
	runtime, a, started := managedLateResidualRemainingAuditFixture(t)
	r := runtime.ServiceSessionRecovery.ManagedLateRemaining
	old := mustProviderRecoveryJSON(r.ResidualAuthority)
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(old, &legacy); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"original_source_sha256", "prior_authority_sha256", "completed_sha256"} {
		if _, ok := legacy[key]; ok {
			t.Fatal("new omitted authority field changed original raw bytes", key)
		}
	}
	var decoded createManagedLateResidualCleanupAuthority
	if json.Unmarshal(old, &decoded) != nil || !bytes.Equal(old, mustProviderRecoveryJSON(decoded)) {
		t.Fatal("old authority no longer roundtrips byte-identically")
	}
	phase := r.ResidualRemainingAuthority
	if !phase.Deadline.Equal(started.Add(60*time.Minute)) || !phase.OverallDeadline.Equal(started.Add(90*time.Minute)) || phase.SourceSHA == r.ResidualAuthority.SourceSHA || phase.PriorAuthoritySHA != deploymentplan.Digest(r.ResidualAuthority) {
		t.Fatal("new grant reused old clock/source or lost prior authority")
	}
	before := managedLateRemainingDigest(r)
	copy := *r
	copy.ResidualRemaining, copy.ResidualRemainingAuthority = nil, nil
	if before != managedLateRemainingDigest(&copy) {
		t.Fatal("new optional phase changed immutable Remaining descriptor digest")
	}
	for _, mode := range []string{"genuine", "caller-cap", "short-first-caller", "caller-over90", "missing-raw-go", "wrong-policy", "wrong-old-authority", "replayed-completed-object", "foreign-native-credential"} {
		t.Run(mode, func(t *testing.T) {
			current := *r
			p := *r.ResidualRemaining
			approval := *p.Approval
			approval.Objects = slices.Clone(approval.Objects)
			p.Approval, current.ResidualRemaining = &approval, &p
			var artifact createFoundationRecoveryArtifact
			_ = json.Unmarshal(mustProviderRecoveryJSON(a), &artifact)
			overall, rawSHA := started.Add(90*time.Minute), environmentPreflightBytesSHA256(mustProviderRecoveryJSON(artifact))
			switch mode {
			case "caller-cap":
				overall = started.Add(20 * time.Minute)
			case "short-first-caller":
				overall = started.Add(15*time.Minute - time.Nanosecond)
			case "caller-over90":
				overall = started.Add(90*time.Minute + time.Nanosecond)
			case "missing-raw-go":
				rawSHA = ""
			case "wrong-policy":
				artifact.Plan.Materials[managedLateRemainingMaterial+"residual-remaining-policy"] = strings.Repeat("a", 64)
			case "wrong-old-authority":
				p.OriginalAuthoritySHA = strings.Repeat("a", 64)
			case "replayed-completed-object":
				approval.Objects[0] = approval.Completed[0]
				approval.InventorySHA = deploymentplan.Digest(approval.Objects)
			case "foreign-native-credential":
				p.NativeEvidence = map[string]string{}
				for k, v := range r.ResidualRemaining.NativeEvidence {
					p.NativeEvidence[k] = v
				}
				p.NativeEvidence["current-credential"] = strings.Repeat("a", 64)
			}
			artifact.Plan.Materials[managedLateRemainingMaterial+"residual-remaining"] = deploymentplan.Digest(&p)
			managedLateRemainingNegativeBind(t, &current, &artifact)
			got, err := newManagedLateResidualRemainingAuthority(&current, artifact, overall, started, rawSHA)
			pass := mode == "genuine" || mode == "caller-cap"
			if (err == nil) != pass {
				t.Fatal("new grant clock/provenance guard differed", err)
			}
			if pass && (got.Deadline.After(phase.Deadline) || !got.StartedAt.Equal(started)) {
				t.Fatal("retry moved admission epoch or extended immutable maximum")
			}
		})
	}
	if !bytes.Equal(old, mustProviderRecoveryJSON(r.ResidualAuthority)) {
		t.Fatal("constructor modified old pair")
	}
	// Reflection pins all newly serialized optional fields without duplicating
	// the legacy wire definition by hand.
	typ := reflect.TypeOf(*r)
	for _, name := range []string{"ResidualRemaining", "ResidualRemainingAuthority"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatal("new phase fixture lost optional field", name)
		}
	}
}

func TestManagedLateResidualRemainingAuditPairNeverResetsPriorGrant(t *testing.T) {
	runtime, _, _ := managedLateResidualRemainingAuditFixture(t)
	r, store := runtime.ServiceSessionRecovery.ManagedLateRemaining, runtime.Execution.Store
	old, err := readManagedLateResidualCleanupPair(store, r.ParentSHA)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustProviderRecoveryJSON(r.ResidualRemainingAuthority)
	if err := writeManagedLateResidualRemainingPair(store, r.ParentSHA, raw); err != nil {
		t.Fatal(err)
	}
	got, err := readManagedLateResidualRemainingPair(store, r.ParentSHA)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("new pair readback", err)
	}
	if writeManagedLateResidualRemainingPair(store, r.ParentSHA, raw) == nil {
		t.Fatal("admitted pair was recreated")
	}
	for _, witness := range []bool{true, false} {
		path := filepath.Join(store.Root, managedLateResidualRemainingPath(r.ParentSHA, witness))
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := readManagedLateResidualRemainingPair(store, r.ParentSHA); err == nil {
			t.Fatal("orphan remaining grant accepted")
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := readManagedLateResidualCleanupPair(store, r.ParentSHA)
	if err != nil || !bytes.Equal(old, after) {
		t.Fatal("new pair rewrote prior grant", err)
	}
	if err := runtime.Execution.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLateResidualRemainingAuditDurableCompletionLinksBothWindows(t *testing.T) {
	runtime, _, _ := managedLateResidualRemainingAuditFixture(t)
	e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
	oldRaw, err := readManagedLateResidualCleanupPair(e.Store, r.ParentSHA)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal = managedLateResidualRemainingAuditCompleted(t, runtime)
	entry := &e.Journal.Entries[395]
	entry.State = "completed"
	entry.CompletedAt = r.ResidualRemainingAuthority.StartedAt.Add(time.Minute)
	entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"native_finalization": r.PostNativeCleanup.Native, "custody_tombstone_sha256": r.PostNativeCleanup.TombstoneSHA, "copies_absent": true})
	if !r.PostNativeCleanup.Native.FinalizedAt.Before(r.ResidualAuthority.StartedAt) || !entry.CompletedAt.After(r.ResidualAuthority.Deadline) {
		t.Fatal("fixture did not separate historical native finalization, old3 and new local completion")
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.Store.Root, managedLateTerminalFinalizerCompletedPath(r.ParentSHA))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := completeManagedLateTerminalFinalizer(context.Background(), runtime); err != nil {
		t.Fatal("genuine mixed grant completion was refused", err)
	}
	var proof createManagedLateTerminalFinalizerCompleted
	if err := createOpenBaoACKCompletionRead(e.Store, managedLateTerminalFinalizerCompletedPath(r.ParentSHA), &proof); err != nil {
		t.Fatal(err)
	}
	if proof.ResidualReview != r.ResidualAuthority.Review || proof.ResidualAuthoritySHA != deploymentplan.Digest(r.ResidualAuthority) || proof.ResidualRemainingReview != r.ResidualRemainingAuthority.Review || proof.ResidualRemainingAuthoritySHA != deploymentplan.Digest(r.ResidualRemainingAuthority) || proof.PendingSHA != r.TerminalFinalizerCompletion.PendingSHA || proof.EntrySHA != deploymentplan.Digest(entry) || !proof.CompletedAt.Equal(entry.CompletedAt) {
		t.Fatal("durable proof replaced old grant or omitted distinct new21 authority")
	}
	for _, mode := range []string{"genuine", "old-authority-substitution", "missing-new-link", "changed-new-link"} {
		copy := proof
		switch mode {
		case "old-authority-substitution":
			copy.ResidualRemainingReview = copy.ResidualReview
			copy.ResidualRemainingAuthoritySHA = copy.ResidualAuthoritySHA
		case "missing-new-link":
			copy.ResidualRemainingReview = ""
			copy.ResidualRemainingAuthoritySHA = ""
		case "changed-new-link":
			copy.ResidualRemainingAuthoritySHA = strings.Repeat("a", 64)
		}
		if managedLateResidualCompletedBudgetMatches(copy, r.ResidualRemainingAuthority) != (mode == "genuine") {
			t.Fatal("handoff accepted wrong durable remaining21 authority", mode)
		}
	}
	after, err := readManagedLateResidualCleanupPair(e.Store, r.ParentSHA)
	if err != nil || !bytes.Equal(oldRaw, after) {
		t.Fatal("durable completion changed original authority bytes", err)
	}
}

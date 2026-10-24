package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func rootCRLInstalledPartialFixture() (environmentCreateJournal, createRootCRLRecoveryArtifact, environmentCreateJournal, environmentCreateJournal) {
	final, original, originalSource := rootCRLTailFixture()
	originalSource.PlanSHA256, originalSource.OwnerToken = original.Plan.Parent, strings.Repeat("a", 64)
	final.PlanSHA256, final.OwnerToken = originalSource.PlanSHA256, originalSource.OwnerToken
	partial := final
	partial.Entries = append([]environmentCreateJournalEntry(nil), final.Entries[:len(originalSource.Entries)+4]...)
	for _, index := range []int{len(originalSource.Entries), len(originalSource.Entries) + 3} {
		partial.Entries[index].State, partial.Entries[index].CompletedAt, partial.Entries[index].Receipt = "pending", time.Time{}, nil
	}
	return partial, original, originalSource, final
}

func TestOriginalRootCRLInstalledSourcePinsPartialAndCompleteOriginalTail(t *testing.T) {
	for _, mode := range []string{"partial", "completed-controller", "all-complete-missing-pointer", "missing-import", "wrong-secret", "wrong-state-intent", "unknown-tail", "changed-source"} {
		t.Run(mode, func(t *testing.T) {
			partial, original, source, final := rootCRLInstalledPartialFixture()
			switch mode {
			case "completed-controller":
				partial.Entries[len(source.Entries)+3] = final.Entries[len(source.Entries)+3]
			case "all-complete-missing-pointer":
				partial = final
			case "missing-import":
				partial.Entries[len(source.Entries)+1].State = "pending"
			case "wrong-secret":
				partial.Entries[len(source.Entries)+2].Receipt = mustProviderRecoveryJSON(map[string]string{"uid": "replacement"})
			case "wrong-state-intent":
				partial.Entries[len(source.Entries)+3].IntentSHA256 = strings.Repeat("f", 64)
			case "unknown-tail":
				partial.Entries = append(partial.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending"})
			case "changed-source":
				partial.Entries[0].IntentSHA256 += "changed"
			}
			want := mode == "partial" || mode == "completed-controller" || mode == "all-complete-missing-pointer"
			if rootCRLInstalledSourceValid(partial, original, source) != want {
				t.Fatal("unsafe installed source", mode)
			}
		})
	}
}

func TestOriginalRootCRLInstalledRetryAcceptsOnlyExactMonotonicProgress(t *testing.T) {
	for _, mode := range []string{"unchanged", "controller-complete", "all-complete-before-sidecar", "qualified-lease", "foreign-lease", "rewrite-completed", "extra-stage", "changed-owner", "changed-intent"} {
		t.Run(mode, func(t *testing.T) {
			partial, original, originalSource, final := rootCRLInstalledPartialFixture()
			current := partial
			current.Entries = append([]environmentCreateJournalEntry(nil), partial.Entries...)
			review := strings.Repeat("9", 64)
			switch mode {
			case "controller-complete":
				current.Entries[len(originalSource.Entries)+3] = final.Entries[len(originalSource.Entries)+3]
			case "all-complete-before-sidecar":
				current = final
			case "qualified-lease", "foreign-lease":
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: partial.Environment, Stack: partial.Stack, Operation: "scoped", BindingsSHA256: review}}
				holder := plan.SHA256() + ":native-nonce"
				if mode == "foreign-lease" {
					holder = "foreign"
				}
				current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + partial.Stack + "-video-cloud/rtk-deployment-execution", UID: "new-native-uid", Holder: holder})
			case "rewrite-completed":
				current.Entries[len(originalSource.Entries)+1].CompletedAt = time.Now()
			case "extra-stage":
				current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending"})
			case "changed-owner":
				current.OwnerToken = "foreign"
			case "changed-intent":
				current.Entries[len(originalSource.Entries)+3].IntentSHA256 = strings.Repeat("f", 64)
			}
			want := mode == "unchanged" || mode == "controller-complete" || mode == "all-complete-before-sidecar" || mode == "qualified-lease"
			if rootCRLInstalledProgressValid(partial, current, original, originalSource, review) != want {
				t.Fatal("unsafe installed retry", mode)
			}
		})
	}
}

func TestOriginalRootCRLInstalledStateRequiresActualFreshSignedRecord(t *testing.T) {
	for _, mode := range []string{"genuine", "expired-prior", "wrong-authority", "wrong-digest", "wrong-number", "terminal", "retained"} {
		t.Run(mode, func(t *testing.T) {
			root, key, snapshot, now := rootCRLFixture(t)
			request, err := buildCreateRootCRLRequest(root, snapshot, now)
			if err != nil {
				t.Fatal(err)
			}
			signed := signedRootCRLFixture(t, root, key, request)
			state := map[string]any{"issuer_fingerprint": root.RootFingerprint, "crl": signed}
			switch mode {
			case "expired-prior":
				state["crl"] = snapshot.Prior
			case "wrong-authority":
				state["issuer_fingerprint"] = strings.Repeat("f", 64)
			case "wrong-digest":
				corrupt := signed
				corrupt.Digest = strings.Repeat("f", 64)
				state["crl"] = corrupt
			case "wrong-number":
				corrupt := signed
				corrupt.Number = "999"
				state["crl"] = corrupt
			case "terminal":
				state["terminal_issuer_id"] = root.ID
			case "retained":
				state["retained_crl"] = snapshot.Prior
			}
			if err := validateRootCRLInstalledState(mustProviderRecoveryJSON(state), root, signed); (err == nil) != (mode == "genuine") {
				t.Fatal("unsafe installed signed evidence", mode, err)
			}
		})
	}
}

func rootCRLInstalledCompletionFixture(t *testing.T, mode string) (secretStore, *environmentCreateExecution, createRootCRLRecoveryArtifact, environmentCreateJournal, createRootCRLRecoveryProof) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	partial, original, originalSource, final := rootCRLInstalledPartialFixture()
	e := &environmentCreateExecution{Journal: final}
	old := deploymentplan.Digest(original.Plan)
	plan := createRootCRLInstalledPlan{Schema: createRootCRLInstalledSchema, Parent: original.Plan.Parent, OriginalReview: old, OriginalArtifactSHA: deploymentplan.Digest(original), SourceJournalSHA: deploymentplan.Digest(partial), OwnerSHA: deploymentplan.Digest(e.owner), CheckerSHA: strings.Repeat("c", 64), Evidence: map[string]string{"actual-native-signed-record": original.Plan.Signed.Digest}}
	review := deploymentplan.Digest(plan)
	now := time.Now().UTC().Add(-time.Minute)
	a := createRootCRLInstalledArtifact{Schema: createRootCRLInstalledSchema + "/artifact", Plan: plan, Qualification: deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	for _, id := range createRootCRLInstalledGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: original.Plan.Parent, Review: old, File: original.Plan.Candidate + "/signed/revocations.pem", Digest: original.Plan.Signed.Digest, Completed: true}
	end := len(originalSource.Entries) + 5
	completed := createRootCRLInstalledCompletion{Schema: createRootCRLInstalledSchema + "/completion", Review: review, OriginalReview: old, FinalEntriesSHA: deploymentplan.Digest(final.Entries[:end]), ProofSHA: activeTransportJournalDigest(proof), EvidenceSHA: deploymentplan.Digest(plan.Evidence), StartedAt: now.Add(time.Second), CompletedAt: now.Add(2 * time.Second), RuntimeReloadRequired: true}
	if !environmentPreflightDigest.MatchString(completed.FinalEntriesSHA) {
		t.Fatal("final journal prefix must have real SHA256")
	}
	switch mode {
	case "empty-prefix":
		completed.FinalEntriesSHA = ""
	case "wrong-prefix":
		completed.FinalEntriesSHA = strings.Repeat("f", 64)
	case "wrong-evidence":
		completed.EvidenceSHA = strings.Repeat("f", 64)
	case "wrong-proof":
		completed.ProofSHA = strings.Repeat("f", 64)
	case "expired-admission":
		completed.StartedAt = a.ValidUntil
	case "false-ack":
		completed.RuntimeReloadRequired = false
	case "source-receipt-rewritten":
		partial.Entries[len(originalSource.Entries)+1].Receipt = mustProviderRecoveryJSON(map[string]string{"native": "different"})
	case "later-work":
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "later-qualified-deployment-work", State: "pending"})
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	base := rootCRLInstalledBase(old, review)
	write(filepath.Join(base, "qualification.json"), a)
	write(filepath.Join(base, "source-journal.json"), partial)
	write(filepath.Join(base, "completed.json"), completed)
	write(filepath.Join("deployment/create/root-crl-recovery", old, "installed-resume/completed.json"), createRootCRLInstalledPointer{Review: review, OriginalReview: old, CompletionSHA: deploymentplan.Digest(completed)})
	return store, e, original, originalSource, proof
}

func TestOriginalRootCRLInstalledCompletionAuthenticatesSidecarAndBoundedPrefix(t *testing.T) {
	for _, mode := range []string{"genuine", "later-work", "empty-prefix", "wrong-prefix", "wrong-evidence", "wrong-proof", "expired-admission", "false-ack", "source-receipt-rewritten"} {
		t.Run(mode, func(t *testing.T) {
			store, e, original, source, proof := rootCRLInstalledCompletionFixture(t, mode)
			err := verifyCreateRootCRLInstalledCompletion(store, e, original, source, proof)
			want := mode == "genuine" || mode == "later-work"
			if (err == nil) != want {
				t.Fatal("unsafe installed sidecar accepted", mode, err)
			}
		})
	}
}

func TestOriginalRootCRLInstalledImmutablePointerPreservesOriginal(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	path := "deployment/create/root-crl-recovery/isolated/installed-resume/completed.json"
	value := map[string]string{"review": "original"}
	if err := writeRootCRLInstalledImmutable(store, path, value); err != nil {
		t.Fatal(err)
	}
	if err := writeRootCRLInstalledImmutable(store, path, value); err != nil {
		t.Fatal("exact retained completion was not reusable", err)
	}
	if err := writeRootCRLInstalledImmutable(store, path, map[string]string{"review": "changed"}); err == nil {
		t.Fatal("original completion overwritten")
	}
	raw, err := store.read(path)
	var observed map[string]string
	if err != nil || json.Unmarshal([]byte(raw), &observed) != nil || observed["review"] != "original" {
		t.Fatal("original pointer changed", err)
	}
}

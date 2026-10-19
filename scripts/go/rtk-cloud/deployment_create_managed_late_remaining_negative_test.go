package main

import (
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

func managedLateRemainingNegativeFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	remaining := &createManagedLateRemaining{
		ParentSHA: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 64), OwnerSHA: strings.Repeat("c", 64),
		OriginalReview: strings.Repeat("d", 64), OriginalArtifactSHA: strings.Repeat("e", 64), DescriptorSHA: strings.Repeat("f", 64),
		SeedObservationSHA: strings.Repeat("1", 64), CredentialObservationSHA: strings.Repeat("2", 64),
		OwnersPlanSHA: strings.Repeat("3", 64), OwnersJournalSHA: strings.Repeat("4", 64), StopPlanSHA: strings.Repeat("5", 64), StopJournalSHA: strings.Repeat("6", 64),
		SessionID: "88888888-8888-4888-8888-888888888888", Fingerprint: strings.Repeat("7", 64), KeySHA: strings.Repeat("8", 64), CSRSHA: strings.Repeat("9", 64),
		NotBefore: now.Add(-10 * time.Minute), LeafNotAfter: now.Add(8 * time.Hour), SessionExpires: now.Add(8 * time.Hour),
	}
	remaining.Ceremony.BootstrapSessionID = remaining.SessionID
	artifact := createFoundationRecoveryArtifact{
		Schema:      "rtk-cloud.create-openbao-recovery-artifact/v1",
		Plan:        createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: remaining.ParentSHA, ParentJournalSHA: remaining.SourceSHA, ParentOwnerSHA: remaining.OwnerSHA, CheckerSHA: strings.Repeat("0", 64), Materials: map[string]string{managedLateRemainingMaterial + "descriptor": managedLateRemainingDigest(remaining)}},
		QualifiedAt: now.Add(-time.Minute),
	}
	artifact.ValidUntil = artifact.QualifiedAt.Add(15 * time.Minute)
	managedLateRemainingNegativeBind(t, remaining, &artifact)
	return remaining, artifact, now
}

func managedLateRemainingNegativeBind(t *testing.T, r *createManagedLateRemaining, a *createFoundationRecoveryArtifact) {
	t.Helper()
	a.Plan.Materials[managedLateRemainingMaterial+"descriptor"] = managedLateRemainingDigest(r)
	a.Plan.Materials[managedLateRemainingMaterial+"policy"] = managedLateRemainingPolicySHA()
	r.Review = deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: r.Review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: r.Review})
	}
}

func TestManagedLateRemainingNegativeMembershipKeepsLiteralStopped388(t *testing.T) {
	now := time.Now().UTC()
	parent, review := strings.Repeat("a", 64), strings.Repeat("b", 64)
	e := &environmentCreateExecution{owner: environmentCreateOwner{PlanSHA256: parent, OwnerToken: "isolated-original-owner"}, Journal: environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-original-owner"}}
	for i := range 388 {
		at := now.Add(-time.Hour).Add(time.Duration(i) * time.Millisecond)
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-completed-%03d", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("c", 64), Receipt: json.RawMessage(`{"isolated":"receipt"}`)})
	}
	e.Journal.Entries[374].Key = "openbao-recovery-admission-" + review
	e.Journal.Entries[387].Key = "kubernetes/managed-late-log-stop-" + review
	late := &createManagedLateSessionRecovery{Review: review, ParentSHA: parent, SourceOwnerSHA: deploymentplan.Digest(e.owner), ExpiredRenewal: &createManagedLateExpiredRenewal{}}
	before := deploymentplan.Digest([]any{e.Journal, e.owner, late})
	if !managedLateRemainingMembership(e, late, now) {
		t.Fatal("literal fully completed stopped388 not selected by structural guard")
	}
	for _, mode := range []string{"extra-helper", "missing-stop", "pending-stop", "missing-receipt", "zero-start", "zero-completion", "reverse-completion", "future-completion", "overall-completed", "wrong-stop-review", "wrong-admission-review", "foreign-parent", "foreign-owner", "ordinary-late"} {
		t.Run(mode, func(t *testing.T) {
			var journal environmentCreateJournal
			var descriptor createManagedLateSessionRecovery
			_ = json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &journal)
			_ = json.Unmarshal(mustProviderRecoveryJSON(late), &descriptor)
			changed := *e
			changed.Journal = journal
			switch mode {
			case "extra-helper":
				changed.Journal.Entries = append(changed.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/managed-late-log-existing-ack-" + review, State: "pending"})
			case "missing-stop":
				changed.Journal.Entries = changed.Journal.Entries[:387]
			case "pending-stop":
				changed.Journal.Entries[387].State = "pending"
			case "missing-receipt":
				changed.Journal.Entries[380].Receipt = nil
			case "zero-start":
				changed.Journal.Entries[380].StartedAt = time.Time{}
			case "zero-completion":
				changed.Journal.Entries[380].CompletedAt = time.Time{}
			case "reverse-completion":
				changed.Journal.Entries[380].CompletedAt = changed.Journal.Entries[380].StartedAt.Add(-time.Nanosecond)
			case "future-completion":
				changed.Journal.Entries[380].CompletedAt = now.Add(time.Second)
			case "overall-completed":
				changed.Journal.CompletedAt = now
			case "wrong-stop-review":
				changed.Journal.Entries[387].Key = "kubernetes/managed-late-log-stop-" + strings.Repeat("d", 64)
			case "wrong-admission-review":
				changed.Journal.Entries[374].Key = "openbao-recovery-admission-" + strings.Repeat("d", 64)
			case "foreign-parent":
				descriptor.ParentSHA = strings.Repeat("d", 64)
			case "foreign-owner":
				changed.owner.OwnerToken = "foreign-owner"
			case "ordinary-late":
				descriptor.ExpiredRenewal = nil
			}
			if managedLateRemainingMembership(&changed, &descriptor, now) {
				t.Fatal("foreign, incomplete or later outcome entered remaining-only selection")
			}
		})
	}
	if deploymentplan.Digest([]any{e.Journal, e.owner, late}) != before {
		t.Fatal("source selection modified accepted history")
	}
}

func TestManagedLateRemainingNegativeSessionRequiresExactlyEightWithoutLog(t *testing.T) {
	remaining, _, now := managedLateRemainingNegativeFixture(t)
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	installed := slices.DeleteFunc(slices.Clone(subjects), func(s string) bool { return s == "service:video-cloud-logingester" })
	original := managedSessionState{Session: remaining.SessionID, Fingerprint: remaining.Fingerprint, Status: "active", Expires: remaining.SessionExpires, Subjects: slices.Clone(subjects), Installed: installed}
	if !managedLateRemainingSessionValid(original, remaining, subjects, now) {
		t.Fatal("genuine unchanged eight installations were refused")
	}
	for _, mode := range []string{"wrong-session", "wrong-fingerprint", "changed-expiry", "expired", "exact-twenty-minute-life", "other-active", "sealed", "expired-status", "seven", "all-nine", "wrong-missing", "duplicate", "foreign-installed", "changed-roster"} {
		t.Run(mode, func(t *testing.T) {
			state := original
			state.Subjects, state.Installed = slices.Clone(original.Subjects), slices.Clone(original.Installed)
			r := *remaining
			switch mode {
			case "wrong-session":
				state.Session = "77777777-7777-4777-8777-777777777777"
			case "wrong-fingerprint":
				state.Fingerprint = strings.Repeat("a", 64)
			case "changed-expiry":
				state.Expires = state.Expires.Add(time.Minute)
			case "expired":
				state.Expires = now.Add(-time.Second)
				r.SessionExpires = state.Expires
			case "exact-twenty-minute-life":
				state.Expires = now.Add(20 * time.Minute)
				r.SessionExpires = state.Expires
			case "other-active":
				state.OtherActive = 1
			case "sealed":
				state.Status = "sealed"
			case "expired-status":
				state.Status = "expired"
			case "seven":
				state.Installed = state.Installed[:7]
			case "all-nine":
				state.Installed = slices.Clone(subjects)
			case "wrong-missing":
				state.Installed[0] = "service:video-cloud-logingester"
			case "duplicate":
				state.Installed[1] = state.Installed[0]
			case "foreign-installed":
				state.Installed[0] = "service:foreign"
			case "changed-roster":
				state.Subjects[0] = "service:foreign"
			}
			if managedLateRemainingSessionValid(state, &r, subjects, now) {
				t.Fatal("unproved session lifecycle or installation roster admitted")
			}
		})
	}
}

func TestManagedLateRemainingNegativeAuthorityUsesSignedLeafEpochAndFreshGO(t *testing.T) {
	for _, mode := range []string{"leaf-origin", "caller-cap", "authority-reserve", "exact-finalizer", "too-little-finalizer", "future-leaf", "zero-leaf", "different-expiry", "old-GO", "not-yet-qualified", "wrong-outer-schema", "wrong-plan-schema", "source-drift", "owner-drift", "descriptor-drift", "wrong-review", "failed-gate", "optional-gate", "wrong-gate-evidence", "missing-gate", "changed-checker"} {
		t.Run(mode, func(t *testing.T) {
			r, a, now := managedLateRemainingNegativeFixture(t)
			overall, expected := now.Add(90*time.Minute), now.Add(90*time.Minute)
			pass := mode == "leaf-origin" || mode == "caller-cap" || mode == "authority-reserve" || mode == "exact-finalizer"
			switch mode {
			case "leaf-origin":
				r.NotBefore = now.Add(-40 * time.Minute)
				expected = now.Add(80 * time.Minute)
				managedLateRemainingNegativeBind(t, r, &a)
			case "caller-cap":
				overall = now.Add(35 * time.Minute)
				expected = overall
			case "authority-reserve":
				r.SessionExpires = now.Add(40 * time.Minute)
				r.LeafNotAfter = r.SessionExpires
				expected = now.Add(35 * time.Minute)
				managedLateRemainingNegativeBind(t, r, &a)
			case "exact-finalizer":
				overall = now.Add(15 * time.Minute)
				expected = overall
			case "too-little-finalizer":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "future-leaf":
				r.NotBefore = now.Add(time.Second)
				managedLateRemainingNegativeBind(t, r, &a)
			case "zero-leaf":
				r.NotBefore = time.Time{}
				managedLateRemainingNegativeBind(t, r, &a)
			case "different-expiry":
				r.LeafNotAfter = r.LeafNotAfter.Add(-time.Second)
				managedLateRemainingNegativeBind(t, r, &a)
			case "old-GO":
				now = a.ValidUntil
			case "not-yet-qualified":
				now = a.QualifiedAt.Add(-time.Nanosecond)
			case "wrong-outer-schema":
				a.Schema = "foreign"
			case "wrong-plan-schema":
				a.Plan.Schema = managedLatePostMintSchema
			case "source-drift":
				r.SourceSHA = strings.Repeat("f", 64)
			case "owner-drift":
				r.OwnerSHA = strings.Repeat("f", 64)
			case "descriptor-drift":
				r.KeySHA = strings.Repeat("f", 64)
			case "wrong-review":
				r.Review = strings.Repeat("f", 64)
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "optional-gate":
				a.Qualification.Gates[3].Required = false
			case "wrong-gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("f", 64)
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "changed-checker":
				a.Plan.CheckerSHA = strings.Repeat("f", 64)
			}
			before := deploymentplan.Digest([]any{r, a})
			clock, err := newManagedLateRemainingAuthority(r, a, overall, now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
			if (err == nil) != pass {
				t.Fatalf("fixed remaining authority acceptance differs: %v", err)
			}
			if pass && (!clock.StartedAt.Equal(r.NotBefore) || !clock.Deadline.Equal(expected) || clock.StartedAt.Equal(now) || !clock.AdmittedAt.Equal(now) || !managedLateRemainingAuthorityValid(clock)) {
				t.Fatal("remaining policy reset the leaf epoch or lost the tightest bound")
			}
			if deploymentplan.Digest([]any{r, a}) != before {
				t.Fatal("authority construction changed historical evidence")
			}
		})
	}
}

func TestManagedLateRemainingNegativeAuthorityMarkerIsImmutableAndSourceBound(t *testing.T) {
	for _, mode := range []string{"genuine", "missing", "orphan-content", "changed-source", "changed-owner", "changed-key", "changed-csr", "changed-leaf-epoch", "changed-expiry", "changed-seed-observation", "extended-clock", "future-reset", "missing-artifact", "different-artifact", "changed-qualification-bytes", "noncanonical", "unsafe-mode", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			r, a, now := managedLateRemainingNegativeFixture(t)
			clock, err := newManagedLateRemainingAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
			if err != nil {
				t.Fatal(err)
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json"), mustProviderRecoveryJSON(a)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, managedLateRemainingAuthorityPath(r.ParentSHA))
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if mode != "missing" {
				switch mode {
				case "orphan-content":
					clock.DescriptorSHA = strings.Repeat("f", 64)
				case "extended-clock":
					clock.Deadline = clock.Deadline.Add(time.Minute)
				case "future-reset":
					clock.StartedAt = clock.StartedAt.Add(time.Minute)
					clock.Deadline = clock.Deadline.Add(time.Minute)
				}
				raw, _ = json.Marshal(clock)
				if mode == "noncanonical" {
					raw = append(raw, '\n')
				}
				if err := writeExclusivePrivateDeploymentJSON(path, raw); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "changed-source":
				r.SourceSHA = strings.Repeat("f", 64)
			case "changed-owner":
				r.OwnerSHA = strings.Repeat("f", 64)
			case "changed-key":
				r.KeySHA = strings.Repeat("f", 64)
			case "changed-csr":
				r.CSRSHA = strings.Repeat("f", 64)
			case "changed-leaf-epoch":
				r.NotBefore = r.NotBefore.Add(time.Minute)
			case "changed-expiry":
				r.LeafNotAfter = r.LeafNotAfter.Add(time.Minute)
				r.SessionExpires = r.LeafNotAfter
			case "changed-seed-observation":
				r.SeedObservationSHA = strings.Repeat("f", 64)
			case "missing-artifact":
				if err := os.Remove(filepath.Join(store.Root, "deployment/create/openbao-recovery", r.Review, "qualification.json")); err != nil {
					t.Fatal(err)
				}
			case "different-artifact":
				a.Qualification.Gates[3].Status = "BLOCKED"
				if err := os.WriteFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", r.Review, "qualification.json"), mustProviderRecoveryJSON(a), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-qualification-bytes":
				// Identical interpreted artifact, different exact admitted bytes.
				if err := os.WriteFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", r.Review, "qualification.json"), append(mustProviderRecoveryJSON(a), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsafe-mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readManagedLateRemainingAuthority(store, r)
			if mode == "missing" {
				if err != nil || got != nil {
					t.Fatal("absent unadmitted marker was fabricated", err)
				}
				return
			}
			if mode == "genuine" {
				if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(clock) {
					t.Fatal("original immutable authority was not reusable", err)
				}
				before, _ := os.ReadFile(path)
				if err := writeExclusivePrivateDeploymentJSON(path, []byte(`{"reset":true}`)); err == nil {
					t.Fatal("retry overwrote the one original authority marker")
				}
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Fatal("retry changed original authority bytes")
				}
			} else if err == nil {
				t.Fatal("orphaned or changed remaining authority admitted")
			}
		})
	}
}

func TestManagedLateRemainingNegativeCompletionCannotDropExistingAuthority(t *testing.T) {
	for _, mode := range []string{"historical-no-marker", "existing-marker", "orphan-marker", "symlink-marker", "review-only", "hash-only"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			r, _, _ := managedLateRemainingNegativeFixture(t)
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: r.ParentSHA}}
			proof := &createOpenBaoACKCompletion{Native: deploymentplan.Object{}}
			path := filepath.Join(store.Root, managedLateRemainingAuthorityPath(r.ParentSHA))
			if mode != "historical-no-marker" && mode != "review-only" && mode != "hash-only" {
				if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				raw := []byte(`{}`)
				if mode == "orphan-marker" {
					raw = []byte(`{"not":"authority"}`)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "symlink-marker" {
					if err := os.Rename(path, path+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".original", path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "review-only" {
				proof.Native["late_remaining_review_sha256"] = r.Review
			}
			if mode == "hash-only" {
				proof.Native["late_remaining_authority_sha256"] = strings.Repeat("a", 64)
			}
			err := validateManagedLateRemainingCompletion(store, e, &createManagedLateSessionRecovery{}, proof)
			if (err == nil) != (mode == "historical-no-marker") {
				t.Fatal("completed proof dropped or invented the required immutable remaining authority", err)
			}
		})
	}
}

func TestManagedLateRemainingNegativeCompletionPinsAuthorityReviewSelector(t *testing.T) {
	for _, mode := range []string{"genuine", "substituted-valid-review", "archived-review-drift"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			r, a, now := managedLateRemainingNegativeFixture(t)
			late := &createManagedLateSessionRecovery{Review: r.OriginalReview, OriginalKeySHA: r.KeySHA, OriginalCSRSHA: r.CSRSHA, NewSessionID: r.SessionID}
			e := &environmentCreateExecution{Store: store, owner: environmentCreateOwner{PlanSHA256: r.ParentSHA, OwnerToken: "isolated-original-owner"}, Journal: environmentCreateJournal{Schema: environmentCreateJournalSchema, PlanSHA256: r.ParentSHA, OwnerToken: "isolated-original-owner", Entries: make([]environmentCreateJournalEntry, 388)}}
			r.SourceSHA, r.OwnerSHA, r.DescriptorSHA = deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.owner), managedLateSessionSourceDigest(late)
			a.Plan.ParentJournalSHA, a.Plan.ParentOwnerSHA = r.SourceSHA, r.OwnerSHA
			managedLateRemainingNegativeBind(t, r, &a)
			clock, err := newManagedLateRemainingAuthority(r, a, now.Add(time.Hour), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
			if err != nil {
				t.Fatal(err)
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json"), mustProviderRecoveryJSON(a)); err != nil {
				t.Fatal(err)
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", r.Review, managedLateRemainingFilename), mustProviderRecoveryJSON(r)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, managedLateRemainingAuthorityPath(r.ParentSHA))
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(clock)
			if err := writeExclusivePrivateDeploymentJSON(path, raw); err != nil {
				t.Fatal(err)
			}
			proof := &createOpenBaoACKCompletion{CompletedAt: now.Add(time.Second), Native: deploymentplan.Object{"late_remaining_review_sha256": clock.Review, "late_remaining_authority_sha256": deploymentplan.Digest(clock)}}
			if mode == "substituted-valid-review" {
				second := *r
				a.Plan.CheckerSHA = strings.Repeat("f", 64)
				managedLateRemainingNegativeBind(t, &second, &a)
				if second.Review == r.Review || managedLateRemainingDigest(&second) != managedLateRemainingDigest(r) {
					t.Fatal("fixture did not make a distinct valid same-source review")
				}
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", second.Review, "qualification.json"), mustProviderRecoveryJSON(a)); err != nil {
					t.Fatal(err)
				}
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", second.Review, managedLateRemainingFilename), mustProviderRecoveryJSON(second)); err != nil {
					t.Fatal(err)
				}
				proof.Native["late_remaining_review_sha256"] = second.Review
			}
			if mode == "archived-review-drift" {
				archived := *r
				archived.Review = strings.Repeat("e", 64)
				if err := os.WriteFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", r.Review, managedLateRemainingFilename), mustProviderRecoveryJSON(archived), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Completed-parent normalization is allowed only in this authenticated
			// durable reader; the captured388 digest itself remains unchanged.
			e.Journal.CompletedAt = now.Add(2 * time.Second)
			if err := validateManagedLateRemainingCompletion(store, e, late, proof); (err == nil) != (mode == "genuine") {
				t.Fatal("remaining authority accepted a substituted review selector", err)
			}
		})
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRemaining389Fixture(t *testing.T) (*environmentCreateExecution, *createManagedLateSessionRecovery, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	parent, review := strings.Repeat("a", 64), strings.Repeat("b", 64)
	e := &environmentCreateExecution{owner: environmentCreateOwner{PlanSHA256: parent, OwnerToken: "isolated-owner"}, Journal: environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-owner"}}
	for i := range 389 {
		at := now.Add(-time.Hour).Add(time.Duration(i) * time.Millisecond)
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-%03d", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("c", 64), Receipt: json.RawMessage(`{"isolated":"receipt"}`)})
	}
	e.Journal.Entries[374].Key = "openbao-recovery-admission-" + review
	e.Journal.Entries[387].Key = "kubernetes/managed-late-log-stop-" + review
	e.Journal.Entries[388].Key = "kubernetes/managed-late-log-existing-ack-" + review
	late := &createManagedLateSessionRecovery{Review: review, ParentSHA: parent, SourceOwnerSHA: deploymentplan.Digest(e.owner), ExpiredRenewal: &createManagedLateExpiredRenewal{}}
	return e, late, now
}

func TestManagedLateRemaining389NegativeLiteralAcceptedHelperOnly(t *testing.T) {
	e, late, now := managedLateRemaining389Fixture(t)
	before := deploymentplan.Digest([]any{e.Journal, e.owner, late})
	if !managedLateRemainingMembership(e, late, now) {
		t.Fatal("completed exact helper389 refused")
	}
	for _, mode := range []string{"pending-helper", "missing-helper-receipt", "zero-helper-start", "zero-helper-completion", "future-helper", "foreign-helper-review", "unknown-helper", "unknown-tail", "wrong-stop", "changed-owner", "overall-completed"} {
		t.Run(mode, func(t *testing.T) {
			changed := *e
			changed.Journal = environmentCreateJournal{}
			_ = json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &changed.Journal)
			switch mode {
			case "pending-helper":
				changed.Journal.Entries[388].State = "pending"
			case "missing-helper-receipt":
				changed.Journal.Entries[388].Receipt = nil
			case "zero-helper-start":
				changed.Journal.Entries[388].StartedAt = time.Time{}
			case "zero-helper-completion":
				changed.Journal.Entries[388].CompletedAt = time.Time{}
			case "future-helper":
				changed.Journal.Entries[388].CompletedAt = now.Add(time.Second)
			case "foreign-helper-review":
				changed.Journal.Entries[388].Key = "kubernetes/managed-late-log-existing-ack-" + strings.Repeat("d", 64)
			case "unknown-helper":
				changed.Journal.Entries[388].Key = "kubernetes/foreign-helper-" + late.Review
			case "unknown-tail":
				changed.Journal.Entries = append(changed.Journal.Entries, changed.Journal.Entries[388])
				changed.Journal.Entries[389].Key = "kubernetes/foreign-extra-" + late.Review
			case "wrong-stop":
				changed.Journal.Entries[387].Key = "kubernetes/foreign-stop-" + late.Review
			case "changed-owner":
				changed.owner.OwnerToken = "foreign"
			case "overall-completed":
				changed.Journal.CompletedAt = now
			}
			if managedLateRemainingMembership(&changed, late, now) {
				t.Fatal("foreign or incomplete checkpoint admitted")
			}
		})
	}
	if deploymentplan.Digest([]any{e.Journal, e.owner, late}) != before {
		t.Fatal("selection changed accepted history")
	}
}

func TestManagedLateRemaining389NegativeRequiresAllNineCurrentACKs(t *testing.T) {
	r, _, now := managedLateRemainingNegativeFixture(t)
	r.CurrentSourceSHA = strings.Repeat("a", 64)
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	state := managedSessionState{Session: r.SessionID, Fingerprint: r.Fingerprint, Status: "active", Expires: r.SessionExpires, Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects)}
	if !managedLateRemainingSessionValid(state, r, subjects, now) {
		t.Fatal("genuine nine-subject helper completion refused")
	}
	for _, mode := range []string{"eight-without-log", "eight-with-log", "duplicate", "foreign", "other-active", "expired", "wrong-session", "wrong-fingerprint", "changed-expiry", "twenty-minutes", "changed-roster"} {
		t.Run(mode, func(t *testing.T) {
			changed := state
			changed.Subjects = slices.Clone(state.Subjects)
			changed.Installed = slices.Clone(state.Installed)
			copy := *r
			switch mode {
			case "eight-without-log":
				changed.Installed = slices.DeleteFunc(changed.Installed, func(s string) bool { return s == "service:video-cloud-logingester" })
			case "eight-with-log":
				changed.Installed = changed.Installed[1:]
			case "duplicate":
				changed.Installed[1] = changed.Installed[0]
			case "foreign":
				changed.Installed[0] = "service:foreign"
			case "other-active":
				changed.OtherActive = 1
			case "expired":
				changed.Status = "expired"
			case "wrong-session":
				changed.Session = "77777777-7777-4777-8777-777777777777"
			case "wrong-fingerprint":
				changed.Fingerprint = strings.Repeat("f", 64)
			case "changed-expiry":
				changed.Expires = changed.Expires.Add(time.Second)
			case "twenty-minutes":
				changed.Expires = now.Add(20 * time.Minute)
				copy.SessionExpires = changed.Expires
			case "changed-roster":
				changed.Subjects[0] = "service:foreign"
			}
			if managedLateRemainingSessionValid(changed, &copy, subjects, now) {
				t.Fatal("incomplete or changed native session admitted")
			}
		})
	}
}

func TestManagedLateRemaining389NegativeKeepsOriginalAuthorityAndSeparateCurrentPins(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	original, a, now := managedLateRemainingNegativeFixture(t)
	authority, err := newManagedLateRemainingAuthority(original, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", original.Review, "qualification.json"), mustProviderRecoveryJSON(a)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, managedLateRemainingAuthorityPath(original.ParentSHA))
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusivePrivateDeploymentJSON(path, mustProviderRecoveryJSON(authority)); err != nil {
		t.Fatal(err)
	}
	resumed := *original
	resumed.Review = strings.Repeat("2", 64)
	resumed.CurrentSourceSHA = strings.Repeat("3", 64)
	resumed.HelperObservationSHA = strings.Repeat("4", 64)
	resumed.Authority = authority
	originalSHA := deploymentplan.Digest(authority)
	if managedLateRemainingDigest(&resumed) != managedLateRemainingDigest(original) {
		t.Fatal("resume replaced immutable388 descriptor")
	}
	if managedLateRemainingCurrentSource(&resumed) != resumed.CurrentSourceSHA || managedLateRemainingCurrentSource(original) != original.SourceSHA {
		t.Fatal("current389 source confused with original388 source")
	}
	currentPins := managedLateRemainingPins(&resumed)
	if currentPins["current-source"] != resumed.CurrentSourceSHA || currentPins["helper-observation"] != resumed.HelperObservationSHA || currentPins["source"] != original.SourceSHA || currentPins["authority"] != originalSHA {
		t.Fatal("fresh389 evidence not independently bound")
	}
	got, err := readManagedLateRemainingAuthority(store, &resumed)
	if err != nil || deploymentplan.Digest(got) != originalSHA || !got.Deadline.Equal(authority.Deadline) || got.Review != original.Review {
		t.Fatal("same admitted authority did not preserve original review/deadline", err)
	}
	for _, mode := range []string{"source", "owner", "key", "csr", "seed", "epoch", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			changed := resumed
			switch mode {
			case "source":
				changed.SourceSHA = strings.Repeat("e", 64)
			case "owner":
				changed.OwnerSHA = strings.Repeat("e", 64)
			case "key":
				changed.KeySHA = strings.Repeat("e", 64)
			case "csr":
				changed.CSRSHA = strings.Repeat("e", 64)
			case "seed":
				changed.SeedObservationSHA = strings.Repeat("e", 64)
			case "epoch":
				changed.NotBefore = changed.NotBefore.Add(time.Second)
			case "expiry":
				changed.LeafNotAfter = changed.LeafNotAfter.Add(time.Second)
				changed.SessionExpires = changed.LeafNotAfter
			}
			if _, err := readManagedLateRemainingAuthority(store, &changed); err == nil {
				t.Fatal("resume rebaselined original admitted authority")
			}
		})
	}
	changedPin := resumed
	changedPin.HelperObservationSHA = strings.Repeat("5", 64)
	if deploymentplan.Digest(managedLateRemainingPins(&changedPin)) == deploymentplan.Digest(currentPins) {
		t.Fatal("helper observation drift disappeared from fresh qualification")
	}
	if deploymentplan.Digest(authority) != originalSHA {
		t.Fatal("resume modified original authority")
	}
}

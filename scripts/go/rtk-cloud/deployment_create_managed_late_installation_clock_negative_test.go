package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateInstallationNegativeFixture(t *testing.T) (secretStore, *createManagedLatePostMint, createFoundationRecoveryArtifact, time.Time, time.Time) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	epoch := time.Now().UTC().Add(-time.Minute)
	post := &createManagedLatePostMint{
		InstallationPolicy: managedLateInstallationPolicy,
		SourceSHA:          strings.Repeat("a", 64), SourceOwnerSHA: strings.Repeat("b", 64),
		Fingerprint: strings.Repeat("c", 64), SessionExpires: epoch.Add(8 * time.Hour), LeafNotAfter: epoch.Add(8 * time.Hour),
		Ceremony: environmentCreateServiceCeremony{BootstrapSessionID: "a849212c-47ee-4b29-8f98-bb281fb45bb4"},
	}
	a := createFoundationRecoveryArtifact{
		Schema:      "rtk-cloud.create-openbao-recovery-artifact/v1",
		Plan:        createFoundationRecoveryPlan{Schema: managedLatePostMintSchema, ParentSHA: strings.Repeat("d", 64), ParentJournalSHA: post.SourceSHA, ParentOwnerSHA: post.SourceOwnerSHA, CheckerSHA: strings.Repeat("e", 64), Materials: map[string]string{}},
		QualifiedAt: epoch.Add(-time.Minute),
	}
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	a.Plan.Materials[managedLatePostMintMaterial+"descriptor"] = managedLatePostMintDigest(post)
	post.Review = deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: post.Review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: post.Review})
	}
	return store, post, a, epoch.Add(managedLateOverallLimit), epoch
}

func managedLateInstallationNegativeAdmission(t *testing.T, post *createManagedLatePostMint, a createFoundationRecoveryArtifact, clock *createManagedLateInstallationClock) environmentCreateJournalEntry {
	t.Helper()
	sha := managedLateInstallationClockSHA(clock)
	intent := map[string]string{"review_sha256": post.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": post.SourceSHA}
	return environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + post.Review, Phase: "recovery", State: "completed", StartedAt: clock.StartedAt, CompletedAt: clock.StartedAt.Add(time.Second), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": post.Review, "audit_sha256": strings.Repeat("f", 64), "installation_clock_sha256": sha})}
}

func TestManagedLateInstallationClockBoundsFirstEpochWithoutReset(t *testing.T) {
	for _, mode := range []string{"ninety-minute maximum", "caller deadline", "actual authority deadline", "exact finalizer boundary", "too little finalizer", "extended caller", "expired authority", "different leaf expiry", "zero epoch", "old qualification", "exact qualification expiry", "already clocked", "prior native start", "foreign policy", "source changed", "owner changed", "checker changed", "session changed", "fingerprint changed", "failed gate"} {
		t.Run(mode, func(t *testing.T) {
			_, post, a, overall, epoch := managedLateInstallationNegativeFixture(t)
			wanted := overall
			valid := mode == "ninety-minute maximum" || mode == "caller deadline" || mode == "actual authority deadline" || mode == "exact finalizer boundary"
			switch mode {
			case "caller deadline":
				overall = epoch.Add(40 * time.Minute)
				wanted = overall
			case "actual authority deadline":
				post.SessionExpires = epoch.Add(45 * time.Minute)
				post.LeafNotAfter = post.SessionExpires
				wanted = epoch.Add(40 * time.Minute)
			case "exact finalizer boundary":
				overall = epoch.Add(managedLateFinalizerLimit)
				wanted = overall
			case "too little finalizer":
				overall = epoch.Add(managedLateFinalizerLimit - time.Nanosecond)
			case "extended caller":
				overall = epoch.Add(managedLateOverallLimit + time.Nanosecond)
			case "expired authority":
				post.SessionExpires = epoch.Add(-time.Second)
				post.LeafNotAfter = post.SessionExpires
			case "different leaf expiry":
				post.LeafNotAfter = post.LeafNotAfter.Add(-time.Minute)
			case "zero epoch":
				epoch = time.Time{}
			case "old qualification":
				epoch = a.QualifiedAt.Add(-time.Nanosecond)
			case "exact qualification expiry":
				epoch = a.ValidUntil
			case "already clocked":
				post.InstallationClock = &createManagedLateInstallationClock{}
			case "prior native start":
				post.NativeStartedAt = epoch.Add(-time.Minute)
			case "foreign policy":
				post.InstallationPolicy = "retry-clock"
			case "source changed":
				post.SourceSHA = strings.Repeat("1", 64)
			case "owner changed":
				post.SourceOwnerSHA = strings.Repeat("1", 64)
			case "checker changed":
				a.Plan.CheckerSHA = strings.Repeat("1", 64)
			case "session changed":
				post.Ceremony.BootstrapSessionID = "not-an-owned-session"
			case "fingerprint changed":
				post.Fingerprint = ""
			case "failed gate":
				a.Qualification.Gates[2].Status = "BLOCKED"
			}
			// Legitimate expiry constraints are part of the qualified descriptor.
			if mode == "actual authority deadline" || mode == "expired authority" || mode == "different leaf expiry" {
				a.Plan.Materials[managedLatePostMintMaterial+"descriptor"] = managedLatePostMintDigest(post)
				post.Review = deploymentplan.Digest(a.Plan)
				a.Qualification.PlanSHA256 = post.Review
				for i := range a.Qualification.Gates {
					a.Qualification.Gates[i].EvidenceSHA256 = post.Review
				}
			}
			clock, err := newManagedLateInstallationClock(post, a, post.SourceOwnerSHA, overall, epoch)
			if (err == nil) != valid {
				t.Fatalf("%s acceptance differs: %v", mode, err)
			}
			if valid && (!clock.StartedAt.Equal(epoch) || !clock.Deadline.Equal(wanted) || !managedLateInstallationClockValid(clock)) {
				t.Fatal("first epoch or tightest immutable cutoff changed")
			}
		})
	}
}

func TestManagedLateInstallationClockMissingAfterAdmissionCannotRegenerate(t *testing.T) {
	for _, state := range []string{"pending", "completed"} {
		t.Run(state, func(t *testing.T) {
			store, post, a, overall, epoch := managedLateInstallationNegativeFixture(t)
			clock, err := newManagedLateInstallationClock(post, a, post.SourceOwnerSHA, overall, epoch)
			if err != nil {
				t.Fatal(err)
			}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: a.Plan.ParentSHA}}
			entry := managedLateInstallationNegativeAdmission(t, post, a, clock)
			entry.State = state
			if state == "pending" {
				entry.CompletedAt = time.Time{}
				entry.Receipt = nil
			}
			if _, err := validateManagedLateInstallationAdmission(store, e, post, a, entry); err == nil {
				t.Fatal("missing first clock accepted after durable admission")
			}
			if _, err := os.Lstat(filepath.Join(store.Root, managedLateInstallationClockPath(a.Plan.ParentSHA))); !os.IsNotExist(err) {
				t.Fatal("validator regenerated missing clock")
			}
		})
	}
}

func TestManagedLateInstallationClockExclusiveParentPathBlocksSameOrLaterReview(t *testing.T) {
	store, post, a, overall, epoch := managedLateInstallationNegativeFixture(t)
	clock, err := newManagedLateInstallationClock(post, a, post.SourceOwnerSHA, overall, epoch)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := writeManagedLateInstallationClock(store, clock)
	if err != nil || sha != managedLateInstallationClockSHA(clock) {
		t.Fatal("first clock not durably created", err)
	}
	path := filepath.Join(store.Root, managedLateInstallationClockPath(clock.Parent))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same review", "later epoch", "different review"} {
		changed := *clock
		if mode == "later epoch" {
			changed.StartedAt = changed.StartedAt.Add(time.Minute)
			changed.OverallDeadline = changed.StartedAt.Add(managedLateOverallLimit)
			changed.Deadline = changed.OverallDeadline
		}
		if mode == "different review" {
			changed.Review = strings.Repeat("1", 64)
		}
		if _, err := writeManagedLateInstallationClock(store, &changed); err == nil {
			t.Fatal("existing parent clock replaced:", mode)
		}
	}
	read, err := readManagedLateInstallationClock(store, clock.Parent)
	after, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || deploymentplan.Digest(read) != deploymentplan.Digest(clock) || string(after) != string(original) {
		t.Fatal("retry changed original clock bytes")
	}
}

func TestManagedLateInstallationClockAdmissionPinsAllActualAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "pending", "changed epoch", "zero completion", "future completion", "changed receipt clock", "changed audit", "extra receipt", "changed intent", "changed source", "changed checker", "changed fingerprint", "changed session", "changed expiry", "clock symlink", "public clock"} {
		t.Run(mode, func(t *testing.T) {
			store, post, a, overall, epoch := managedLateInstallationNegativeFixture(t)
			clock, err := newManagedLateInstallationClock(post, a, post.SourceOwnerSHA, overall, epoch)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeManagedLateInstallationClock(store, clock); err != nil {
				t.Fatal(err)
			}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: a.Plan.ParentSHA}}
			entry := managedLateInstallationNegativeAdmission(t, post, a, clock)
			path := filepath.Join(store.Root, managedLateInstallationClockPath(clock.Parent))
			switch mode {
			case "pending":
				entry.State = "pending"
				entry.CompletedAt = time.Time{}
				entry.Receipt = nil
			case "changed epoch":
				entry.StartedAt = entry.StartedAt.Add(-time.Second)
			case "zero completion":
				entry.CompletedAt = time.Time{}
			case "future completion":
				entry.CompletedAt = time.Now().UTC().Add(time.Minute)
			case "changed receipt clock", "changed audit", "extra receipt":
				receipt := map[string]string{"qualified_recovery_sha256": post.Review, "audit_sha256": strings.Repeat("f", 64), "installation_clock_sha256": managedLateInstallationClockSHA(clock)}
				if mode == "changed receipt clock" {
					receipt["installation_clock_sha256"] = strings.Repeat("1", 64)
				}
				if mode == "changed audit" {
					receipt["audit_sha256"] = "not-a-real-audit"
				}
				if mode == "extra receipt" {
					receipt["reset_at"] = "retry"
				}
				entry.Receipt = mustProviderRecoveryJSON(receipt)
			case "changed intent":
				entry.IntentSHA256 = strings.Repeat("1", 64)
			case "changed source":
				post.SourceSHA = strings.Repeat("1", 64)
			case "changed checker":
				a.Plan.CheckerSHA = strings.Repeat("1", 64)
			case "changed fingerprint":
				post.Fingerprint = strings.Repeat("1", 64)
			case "changed session":
				post.Ceremony.BootstrapSessionID = "00000000-0000-4000-8000-000000000002"
			case "changed expiry":
				post.SessionExpires = post.SessionExpires.Add(time.Minute)
				post.LeafNotAfter = post.SessionExpires
			case "clock symlink":
				other := path + ".retained"
				if os.Rename(path, other) != nil || os.Symlink(other, path) != nil {
					t.Fatal("isolated symlink fixture unavailable")
				}
			case "public clock":
				if os.Chmod(path, 0644) != nil {
					t.Fatal("isolated mode fixture unavailable")
				}
			}
			_, err = validateManagedLateInstallationAdmission(store, e, post, a, entry)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s acceptance differs: %v", mode, err)
			}
		})
	}
}

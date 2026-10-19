package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualRetry403Fixture(t *testing.T) (environmentCreateBootstrapRuntime, createFoundationRecoveryArtifact, []byte) {
	t.Helper()
	runtime, _, _ := managedLateResidualCompletedHandoffFixture(t)
	e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
	e.Journal.Entries = append([]environmentCreateJournalEntry(nil), e.Journal.Entries[:403]...)
	e.Journal.Entries[395].State = "pending"
	e.Journal.Entries[395].CompletedAt = time.Time{}
	e.Journal.Entries[395].Receipt = nil
	var original createFoundationRecoveryArtifact
	if err := createOpenBaoACKCompletionRead(e.Store, filepath.Join("deployment/create/openbao-recovery", r.ResidualAuthority.Review, "qualification.json"), &original); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-5 * time.Minute)
	original.Plan.CheckerSHA = strings.Repeat("1", 64)
	original.QualifiedAt, original.ValidUntil = started.Add(-time.Minute), started.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &original)
	phase, err := newManagedLateResidualCleanupAuthority(r, original, now.Add(70*time.Minute), started, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(original)))
	if err != nil {
		t.Fatal(err)
	}
	r.ResidualAuthority = phase
	write := func(relative string, value any) {
		t.Helper()
		path := filepath.Join(e.Store.Root, relative)
		if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, mustProviderRecoveryJSON(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("deployment/create/openbao-recovery", phase.Review, "qualification.json"), original)
	write(filepath.Join("deployment/create/openbao-recovery", phase.Review, managedLateRemainingFilename), r)
	for _, witness := range []bool{true, false} {
		write(managedLateResidualCleanupPath(r.ParentSHA, witness), phase)
	}
	for i := 400; i < 403; i++ {
		e.Journal.Entries[i].StartedAt = started.Add(time.Duration(i-399) * time.Second)
		e.Journal.Entries[i].CompletedAt = e.Journal.Entries[i].StartedAt.Add(time.Second)
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if !managedLateResidualCleanupProgressValid(e.Journal, r) {
		t.Fatal("exact403 fixture lost the original400 or approved retirement proof")
	}
	var fresh createFoundationRecoveryArtifact
	if err := json.Unmarshal(mustProviderRecoveryJSON(original), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Plan.ParentJournalSHA = deploymentplan.Digest(e.Journal)
	fresh.Plan.KubeconfigSHA, fresh.Plan.FoundationSHA, fresh.Plan.FoundationJournalSHA = strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)
	fresh.Plan.Plan.Environment, fresh.Plan.Plan.Stack, fresh.Plan.Plan.ConfigurationSHA256 = e.Journal.Environment, e.Journal.Stack, strings.Repeat("5", 64)
	fresh.Plan.CheckerSHA, err = environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	fresh.QualifiedAt, fresh.ValidUntil = now.Add(-time.Minute), now.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &fresh)
	write(filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json"), fresh)
	return runtime, fresh, mustProviderRecoveryJSON(phase)
}

func TestManagedLateResidualRetry403UsesFreshQualificationAndOriginalAuthority(t *testing.T) {
	runtime, fresh, originalRaw := managedLateResidualRetry403Fixture(t)
	e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
	original := *r.ResidualAuthority
	if !managedLateRemainingSelected(e.Journal) || !managedLateTerminalSourceSelected(e.Journal) || managedLatePostNativeCleanupSelected(e.Journal) || fresh.Plan.CheckerSHA == original.CheckerSHA || r.Review == original.Review || fresh.Plan.ParentJournalSHA == original.SourceSHA {
		t.Fatal("retry did not separate current403/current checker from original400 authority")
	}
	if err := verifyManagedLateRecordedResidualCleanupProgress(e.Store, e); err != nil {
		t.Fatal("pure transport scope could not authenticate the recorded403 chain", err)
	}
	before := deploymentplan.Digest([]any{e.Journal, e.owner})
	overall, stop := context.WithDeadline(context.Background(), original.OverallDeadline.Add(time.Minute))
	defer stop()
	if _, err := admitManagedLateRemaining(overall, e.Store, e, fresh, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(fresh)), r, original.OverallDeadline.Add(time.Minute)); err != nil {
		t.Fatal("fresh403 qualification could not reuse the prior authority", err)
	}
	got, err := readManagedLateResidualCleanupPair(e.Store, r.ParentSHA)
	if err != nil || !bytes.Equal(got, originalRaw) || deploymentplan.Digest(r.ResidualAuthority) != deploymentplan.Digest(original) || !r.ResidualAuthority.Deadline.Equal(original.Deadline) || !r.ResidualAuthority.OverallDeadline.Equal(original.OverallDeadline) || before != deploymentplan.Digest([]any{e.Journal, e.owner}) {
		t.Fatal("retry rewrote original clock, checker, raw authority or accepted journal", err)
	}
}

func TestManagedLateResidualRetry403RejectsUnprovedProgress(t *testing.T) {
	for _, mode := range []string{"wrong-uid", "foreign-resource", "wrong-intent", "missing-absence", "pending", "zero-completion", "future-within-grant", "duplicate", "unknown-tail", "late", "changed400", "missing-authority", "orphan-authority", "missing-recorded-descriptor", "changed-recorded-go", "short-caller"} {
		t.Run(mode, func(t *testing.T) {
			runtime, fresh, originalRaw := managedLateResidualRetry403Fixture(t)
			e, r := runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining
			entry := &e.Journal.Entries[400]
			overall := r.ResidualAuthority.OverallDeadline
			switch mode {
			case "wrong-uid", "foreign-resource", "missing-absence":
				var receipt deploymentplan.Object
				if err := json.Unmarshal(entry.Receipt, &receipt); err != nil {
					t.Fatal(err)
				}
				if mode == "wrong-uid" {
					receipt["uid"] = r.ResidualCleanup.Approval.Objects[1].UID
				} else if mode == "foreign-resource" {
					receipt["resource_id"] = r.ResidualCleanup.Approval.Objects[1].ResourceID
				} else {
					receipt["absent"] = false
				}
				entry.Receipt = mustProviderRecoveryJSON(receipt)
			case "wrong-intent":
				entry.IntentSHA256 = strings.Repeat("f", 64)
			case "pending":
				entry.State, entry.CompletedAt, entry.Receipt = "pending", time.Time{}, nil
			case "zero-completion":
				entry.CompletedAt = time.Time{}
			case "future-within-grant":
				entry.CompletedAt = time.Now().UTC().Add(time.Minute)
			case "duplicate":
				e.Journal.Entries = append(e.Journal.Entries, *entry)
			case "unknown-tail":
				entry.Key = "bootstrap-copy-retire-foreign-session"
			case "late":
				entry.CompletedAt = r.ResidualAuthority.Deadline.Add(time.Nanosecond)
			case "changed400":
				e.Journal.Entries[399].Receipt = []byte(`{"changed":true}`)
			case "missing-authority", "orphan-authority":
				for _, witness := range []bool{false, true} {
					if mode == "orphan-authority" && witness {
						continue
					}
					if err := os.Remove(filepath.Join(e.Store.Root, managedLateResidualCleanupPath(r.ParentSHA, witness))); err != nil {
						t.Fatal(err)
					}
				}
			case "short-caller":
				overall = r.ResidualAuthority.Deadline.Add(-time.Nanosecond)
			case "missing-recorded-descriptor":
				if err := os.Remove(filepath.Join(e.Store.Root, "deployment/create/openbao-recovery", r.ResidualAuthority.Review, managedLateRemainingFilename)); err != nil {
					t.Fatal(err)
				}
			case "changed-recorded-go":
				if err := os.WriteFile(filepath.Join(e.Store.Root, "deployment/create/openbao-recovery", r.ResidualAuthority.Review, "qualification.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithDeadline(context.Background(), overall)
			defer stop()
			if mode != "short-caller" && verifyManagedLateRecordedResidualCleanupProgress(e.Store, e) == nil {
				t.Fatal("pure transport scope accepted unproved recorded progress")
			}
			if mode != "missing-recorded-descriptor" {
				if _, err := admitManagedLateResidualCleanup(ctx, e.Store, e, fresh, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(fresh)), r, overall); err == nil {
					t.Fatal("unproved403 retry was accepted")
				}
			}
			if mode != "missing-authority" && mode != "orphan-authority" {
				raw, err := readManagedLateResidualCleanupPair(e.Store, r.ParentSHA)
				if err != nil || !bytes.Equal(raw, originalRaw) {
					t.Fatal("refused retry altered existing authority", err)
				}
			}
		})
	}
}

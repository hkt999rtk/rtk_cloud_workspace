package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateSourceWriteFixture(t *testing.T) (secretStore, *environmentCreateExecution, deploymentConfig, map[string]string, string) {
	t.Helper()
	store, e, cfg, paths := managedLateSourceReuseNegativeFixture(t)
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	var err error
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		t.Fatal(err)
	}
	e.running, err = deploymentplan.LockExecutionOwner(fence, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.runningInfo, err = e.running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	e.JournalPath = filepath.Join(store.Root, e.owner.Journal)
	e.Directory = filepath.Dir(e.JournalPath)
	if err := e.Check(context.Background()); err != nil {
		t.Fatal("isolated original source fence refused", err)
	}
	return store, e, cfg, paths, strings.Repeat("7", 64)
}

func TestManagedLateSourceWriteEndsOldPhaseBeforeExistingReviewSnapshots(t *testing.T) {
	store, e, cfg, _, review := managedLateSourceWriteFixture(t)
	const kind = "isolated-strong-original-source"
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"verified": "old-phase"}); err != nil {
		t.Fatal(err)
	}
	old := e.lateSourceReuse
	shared := *e
	parentBefore, ownerBefore := deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.owner)
	if err := writeCreateRecoveryAdmissionSource(context.Background(), store, e, review); err != nil {
		t.Fatal("exact local snapshots were refused", err)
	}
	if managedLateSourceReuseActive(&shared) || e.lateSourceReuse == old || !managedLateSourceReuseActive(e) {
		t.Fatal("local writes retained old authority or omitted the empty new phase")
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		t.Fatal("new phase did not capture both exact snapshot files", err)
	}
	var history map[string]string
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &history); err != nil || hit {
		t.Fatal("post-write authentication borrowed a pre-write derivation", err)
	}
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"verified": "new-strong-derivation"}); err != nil {
		t.Fatal(err)
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &history); err != nil || !hit || history["verified"] != "new-strong-derivation" {
		t.Fatal("same new phase could not reuse its independently rederived source", err)
	}
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	var recordedOwner environmentCreateOwner
	var recordedJournal environmentCreateJournal
	if readExecutionJSON(filepath.Join(base, "source-owner.json"), &recordedOwner) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &recordedJournal) != nil || deploymentplan.Digest(recordedOwner) != ownerBefore || deploymentplan.Digest(recordedJournal) != parentBefore || deploymentplan.Digest(e.Journal) != parentBefore || deploymentplan.Digest(e.owner) != ownerBefore {
		t.Fatal("snapshot publication changed the original parent or owner")
	}
}

func TestManagedLateSourceWriteUnframedAdditionStillInvalidatesAuthority(t *testing.T) {
	store, e, cfg, _, review := managedLateSourceWriteFixture(t)
	const kind = "isolated-strong-original-source"
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, map[string]string{"verified": "old-phase"}); err != nil {
		t.Fatal(err)
	}
	// Reproduce the old execute ordering: an existing qualified review was
	// already an input when these two new files were added without a boundary.
	for name, value := range map[string]any{"source-owner.json": e.owner, "source-journal.json": e.Journal} {
		managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create/openbao-recovery", review, name), mustProviderRecoveryJSON(value))
	}
	var history map[string]string
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &history); err == nil || hit || managedLateSourceReuseActive(e) {
		t.Fatal("unframed local input additions bypassed the manifest guard")
	}
}

func TestManagedLateSourceWriteRefusesChangedInputOrPartialSnapshot(t *testing.T) {
	for _, mode := range []string{"changed-input", "changed-parent", "changed-owner", "foreign-store", "partial-snapshot"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, paths, review := managedLateSourceWriteFixture(t)
			selected := store
			before := deploymentplan.Digest(e.Journal)
			switch mode {
			case "changed-input":
				managedLateSourceReuseNegativeWrite(t, store, paths["material"], []byte(`{"changed":"qualified-input"}`))
			case "changed-parent":
				managedLateSourceReuseNegativeWrite(t, store, paths["source"], []byte(`{"changed":"parent"}`))
			case "changed-owner":
				managedLateSourceReuseNegativeWrite(t, store, paths["owner"], []byte(`{"changed":"owner"}`))
			case "foreign-store":
				selected.Environment = "staging"
			case "partial-snapshot":
				managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create/openbao-recovery", review, "source-owner.json"), []byte(`{"foreign":"snapshot"}`))
				fresh, err := newManagedLateSourceReuse(store, e)
				if err != nil {
					t.Fatal(err)
				}
				e.lateSourceReuse = fresh
			}
			old := e.lateSourceReuse
			if err := writeCreateRecoveryAdmissionSource(context.Background(), selected, e, review); err == nil {
				t.Fatal("changed source or unknown partial output became admission authority", mode)
			}
			if e.lateSourceReuse != old || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("refused snapshot publication reset source authority or parent history")
			}
			if _, err := os.Lstat(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "source-journal.json")); !os.IsNotExist(err) {
				t.Fatal("failed boundary published a later snapshot")
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the common execute entry's private-file fingerprint, followed by the
// normal profile's actual six-GO and first-clock validators. Normal completion
// must remain ineligible for the generic parent-intent appender.
func TestManagedLateNormalQualifiedExecutionEntryKeepsTypedAdmission(t *testing.T) {
	_, _, _, store, e, _, _ := createPendingIngressFixture(t)
	before, err := os.ReadFile(e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	n, artifact, started, now := normalTailAuthorityFixture(t)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	n.SourceSHA, n.CurrentSHA = deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.Journal)
	n.OwnerSHA, n.Approval.ParentSHA = deploymentplan.Digest(e.owner), e.Journal.PlanSHA256
	artifact.Plan.ParentSHA, artifact.Plan.ParentJournalSHA, artifact.Plan.ParentOwnerSHA = e.Journal.PlanSHA256, n.SourceSHA, n.OwnerSHA
	artifact.Plan.CheckerSHA = checker
	artifact.Plan.FoundationSHA, artifact.Plan.FoundationJournalSHA = strings.Repeat("6", 64), strings.Repeat("7", 64)
	artifact.Plan.Plan.ConfigurationSHA256 = strings.Repeat("8", 64)
	artifact.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	n.Review = deploymentplan.Digest(artifact.Plan)
	artifact.Qualification.PlanSHA256 = n.Review
	for i := range artifact.Qualification.Gates {
		artifact.Qualification.Gates[i].EvidenceSHA256 = n.Review
	}
	base := artifact.Plan
	relative := filepath.Join("deployment/create/openbao-recovery", n.Review, "qualification.json")
	raw := mustProviderRecoveryJSON(artifact)
	if err := store.write(relative, raw, false); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := readManagedLateQualificationFingerprint(store, artifact)
	if err != nil || fingerprint != environmentPreflightBytesSHA256(raw) {
		t.Fatal("normal execution rejected at its common qualified entry", err)
	}
	if !managedLateQualifiedArtifactValid(artifact, e, base, n.Review, now, managedLateNormalTailSchema) {
		t.Fatal("fresh normal entry lost exact source/checker/six-GO authority")
	}
	phase, err := newManagedLateNormalTailAuthority(n, artifact, fingerprint, started, now)
	if err != nil || phase == nil || !phase.Deadline.Equal(started.Add(90*time.Minute)) {
		t.Fatal("common fingerprint cannot enter the typed first-clock validator", err)
	}
	intent := mustProviderRecoveryJSON(map[string]string{"review_sha256": n.Review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": artifact.Plan.ParentJournalSHA})
	if beginManagedLateParentAdmission(context.Background(), store, e, artifact, fingerprint, intent, now) == nil {
		t.Fatal("normal profile gained a second generic parent admission")
	}
	for _, mode := range []string{"expired", "future", "nonrequired-gate", "failed-gate", "missing-gate", "gate-proof", "checker", "source", "unknown-schema", "artifact-bytes", "public-file", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			changed := artifact
			changed.Qualification.Gates = append([]deploymentplan.GateResult(nil), artifact.Qualification.Gates...)
			at := now
			switch mode {
			case "expired":
				at = artifact.ValidUntil
			case "future":
				at = artifact.QualifiedAt.Add(-time.Nanosecond)
			case "nonrequired-gate":
				changed.Qualification.Gates[0].Required = false
			case "failed-gate":
				changed.Qualification.Gates[0].Status = "BLOCKED"
			case "missing-gate":
				changed.Qualification.Gates = changed.Qualification.Gates[:5]
			case "gate-proof":
				changed.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("9", 64)
			case "checker":
				changed.Plan.CheckerSHA = strings.Repeat("9", 64)
			case "source":
				changed.Plan.ParentJournalSHA = strings.Repeat("9", 64)
			case "unknown-schema":
				changed.Plan.Schema = "rtk-cloud.create-managed-late-unqualified/v1"
			}
			if mode == "artifact-bytes" || mode == "public-file" || mode == "symlink" {
				path := filepath.Join(store.Root, relative)
				switch mode {
				case "artifact-bytes":
					if err := store.write(relative, append(raw, '\n'), true); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = store.write(relative, raw, true) }()
				case "public-file":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
					defer os.Chmod(path, 0600)
				case "symlink":
					target := filepath.Join(t.TempDir(), "qualification.json")
					if os.WriteFile(target, raw, 0600) != nil || os.Remove(path) != nil || os.Symlink(target, path) != nil {
						t.Fatal("isolated symlink setup failed")
					}
					defer func() { _ = os.Remove(path); _ = store.write(relative, raw, false) }()
				}
				current, err := readManagedLateQualificationFingerprint(store, changed)
				if err == nil && current == fingerprint {
					t.Fatal("changed or unsafe common-entry artifact retained its original fingerprint")
				}
				return
			}
			if mode == "unknown-schema" {
				if _, err := readManagedLateQualificationFingerprint(store, changed); err == nil {
					t.Fatal("unknown qualified schema accepted")
				}
				return
			}
			review := n.Review
			if mode == "checker" || mode == "source" {
				review = deploymentplan.Digest(changed.Plan)
				changed.Qualification.PlanSHA256 = review
				for i := range changed.Qualification.Gates {
					changed.Qualification.Gates[i].EvidenceSHA256 = review
				}
			}
			if managedLateQualifiedArtifactValid(changed, e, base, review, at, managedLateNormalTailSchema) {
				t.Fatal("unqualified normal execution entry accepted")
			}
		})
	}
	after, err := os.ReadFile(e.JournalPath)
	if err != nil || !bytes.Equal(before, after) || len(e.Journal.Entries) != 433 || e.Journal.Entries[431].State != "pending" {
		t.Fatal("entry validation changed original pending ingress history")
	}
	for _, witness := range []bool{false, true} {
		if _, err := os.Lstat(filepath.Join(store.Root, managedLateNormalTailPath(e.Journal.PlanSHA256, witness))); !os.IsNotExist(err) {
			t.Fatal("qualified entry validation published a normal grant")
		}
	}
}

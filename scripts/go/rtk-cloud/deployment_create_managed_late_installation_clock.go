package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateInstallationClockFilename = "managed-late-installation-clock.json"
const managedLateInstallationClockSchema = "rtk-cloud.create-managed-late-installation-clock/v1"
const managedLateInstallationPolicy = "first-admission-fixed-90m-current-managed-authority/v1"

type createManagedLateInstallationClock struct {
	Schema, Parent, Review, SourceSHA, OwnerSHA, CheckerSHA, SessionID, Fingerprint string
	DescriptorSHA, QualificationSHA                                                 string
	StartedAt, Deadline, OverallDeadline, SessionExpires, LeafNotAfter              time.Time
}

func managedLateInstallationClockPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", managedLateInstallationClockFilename)
}

func managedLateInstallationClockDeadline(epoch, overall, leaf, session time.Time) time.Time {
	deadline := epoch.Add(managedLateOverallLimit)
	for _, bound := range []time.Time{overall, leaf.Add(-managedLateExpiryReserve), session.Add(-managedLateExpiryReserve)} {
		if bound.Before(deadline) {
			deadline = bound
		}
	}
	return deadline
}

func managedLateInstallationClockValid(clock *createManagedLateInstallationClock) bool {
	if clock == nil || clock.Schema != managedLateInstallationClockSchema || !environmentBootstrapOwnerUUID.MatchString(clock.SessionID) {
		return false
	}
	for _, sha := range []string{clock.Parent, clock.Review, clock.SourceSHA, clock.OwnerSHA, clock.CheckerSHA, clock.Fingerprint, clock.DescriptorSHA, clock.QualificationSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	if clock.StartedAt.IsZero() || clock.OverallDeadline.IsZero() || clock.SessionExpires.IsZero() || clock.LeafNotAfter.IsZero() || !clock.LeafNotAfter.Equal(clock.SessionExpires) || clock.OverallDeadline.After(clock.StartedAt.Add(managedLateOverallLimit)) || !clock.Deadline.Equal(managedLateInstallationClockDeadline(clock.StartedAt, clock.OverallDeadline, clock.LeafNotAfter, clock.SessionExpires)) || clock.Deadline.Sub(clock.StartedAt) < managedLateFinalizerLimit {
		return false
	}
	return true
}

func newManagedLateInstallationClock(post *createManagedLatePostMint, artifact createFoundationRecoveryArtifact, ownerSHA string, overallDeadline, epoch time.Time) (*createManagedLateInstallationClock, error) {
	bad := func() (*createManagedLateInstallationClock, error) {
		return nil, createOpenBaoACKBlock("late-first-installation-clock")
	}
	if post == nil || post.InstallationPolicy != managedLateInstallationPolicy || !post.NativeStartedAt.IsZero() || !post.NativeDeadline.IsZero() || post.InstallationClock != nil || post.Review != deploymentplan.Digest(artifact.Plan) || artifact.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || artifact.Plan.Schema != managedLatePostMintSchema || artifact.Plan.ParentJournalSHA != post.SourceSHA || artifact.Plan.ParentOwnerSHA != post.SourceOwnerSHA || ownerSHA != post.SourceOwnerSHA || artifact.Plan.Materials[managedLatePostMintMaterial+"descriptor"] != managedLatePostMintDigest(post) || epoch.IsZero() || epoch.Before(artifact.QualifiedAt) || !epoch.Before(artifact.ValidUntil) || !artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(15*time.Minute)) || artifact.Qualification.PlanSHA256 != post.Review || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		gate := artifact.Qualification.Gates[i]
		if gate.ID != id || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != post.Review {
			return bad()
		}
	}
	clock := &createManagedLateInstallationClock{Schema: managedLateInstallationClockSchema, Parent: artifact.Plan.ParentSHA, Review: post.Review, SourceSHA: post.SourceSHA, OwnerSHA: ownerSHA, CheckerSHA: artifact.Plan.CheckerSHA, SessionID: post.Ceremony.BootstrapSessionID, Fingerprint: post.Fingerprint, DescriptorSHA: managedLatePostMintDigest(post), QualificationSHA: deploymentplan.Digest(artifact), StartedAt: epoch.UTC(), OverallDeadline: overallDeadline.UTC(), SessionExpires: post.SessionExpires, LeafNotAfter: post.LeafNotAfter}
	clock.Deadline = managedLateInstallationClockDeadline(clock.StartedAt, clock.OverallDeadline, clock.LeafNotAfter, clock.SessionExpires)
	if !managedLateInstallationClockValid(clock) {
		return bad()
	}
	return clock, nil
}

// Only the actual admission hook invokes this exclusive producer. The parent-
// stable path prevents a later review from hiding an uncertain first outcome.
func writeManagedLateInstallationClock(store secretStore, clock *createManagedLateInstallationClock) (string, error) {
	if store.Environment != "dev" || !managedLateInstallationClockValid(clock) {
		return "", createOpenBaoACKBlock("late-first-installation-clock")
	}
	relative := managedLateInstallationClockPath(clock.Parent)
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) {
		return "", createOpenBaoACKBlock("late-first-installation-clock-path")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return "", createOpenBaoACKBlock("late-first-installation-clock-path")
	}
	raw, err := json.Marshal(clock)
	if err != nil || writeExclusivePrivateDeploymentJSON(path, raw) != nil {
		return "", createOpenBaoACKBlock("late-first-installation-clock-exists-or-unavailable")
	}
	return environmentPreflightBytesSHA256(raw), nil
}

func readManagedLateInstallationClock(store secretStore, parent string) (*createManagedLateInstallationClock, error) {
	if !environmentPreflightDigest.MatchString(parent) {
		return nil, createOpenBaoACKBlock("late-first-installation-clock-parent")
	}
	relative := managedLateInstallationClockPath(parent)
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) {
		return nil, createOpenBaoACKBlock("late-first-installation-clock-path")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if _, statErr := os.Lstat(dir); statErr == nil && validateEnvironmentPreflightDirectory(dir) != nil || statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return nil, createOpenBaoACKBlock("late-first-installation-clock-path")
			}
			if dir == store.Root {
				break
			}
		}
		return nil, nil
	} else if err != nil {
		return nil, createOpenBaoACKBlock("late-first-installation-clock-path")
	}
	var clock createManagedLateInstallationClock
	raw, readErr := readBoundedEnvironmentFile(path, true)
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readErr != nil || decodeEnvironmentJSON(raw, &clock) != nil {
		return nil, createOpenBaoACKBlock("late-first-installation-clock-content")
	}
	canonical, _ := json.Marshal(&clock)
	if !bytes.Equal(raw, canonical) || !managedLateInstallationClockValid(&clock) || clock.Parent != parent {
		return nil, createOpenBaoACKBlock("late-first-installation-clock-content")
	}
	return &clock, nil
}

func validateManagedLateInstallationAdmission(store secretStore, e *environmentCreateExecution, post *createManagedLatePostMint, artifact createFoundationRecoveryArtifact, entry environmentCreateJournalEntry) (*createManagedLateInstallationClock, error) {
	bad := func() (*createManagedLateInstallationClock, error) {
		return nil, createOpenBaoACKBlock("late-first-installation-admission-clock")
	}
	if e == nil || post == nil || entry.Key != "openbao-recovery-admission-"+post.Review || entry.Phase != "recovery" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) {
		return bad()
	}
	clock, err := readManagedLateInstallationClock(store, e.Journal.PlanSHA256)
	if err != nil || clock == nil {
		return bad()
	}
	expected, err := newManagedLateInstallationClock(post, artifact, post.SourceOwnerSHA, clock.OverallDeadline, entry.StartedAt)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(clock) || clock.StartedAt.After(time.Now().UTC()) || entry.CompletedAt.After(clock.Deadline) || entry.CompletedAt.After(time.Now().UTC()) {
		return bad()
	}
	var receipt map[string]string
	intent := map[string]string{"review_sha256": post.Review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": post.SourceSHA}
	if entry.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || len(receipt) != 3 || receipt["qualified_recovery_sha256"] != post.Review || !environmentPreflightDigest.MatchString(receipt["audit_sha256"]) || receipt["installation_clock_sha256"] != managedLateInstallationClockSHA(clock) {
		return bad()
	}
	return clock, nil
}

func managedLateInstallationClockSHA(clock *createManagedLateInstallationClock) string {
	raw, err := json.Marshal(clock)
	if err != nil {
		return ""
	}
	return environmentPreflightBytesSHA256(raw)
}

func managedLateInstallationClockAbsent(store secretStore, parent string) error {
	clock, err := readManagedLateInstallationClock(store, parent)
	if err != nil {
		return err
	}
	if clock != nil {
		return createOpenBaoACKBlock("late-first-installation-clock-already-committed")
	}
	return nil
}

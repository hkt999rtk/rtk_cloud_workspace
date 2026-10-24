package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func readManagedLateQualificationFingerprint(store secretStore, expected createFoundationRecoveryArtifact) (string, error) {
	if expected.Plan.Schema != managedLateSessionSchema && expected.Plan.Schema != managedLateSeedForwardSchema && expected.Plan.Schema != managedLatePostMintSchema && expected.Plan.Schema != managedLateExpiredRenewalSchema && expected.Plan.Schema != managedLateNormalTailSchema {
		return "", createOpenBaoACKBlock("late-parent-qualification")
	}
	path := filepath.Join(store.Root, "deployment/create/openbao-recovery", deploymentplan.Digest(expected.Plan), "qualification.json")
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return "", createOpenBaoACKBlock("late-parent-qualification-changed")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var current createFoundationRecoveryArtifact
	if err != nil || decodeEnvironmentJSON(raw, &current) != nil || deploymentplan.Digest(current) != deploymentplan.Digest(expected) {
		return "", createOpenBaoACKBlock("late-parent-qualification-changed")
	}
	return environmentPreflightBytesSHA256(raw), nil
}

// Current checks may consume most of the reuse window. Check the same exact
// qualification again after initialized-audit reads, before creating any new
// parent intent. Refusal therefore leaves the original source journal intact.
func beginManagedLateParentAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, expected createFoundationRecoveryArtifact, bytesSHA string, intent json.RawMessage, now time.Time) error {
	if e == nil || e.Check(ctx) != nil || store.Root != e.Store.Root || store.Environment != "dev" || expected.Plan.Schema != managedLateSessionSchema && expected.Plan.Schema != managedLateSeedForwardSchema && expected.Plan.Schema != managedLatePostMintSchema && expected.Plan.Schema != managedLateExpiredRenewalSchema || expected.Plan.ParentSHA != e.Journal.PlanSHA256 || expected.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) || expected.Plan.ParentOwnerSHA != deploymentplan.Digest(e.owner) || !environmentPreflightDigest.MatchString(bytesSHA) {
		return createOpenBaoACKBlock("late-parent-admission-source")
	}
	currentSHA, err := readManagedLateQualificationFingerprint(store, expected)
	if err != nil || currentSHA != bytesSHA {
		return createOpenBaoACKBlock("late-parent-qualification-changed")
	}
	if expected.QualifiedAt.IsZero() || now.Before(expected.QualifiedAt) || !expected.ValidUntil.Equal(expected.QualifiedAt.Add(15*time.Minute)) || !now.Before(expected.ValidUntil) {
		return createOpenBaoACKBlock("late-parent-qualification-expired-or-future")
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || checker != expected.Plan.CheckerSHA || expected.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || expected.Qualification.PlanSHA256 != deploymentplan.Digest(expected.Plan) || expected.Qualification.Decision != "GO" || len(expected.Qualification.Gates) != 6 {
		return createOpenBaoACKBlock("late-parent-qualified-checker")
	}
	for index, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		gate := expected.Qualification.Gates[index]
		if gate.ID != id || gate.Status != "PASS" || !gate.Required || gate.EvidenceSHA256 != expected.Qualification.PlanSHA256 {
			return createOpenBaoACKBlock("late-parent-six-qualified-gates")
		}
	}
	key := "openbao-recovery-admission-" + expected.Qualification.PlanSHA256
	wanted := map[string]string{"review_sha256": expected.Qualification.PlanSHA256, "qualification_sha256": deploymentplan.Digest(expected), "original_parent_journal_sha256": expected.Plan.ParentJournalSHA}
	if activeTransportJournalDigest(intent) != activeTransportJournalDigest(wanted) {
		return createOpenBaoACKBlock("late-parent-admission-intent")
	}
	// File/checker reads are part of this final check, not a fresh authority.
	// Use the actual later clock at the journal boundary as well.
	if checkedAt := time.Now().UTC(); checkedAt.After(now) {
		now = checkedAt
	}
	if now.Before(expected.QualifiedAt) || !now.Before(expected.ValidUntil) {
		return createOpenBaoACKBlock("late-parent-qualification-expired-or-future")
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	invalidateManagedLateSourceReuse(e)
	return e.BeginEntry(key, "recovery", intent, now)
}

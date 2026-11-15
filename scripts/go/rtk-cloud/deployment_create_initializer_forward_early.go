package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// During original preflight an admitted q1 may already have written its one
// successor. Authenticate the old admission and frozen proof before allowing
// that recorded owner body; an unadmitted qualification is not sufficient.
func earlyInitializerForwardSuccessor(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, original *deploymentplan.Plan, source deploymentplan.Journal) (*deploymentplan.Plan, error) {
	return earlyInitializerForwardSuccessorWithAdmission(ctx, store, e, b, original, source, authenticatedInitializerForwardAdmission)
}

type initializerForwardAdmissionLoader func(secretStore, *environmentCreateExecution, createInitializerForwardProof) (issuerEnrollmentContinuationProof, string, error)

func authenticatedInitializerForwardAdmission(store secretStore, e *environmentCreateExecution, q1 createInitializerForwardProof) (issuerEnrollmentContinuationProof, string, error) {
	admitted, admission, ok, err := loadPartialSessionAdmission(store, e)
	if err != nil || !ok {
		return issuerEnrollmentContinuationProof{}, "", createProviderRecoveryBlock("original partial session admission is unavailable")
	}
	if forward, forwardSHA, found, err := loadPartialSessionTrustForwardAdmission(store, e, admitted, admission); err != nil {
		return issuerEnrollmentContinuationProof{}, "", err
	} else if found && forwardSHA == q1.OriginalAdmissionSHA256 {
		admitted, admission = forward, forwardSHA
	}
	if admission != q1.OriginalAdmissionSHA256 {
		return issuerEnrollmentContinuationProof{}, "", createProviderRecoveryBlock("original initializer admission differs")
	}
	return admitted, admission, nil
}

func earlyInitializerForwardSuccessorWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, original *deploymentplan.Plan, source deploymentplan.Journal, load initializerForwardAdmissionLoader) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("original application owner or admitted initializer successor changed")
	}
	if e == nil || e.Store != store || b == nil || original == nil || load == nil || e.Check(ctx) != nil {
		return bad()
	}
	const prefix = "initializer-forward-"
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		item := &e.Journal.Entries[i]
		if item.Phase == "initializer-forward" && strings.HasPrefix(item.Key, prefix) {
			if entry != nil {
				return bad()
			}
			entry = item
		}
	}
	if entry == nil {
		return nil, nil
	}
	review := strings.TrimPrefix(entry.Key, prefix)
	q1, update, retain, err := loadInitializerForwardBundle(store, e, review)
	if err != nil {
		return bad()
	}
	if entry.Phase != "initializer-forward" || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") || entry.StartedAt.Before(q1.QualifiedAt) || q1.OriginalPlanSHA256 != original.SHA256() || q1.OriginalJournalSHA256 != deploymentplan.Digest(source) {
		return bad()
	}
	startedDeadline, err := initializerEffectiveDeadline(ctx, store, e, q1, entry.StartedAt)
	if err != nil || !entry.StartedAt.Before(startedDeadline) {
		return bad()
	}
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "original_admission_sha256": q1.OriginalAdmissionSHA256, "original_forward_sha256": q1.FailedForwardSHA256, "update_plan_sha256": q1.UpdatePlanSHA256, "retain_plan_sha256": q1.RetainPlanSHA256})
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.IntentSHA256 != sha {
		return bad()
	}
	q0, _, _, err := loadFailedForwardBundle(store, e, q1.FailedForwardSHA256)
	if err != nil {
		return bad()
	}
	admitted, admission, err := load(store, e, q1)
	if err != nil {
		return bad()
	}
	if admission != q1.OriginalAdmissionSHA256 || validateInitializerForward(ctx, store, e, q1, q0, admitted, admission, q1.ExecutionDeadline, update, retain) != nil {
		return bad()
	}
	mountUpdate, found, err := earlyInitializerMountForwardSuccessor(ctx, store, e, b, q1, q0, admitted, admission, update, retain)
	if err != nil {
		return bad()
	}
	if found {
		return mountUpdate, nil
	}
	if verifyInitializerForwardOwners(ctx, store, e, b, q1, update) != nil {
		return bad()
	}
	stage, err := readEnvironmentCreateStageInput(store, e, q1.UpdateStage)
	if err != nil {
		return bad()
	}
	if stage == nil {
		return nil, nil
	}
	if stage.SHA256() != update.SHA256() || len(update.Resources) != 1 {
		return bad()
	}
	j, exists, err := stage.ReadExecution(store.Root)
	if err != nil {
		return bad()
	}
	if !exists {
		return nil, nil
	}
	target := update.Resources[0].ID
	written := false
	for _, id := range j.Completed {
		if id == target {
			written = true
		}
	}
	if j.Failed == target && j.Owners[target] == source.Owners[target] {
		written = true
	}
	if !written {
		return nil, nil
	}
	if j.Owners[target] == "" || j.Owners[target] != source.Owners[target] {
		return bad()
	}
	return update, nil
}

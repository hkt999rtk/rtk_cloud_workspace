package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

func earlyInitializerMountForwardSuccessor(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admission string, prior, priorRetain *deploymentplan.Plan) (*deploymentplan.Plan, bool, error) {
	bad := func() (*deploymentplan.Plan, bool, error) {
		return nil, true, createProviderRecoveryBlock("admitted private state successor or recorded owner changed")
	}
	const prefix = "initializer-mount-forward-"
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		item := &e.Journal.Entries[i]
		if item.Phase == "initializer-mount-forward" && strings.HasPrefix(item.Key, prefix) {
			if entry != nil {
				return bad()
			}
			entry = item
		}
	}
	if entry == nil {
		return nil, false, nil
	}
	review := strings.TrimPrefix(entry.Key, prefix)
	q2, update, retain, err := loadInitializerMountForwardBundle(store, e, review)
	if err != nil {
		return bad()
	}
	startedDeadline, err := initializerEffectiveDeadline(ctx, store, e, q1, entry.StartedAt)
	if err != nil || q2.ExecutionDeadline.After(startedDeadline) || !entry.StartedAt.Before(startedDeadline) {
		return bad()
	}
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "initializer_qualification_sha256": q2.InitializerQualificationSHA256, "previous_update_journal_sha256": q2.PreviousUpdateJournalSHA256, "update_plan_sha256": q2.UpdatePlanSHA256, "retain_plan_sha256": q2.RetainPlanSHA256})
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.IntentSHA256 != sha || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") || entry.StartedAt.Before(q2.QualifiedAt) || validateInitializerMountForward(ctx, store, e, q2, q1, q0, admitted, admission, prior, priorRetain, update, retain, q2.ExecutionDeadline) != nil || verifyInitializerMountForwardOwners(ctx, store, e, b, q2, prior, update) != nil {
		return bad()
	}
	stage, err := readEnvironmentCreateStageInput(store, e, q2.UpdateStage)
	if err != nil {
		return bad()
	}
	if stage == nil {
		return prior, true, nil
	}
	if stage.SHA256() != update.SHA256() || len(update.Resources) != 1 {
		return bad()
	}
	j, exists, err := stage.ReadExecution(store.Root)
	if err != nil {
		return bad()
	}
	if !exists {
		return prior, true, nil
	}
	target := update.Resources[0].ID
	written := j.Failed == target && j.Owners[target] == update.Resources[0].UID
	for _, id := range j.Completed {
		if id == target && j.Owners[target] == update.Resources[0].UID {
			written = true
		}
	}
	if written {
		return update, true, nil
	}
	return prior, true, nil
}

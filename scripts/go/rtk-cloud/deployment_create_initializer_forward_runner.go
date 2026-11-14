package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// executeInitializerForward never alters the original failed child. Its
// successor children are independently fenced, durable and resumable.
func executeInitializerForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, update, retain *deploymentplan.Plan, deadline time.Time, execute func(context.Context, string, *deploymentplan.Plan) error, accept func(context.Context, *deploymentplan.Plan) error, clock func() time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("initializer successor requires its admitted parent, original deadline and native child plans")
	}
	if execute == nil || accept == nil || clock == nil || !clock().Before(deadline) {
		return bad()
	}
	if err := validateInitializerForward(ctx, store, e, q, q0, admitted, admissionSHA, q.ExecutionDeadline, update, retain); err != nil {
		return err
	}
	review := deploymentplan.Digest(q)
	key := "initializer-forward-" + review
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "original_admission_sha256": admissionSHA, "original_forward_sha256": q.FailedForwardSHA256, "update_plan_sha256": q.UpdatePlanSHA256, "retain_plan_sha256": q.RetainPlanSHA256})
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if deploymentplan.Digest(e.Journal) != q.SourceJournalSHA256 {
			if !issuerSupplementOnlyJournalAdvance(q.SourceJournal, e.Journal, q.OriginalAdmissionSHA256) {
				return bad()
			}
			if _, err := initializerEffectiveDeadline(ctx, store, e, q, clock().UTC()); err != nil {
				return bad()
			}
		}
		if err = e.BeginEntry(key, "initializer-forward", intent, clock().UTC()); err != nil {
			return err
		}
	} else if entry.Phase != "initializer-forward" || entry.IntentSHA256 != intentSHA || entry.StartedAt.Before(q.QualifiedAt) || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return bad()
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	startedDeadline, err := initializerEffectiveDeadline(ctx, store, e, q, entry.StartedAt)
	if err != nil || !entry.StartedAt.Before(startedDeadline) {
		return bad()
	}
	if err = execute(ctx, q.UpdateStage, update); err != nil {
		return err
	}
	// The retained plan was compiled against a predicted future inventory. Its
	// native executor re-reads all owners and admits the actual post-update body.
	if err = execute(ctx, q.RetainStage, retain); err != nil {
		return err
	}
	completedAt := clock().UTC()
	if err = completeInitializerForwardAlias(ctx, store, e, b, q, q0, admitted, admissionSHA, accept, completedAt); err != nil {
		return err
	}
	originalKey := "failed-stage-forward-" + q.FailedForwardSHA256
	originalReceipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": q.FailedForwardSHA256, "original_stage": "kubernetes/" + q0.FailedStage.Stage, "repair_stage": "kubernetes/" + q.CRLStage, "retain_stage": "kubernetes/" + q.RetainStage, "original_failed_journal_sha256": q.OriginalJournalSHA256, "initializer_qualification_sha256": review})
	originalEntry := createOpenBaoACKCompletionEntry(e.Journal, originalKey)
	if originalEntry == nil {
		return bad()
	}
	if originalEntry.State == "completed" {
		if !samePersistedJSONReceipt(originalEntry.Receipt, json.RawMessage(originalReceipt)) {
			return bad()
		}
	} else {
		if err = e.RecordReceipt(originalKey, json.RawMessage(originalReceipt)); err != nil {
			return err
		}
		if err = e.CompleteEntry(originalKey, clock().UTC()); err != nil {
			return err
		}
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "original_forward_sha256": q.FailedForwardSHA256, "update_stage": "kubernetes/" + q.UpdateStage, "retain_stage": "kubernetes/" + q.RetainStage})
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, json.RawMessage(receipt)) {
			return bad()
		}
		return e.Check(ctx)
	}
	if err = e.RecordReceipt(key, json.RawMessage(receipt)); err != nil {
		return err
	}
	return e.CompleteEntry(key, clock().UTC())
}

package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func executeInitializerMountForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, q1Update, q1Retain, update, retain *deploymentplan.Plan, deadline time.Time, execute func(context.Context, string, *deploymentplan.Plan) error, accept func(context.Context, *deploymentplan.Plan) error, alias func(context.Context, secretStore, *environmentCreateExecution, deploymentplan.Backend, createInitializerMountForwardProof, createInitializerForwardProof, createFailedForwardProof, issuerEnrollmentContinuationProof, string, func(context.Context, *deploymentplan.Plan) error, time.Time) error, clock func() time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("private state successor requires the admitted original and native owner chain")
	}
	if execute == nil || accept == nil || alias == nil || clock == nil || !clock().Before(deadline) {
		return bad()
	}
	if err := validateInitializerMountForward(ctx, store, e, q, q1, q0, admitted, admissionSHA, q1Update, q1Retain, update, retain, q.ExecutionDeadline); err != nil {
		return err
	}
	review := deploymentplan.Digest(q)
	key := "initializer-mount-forward-" + review
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "initializer_qualification_sha256": q.InitializerQualificationSHA256, "previous_update_journal_sha256": q.PreviousUpdateJournalSHA256, "update_plan_sha256": q.UpdatePlanSHA256, "retain_plan_sha256": q.RetainPlanSHA256})
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if deploymentplan.Digest(e.Journal) != q.SourceJournalSHA256 {
			if !issuerSupplementOnlyJournalAdvance(q.SourceJournal, e.Journal, q1.OriginalAdmissionSHA256) {
				return bad()
			}
			if _, err := initializerEffectiveDeadline(ctx, store, e, q1, clock().UTC()); err != nil {
				return bad()
			}
		}
		if err = e.BeginEntry(key, "initializer-mount-forward", intent, clock().UTC()); err != nil {
			return err
		}
	} else if entry.Phase != "initializer-mount-forward" || entry.IntentSHA256 != intentSHA || entry.StartedAt.Before(q.QualifiedAt) || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return bad()
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	startedDeadline, err := initializerEffectiveDeadline(ctx, store, e, q1, entry.StartedAt)
	if err != nil || !entry.StartedAt.Before(startedDeadline) {
		return bad()
	}
	for _, child := range []struct {
		stage string
		plan  *deploymentplan.Plan
	}{{q.UpdateStage, update}, {q.RetainStage, retain}} {
		key := "kubernetes/" + child.stage
		if completed := createOpenBaoACKCompletionEntry(e.Journal, key); completed != nil && completed.State == "completed" && originalStageSupersessionReceiptMatches(completed.Receipt) {
			if _, _, err := readEnvironmentCreateStageProgress(store, e, child.stage, child.plan); err != nil {
				return err
			}
			var receipt originalStageSupersessionReceipt
			if decodeEnvironmentJSON(completed.Receipt, &receipt) != nil {
				return bad()
			}
			if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, receipt.SuccessorStage); err != nil {
				return err
			}
		} else if completed := createOpenBaoACKCompletionEntry(e.Journal, key); completed != nil && completed.State == "completed" {
			// Later admitted sessions may have advanced these original owners.
			// Observe that exact history; never replay an older session template.
			accepted, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, key)
			if err != nil {
				return err
			}
			if accepted.SHA256() != child.plan.SHA256() {
				return bad()
			}
		} else {
			stage, candidate := child.stage, child.plan
			if stage == q.RetainStage {
				forwardStage, forwardPlan, err := qualifyInitializerSessionRetain(ctx, store, e, b, q, q0, retain)
				if err != nil {
					return err
				}
				if forwardPlan != nil {
					stage, candidate = forwardStage, forwardPlan
				}
			}
			if completed := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage); completed != nil && completed.State == "completed" {
				if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, "kubernetes/"+stage); err != nil {
					return err
				}
			} else if err := execute(ctx, stage, candidate); err != nil {
				return err
			}
		}
	}
	if err = alias(ctx, store, e, b, q, q1, q0, admitted, admissionSHA, accept, clock().UTC()); err != nil {
		return err
	}
	if err := completeInitializerSessionSources(ctx, store, e, b, q, q1, q0, clock().UTC()); err != nil {
		return err
	}
	retainedStage := q.RetainStage
	if original := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q0.FailedStage.Stage); original != nil {
		var accepted createFailedForwardAlias
		if decodeEnvironmentJSON(original.Receipt, &accepted) != nil {
			return bad()
		}
		if accepted.ServiceSessionQualificationSHA256 != "" {
			retainedStage = strings.TrimPrefix(accepted.RetainStage, "kubernetes/")
		}
	}
	receipts := []struct {
		key  string
		body deploymentplan.Object
	}{
		{"failed-stage-forward-" + q1.FailedForwardSHA256, deploymentplan.Object{"qualification_sha256": q1.FailedForwardSHA256, "original_stage": "kubernetes/" + q0.FailedStage.Stage, "repair_stage": "kubernetes/" + q1.CRLStage, "retain_stage": "kubernetes/" + retainedStage, "original_failed_journal_sha256": q.OriginalJournalSHA256, "initializer_qualification_sha256": q.InitializerQualificationSHA256, "initializer_mount_qualification_sha256": review}},
		{"initializer-forward-" + q.InitializerQualificationSHA256, deploymentplan.Object{"qualification_sha256": q.InitializerQualificationSHA256, "original_forward_sha256": q1.FailedForwardSHA256, "update_stage": "kubernetes/" + q1.UpdateStage, "retain_stage": "kubernetes/" + retainedStage, "initializer_mount_qualification_sha256": review}},
		{key, deploymentplan.Object{"qualification_sha256": review, "initializer_qualification_sha256": q.InitializerQualificationSHA256, "update_stage": "kubernetes/" + q.UpdateStage, "retain_stage": "kubernetes/" + retainedStage}},
	}
	for _, item := range receipts {
		raw := mustProviderRecoveryJSON(item.body)
		existing := createOpenBaoACKCompletionEntry(e.Journal, item.key)
		if existing == nil {
			return bad()
		}
		if existing.State == "completed" {
			if !samePersistedJSONReceipt(existing.Receipt, json.RawMessage(raw)) {
				return bad()
			}
			continue
		}
		if err = e.RecordReceipt(item.key, json.RawMessage(raw)); err != nil {
			return err
		}
		if err = e.CompleteEntry(item.key, clock().UTC()); err != nil {
			return err
		}
	}
	return e.Check(ctx)
}

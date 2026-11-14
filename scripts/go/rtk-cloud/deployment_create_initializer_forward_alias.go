package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Complete the original pending application stage only after the original q0
// CRL repair and q1 initializer successor have both been accepted. The source
// child journal remains immutable; native lease verification records one alias.
func completeInitializerForwardAlias(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, accept func(context.Context, *deploymentplan.Plan) error, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("initializer alias requires the original deadline and accepted same-owner successor")
	}
	if e == nil || e.Store != store || b == nil || accept == nil || now.IsZero() || now.Before(q1.QualifiedAt) || q1.FailedForwardSHA256 != deploymentplan.Digest(q0) || q1.OriginalAdmissionSHA256 != admissionSHA || e.Check(ctx) != nil {
		return bad()
	}
	effective, err := initializerEffectiveDeadline(ctx, store, e, q1, now)
	if err != nil || !now.Before(effective) {
		return bad()
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.After(effective) {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q0.FailedStage.Child.PlanSHA256 {
		return bad()
	}
	journal, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(journal) != q0.FailedStage.Child.JournalSHA256 || journal.Failed != q0.FailedStage.Child.FailedResource {
		return bad()
	}
	_, repair, oldRetain, err := loadFailedForwardBundle(store, e, q1.FailedForwardSHA256)
	if err != nil {
		return bad()
	}
	_, update, retained, err := loadInitializerForwardBundle(store, e, deploymentplan.Digest(q1))
	if err != nil {
		return bad()
	}
	_ = update
	if validateCreateFailedForwardProofBindings(q0, admitted, admissionSHA, q0.FailedStage, repair, oldRetain, q0.QualifiedAt) != nil {
		return bad()
	}
	alias := createFailedForwardAlias{QualificationSHA256: deploymentplan.Digest(q0), InitializerQualificationSHA256: deploymentplan.Digest(q1), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(journal), FailedResource: journal.Failed, RepairStage: "kubernetes/" + q1.CRLStage, RepairPlanSHA256: repair.SHA256(), RetainStage: "kubernetes/" + q1.RetainStage, RetainPlanSHA256: retained.SHA256()}
	verified, err := verifyInitializerSuccessorAlias(ctx, store, e, b, alias, original, journal, q0, repair, oldRetain)
	if err != nil || verified.SHA256() != retained.SHA256() {
		return bad()
	}
	key := "kubernetes/" + q0.FailedStage.Stage
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil || entry.Phase != q0.FailedStage.Stage || entry.IntentSHA256 != q0.FailedStage.ParentIntentSHA256 {
		return bad()
	}
	raw := mustProviderRecoveryJSON(alias)
	if entry.State == "completed" {
		var prior createFailedForwardAlias
		if decodeEnvironmentJSON(entry.Receipt, &prior) != nil || deploymentplan.Digest(prior) != deploymentplan.Digest(alias) || entry.CompletedAt.IsZero() {
			return bad()
		}
		return e.Check(ctx)
	}
	if entry.State != "pending" && entry.State != "allocated" {
		return bad()
	}
	_, err = original.VerifyFullyWrittenFailedSuccessor(ctx, b, store.Root, original.SHA256(), deploymentplan.Digest(journal), journal.Failed, retained, accept, func(ctx context.Context, _ deploymentplan.FailedForwardEvidence, lease deploymentplan.ExecutionLease) error {
		if e.Check(ctx) != nil || lease.Check(ctx) != nil {
			return bad()
		}
		return e.RecordReceipt(key, json.RawMessage(raw))
	})
	if err != nil {
		return err
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	completedAt := time.Now().UTC()
	if completedAt.Before(now) {
		completedAt = now
	}
	return e.CompleteEntry(key, completedAt)
}

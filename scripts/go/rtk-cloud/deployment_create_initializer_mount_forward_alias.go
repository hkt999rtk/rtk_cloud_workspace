package main

import (
	"context"
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

func verifyInitializerMountSuccessorAlias(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, alias createFailedForwardAlias, original *deploymentplan.Plan, source deploymentplan.Journal, q0 createFailedForwardProof, repair, oldRetain *deploymentplan.Plan) (*deploymentplan.Plan, error) {
	return verifyInitializerMountSuccessorAliasWithAdmission(ctx, store, e, b, alias, original, source, q0, repair, oldRetain, authenticatedInitializerForwardAdmission)
}

func verifyInitializerMountSuccessorAliasWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, alias createFailedForwardAlias, original *deploymentplan.Plan, source deploymentplan.Journal, q0 createFailedForwardProof, repair, oldRetain *deploymentplan.Plan, load initializerForwardAdmissionLoader) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("private state alias lacks its accepted original owner chain")
	}
	if e == nil || b == nil || original == nil || repair == nil || oldRetain == nil || load == nil || e.Check(ctx) != nil || alias.QualificationSHA256 != deploymentplan.Digest(q0) {
		return bad()
	}
	q1, q1Update, q1Retain, err := loadInitializerForwardBundle(store, e, alias.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	q2, update, retained, err := loadInitializerMountForwardBundle(store, e, alias.InitializerMountQualificationSHA256)
	if err != nil {
		return bad()
	}
	if q2.InitializerQualificationSHA256 != alias.InitializerQualificationSHA256 || q1.FailedForwardSHA256 != alias.QualificationSHA256 || q2.OriginalPlanSHA256 != original.SHA256() || q2.OriginalJournalSHA256 != deploymentplan.Digest(source) || alias.RepairStage != "kubernetes/"+q1.CRLStage || alias.RepairPlanSHA256 != repair.SHA256() || q1.CRLPlanSHA256 != repair.SHA256() || q1.FailedRetainPlanSHA256 != oldRetain.SHA256() {
		return bad()
	}
	originalRetained := retained
	if alias.ServiceSessionQualificationSHA256 == "" {
		if alias.RetainStage != "kubernetes/"+q2.RetainStage || alias.RetainPlanSHA256 != retained.SHA256() {
			return bad()
		}
	} else {
		stage, expected, err := initializerSessionRetainPlan(ctx, store, e, b, q2, retained, alias.ServiceSessionQualificationSHA256)
		if err != nil || alias.RetainStage != "kubernetes/"+stage || alias.RetainPlanSHA256 != expected.SHA256() {
			return bad()
		}
		retained = expected
	}
	admitted, admission, err := load(store, e, q1)
	if err != nil || validateInitializerMountForward(ctx, store, e, q2, q1, q0, admitted, admission, q1Update, q1Retain, update, originalRetained, q2.ExecutionDeadline) != nil {
		return bad()
	}
	actualRepair, _, err := acceptedCreateStage(store, e, "kubernetes/"+q1.CRLStage)
	if err != nil || actualRepair.SHA256() != repair.SHA256() {
		return bad()
	}
	actualUpdate, _, err := acceptedCreateStage(store, e, "kubernetes/"+q2.UpdateStage)
	if err != nil || actualUpdate.SHA256() != update.SHA256() {
		return bad()
	}
	actualRetain, j, err := acceptedCreateStage(store, e, alias.RetainStage)
	if err != nil || actualRetain.SHA256() != retained.SHA256() || len(j.Owners) != len(original.Resources) {
		return bad()
	}
	for _, r := range original.Resources {
		if j.Owners[r.ID] != source.Owners[r.ID] {
			return bad()
		}
	}
	return retained, e.Check(ctx)
}

func completeInitializerMountForwardAlias(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q2 createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, accept func(context.Context, *deploymentplan.Plan) error, now time.Time) error {
	return completeInitializerMountForwardAliasWithAdmission(ctx, store, e, b, q2, q1, q0, admitted, admissionSHA, accept, now, authenticatedInitializerForwardAdmission)
}

func completeInitializerMountForwardAliasWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q2 createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, accept func(context.Context, *deploymentplan.Plan) error, now time.Time, load initializerForwardAdmissionLoader) error {
	bad := func() error {
		return createProviderRecoveryBlock("private state alias requires its admitted original and accepted successor")
	}
	if e == nil || e.Store != store || b == nil || accept == nil || now.IsZero() || now.Before(q2.QualifiedAt) || q2.InitializerQualificationSHA256 != deploymentplan.Digest(q1) || q1.FailedForwardSHA256 != deploymentplan.Digest(q0) || q1.OriginalAdmissionSHA256 != admissionSHA || e.Check(ctx) != nil {
		return bad()
	}
	if q2.ExecutionDeadline.Before(q1.ExecutionDeadline) {
		return bad()
	}
	effective, err := initializerEffectiveDeadline(ctx, store, e, q1, now)
	if err != nil || q2.ExecutionDeadline.After(effective) || !now.Before(effective) {
		return bad()
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.After(effective) {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q0.FailedStage.Child.PlanSHA256 {
		return bad()
	}
	source, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(source) != q0.FailedStage.Child.JournalSHA256 || source.Failed != q0.FailedStage.Child.FailedResource {
		return bad()
	}
	_, repair, oldRetain, err := loadFailedForwardBundle(store, e, deploymentplan.Digest(q0))
	if err != nil {
		return bad()
	}
	if validateCreateFailedForwardProofBindings(q0, admitted, admissionSHA, q0.FailedStage, repair, oldRetain, q0.QualifiedAt) != nil {
		return bad()
	}
	_, _, retained, err := loadInitializerMountForwardBundle(store, e, deploymentplan.Digest(q2))
	if err != nil {
		return bad()
	}
	alias := createFailedForwardAlias{QualificationSHA256: deploymentplan.Digest(q0), InitializerQualificationSHA256: deploymentplan.Digest(q1), InitializerMountQualificationSHA256: deploymentplan.Digest(q2), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(source), FailedResource: source.Failed, RepairStage: "kubernetes/" + q1.CRLStage, RepairPlanSHA256: repair.SHA256(), RetainStage: "kubernetes/" + q2.RetainStage, RetainPlanSHA256: retained.SHA256()}
	if review, err := initializerSessionRetainReview(store, e, q0.FailedStage.Stage); err != nil {
		return err
	} else if review != "" {
		stage, expected, err := initializerSessionRetainPlan(ctx, store, e, b, q2, retained, review)
		if err != nil {
			return err
		}
		alias.ServiceSessionQualificationSHA256, alias.RetainStage, alias.RetainPlanSHA256 = review, "kubernetes/"+stage, expected.SHA256()
		retained = expected
	}
	verified, err := verifyInitializerMountSuccessorAliasWithAdmission(ctx, store, e, b, alias, original, source, q0, repair, oldRetain, load)
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
	_, err = original.VerifyFullyWrittenFailedSuccessor(ctx, b, store.Root, original.SHA256(), deploymentplan.Digest(source), source.Failed, retained, accept, func(ctx context.Context, _ deploymentplan.FailedForwardEvidence, lease deploymentplan.ExecutionLease) error {
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

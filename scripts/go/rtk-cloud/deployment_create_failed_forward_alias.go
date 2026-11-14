package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createFailedForwardAlias struct {
	QualificationSHA256                 string `json:"qualification_sha256"`
	ServiceSessionQualificationSHA256   string `json:"service_session_qualification_sha256,omitempty"`
	InitializerQualificationSHA256      string `json:"initializer_qualification_sha256,omitempty"`
	InitializerMountQualificationSHA256 string `json:"initializer_mount_qualification_sha256,omitempty"`
	OriginalPlanSHA256                  string `json:"original_failed_plan_sha256"`
	OriginalJournalSHA256               string `json:"original_failed_journal_sha256"`
	FailedResource                      string `json:"failed_resource"`
	RepairStage                         string `json:"repair_stage"`
	RepairPlanSHA256                    string `json:"repair_plan_sha256"`
	RetainStage                         string `json:"actual_qualified_forward_stage"`
	RetainPlanSHA256                    string `json:"retain_plan_sha256"`
}

// completeCreateFailedForwardAlias is the narrowly scoped consumer for a
// previously failed, fully written child. It does not change the original
// child journal. An allocated alias after interruption is reverified, never
// interpreted as a completed stage merely because a receipt exists.
func completeCreateFailedForwardAlias(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA, repairStage, retainStage string, token *verifiedCreateFailedForward, accept func(context.Context, *deploymentplan.Plan) error, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("failed-stage alias requires its exact qualified repair, retained owners and active execution")
	}
	review := deploymentplan.Digest(q)
	if e == nil || e.Store != store || b == nil || token == nil || token.proofSHA != review || accept == nil || !secretEnvironmentPattern.MatchString(repairStage) || !secretEnvironmentPattern.MatchString(retainStage) || repairStage == retainStage || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.FailedStage.ParentSHA256 != e.Journal.PlanSHA256 || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || e.Check(ctx) != nil {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q.FailedStage.Child.PlanSHA256 {
		return bad()
	}
	source, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(source) != q.FailedStage.Child.JournalSHA256 || source.Failed != q.FailedStage.Child.FailedResource || source.Accepted || source.AcceptanceStarted || source.InFlight != "" || len(source.Completed) != len(original.Resources) || len(source.Owners) != len(original.Resources) {
		return bad()
	}
	repair, _, err := acceptedCreateStage(store, e, "kubernetes/"+repairStage)
	if err != nil {
		return err
	}
	retained, retainedJournal, err := acceptedCreateStage(store, e, "kubernetes/"+retainStage)
	if err != nil {
		return err
	}
	if err = validateCreateFailedForwardProofBindings(q, admitted, admissionSHA, q.FailedStage, repair, retained, q.QualifiedAt); err != nil {
		return err
	}
	if len(retained.Resources) != len(original.Resources) {
		return bad()
	}
	for i, r := range retained.Resources {
		src := original.Resources[i]
		if r.ID != src.ID || r.Action != "retain" || r.UID != source.Owners[src.ID] || retainedJournal.Owners[r.ID] != source.Owners[src.ID] || !deploymentplan.Matches(src.Desired, r.Desired) || deploymentplan.Digest(src.Dependencies) != deploymentplan.Digest(r.Dependencies) {
			return bad()
		}
	}
	if err = repair.AdmitResumable(ctx, b, store.Root); err != nil {
		return err
	}
	if err = retained.AdmitResumable(ctx, b, store.Root); err != nil {
		return err
	}
	key := "kubernetes/" + q.FailedStage.Stage
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil || entry.Phase != q.FailedStage.Stage || entry.IntentSHA256 != q.FailedStage.ParentIntentSHA256 {
		return bad()
	}
	alias := createFailedForwardAlias{QualificationSHA256: review, OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: q.FailedStage.Child.JournalSHA256, FailedResource: source.Failed, RepairStage: "kubernetes/" + repairStage, RepairPlanSHA256: repair.SHA256(), RetainStage: "kubernetes/" + retainStage, RetainPlanSHA256: retained.SHA256()}
	raw := mustProviderRecoveryJSON(alias)
	if entry.State == "completed" {
		var old createFailedForwardAlias
		if decodeEnvironmentJSON(entry.Receipt, &old) != nil || deploymentplan.Digest(old) != deploymentplan.Digest(alias) || entry.CompletedAt.IsZero() {
			return bad()
		}
		return e.Check(ctx)
	}
	if entry.State != "pending" && entry.State != "allocated" {
		return bad()
	}
	_, err = original.VerifyFullyWrittenFailedForward(ctx, b, store.Root, original.SHA256(), q.FailedStage.Child.JournalSHA256, source.Failed, accept, func(ctx context.Context, _ deploymentplan.FailedForwardEvidence, lease deploymentplan.ExecutionLease) error {
		if e.Check(ctx) != nil || lease.Check(ctx) != nil {
			return bad()
		}
		return e.RecordReceipt(key, json.RawMessage(raw))
	})
	if err != nil {
		return err
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	return e.CompleteEntry(key, now)
}

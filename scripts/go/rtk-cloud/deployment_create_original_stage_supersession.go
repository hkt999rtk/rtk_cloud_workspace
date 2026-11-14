package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const originalStageSupersessionSchema = "rtk-cloud.original-child-supersession/v1"

type originalStageSupersessionReceipt struct {
	Schema          string `json:"schema"`
	OriginalPlan    string `json:"original_failed_plan_sha256"`
	OriginalJournal string `json:"original_failed_journal_sha256"`
	SuccessorStage  string `json:"actual_qualified_forward_stage"`
	SuccessorPlan   string `json:"actual_qualified_forward_plan_sha256"`
	Initializer     string `json:"initializer_mount_qualification_sha256"`
	Session         string `json:"service_session_qualification_sha256"`
}

// Parent completion explicitly means superseded by this genuine accepted
// child. The original child remains failed and its bytes are never rewritten.
func validateOriginalStageSupersession(store secretStore, e *environmentCreateExecution, stage string, plan *deploymentplan.Plan, journal deploymentplan.Journal, raw json.RawMessage) error {
	bad := func() error {
		return createProviderRecoveryBlock("original failed stage supersession differs from its accepted native successor")
	}
	var receipt originalStageSupersessionReceipt
	if decodeEnvironmentJSON(raw, &receipt) != nil || receipt.Schema != originalStageSupersessionSchema || receipt.OriginalPlan != plan.SHA256() || receipt.OriginalJournal != deploymentplan.Digest(journal) || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed == "" || len(journal.Completed) != len(plan.Resources) || len(journal.Owners) != len(plan.Resources) {
		return bad()
	}
	q2, _, retained, err := loadInitializerMountForwardBundle(store, e, receipt.Initializer)
	if err != nil {
		return bad()
	}
	q1, _, _, err := loadInitializerForwardBundle(store, e, q2.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	switch stage {
	case q1.FailedRetainStage:
		if plan.SHA256() != q1.FailedRetainPlanSHA256 || deploymentplan.Digest(journal) != q1.FailedRetainJournalSHA256 {
			return bad()
		}
	case q1.UpdateStage:
		if plan.SHA256() != q2.PreviousUpdatePlanSHA256 || deploymentplan.Digest(journal) != q2.PreviousUpdateJournalSHA256 {
			return bad()
		}
	case q2.RetainStage:
		if plan.SHA256() != q2.RetainPlanSHA256 {
			return bad()
		}
	default:
		return bad()
	}
	// The one-owner q1 update is superseded only through the exact q2
	// transition already qualified by the original runner, never UID inclusion alone.
	update, _, err := acceptedCreateStage(store, e, "kubernetes/"+q2.UpdateStage)
	if err != nil || update.SHA256() != q2.UpdatePlanSHA256 || len(update.Resources) != 1 {
		return bad()
	}
	if stage == q1.UpdateStage && (len(plan.Resources) != 1 || plan.Resources[0].ID != update.Resources[0].ID || plan.Resources[0].UID != update.Resources[0].UID) {
		return bad()
	}
	source, exists, err := retained.ReadExecution(store.Root)
	if err != nil || !exists {
		return bad()
	}
	expectedStage, expected, err := initializerSessionRetainPlanWithSource(context.Background(), store, e, nil, q2, retained, receipt.Session, &source)
	if err != nil || receipt.SuccessorStage != "kubernetes/"+expectedStage || receipt.SuccessorPlan != expected.SHA256() {
		return bad()
	}
	accepted, acceptedJournal, err := acceptedCreateStage(store, e, receipt.SuccessorStage)
	if err != nil || accepted.SHA256() != expected.SHA256() {
		return bad()
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, expectedStage, accepted)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(acceptedJournal) {
		return bad()
	}
	for i, r := range plan.Resources {
		if journal.Completed[i] != r.ID || journal.Owners[r.ID] == "" || acceptedJournal.Owners[r.ID] != journal.Owners[r.ID] || (r.UID != "" && r.UID != journal.Owners[r.ID]) {
			return bad()
		}
	}
	return nil
}

func completeInitializerSessionSources(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, q2 createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, now time.Time) error {
	original := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q0.FailedStage.Stage)
	var alias createFailedForwardAlias
	if original == nil || original.State != "completed" || decodeEnvironmentJSON(original.Receipt, &alias) != nil {
		return createProviderRecoveryBlock("original accepted session retain alias is unavailable")
	}
	if alias.ServiceSessionQualificationSHA256 == "" {
		return nil
	}
	successor, _, err := acceptedCreateStage(store, e, alias.RetainStage)
	if err != nil {
		return err
	}
	if successor.SHA256() != alias.RetainPlanSHA256 {
		return createProviderRecoveryBlock("accepted original session retain plan changed")
	}
	if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, backend, alias.RetainStage); err != nil {
		return err
	}
	for _, stage := range []string{q1.FailedRetainStage, q1.UpdateStage, q2.RetainStage} {
		plan, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || plan == nil {
			return createProviderRecoveryBlock("superseded original stage input is unavailable")
		}
		journal, exists, err := plan.ReadExecution(store.Root)
		if err != nil || !exists {
			return createProviderRecoveryBlock("superseded original child journal is unavailable")
		}
		receipt := originalStageSupersessionReceipt{Schema: originalStageSupersessionSchema, OriginalPlan: plan.SHA256(), OriginalJournal: deploymentplan.Digest(journal), SuccessorStage: alias.RetainStage, SuccessorPlan: alias.RetainPlanSHA256, Initializer: deploymentplan.Digest(q2), Session: alias.ServiceSessionQualificationSHA256}
		raw, err := canonicalEnvironmentCreateJSON(mustProviderRecoveryJSON(receipt))
		if err != nil {
			return err
		}
		if err := validateOriginalStageSupersession(store, e, stage, plan, journal, raw); err != nil {
			return err
		}
		key := "kubernetes/" + stage
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry == nil || e.Check(ctx) != nil {
			return createProviderRecoveryBlock("superseded original parent request is unavailable")
		}
		if entry.State == "completed" {
			if !samePersistedJSONReceipt(entry.Receipt, raw) {
				return createProviderRecoveryBlock("original supersession receipt changed")
			}
			continue
		}
		if entry.State == "pending" {
			if err := e.RecordReceipt(key, raw); err != nil {
				return err
			}
		} else if entry.State != "allocated" || !samePersistedJSONReceipt(entry.Receipt, raw) {
			return createProviderRecoveryBlock("original supersession receipt changed")
		}
		if err := e.CompleteEntry(key, now); err != nil {
			return err
		}
		current, exists, err := plan.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(current) != deploymentplan.Digest(journal) {
			return createProviderRecoveryBlock("original failed child changed during supersession")
		}
	}
	return e.Check(ctx)
}

func originalStageSupersessionReceiptMatches(raw json.RawMessage) bool {
	var receipt originalStageSupersessionReceipt
	return json.Unmarshal(raw, &receipt) == nil && receipt.Schema == originalStageSupersessionSchema && strings.HasPrefix(receipt.SuccessorStage, "kubernetes/session-owner-retain-")
}

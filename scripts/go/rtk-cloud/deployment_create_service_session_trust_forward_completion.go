package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Complete the original admitted trust-forward parent only after its exact
// native Job/alias and the later real-ACK session batch are both proven.
func completeServiceSessionTrustForward(ctx context.Context, store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("original Service trust-forward completion evidence changed")
	}
	if e == nil || now.IsZero() || e.Check(ctx) != nil {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil || !done {
		return bad()
	}
	head, headReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil || headReview != review || deploymentplan.Digest(*head) != review {
		return bad()
	}
	original, originalReview, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || original.PartialSession == nil {
		return bad()
	}
	forward, forwardReview, exists, err := loadPartialSessionTrustForwardAdmission(store, e, original, originalReview)
	if err != nil {
		return bad()
	}
	if !exists {
		return nil
	}
	if forward.PartialTrustForward == nil || forward.PartialTrustForward.Forward == nil {
		return bad()
	}
	// A previously completed parent keeps the batch that actually completed it.
	// A later successor must prove that batch is still in the same admitted
	// lineage; it must never replace the original durable receipt.
	entry := createOpenBaoACKCompletionEntry(e.Journal, "partial-session-trust-forward-"+forwardReview)
	if entry != nil && (entry.State == "allocated" || entry.State == "completed") {
		var receipt struct {
			Batch string `json:"completed_session_batch_sha256"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Batch == "" {
			return bad()
		}
		cursor, cursorReview := *head, headReview
		for cursorReview != receipt.Batch {
			previous := cursor.Renewal.PreviousReview
			if previous == "" || previous == originalReview {
				return bad()
			}
			cursor, err = loadSecondSessionAdmission(store, e, previous)
			if err != nil {
				return bad()
			}
			cursorReview = previous
		}
		complete, err := completedServiceSessionBatch(store, e, cursor, cursorReview)
		if err != nil || !complete {
			return bad()
		}
		review = cursorReview
	}
	return completeServiceSessionTrustForwardRecord(ctx, store, e, original, originalReview, forward, forwardReview, review, now)
}

// The caller authenticates the immutable qualifications and completed batch;
// this part checks the original native Job, alias, intent and durable receipt.
func completeServiceSessionTrustForwardRecord(ctx context.Context, store secretStore, e *environmentCreateExecution, original issuerEnrollmentContinuationProof, originalReview string, forward issuerEnrollmentContinuationProof, forwardReview, review string, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("original Service trust-forward completion evidence changed")
	}
	if e == nil || original.PartialSession == nil || forward.PartialTrustForward == nil || forward.PartialTrustForward.Forward == nil || now.IsZero() || e.Check(ctx) != nil {
		return bad()
	}
	f := forward.PartialTrustForward.Forward
	stage := "partial-session-trust-forward-" + forwardReview
	plan, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil || plan == nil || f.Candidate == nil || plan.SHA256() != f.Candidate.SHA256() || len(plan.Resources) != 1 || plan.Resources[0].Desired["kind"] != "Job" || journal.Owners[plan.Resources[0].ID] == "" {
		return bad()
	}
	checked, ok, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
	if err != nil || !ok || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return bad()
	}
	failed, ok, err := original.PartialSession.Candidate.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(failed) != f.FailedJournalSHA {
		return bad()
	}
	alias := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/service-session-original-key-renewal-"+originalReview)
	if alias == nil || alias.State != "completed" || !samePersistedJSONReceipt(alias.Receipt, mustProviderRecoveryJSON(partialSessionTrustForwardAlias(f, forwardReview))) {
		return bad()
	}
	key := "partial-session-trust-forward-" + forwardReview
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intent, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(partialSessionTrustForwardIntent(forward.Parent, forwardReview, forward.PartialTrustForward)))
	if entry == nil || err != nil || entry.Phase != "partial-session-trust-forward" || entry.IntentSHA256 != intent {
		return bad()
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": forwardReview, "candidate_plan_sha256": plan.SHA256(), "candidate_journal_sha256": deploymentplan.Digest(journal), "job_uid": journal.Owners[plan.Resources[0].ID], "completed_session_batch_sha256": review})
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, receipt) {
			return bad()
		}
		return e.Check(ctx)
	}
	if entry.State == "pending" {
		if err := e.RecordReceipt(key, receipt); err != nil {
			return err
		}
	} else if entry.State != "allocated" || !samePersistedJSONReceipt(entry.Receipt, receipt) {
		return bad()
	}
	// Re-read the durable receipt before completing an interrupted transition.
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	var persisted map[string]any
	if entry == nil || json.Unmarshal(entry.Receipt, &persisted) != nil || !samePersistedJSONReceipt(entry.Receipt, receipt) || e.Check(ctx) != nil {
		return bad()
	}
	return e.CompleteEntry(key, now)
}

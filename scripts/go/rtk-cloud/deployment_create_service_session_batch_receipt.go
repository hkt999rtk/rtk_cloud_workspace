package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type serviceSessionBatchReceipt struct {
	Review, ReferencePlanSHA string
	BatchPlanSHA             []string
	ACK                      managedSessionState
	ObservedAt               time.Time
}

func serviceSessionBatchIntent(review string, q secondSessionAdmission) []byte {
	return mustProviderRecoveryJSON(deploymentplan.Object{"review": review, "session": q.Renewal.RenewedIntent.SessionID, "reference_plan": q.Reference.Plan.SHA256()})
}

func validServiceSessionBatchReceipt(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string, receipt serviceSessionBatchReceipt) bool {
	if e == nil || !validSecondSessionAdmission(store, e, q) || deploymentplan.Digest(q) != review || receipt.Review != review || receipt.ReferencePlanSHA != q.Reference.Plan.SHA256() || receipt.ObservedAt.Before(q.QualifiedAt) || !sessionExistingConsumersACKed(q, receipt.ACK, receipt.ObservedAt) {
		return false
	}
	deadline, err := sessionEffectiveDeadline(context.Background(), store, e, q, receipt.ObservedAt)
	if err != nil || !receipt.ObservedAt.Before(deadline) {
		return false
	}
	var want []string
	for _, batch := range q.Batches {
		if batch.Plan != nil {
			want = append(want, batch.Plan.SHA256())
		}
	}
	if deploymentplan.Digest(want) != deploymentplan.Digest(receipt.BatchPlanSHA) {
		return false
	}
	for _, item := range append([]serviceSessionReference{{Stage: q.Reference.Stage, Plan: q.Reference.Plan}}, batchReferencePlans(q.Batches)...) {
		plan, journal, err := acceptedCreateStage(store, e, "kubernetes/"+item.Stage)
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+item.Stage)
		if err != nil || plan == nil || plan.SHA256() != item.Plan.SHA256() || !journal.Accepted || entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.After(receipt.ObservedAt) {
			return false
		}
		checked, exists, err := readEnvironmentCreateStageProgress(store, e, item.Stage, plan)
		if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
			return false
		}
	}
	return true
}

func batchReferencePlans(batches []serviceSessionBatch) []serviceSessionReference {
	var result []serviceSessionReference
	for _, batch := range batches {
		if batch.Plan != nil {
			result = append(result, serviceSessionReference{Stage: batch.Stage, Plan: batch.Plan})
		}
	}
	return result
}

func completedServiceSessionBatch(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string) (bool, error) {
	if e == nil || !validSecondSessionAdmission(store, e, q) || q.Reference == nil {
		return false, createProviderRecoveryBlock("Service session completion evidence is unavailable")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-batches-"+review)
	if entry == nil {
		return false, nil
	}
	sha, err := environmentCreatePlanSHA256(serviceSessionBatchIntent(review, q))
	if err != nil || entry.Phase != "service-session-batches" || entry.IntentSHA256 != sha || entry.StartedAt.Before(q.QualifiedAt) {
		return false, createProviderRecoveryBlock("Service session completion intent changed")
	}
	deadline, err := sessionEffectiveDeadline(context.Background(), store, e, q, entry.StartedAt)
	if err != nil || !entry.StartedAt.Before(deadline) {
		return false, createProviderRecoveryBlock("Service session completion started outside its admitted deadline")
	}
	if entry.State == "pending" && len(entry.Receipt) == 0 && entry.CompletedAt.IsZero() {
		return false, nil
	}
	var receipt serviceSessionBatchReceipt
	if (entry.State != "allocated" && entry.State != "completed") || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.ObservedAt.Before(entry.StartedAt) || entry.State == "completed" && entry.CompletedAt.Before(receipt.ObservedAt) || !validServiceSessionBatchReceipt(store, e, q, review, receipt) {
		return false, createProviderRecoveryBlock("Service session completion evidence changed")
	}
	return entry.State == "completed" && !entry.CompletedAt.IsZero(), nil
}

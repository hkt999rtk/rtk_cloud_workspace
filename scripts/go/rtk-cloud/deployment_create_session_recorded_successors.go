package main

import (
	"context"
	"encoding/json"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// completedSessionRecordedSuccessors follows only a completed session's
// authenticated native writes. A merely admitted or pending batch is never
// treated as completion. The common fold checks the exact predecessor and UID.
func completedSessionRecordedStages(ctx context.Context, store secretStore, e *environmentCreateExecution) ([]serviceSessionRecordedPlan, error) {
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("completed Service session owner successor cannot be authenticated")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	var completed []string
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "service-session-batches" && strings.HasPrefix(entry.Key, "service-session-batches-") && entry.State == "completed" {
			completed = append(completed, strings.TrimPrefix(entry.Key, "service-session-batches-"))
		}
	}
	if len(completed) == 0 {
		return nil, nil
	}
	// This validates the entire admitted lineage, including all older reviews.
	load := newSecondSessionAdmissionReader(store, e)
	if _, _, err := sessionLineageHeadWithLoader(store, e, load); err != nil {
		return bad()
	}
	admitted := map[string]bool{}
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "session-lineage" && strings.HasPrefix(entry.Key, sessionLineagePrefix) {
			admitted[strings.TrimPrefix(entry.Key, sessionLineagePrefix)] = true
		}
	}
	var stages []serviceSessionRecordedPlan
	seen := map[string]bool{}
	for _, review := range completed {
		if !admitted[review] || seen[review] {
			return bad()
		}
		seen[review] = true
		q, err := load(review)
		if err != nil || deploymentplan.Digest(q) != review {
			return bad()
		}
		ok, err := completedServiceSessionBatch(store, e, q, review)
		if err != nil || !ok {
			return bad()
		}
		for _, batch := range q.Batches {
			if batch.Plan == nil {
				continue
			}
			key := "kubernetes/" + batch.Stage
			plan, journal, err := acceptedCreateStage(store, e, key)
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			if err != nil || plan == nil || plan.SHA256() != batch.Plan.SHA256() || entry == nil || entry.State != "completed" || entry.Phase != batch.Stage {
				return bad()
			}
			intent, err := environmentCreateKubernetesStageIntent(plan)
			if err != nil {
				return bad()
			}
			intentSHA, err := environmentCreatePlanSHA256(intent)
			var receipt struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			if err != nil || entry.IntentSHA256 != intentSHA || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Plan != plan.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(journal.Owners) {
				return bad()
			}
			stages = append(stages, serviceSessionRecordedPlan{Stage: batch.Stage, Plan: plan, Journal: journal})
		}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return stages, nil
}

func completedSessionRecordedSuccessors(ctx context.Context, store secretStore, e *environmentCreateExecution, base []activeTransportRenewalResource) ([]activeTransportRenewalResource, error) {
	sources, err := sessionRecordedStages(ctx, store, e)
	if err != nil {
		return nil, err
	}
	var stages []completedRecordedSuccessorStage
	for _, source := range sources {
		stages = append(stages, completedRecordedSuccessorStage{Plan: source.Plan, Journal: source.Journal, Written: !source.Journal.Accepted})
	}
	return foldCompletedRecordedSuccessors(base, stages)
}

func sessionRecordedOwnerBase(id, uid string, desired deploymentplan.Object) activeTransportRenewalResource {
	return activeTransportRenewalResource{ID: id, UID: uid, Before: desired, Desired: desired}
}

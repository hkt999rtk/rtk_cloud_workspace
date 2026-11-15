package main

import (
	"context"
	"encoding/json"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Observe only the writes already recorded by an admitted pending batch. Such
// a prefix is not a completed batch and does not imply any session ACK.
func pendingSessionRecordedStages(ctx context.Context, store secretStore, e *environmentCreateExecution) ([]serviceSessionRecordedPlan, error) {
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("pending Service session owner writes lack their admitted original journal")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	hasPending := false
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "service-session-batches" && (entry.State == "pending" || entry.State == "allocated") {
			hasPending = true
		}
	}
	if !hasPending {
		return nil, nil
	}
	head, headReview, err := sessionLineageHead(store, e)
	if err != nil {
		return bad()
	}
	admitted := map[string]bool{}
	if head != nil {
		admitted[headReview] = true
	}
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "session-lineage" && strings.HasPrefix(entry.Key, sessionLineagePrefix) {
			admitted[strings.TrimPrefix(entry.Key, sessionLineagePrefix)] = true
		}
	}
	var result []serviceSessionRecordedPlan
	seen := map[string]bool{}
	for _, entry := range e.Journal.Entries {
		const prefix = "service-session-batches-"
		if entry.Phase != "service-session-batches" || !strings.HasPrefix(entry.Key, prefix) || entry.State != "pending" && entry.State != "allocated" {
			continue
		}
		review := strings.TrimPrefix(entry.Key, prefix)
		if !admitted[review] || seen[review] {
			return bad()
		}
		seen[review] = true
		q, err := loadSecondSessionAdmission(store, e, review)
		if err != nil || deploymentplan.Digest(q) != review {
			return bad()
		}
		complete, err := completedServiceSessionBatch(store, e, q, review)
		if err != nil || complete {
			return bad()
		}
		for _, batch := range q.Batches {
			if batch.Plan == nil {
				continue
			}
			saved, err := readEnvironmentCreateStageInput(store, e, batch.Stage)
			if err != nil {
				return bad()
			}
			if saved == nil {
				continue
			}
			if saved.SHA256() != batch.Plan.SHA256() {
				return bad()
			}
			journal, found, err := readEnvironmentCreateStageProgress(store, e, batch.Stage, saved)
			if err != nil {
				return bad()
			}
			if !found {
				continue
			}
			if journal.InFlight != "" || len(journal.Completed) > len(saved.Resources) {
				return bad()
			}
			result = append(result, serviceSessionRecordedPlan{Stage: batch.Stage, Plan: saved, Journal: journal})
		}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return result, nil
}

func sessionRecordedStages(ctx context.Context, store secretStore, e *environmentCreateExecution) ([]serviceSessionRecordedPlan, error) {
	completed, err := completedSessionRecordedStages(ctx, store, e)
	if err != nil {
		return nil, err
	}
	pending, err := pendingSessionRecordedStages(ctx, store, e)
	if err != nil {
		return nil, err
	}
	byStage := map[string]serviceSessionRecordedPlan{}
	for _, source := range append(completed, pending...) {
		if source.Stage == "" || byStage[source.Stage].Plan != nil {
			return nil, createProviderRecoveryBlock("ambiguous Service session owner stage")
		}
		byStage[source.Stage] = source
	}
	var result []serviceSessionRecordedPlan
	for _, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		stage := strings.TrimPrefix(entry.Key, "kubernetes/")
		if source, ok := byStage[stage]; ok {
			result = append(result, source)
			delete(byStage, stage)
		}
	}
	if len(byStage) != 0 {
		return nil, createProviderRecoveryBlock("Service session owner stage parent is unavailable")
	}
	return result, nil
}

// serviceSessionOwnerHistory provides one chronological native history for core
// and application owners. A completed receipt or a known pending prefix is
// evidence of writes, not evidence that an interrupted workload passed readiness.
func serviceSessionOwnerHistory(ctx context.Context, store secretStore, e *environmentCreateExecution) ([]serviceSessionRecordedPlan, map[string]int, error) {
	bad := func() ([]serviceSessionRecordedPlan, map[string]int, error) {
		return nil, nil, createProviderRecoveryBlock("Service session native owner history differs")
	}
	sessions, err := sessionRecordedStages(ctx, store, e)
	if err != nil {
		return nil, nil, err
	}
	references, err := pendingServiceSessionReferences(ctx, store, e)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]serviceSessionRecordedPlan{}
	for _, source := range append(sessions, references...) {
		known[source.Stage] = source
	}
	pending, err := ordinaryRecordedPendingWrites(ctx, store, e, nil)
	if err != nil {
		return nil, nil, err
	}
	order := map[string]int{}
	var history []serviceSessionRecordedPlan
	for i, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		stage := strings.TrimPrefix(entry.Key, "kubernetes/")
		if _, exists := order[stage]; exists {
			return bad()
		}
		order[stage] = i
		source, exists := known[stage]
		if written, ok := pending[entry.Key]; ok {
			if exists && (source.Plan.SHA256() != written.Plan.SHA256() || deploymentplan.Digest(source.Journal) != deploymentplan.Digest(written.Journal)) {
				return bad()
			}
			if !exists {
				source, exists = serviceSessionRecordedPlan{Stage: stage, Plan: written.Plan, Journal: written.Journal}, true
			}
		}
		if !exists && entry.State == "completed" {
			var receipt struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			// Logical supersession aliases have separate typed proof. Never
			// relabel their original failed journal as an accepted native child.
			if json.Unmarshal(entry.Receipt, &receipt) == nil && receipt.Plan != "" && receipt.Owners != nil {
				p, j, err := acceptedCreateStage(store, e, entry.Key)
				if err != nil || p == nil || receipt.Plan != p.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) {
					return bad()
				}
				checked, ok, err := readEnvironmentCreateStageProgress(store, e, stage, p)
				if err != nil || !ok || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
					return bad()
				}
				source, exists = serviceSessionRecordedPlan{Stage: stage, Plan: p, Journal: j}, true
			}
		}
		if exists {
			history = append(history, source)
			delete(known, stage)
		}
	}
	if len(known) != 0 || e.Check(ctx) != nil {
		return bad()
	}
	return history, order, nil
}

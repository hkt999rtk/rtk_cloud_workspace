package main

import (
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productSessionForwardResponseMatches(entry environmentCreateJournalEntry, d *createNormalTailProductSessionForward, plan *deploymentplan.Plan, j deploymentplan.Journal) bool {
	if entry.State == "pending" {
		return len(entry.Receipt) == 0 && entry.CompletedAt.IsZero()
	}
	if d == nil || plan == nil || !j.Accepted {
		return false
	}
	want := createNormalTailProductSessionForwardReceipt{Schema: createNormalTailProductSessionForwardSchema, PreviousPlanSHA256: d.PreviousPlanSHA256, PreviousJournalSHA256: d.PreviousJournalSHA256, RenewalSHA256: d.RenewalSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	var got createNormalTailProductSessionForwardReceipt
	return decodeEnvironmentJSON(entry.Receipt, &got) == nil && deploymentplan.Digest(got) == deploymentplan.Digest(want)
}

// The zero-write first forward contributes no allocation authority. Its exact
// failed journal is authenticated by the descriptor, while original allocations
// and the real successor's remaining allocations retain the existing two-source
// volume contract.
func readCreateNormalTailProductSessionWorkloadSources(store secretStore, e *environmentCreateExecution, frozen, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, previous *createNormalTailWorkloadForward, d *createNormalTailProductSessionForward) ([]createNormalTailVolumeStage, error) {
	bad := func() ([]createNormalTailVolumeStage, error) { return nil, productSessionForwardBlocked() }
	if e == nil || previous == nil || d == nil || frozen == nil || frozen.SHA256() != original.SHA256() && frozen.SHA256() != previous.Target.Plan.SHA256() && frozen.SHA256() != d.Target.Plan.SHA256() {
		return bad()
	}
	old := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.ForwardStage)
	outer := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	if old == nil || outer == nil {
		return bad()
	}
	sources := []createNormalTailVolumeStage{{Stage: "workload", Plan: original, Journal: originalJournal}}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage)
	if entry == nil {
		if old.State != "pending" || len(old.Receipt) != 0 || outer.State != "pending" || len(outer.Receipt) != 0 {
			return bad()
		}
		if _, exists, err := d.Target.Plan.ReadExecution(store.Root); err != nil || exists {
			return bad()
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if strings.HasPrefix(lease.Holder, d.Target.Plan.SHA256()+":") {
				return bad()
			}
		}
		return sources, nil
	}
	p, j, err := readCreateNormalTailStage(store, e, d.ForwardStage, d.Target.Plan)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if old.State != "pending" || outer.State != "pending" {
			return bad()
		}
		return sources, nil
	}
	if !productSessionForwardResponseMatches(*old, d, p, j) {
		return bad()
	}
	if old.State != "pending" && (!j.Accepted || entry.State != "completed") {
		return bad()
	}
	if outer.State != "pending" {
		var response createNormalTailWorkloadForwardReceipt
		want := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(originalJournal), ForwardStage: d.ForwardStage, ForwardPlanSHA256: p.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
		if old.State != "completed" || !j.Accepted || entry.State != "completed" || decodeEnvironmentJSON(outer.Receipt, &response) != nil || deploymentplan.Digest(response) != deploymentplan.Digest(want) {
			return bad()
		}
	}
	return append(sources, createNormalTailVolumeStage{Stage: d.ForwardStage, Plan: p, Journal: j, originalPlanSHA256: original.SHA256()}), nil
}

func validateCreateNormalTailProductSessionSupersession(store secretStore, e *environmentCreateExecution, previous *createNormalTailWorkloadForward, d *createNormalTailProductSessionForward) error {
	old := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.ForwardStage)
	if old == nil {
		return productSessionForwardBlocked()
	}
	if old.State == "pending" {
		if len(old.Receipt) != 0 || !old.CompletedAt.IsZero() {
			return productSessionForwardBlocked()
		}
		return nil
	}
	if old.State != "allocated" && old.State != "completed" {
		return productSessionForwardBlocked()
	}
	if runtime, err := readCreateNormalTailRuntimeForward(store, e); err != nil || runtime != nil {
		return err // The exact runtime reader authenticates all three logical aliases.
	}
	if old.StartedAt.IsZero() || len(old.Receipt) == 0 || old.State == "allocated" && !old.CompletedAt.IsZero() || old.State == "completed" && old.CompletedAt.Before(old.StartedAt) {
		return productSessionForwardBlocked()
	}
	p, j, err := readCreateNormalTailStage(store, e, d.ForwardStage, d.Target.Plan)
	winner := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage)
	if err != nil || p == nil || winner == nil || winner.State != "completed" || !productSessionForwardResponseMatches(*old, d, p, j) || old.State == "completed" && old.CompletedAt.Before(winner.CompletedAt) {
		return productSessionForwardBlocked()
	}
	return nil
}

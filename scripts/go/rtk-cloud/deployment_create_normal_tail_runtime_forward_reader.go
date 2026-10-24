package main

import (
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This reads the immutable partial child without pretending that its logical
// supersession is a native acceptance response.
func readCreateNormalTailRuntimePreviousStage(store secretStore, e *environmentCreateExecution, d *createNormalTailProductSessionForward) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, deploymentplan.Journal{}, createNormalTailRuntimeBlock("previous original child")
	}
	if e == nil || e.Store != store || d == nil || d.Target.Plan == nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage)
	if entry == nil || entry.Phase != d.ForwardStage || entry.StartedAt.IsZero() || entry.StartedAt.After(time.Now().UTC()) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || entry.State == "pending" && (len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero()) || entry.State == "allocated" && (len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero()) || entry.State == "completed" && (len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC())) {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(d.Target.Plan)
	sha, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || entry.IntentSHA256 != sha {
		return bad()
	}
	p, j, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil || p == nil || p.SHA256() != d.Target.Plan.SHA256() {
		return bad()
	}
	verified, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(verified) != deploymentplan.Digest(j) || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(j.Completed) >= len(p.Resources) {
		return bad()
	}
	next := p.Resources[len(j.Completed)]
	if next.ID != "Deployment/"+p.Stack+"-billing/billing" || j.Failed != next.ID || !environmentBootstrapOwnerUUID.MatchString(j.Owners[next.ID]) {
		return bad()
	}
	return p, j, nil
}

func readCreateNormalTailRuntimeForward(store secretStore, e *environmentCreateExecution) (*createNormalTailRuntimeForward, error) {
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return nil, err
	}
	return readCreateNormalTailRuntimeForwardFromChain(store, e, chain)
}

// Reuse only a chain returned by readCreateNormalTailRuntimeChain in this read.
// Winner, response and logical completion checks remain part of selection.
func readCreateNormalTailRuntimeForwardFromChain(store secretStore, e *environmentCreateExecution, chain []createNormalTailRuntimeStep) (*createNormalTailRuntimeForward, error) {
	if len(chain) == 0 {
		return nil, nil
	}
	last := chain[len(chain)-1]
	winner, j, err := readCreateNormalTailStage(store, e, last.Descriptor.ForwardStage, last.Descriptor.Target.Plan)
	if err != nil {
		return nil, err
	}
	for i := len(chain) - 1; i > 0; i-- {
		prior := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+chain[i-1].Descriptor.ForwardStage)
		next := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+chain[i].Descriptor.ForwardStage)
		effective := *chain[i].Descriptor
		effective.ForwardStage = last.Descriptor.ForwardStage
		if prior == nil || !createNormalTailRuntimeResponseMatches(*prior, &effective, winner, j) || prior.State != "pending" && (next == nil || next.State != "completed" || prior.State == "completed" && prior.CompletedAt.Before(next.CompletedAt)) {
			return nil, createNormalTailRuntimeBlock("numbered logical response")
		}
	}
	frozen, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return nil, err
	}
	original, originalJournal, err := readCreateNormalTailWorkloadOriginal(store, e, frozen)
	if err != nil {
		return nil, err
	}
	first, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, original, originalJournal)
	if err != nil {
		return nil, err
	}
	previous, err := readCreateNormalTailProductSessionForwardSource(store, e, original, originalJournal, first)
	if err != nil {
		return nil, err
	}
	effective := *chain[0].Descriptor
	effective.ForwardStage = last.Descriptor.ForwardStage
	effective.Target = last.Descriptor.Target
	if err := validateCreateNormalTailRuntimeAliases(e, original, originalJournal, first, previous, chain[0].PreviousJournal, &effective, winner, j); err != nil {
		return nil, err
	}
	return last.Descriptor, nil
}

func createNormalTailRuntimeResponseMatches(entry environmentCreateJournalEntry, d *createNormalTailRuntimeForward, p *deploymentplan.Plan, j deploymentplan.Journal) bool {
	if entry.State == "pending" {
		return len(entry.Receipt) == 0 && entry.CompletedAt.IsZero()
	}
	if d == nil || p == nil || !j.Accepted {
		return false
	}
	want := createNormalTailRuntimeForwardReceipt{Schema: createNormalTailRuntimeForwardSchema, PreviousPlanSHA256: d.PreviousPlanSHA256, PreviousJournalSHA256: d.PreviousJournalSHA256, SourceSHA256: d.SourceSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: p.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	var got createNormalTailRuntimeForwardReceipt
	return decodeEnvironmentJSON(entry.Receipt, &got) == nil && deploymentplan.Digest(got) == deploymentplan.Digest(want)
}

func validateCreateNormalTailRuntimeAliases(e *environmentCreateExecution, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, first *createNormalTailWorkloadForward, previous *createNormalTailProductSessionForward, priorJournal deploymentplan.Journal, d *createNormalTailRuntimeForward, winner *deploymentplan.Plan, j deploymentplan.Journal) error {
	bad := func() error { return createNormalTailRuntimeBlock("truthful logical completion") }
	old := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.ForwardStage)
	firstEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+first.ForwardStage)
	outer := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	actual := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage)
	if old == nil || firstEntry == nil || outer == nil || !createNormalTailRuntimeResponseMatches(*old, d, winner, j) {
		return bad()
	}
	for _, entry := range []*environmentCreateJournalEntry{old, firstEntry, outer} {
		if entry.StartedAt.IsZero() || entry.StartedAt.After(time.Now().UTC()) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || entry.State == "pending" && (len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero()) || entry.State == "allocated" && (len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero()) || entry.State == "completed" && (len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC())) {
			return bad()
		}
	}
	if old.State != "pending" && (actual == nil || actual.State != "completed" || !j.Accepted || old.State == "completed" && old.CompletedAt.Before(actual.CompletedAt)) {
		return bad()
	}
	effective := *previous
	effective.ForwardStage, effective.Target.Plan = d.ForwardStage, d.Target.Plan
	if !productSessionForwardResponseMatches(*firstEntry, &effective, winner, j) || firstEntry.State != "pending" && (old.State != "completed" || actual == nil || actual.State != "completed" || firstEntry.State == "completed" && firstEntry.CompletedAt.Before(old.CompletedAt)) {
		return bad()
	}
	if outer.State == "pending" {
		if len(outer.Receipt) != 0 || !outer.CompletedAt.IsZero() {
			return bad()
		}
	} else {
		var got createNormalTailWorkloadForwardReceipt
		if winner == nil || !j.Accepted || firstEntry.State != "completed" || actual == nil || actual.State != "completed" {
			return bad()
		}
		want := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(originalJournal), ForwardStage: d.ForwardStage, ForwardPlanSHA256: winner.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
		if decodeEnvironmentJSON(outer.Receipt, &got) != nil || deploymentplan.Digest(got) != deploymentplan.Digest(want) || outer.State == "completed" && outer.CompletedAt.Before(firstEntry.CompletedAt) {
			return bad()
		}
	}
	return nil
}

func readCreateNormalTailRuntimeWorkloadSources(store secretStore, e *environmentCreateExecution, frozen, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, first *createNormalTailWorkloadForward, previous *createNormalTailProductSessionForward, d *createNormalTailRuntimeForward) ([]createNormalTailVolumeStage, error) {
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return nil, err
	}
	return readCreateNormalTailRuntimeWorkloadSourcesFromChain(frozen, original, originalJournal, first, previous, d, chain)
}

// Assemble allocation sources from the chain authenticated during this read.
// Independent callers above still authenticate a fresh chain on each invocation.
func readCreateNormalTailRuntimeWorkloadSourcesFromChain(frozen, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, first *createNormalTailWorkloadForward, previous *createNormalTailProductSessionForward, d *createNormalTailRuntimeForward, chain []createNormalTailRuntimeStep) ([]createNormalTailVolumeStage, error) {
	if d == nil || frozen == nil || len(chain) == 0 {
		return nil, createNormalTailRuntimeBlock("allocation target")
	}
	known := frozen.SHA256() == original.SHA256() || frozen.SHA256() == first.Target.Plan.SHA256() || frozen.SHA256() == previous.Target.Plan.SHA256()
	for _, step := range chain {
		known = known || frozen.SHA256() == step.Descriptor.Target.Plan.SHA256()
	}
	if !known {
		return nil, createNormalTailRuntimeBlock("allocation target")
	}
	sources := []createNormalTailVolumeStage{{Stage: "workload", Plan: original, Journal: originalJournal}, {Stage: previous.ForwardStage, Plan: chain[0].PreviousPlan, Journal: chain[0].PreviousJournal, originalPlanSHA256: original.SHA256()}}
	for _, step := range chain {
		if step.Child != nil {
			sources = append(sources, createNormalTailVolumeStage{Stage: step.Descriptor.ForwardStage, Plan: step.Child, Journal: step.Journal, originalPlanSHA256: step.PreviousPlan.SHA256()})
		}
	}
	return sources, nil
}

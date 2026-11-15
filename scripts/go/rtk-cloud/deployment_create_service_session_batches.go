package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// serviceSessionBatch is an immutable scoped candidate for one recorded prior
// session. Candidates are executed in the caller's dependency order.
type serviceSessionBatch struct {
	Previous string
	Stage    string
	Plan     *deploymentplan.Plan
	Pending  []serviceSessionPendingOwner
}

// Each admitted update is derived from its original baseline by changing only
// the selected session reference. The frozen plan remains independently bound
// to its environment, release and recorded owner UID.
func validServiceSessionBatches(q secondSessionAdmission) bool {
	if len(q.Batches) < 2 {
		return false
	}
	previous := serviceSessionBatchPrevious(q)
	if previous[0] != q.Renewal.OriginalState.Session || previous[len(previous)-1] != q.Renewal.Intent.SessionID {
		return false
	}
	next := q.Renewal.RenewedIntent.SessionID
	seen := map[string]bool{}
	for i, batch := range q.Batches {
		old := previous[i]
		if !environmentBootstrapOwnerUUID.MatchString(old) || old == next || seen["session/"+old] || batch.Previous != old || batch.Stage != "service-session-forward-"+deploymentplan.Digest([]string{old, next})[:16] {
			return false
		}
		seen["session/"+old] = true
		if batch.Plan != nil {
			p := batch.Plan
			if p.Version != deploymentplan.Version || p.Environment != q.Environment || p.Stack != q.SourceJournal.Stack || p.Operation != "scoped" || len(p.Resources) == 0 || !environmentPreflightDigest.MatchString(p.ConfigurationSHA256) || !environmentPreflightDigest.MatchString(p.ReleaseSHA256) {
				return false
			}
			for _, resource := range p.Resources {
				if resource.ID == "" || seen[resource.ID] || resource.Action != "update" || resource.Desired["kind"] != "Deployment" || resource.UID == "" || resource.Baseline == nil || deploymentplan.ID(resource.Desired) != resource.ID {
					return false
				}
				seen[resource.ID] = true
				before := deploymentplan.Clone(resource.Desired)
				count, err := serviceSessionSIDSlots(before, next, old)
				if err != nil || count == 0 || !deploymentplan.Matches(before, resource.Baseline) {
					return false
				}
			}
		}
		for _, pending := range batch.Pending {
			if pending.ID == "" || seen[pending.ID] || pending.Reason != "non-deployment" && pending.Reason != "not-written" {
				return false
			}
			seen[pending.ID] = true
		}
	}
	return true
}

// prepareRecordedServiceSessionBatches joins the authenticated interrupted q2
// application lineage to the latest completed core owners. Inventory is used
// only to check the recorded owner UIDs and prepare exact update baselines.
func prepareRecordedServiceSessionBatches(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, inventory func(context.Context, string) ([]deploymentplan.Object, error), admission secondSessionAdmission, previous []string, next string) ([]serviceSessionBatch, error) {
	if e == nil || backend == nil || inventory == nil || e.Check(ctx) != nil {
		return nil, createProviderRecoveryBlock("Service session requires the original parent execution")
	}
	if !validSecondSessionAdmission(store, e, admission) || next != admission.Renewal.RenewedIntent.SessionID || len(previous) < 2 || previous[0] != admission.Renewal.OriginalState.Session || previous[len(previous)-1] != admission.Renewal.Intent.SessionID {
		return nil, createProviderRecoveryBlock("Service session batches differ from their admitted original lineage")
	}
	historical, err := verifyServiceSessionHistoricalJobs(ctx, store, e, backend, admission)
	if err != nil {
		return nil, err
	}
	custody, err := verifyServiceSessionMintCustody(ctx, store, e, backend, admission)
	if err != nil {
		return nil, err
	}
	application, err := loadServiceSessionApplicationSources(ctx, store, e, backend)
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, source := range application {
		if source.SelectedIDs == nil {
			return nil, createProviderRecoveryBlock("Service application owner selection is unavailable")
		}
		for id := range source.SelectedIDs {
			excluded[id] = true
		}
	}
	for _, job := range historical {
		if excluded[job.ID] {
			return nil, createProviderRecoveryBlock("Service historical Job overlaps an application owner")
		}
		excluded[job.ID] = true
	}
	ids, err := serviceSessionCoreOwnerIDs(store, e, previous, excluded)
	if err != nil {
		return nil, err
	}
	sources := append([]serviceSessionRecordedPlan(nil), application...)
	if len(ids) > 0 {
		core, err := loadServiceSessionCoreSources(ctx, store, e, backend, ids)
		if err != nil {
			return nil, err
		}
		sources = append(sources, core...)
	}
	live, err := inventory(ctx, e.Journal.Stack)
	if err != nil || e.Check(ctx) != nil {
		return nil, createProviderRecoveryBlock("Service session original owner inventory is unavailable")
	}
	return compileServiceSessionBatches(sources, live, previous, next, append(historical, custody))
}

// compileServiceSessionBatches supports the concurrently installed original
// and successor sessions without selecting any owner from live inventory.
// An owner referring to multiple prior sessions must first be handled as an
// explicit single-owner transition; independent plans cannot safely race.
func compileServiceSessionBatches(sources []serviceSessionRecordedPlan, live []deploymentplan.Object, previous []string, next string, verifiedHistory ...[]serviceSessionHistoricalJob) ([]serviceSessionBatch, error) {
	bad := func() ([]serviceSessionBatch, error) {
		return nil, createProviderRecoveryBlock("Service session batches require distinct recorded previous sessions and owners")
	}
	if len(previous) == 0 || len(verifiedHistory) > 1 || !environmentBootstrapOwnerUUID.MatchString(next) {
		return bad()
	}
	seenSessions := map[string]bool{}
	seenOwners := map[string]bool{}
	var result []serviceSessionBatch
	for _, old := range previous {
		if !environmentBootstrapOwnerUUID.MatchString(old) || old == next || seenSessions[old] {
			return bad()
		}
		seenSessions[old] = true
		stage := "service-session-forward-" + deploymentplan.Digest([]string{old, next})[:16]
		plan, pending, err := compileServiceSessionSIDForward(sources, live, old, next, stage, verifiedHistory...)
		if err != nil {
			return nil, err
		}
		if plan != nil {
			for _, r := range plan.Resources {
				if seenOwners[r.ID] {
					return bad()
				}
				seenOwners[r.ID] = true
			}
		}
		for _, owner := range pending {
			if seenOwners[owner.ID] {
				return bad()
			}
			seenOwners[owner.ID] = true
		}
		result = append(result, serviceSessionBatch{Previous: old, Stage: stage, Plan: plan, Pending: pending})
	}
	return result, nil
}

package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// An interrupted ConfigMap replace is evidence only when the original
// admission, parent intent and native child journal record its exact UID.
func pendingServiceSessionReferences(ctx context.Context, store secretStore, e *environmentCreateExecution) ([]serviceSessionRecordedPlan, error) {
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("interrupted Service session reference evidence changed")
	}
	if e == nil || e.Check(ctx) != nil {
		return bad()
	}
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "session-lineage" {
			if _, _, err := sessionLineageHead(store, e); err != nil {
				return bad()
			}
			break
		}
	}
	var result []serviceSessionRecordedPlan
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "session-lineage" || !strings.HasPrefix(entry.Key, sessionLineagePrefix) {
			continue
		}
		review := strings.TrimPrefix(entry.Key, sessionLineagePrefix)
		q, err := loadSecondSessionAdmission(store, e, review)
		if err != nil {
			return bad()
		}
		if q.Reference == nil {
			continue
		}
		stage := q.Reference.Stage
		parent := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
		if parent == nil || parent.State == "completed" {
			continue
		}
		if verifyServiceSessionReferenceSource(store, e, q) != nil {
			return bad()
		}
		input, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || input == nil || input.SHA256() != q.Reference.Plan.SHA256() {
			return bad()
		}
		j, found, err := readEnvironmentCreateStageProgress(store, e, stage, input)
		if err != nil || !found || j.InFlight != "" {
			return bad()
		}
		if len(input.Resources) != 1 {
			return bad()
		}
		id := input.Resources[0].ID
		if j.Owners[id] == "" {
			continue
		}
		if j.Owners[id] != q.Reference.UID || len(j.Completed) != 1 && j.Failed != id {
			return bad()
		}
		result = append(result, serviceSessionRecordedPlan{Stage: stage, Plan: input, Journal: j})
	}
	return result, nil
}

// Follow only admitted reference stages, preserving each original parent and
// native child journal. A reference recorded but not accepted is not an ACK.
func serviceSessionReferenceRecordedSource(store secretStore, e *environmentCreateExecution, stage string, seen map[string]bool) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("original Service reference owner chain changed")
	}
	if e == nil || seen[stage] {
		return bad()
	}
	seen[stage] = true
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil {
		return bad()
	}
	if entry.State == "completed" || !strings.HasPrefix(stage, "service-session-reference-") {
		p, j, err := serviceSessionReferenceNativeSource(store, e, stage)
		if err != nil || p == nil {
			return bad()
		}
		checked, found, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil || !found || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
			return bad()
		}
		return p, j, nil
	}
	if _, _, err := sessionLineageHead(store, e); err != nil {
		return bad()
	}
	for _, line := range e.Journal.Entries {
		if line.Phase != "session-lineage" || !strings.HasPrefix(line.Key, sessionLineagePrefix) || line.State != "completed" {
			continue
		}
		review := strings.TrimPrefix(line.Key, sessionLineagePrefix)
		q, err := loadSecondSessionAdmission(store, e, review)
		if err != nil {
			return bad()
		}
		if q.Reference == nil || q.Reference.Stage != stage {
			continue
		}
		intent, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(sessionLineageIntent(review, q)))
		if err != nil || line.IntentSHA256 != intent || !samePersistedJSONReceipt(line.Receipt, mustProviderRecoveryJSON(sessionLineageIntent(review, q))) {
			return bad()
		}
		batch := createOpenBaoACKCompletionEntry(e.Journal, "service-session-batches-"+review)
		batchIntent, err := environmentCreatePlanSHA256(serviceSessionBatchIntent(review, q))
		if err != nil || batch == nil || batch.Phase != "service-session-batches" || batch.IntentSHA256 != batchIntent {
			return bad()
		}
		host, hostJournal, err := serviceSessionReferenceNativeSource(store, e, q.Reference.HostStage)
		if err != nil || host == nil || host.SHA256() != q.Reference.HostPlanSHA || deploymentplan.Digest(hostJournal) != q.Reference.HostJournalSHA || hostJournal.Owners[q.Reference.HostID] != q.Reference.HostUID {
			return bad()
		}
		checked, ok, err := readEnvironmentCreateStageProgress(store, e, q.Reference.HostStage, host)
		if err != nil || !ok || deploymentplan.Digest(checked) != q.Reference.HostJournalSHA {
			return bad()
		}
		var hostObject deploymentplan.Object
		for _, resource := range host.Resources {
			if resource.ID == q.Reference.HostID {
				if hostObject != nil {
					return bad()
				}
				hostObject = resource.Desired
			}
		}
		projected, err := serviceSessionTrustMapID(hostObject)
		if err != nil || projected != q.Reference.Plan.Resources[0].ID {
			return bad()
		}
		prior, priorJournal, err := serviceSessionReferenceRecordedSource(store, e, q.Reference.SourceStage, seen)
		if err != nil || prior.SHA256() != q.Reference.SourcePlanSHA || deploymentplan.Digest(priorJournal) != q.Reference.SourceJournalSHA || priorJournal.Owners[q.Reference.Plan.Resources[0].ID] != q.Reference.UID {
			return bad()
		}
		p, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || p == nil || p.SHA256() != q.Reference.Plan.SHA256() {
			return bad()
		}
		j, found, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil || !found || j.InFlight != "" || len(p.Resources) != 1 {
			return bad()
		}
		id := p.Resources[0].ID
		if j.Owners[id] != q.Reference.UID || len(j.Completed) != 1 && j.Failed != id {
			return bad()
		}
		return p, j, nil
	}
	return bad()
}

// A projection source can be a known ordinary write whose whole-workload
// readiness is pending. Preserve its literal frozen input/journal and lease;
// this is owner evidence, never fabricated acceptance or an installation ACK.
func serviceSessionReferenceNativeSource(store secretStore, e *environmentCreateExecution, stage string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	p, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || p == nil {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("original Service projection input unavailable")
	}
	j, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
	if err != nil || !exists || j.InFlight != "" || len(j.Owners) == 0 {
		return nil, j, createProviderRecoveryBlock("original Service projection native write unavailable")
	}
	return p, j, nil
}

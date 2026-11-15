package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A failed all-retain child stays immutable. A completed session can supply a
// separately accepted current-owner child, preserving every original UID.
func initializerSessionRetainPlan(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, q2 createInitializerMountForwardProof, original *deploymentplan.Plan, review string) (string, *deploymentplan.Plan, error) {
	return initializerSessionRetainPlanWithSource(ctx, store, e, backend, q2, original, review, nil)
}

func initializerSessionRetainPlanWithSource(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, q2 createInitializerMountForwardProof, original *deploymentplan.Plan, review string, recorded *deploymentplan.Journal) (string, *deploymentplan.Plan, error) {
	bad := func() (string, *deploymentplan.Plan, error) {
		return "", nil, createProviderRecoveryBlock("session retain successor lacks its original failed owners and completed session lineage")
	}
	head, headReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil || original == nil {
		return bad()
	}
	if review == "" {
		review = headReview
	}
	selected, err := loadSecondSessionAdmission(store, e, review)
	if err != nil {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, selected, review)
	if err != nil || !done {
		return bad()
	}
	// Every selected review must be an ancestor in the admitted native chain.
	allowed := map[string]bool{}
	found := false
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "service-session-batches" || !strings.HasPrefix(entry.Key, "service-session-batches-") {
			continue
		}
		current := strings.TrimPrefix(entry.Key, "service-session-batches-")
		q, err := loadSecondSessionAdmission(store, e, current)
		if err != nil {
			return bad()
		}
		for _, batch := range q.Batches {
			if batch.Plan != nil {
				allowed[batch.Stage] = true
			}
		}
		if current == review {
			found = true
			break
		}
	}
	if !found {
		return bad()
	}
	saved, err := readEnvironmentCreateStageInput(store, e, q2.RetainStage)
	if err != nil || saved == nil || saved.SHA256() != original.SHA256() || original.SHA256() != q2.RetainPlanSHA256 {
		return bad()
	}
	var j deploymentplan.Journal
	var exists bool
	parentEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q2.RetainStage)
	intent, intentErr := environmentCreateKubernetesStageIntent(original)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	leased := false
	for _, lease := range e.Journal.KubernetesLeases {
		leased = leased || managedLateLeaseMatches(lease, e.Journal.Stack, original.SHA256())
	}
	if parentEntry == nil || parentEntry.Phase != q2.RetainStage || intentErr != nil || hashErr != nil || parentEntry.IntentSHA256 != intentSHA || !leased {
		return bad()
	}
	if recorded == nil {
		j, exists, err = readEnvironmentCreateStageProgress(store, e, q2.RetainStage, original)
	} else {
		j, exists, err = original.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(j) != deploymentplan.Digest(*recorded) {
			return bad()
		}
	}
	if err != nil || !exists || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed == "" || len(j.Completed) != len(original.Resources) || len(j.Owners) != len(original.Resources) {
		return bad()
	}
	var bases []activeTransportRenewalResource
	for i, r := range original.Resources {
		if r.Action != "retain" || j.Completed[i] != r.ID || r.UID == "" || j.Owners[r.ID] != r.UID {
			return bad()
		}
		bases = append(bases, sessionRecordedOwnerBase(r.ID, r.UID, r.Desired))
	}
	sources, err := sessionRecordedStages(ctx, store, e)
	if err != nil {
		return bad()
	}
	var stages []completedRecordedSuccessorStage
	for _, source := range sources {
		if allowed[source.Stage] {
			stages = append(stages, completedRecordedSuccessorStage{Plan: source.Plan, Journal: source.Journal, Written: !source.Journal.Accepted})
		}
	}
	latest, err := foldCompletedRecordedSuccessors(bases, stages)
	if err != nil {
		return bad()
	}
	stage := "session-owner-retain-" + deploymentplan.Digest(q2)[:12] + "-" + review[:12]
	inputs := original.Inputs
	inputs.BindingsSHA256 = deploymentplan.Digest(deploymentplan.Object{"profile": "completed-session-owner-retain/v1", "original_qualification": deploymentplan.Digest(q2), "original_plan": original.SHA256(), "original_journal": deploymentplan.Digest(j), "session_qualification": review})
	relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "session-retain", inputs.BindingsSHA256, "candidate.json")
	path, err := store.safePath(relative)
	if err != nil {
		return bad()
	}
	var frozen *deploymentplan.Plan
	if _, err := os.Lstat(path); err == nil {
		frozen = &deploymentplan.Plan{}
		if readExecutionJSON(path, frozen) != nil || frozen.Inputs != inputs || len(frozen.Resources) != len(original.Resources) {
			return bad()
		}
	} else {
		if !os.IsNotExist(err) || review != headReview {
			return bad()
		}
		// Missing operational evidence after execution is never permission to
		// reconstruct a qualification from the current environment.
		if recorded, err := readEnvironmentCreateStageInput(store, e, stage); err != nil || recorded != nil {
			return bad()
		}
	}
	var defs []deploymentplan.Definition
	var live []deploymentplan.Object
	for i, r := range original.Resources {
		desired := latest[i].Desired
		var baseline deploymentplan.Object
		if frozen != nil {
			baseline = deploymentplan.Clone(frozen.Resources[i].Baseline)
			if baseline == nil || frozen.Resources[i].UID != r.UID {
				return bad()
			}
			deploymentplan.Map(baseline["metadata"])["uid"] = r.UID
		} else {
			baseline, err = backend.Read(ctx, r.ID)
			if err != nil || baseline == nil || deploymentplan.Map(baseline["metadata"])["uid"] != r.UID || !deploymentplan.Matches(desired, baseline) {
				return bad()
			}
		}
		live = append(live, baseline)
		defs = append(defs, deploymentplan.Definition{Object: desired, Phase: r.Phase, Dependencies: slices.Clone(r.Dependencies), Retain: true})
	}
	candidate, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil || len(candidate.Resources) != len(original.Resources) {
		return bad()
	}
	for i, r := range candidate.Resources {
		old := original.Resources[i]
		if r.ID != old.ID || r.UID != old.UID || r.Action != "retain" || r.Phase != old.Phase || !slices.Equal(r.Dependencies, old.Dependencies) {
			return bad()
		}
	}
	if frozen != nil {
		if frozen.SHA256() != candidate.SHA256() {
			return bad()
		}
	} else if err := store.write(relative, mustProviderRecoveryJSON(candidate), false); err != nil {
		return "", nil, err
	}
	return stage, candidate, nil
}

// Completed aliases pin their historical session; a later head cannot replace
// their completion evidence. A fresh handoff selects the current completed head.
func initializerSessionRetainReview(store secretStore, e *environmentCreateExecution, sourceStage string) (string, error) {
	if _, _, exists, err := loadPartialSessionAdmission(store, e); err != nil || !exists {
		return "", err
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+sourceStage); entry != nil && (entry.State == "allocated" || entry.State == "completed") {
		var alias createFailedForwardAlias
		if decodeEnvironmentJSON(entry.Receipt, &alias) != nil {
			return "", createProviderRecoveryBlock("original session retain alias is unreadable")
		}
		return alias.ServiceSessionQualificationSHA256, nil
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil || head == nil || head.Batches == nil {
		return "", err
	}
	done, err := completedServiceSessionBatch(store, e, *head, review)
	if err != nil || !done {
		return "", err
	}
	return review, nil
}

func qualifyInitializerSessionRetain(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, q2 createInitializerMountForwardProof, q0 createFailedForwardProof, retained *deploymentplan.Plan) (string, *deploymentplan.Plan, error) {
	// Without any partial admission this is the ordinary historical q2 path.
	if _, _, exists, err := loadPartialSessionAdmission(store, e); err != nil || !exists {
		return "", nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q2.RetainStage)
	if entry == nil || entry.State == "completed" {
		return "", nil, nil
	}
	review, err := initializerSessionRetainReview(store, e, q0.FailedStage.Stage)
	if err != nil || review == "" {
		return "", nil, err
	}
	stage, candidate, err := initializerSessionRetainPlan(ctx, store, e, backend, q2, retained, review)
	if err != nil {
		return "", nil, err
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage); entry != nil && entry.State == "completed" {
		_, err = verifyAcceptedStageAfterRecordedHistory(ctx, store, e, backend, "kubernetes/"+stage)
	} else {
		err = candidate.AdmitResumable(ctx, backend, store.Root)
	}
	return stage, candidate, err
}

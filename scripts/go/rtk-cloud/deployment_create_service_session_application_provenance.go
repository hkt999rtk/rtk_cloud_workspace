package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// loadServiceSessionApplicationSources authenticates original application
// writes and the q0/q1/q2 frozen chain, including a written but not-yet-ready
// retain. It never uses live inventory as desired configuration.
func loadServiceSessionApplicationSources(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) ([]serviceSessionRecordedPlan, error) {
	boundary, sourceName := "original-execution", ""
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("Service session application owners lack their authenticated original successor chain; boundary=" + boundary + " source=" + sourceName)
	}
	if e == nil || e.Store != store || b == nil || e.Check(ctx) != nil {
		return bad()
	}
	// This checks native owner UID/body and exact q0/q1/q2 provenance, but does
	// not wait for readiness or claim application acceptance.
	if _, err := completedApplicationOwnerWrites(ctx, store, e, b); err != nil {
		return nil, err
	}
	boundary = "initializer-review"
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "initializer-mount-forward-"
		if entry.Phase == "initializer-mount-forward" && strings.HasPrefix(entry.Key, prefix) {
			if review != "" {
				return bad()
			}
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if !environmentPreflightDigest.MatchString(review) {
		return bad()
	}
	boundary = "q2-bundle"
	q2, q2Update, q2Retain, err := loadInitializerMountForwardBundle(store, e, review)
	if err != nil {
		return bad()
	}
	boundary = "q1-bundle"
	q1, q1Update, q1Retain, err := loadInitializerForwardBundle(store, e, q2.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	boundary = "q0-bundle"
	q0, _, _, err := loadFailedForwardBundle(store, e, q1.FailedForwardSHA256)
	if err != nil {
		return bad()
	}
	admitted, admission, err := authenticatedInitializerForwardAdmission(store, e, q1)
	if err != nil {
		return nil, err
	}
	if err := validateInitializerMountForward(ctx, store, e, q2, q1, q0, admitted, admission, q1Update, q1Retain, q2Update, q2Retain, q2.ExecutionDeadline); err != nil {
		return nil, err
	}
	originalEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q0.FailedStage.Stage)
	if originalEntry == nil || originalEntry.State != "completed" {
		if _, found, err := earlyInitializerMountForwardSuccessor(ctx, store, e, b, q1, q0, admitted, admission, q1Update, q1Retain); err != nil {
			return nil, err
		} else if !found {
			return bad()
		}
	}
	stages := []struct {
		name     string
		expected *deploymentplan.Plan
	}{
		{q0.FailedStage.Stage, nil}, {q1.FailedRetainStage, nil}, {q1.UpdateStage, q1Update}, {q2.UpdateStage, q2Update}, {q2.RetainStage, q2Retain},
	}
	var sources []serviceSessionRecordedPlan
	for i, stage := range stages {
		boundary, sourceName = "source-input", stage.name
		p, err := readEnvironmentCreateStageInput(store, e, stage.name)
		if err != nil {
			return bad()
		}
		if p == nil {
			if i < 3 {
				return bad()
			}
			continue
		}
		if stage.expected != nil && p.SHA256() != stage.expected.SHA256() {
			return bad()
		}
		boundary = "source-native-journal"
		var j deploymentplan.Journal
		var ok bool
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage.name)
		if i == 0 && entry != nil && (entry.State == "allocated" || entry.State == "completed") {
			// completedApplicationOwnerWrites above authenticated this exact
			// q0/q1/q2 alias and its real accepted successor. Its original child
			// remains FAILED; the generic native completed receipt is inapplicable.
			// Read that literal source, retaining the original intent and lease
			// checks rather than treating the supersession as native acceptance.
			boundary = "original-failed-application-alias"
			j, ok, err = p.ReadExecution(store.Root)
			leased := false
			for _, lease := range e.Journal.KubernetesLeases {
				leased = leased || managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256())
			}
			if err != nil || !ok || !leased || p.SHA256() != q0.FailedStage.Child.PlanSHA256 || deploymentplan.Digest(j) != q0.FailedStage.Child.JournalSHA256 || j.Failed != q0.FailedStage.Child.FailedResource || entry.Phase != stage.name || entry.IntentSHA256 != q0.FailedStage.ParentIntentSHA256 {
				return bad()
			}
		} else {
			j, ok, err = readEnvironmentCreateStageProgress(store, e, stage.name, p)
			if err != nil {
				return nil, err
			}
		}
		if !ok {
			if i < 3 {
				return bad()
			}
			continue
		}
		sources = append(sources, serviceSessionRecordedPlan{Stage: stage.name, Plan: p, Journal: j})
	}
	selected, err := selectLatestServiceSessionOwners(sources)
	if err != nil {
		return nil, err
	}
	later, order, err := serviceSessionOwnerHistory(ctx, store, e)
	if err != nil {
		return bad()
	}
	return appendSessionApplicationSuccessors(selected, later, order)
}

// Keep only application IDs, while accepting a later owner solely if the
// authenticated native batch extends that exact prior body and UID.
func appendSessionApplicationSuccessors(original, later []serviceSessionRecordedPlan, order map[string]int) ([]serviceSessionRecordedPlan, error) {
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("Service application session successor changed its original owner")
	}
	out := append([]serviceSessionRecordedPlan(nil), original...)
	type owner struct {
		source  int
		uid     string
		desired deploymentplan.Object
	}
	owners := map[string]owner{}
	for i, source := range out {
		if source.Plan == nil || source.SelectedIDs == nil {
			return bad()
		}
		out[i].SelectedIDs = map[string]bool{}
		for id, selected := range source.SelectedIDs {
			if !selected {
				continue
			}
			if _, exists := owners[id]; exists {
				return bad()
			}
			var desired deploymentplan.Object
			for _, r := range source.Plan.Resources {
				if r.ID == id {
					desired = r.Desired
					break
				}
			}
			if desired == nil {
				return bad()
			}
			owners[id] = owner{source: i, uid: source.Journal.Owners[id], desired: desired}
			out[i].SelectedIDs[id] = true
		}
	}
	for _, source := range later {
		if source.Plan == nil || source.Journal.PlanSHA256 != source.Plan.SHA256() || source.Journal.InFlight != "" || len(source.Journal.Completed) > len(source.Plan.Resources) || source.Journal.Accepted && (source.Journal.Failed != "" || len(source.Journal.Completed) != len(source.Plan.Resources)) {
			return bad()
		}
		index := len(out)
		source.SelectedIDs = map[string]bool{}
		for i, r := range source.Plan.Resources {
			if i < len(source.Journal.Completed) {
				if source.Journal.Completed[i] != r.ID {
					return bad()
				}
			} else if source.Journal.Failed != r.ID || source.Journal.Owners[r.ID] == "" {
				continue
			}
			prior, exists := owners[r.ID]
			if exists && order != nil && order[source.Stage] <= order[out[prior.source].Stage] {
				continue // An owner starts at its selected original source, not an earlier stage.
			}
			if !exists {
				continue
			}
			if prior.uid == "" || r.UID != prior.uid || source.Journal.Owners[r.ID] != prior.uid || r.Baseline == nil || !deploymentplan.Matches(prior.desired, r.Baseline) || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			if r.Action == "retain" {
				if !deploymentplan.Matches(r.Desired, r.Baseline) {
					return bad()
				}
				continue // No write: preserve the last actual owner body.
			}
			if r.Action != "update" {
				return bad()
			}
			delete(out[prior.source].SelectedIDs, r.ID)
			source.SelectedIDs[r.ID] = true
			owners[r.ID] = owner{source: index, uid: prior.uid, desired: r.Desired}
		}
		out = append(out, source)
	}
	return out, nil
}

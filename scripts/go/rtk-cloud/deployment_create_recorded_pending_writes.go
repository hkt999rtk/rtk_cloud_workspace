package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Authenticate ordinary original stage writes and the existing q0/q1/q2
// lineage. A written prefix is not readiness or acceptance.
func authenticatedRecordedPendingWrites(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) (map[string]completedRecordedSuccessorStage, error) {
	return authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, b, authenticatedInitializerForwardAdmission)
}

func authenticatedRecordedPendingWritesWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, load initializerForwardAdmissionLoader) (map[string]completedRecordedSuccessorStage, error) {
	boundary := "original-execution"
	bad := func() (map[string]completedRecordedSuccessorStage, error) {
		return nil, createProviderRecoveryBlock("recorded pending owner successor cannot be authenticated; boundary=" + boundary)
	}
	if e == nil || e.Store != store || b == nil || e.Check(ctx) != nil {
		return bad()
	}
	out := map[string]completedRecordedSuccessorStage{}
	const prefix = "initializer-mount-forward-"
	review := ""
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "initializer-mount-forward" && strings.HasPrefix(entry.Key, prefix) {
			if review != "" {
				return bad()
			}
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if review == "" {
		return ordinaryRecordedPendingWrites(ctx, store, e, out)
	}
	boundary = "mount-forward-bundle"
	q2, q2Update, _, err := loadInitializerMountForwardBundle(store, e, review)
	if err != nil {
		return bad()
	}
	boundary = "initializer-forward-bundle"
	q1, q1Update, _, err := loadInitializerForwardBundle(store, e, q2.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	boundary = "original-forward-bundle"
	q0, repair, oldRetain, err := loadFailedForwardBundle(store, e, q1.FailedForwardSHA256)
	if err != nil {
		return bad()
	}
	boundary = "original-stage-input"
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil {
		return bad()
	}
	originalEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q0.FailedStage.Stage)
	if originalEntry == nil {
		return bad()
	}
	var source deploymentplan.Journal
	aliased := originalEntry.State == "completed" || originalEntry.State == "allocated"
	if aliased {
		boundary = "accepted-alias-source"
		var alias createFailedForwardAlias
		var ok bool
		source, ok, err = original.ReadExecution(store.Root)
		if err != nil || !ok || decodeEnvironmentJSON(originalEntry.Receipt, &alias) != nil ||
			alias.QualificationSHA256 != deploymentplan.Digest(q0) || alias.InitializerQualificationSHA256 != deploymentplan.Digest(q1) ||
			alias.InitializerMountQualificationSHA256 != review || alias.OriginalPlanSHA256 != original.SHA256() ||
			alias.OriginalJournalSHA256 != deploymentplan.Digest(source) || alias.FailedResource != source.Failed ||
			originalEntry.Phase != q0.FailedStage.Stage || originalEntry.IntentSHA256 != q0.FailedStage.ParentIntentSHA256 {
			return bad()
		}
		boundary = "accepted-alias-proof"
		if _, err = verifyInitializerMountSuccessorAliasWithAdmission(ctx, store, e, b, alias, original, source, q0, repair, oldRetain, load); err != nil {
			return bad()
		}
	} else {
		var ok bool
		source, ok, err = readEnvironmentCreateStageProgress(store, e, q0.FailedStage.Stage, original)
		if err != nil || !ok {
			return bad()
		}
	}
	boundary = "initializer-written-successor"
	latest := q2Update
	if !aliased {
		latest, err = earlyInitializerForwardSuccessorWithAdmission(ctx, store, e, b, original, source, load)
	}
	// An accepted alias already authenticates q2's literal update and retained
	// owner chain. Its old body is history: later ordinary writes are folded
	// below before callers compare the latest same-UID body with live state.
	if err != nil || latest == nil || len(latest.Resources) != 1 {
		return bad()
	}
	// The q2 proof binds q1's actual journal, while the early verifier checks
	// the selected live owner and exact desired body under the original fence.
	if latest.SHA256() != q1Update.SHA256() && latest.SHA256() != q2Update.SHA256() {
		return bad()
	}
	candidates := []struct {
		stage string
		plan  *deploymentplan.Plan
	}{
		{q0.FailedStage.Stage, original}, {q1.UpdateStage, q1Update},
	}
	if latest.SHA256() == q2Update.SHA256() {
		candidates = append(candidates, struct {
			stage string
			plan  *deploymentplan.Plan
		}{q2.UpdateStage, q2Update})
	}
	for _, candidate := range candidates {
		boundary = "child-progress/" + candidate.stage
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+candidate.stage)
		if entry == nil {
			return bad()
		}
		var j deploymentplan.Journal
		var exists bool
		if candidate.stage == q0.FailedStage.Stage && entry.State != "pending" {
			j, exists = source, true // the alias and source were authenticated above
		} else {
			j, exists, err = readEnvironmentCreateStageProgress(store, e, candidate.stage, candidate.plan)
		}
		if err != nil || !exists || j.InFlight != "" {
			return bad()
		}
		// Completed native child stages are handled by accepted receipts. The
		// original interrupted stage is instead backed by its verified alias.
		if entry.State == "completed" && candidate.stage != q0.FailedStage.Stage {
			continue
		}
		if entry.State != "pending" && entry.State != "allocated" && !(entry.State == "completed" && candidate.stage == q0.FailedStage.Stage) {
			return bad()
		}
		out[entry.Key] = completedRecordedSuccessorStage{Plan: candidate.plan, Journal: j, Written: true}
	}
	return ordinaryRecordedPendingWrites(ctx, store, e, out)
}

// Typed aliases above retain their separate authority. For other pending
// requests, the immutable input, original intent and leased child journal must
// prove the known write prefix. Only after chronological folding can callers
// compare its latest same-UID body with native state; a historical body need not
// remain live. Neither a failed readiness check nor acceptance rewrites history.
func ordinaryRecordedPendingWrites(ctx context.Context, store secretStore, e *environmentCreateExecution, typed map[string]completedRecordedSuccessorStage) (map[string]completedRecordedSuccessorStage, error) {
	bad := func() (map[string]completedRecordedSuccessorStage, error) {
		return nil, createProviderRecoveryBlock("original pending stage write cannot be authenticated")
	}
	out := make(map[string]completedRecordedSuccessorStage, len(typed))
	for key, stage := range typed {
		out[key] = stage
	}
	seen := map[string]bool{}
	for _, entry := range e.Journal.Entries {
		if entry.State != "pending" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		if seen[entry.Key] {
			return bad()
		}
		seen[entry.Key] = true
		if _, authenticated := typed[entry.Key]; authenticated {
			continue
		}
		if len(entry.Receipt) != 0 {
			return bad()
		}
		stage := strings.TrimPrefix(entry.Key, "kubernetes/")
		p, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || p == nil {
			return bad()
		}
		j, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil {
			return bad()
		}
		if !exists || len(j.Owners) == 0 {
			continue // no native outcome is inferred from a current object
		}
		// readEnvironmentCreateStageProgress authenticates the literal child
		// journal and lease, including no InFlight outcome and exact known
		// prefix/owner positions. Failed may name the next unwritten resource.
		// Existing folding still refuses a tracked retirement; no absence or
		// successful readiness/acceptance is inferred by exposing these facts.
		out[entry.Key] = completedRecordedSuccessorStage{Plan: p, Journal: j, Written: true}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return out, nil
}

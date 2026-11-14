package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// prepareInitializerMountForward preserves both prior failed journals and
// captures the API-server-admitted memory subPath successor.
func prepareInitializerMountForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, q1Update, q1Retain *deploymentplan.Plan, deadline time.Time, live []deploymentplan.Object, clock func() time.Time) (createInitializerMountForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	bad := func() (createInitializerMountForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return createInitializerMountForwardProof{}, nil, nil, createProviderRecoveryBlock("private state repair requires the admitted original and exact previous initializer write")
	}
	if b == nil || clock == nil || validateInitializerForward(ctx, store, e, q1, q0, admitted, admissionSHA, q1.ExecutionDeadline, q1Update, q1Retain) != nil || verifyInitializerForwardOwners(ctx, store, e, b, q1, q1Update) != nil {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil {
		return bad()
	}
	source, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(source) != q1.OriginalJournalSHA256 {
		return bad()
	}
	oldRetain, err := readEnvironmentCreateStageInput(store, e, q1.FailedRetainStage)
	if err != nil || oldRetain == nil {
		return bad()
	}
	oldJournal, exists, err := readEnvironmentCreateStageProgress(store, e, q1.FailedRetainStage, oldRetain)
	if err != nil || !exists || deploymentplan.Digest(oldJournal) != q1.FailedRetainJournalSHA256 {
		return bad()
	}
	previous, err := readEnvironmentCreateStageInput(store, e, q1.UpdateStage)
	if err != nil || previous == nil || previous.SHA256() != q1.UpdatePlanSHA256 {
		return bad()
	}
	previousJournal, exists, err := readEnvironmentCreateStageProgress(store, e, q1.UpdateStage, previous)
	if err != nil || !exists {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "initializer-forward-"+deploymentplan.Digest(q1))
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q1), "original_admission_sha256": admissionSHA, "original_forward_sha256": q1.FailedForwardSHA256, "update_plan_sha256": q1.UpdatePlanSHA256, "retain_plan_sha256": q1.RetainPlanSHA256})
	intentSHA, intentErr := environmentCreatePlanSHA256(intent)
	if intentErr != nil || entry == nil || entry.IntentSHA256 != intentSHA || entry.Phase != "initializer-forward" || (entry.State != "pending" && entry.State != "allocated") || entry.StartedAt.Before(q1.QualifiedAt) || !entry.StartedAt.Before(deadline) {
		return bad()
	}
	if len(previous.Resources) != 1 {
		return bad()
	}
	target := previous.Resources[0]
	corrected := deploymentplan.Definition{Object: deploymentplan.Clone(target.Desired), Phase: target.Phase}
	changed, err := projectDeviceMemoryTrustPrivateSubPath(corrected.Object)
	if err != nil || !changed {
		return bad()
	}
	suffix := previous.SHA256()[:12] + "-" + original.SHA256()[:12]
	updateStage, retainStage := "private-state-update-"+suffix, "private-state-retain-"+suffix
	update, _, err := compileFailedPrivateStateForward(original, source, oldRetain, oldJournal, previous, previousJournal, corrected, live, nil, updateStage, retainStage)
	if err != nil {
		return createInitializerMountForwardProof{}, nil, nil, err
	}
	if err = update.Admit(ctx, b); err != nil {
		return createInitializerMountForwardProof{}, nil, nil, err
	}
	var current deploymentplan.Object
	for _, o := range live {
		if deploymentplan.ID(o) == target.ID {
			current = o
			break
		}
	}
	body, err := update.Resources[0].Body(current)
	if err != nil || body == nil {
		return bad()
	}
	server, err := b.Write(ctx, update.Resources[0].Action, body, true)
	if err != nil || server == nil || !deploymentplan.Matches(body, server) {
		return bad()
	}
	update, retain, err := compileFailedPrivateStateForward(original, source, oldRetain, oldJournal, previous, previousJournal, corrected, live, server, updateStage, retainStage)
	if err != nil {
		return createInitializerMountForwardProof{}, nil, nil, err
	}
	now := clock().UTC()
	if now.IsZero() || !now.Before(deadline) {
		return bad()
	}
	q := createInitializerMountForwardProof{Schema: initializerMountForwardSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), ParentPlanSHA256: e.Journal.PlanSHA256, SourceJournal: e.Journal, SourceJournalSHA256: deploymentplan.Digest(e.Journal), InitializerQualificationSHA256: deploymentplan.Digest(q1), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(source), PreviousUpdateStage: q1.UpdateStage, PreviousUpdatePlanSHA256: previous.SHA256(), PreviousUpdateJournalSHA256: deploymentplan.Digest(previousJournal), UpdateStage: updateStage, UpdatePlanSHA256: update.SHA256(), RetainStage: retainStage, RetainPlanSHA256: retain.SHA256(), QualifiedAt: now, ExecutionDeadline: deadline}
	if err = saveInitializerMountForwardBundle(store, e, q, update, retain); err != nil {
		return createInitializerMountForwardProof{}, nil, nil, err
	}
	return q, update, retain, nil
}

// validateInitializerMountForward re-renders from immutable captured
// baselines; native execution and early qualification independently read live.
func validateInitializerMountForward(ctx context.Context, store secretStore, e *environmentCreateExecution, q createInitializerMountForwardProof, q1 createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, q1Update, q1Retain, update, retain *deploymentplan.Plan, deadline time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("private state successor differs from its admitted same-owner journal chain")
	}
	if !validInitializerMountForwardBundle(store, e, q, update, retain) || q.InitializerQualificationSHA256 != deploymentplan.Digest(q1) || !q.ExecutionDeadline.Equal(deadline) || validateInitializerForward(ctx, store, e, q1, q0, admitted, admissionSHA, q1.ExecutionDeadline, q1Update, q1Retain) != nil {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q.OriginalPlanSHA256 || q.OriginalPlanSHA256 != q1.OriginalPlanSHA256 {
		return bad()
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(source) != q.OriginalJournalSHA256 || q.OriginalJournalSHA256 != q1.OriginalJournalSHA256 {
		return bad()
	}
	oldRetain, err := readEnvironmentCreateStageInput(store, e, q1.FailedRetainStage)
	if err != nil || oldRetain == nil {
		return bad()
	}
	oldJournal, ok, err := readEnvironmentCreateStageProgress(store, e, q1.FailedRetainStage, oldRetain)
	if err != nil || !ok || deploymentplan.Digest(oldJournal) != q1.FailedRetainJournalSHA256 {
		return bad()
	}
	previous, err := readEnvironmentCreateStageInput(store, e, q1.UpdateStage)
	if err != nil || previous == nil || previous.SHA256() != q.PreviousUpdatePlanSHA256 || q.PreviousUpdateStage != q1.UpdateStage || q.PreviousUpdatePlanSHA256 != q1.UpdatePlanSHA256 {
		return bad()
	}
	previousJournal, ok, err := readEnvironmentCreateStageProgress(store, e, q1.UpdateStage, previous)
	if err != nil || !ok || deploymentplan.Digest(previousJournal) != q.PreviousUpdateJournalSHA256 {
		return bad()
	}
	prior := createOpenBaoACKCompletionEntry(q.SourceJournal, "initializer-forward-"+deploymentplan.Digest(q1))
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q1), "original_admission_sha256": admissionSHA, "original_forward_sha256": q1.FailedForwardSHA256, "update_plan_sha256": q1.UpdatePlanSHA256, "retain_plan_sha256": q1.RetainPlanSHA256})
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil || prior == nil || prior.IntentSHA256 != intentSHA || prior.Phase != "initializer-forward" || (prior.State != "pending" && prior.State != "allocated") || prior.StartedAt.Before(q1.QualifiedAt) || !prior.StartedAt.Before(deadline) {
		return bad()
	}
	if len(previous.Resources) != 1 || len(update.Resources) != 1 {
		return bad()
	}
	target := previous.Resources[0]
	corrected := deploymentplan.Definition{Object: deploymentplan.Clone(target.Desired), Phase: target.Phase}
	changed, err := projectDeviceMemoryTrustPrivateSubPath(corrected.Object)
	if err != nil || !changed {
		return bad()
	}
	suffix := previous.SHA256()[:12] + "-" + original.SHA256()[:12]
	if q.UpdateStage != "private-state-update-"+suffix || q.RetainStage != "private-state-retain-"+suffix {
		return bad()
	}
	baselines := map[string]deploymentplan.Object{}
	for _, r := range retain.Resources {
		if r.Baseline == nil || baselines[r.ID] != nil {
			return bad()
		}
		baselines[r.ID] = r.Baseline
	}
	if update.Resources[0].Baseline == nil {
		return bad()
	}
	var captured []deploymentplan.Object
	for _, r := range original.Resources {
		baseline := baselines[r.ID]
		if r.ID == target.ID {
			baseline = update.Resources[0].Baseline
		}
		if baseline == nil || source.Owners[r.ID] == "" {
			return bad()
		}
		o := deploymentplan.Clone(baseline)
		deploymentplan.Map(o["metadata"])["uid"] = source.Owners[r.ID]
		captured = append(captured, o)
	}
	server := deploymentplan.Clone(baselines[target.ID])
	if server == nil {
		return bad()
	}
	deploymentplan.Map(server["metadata"])["uid"] = source.Owners[target.ID]
	expectedUpdate, expectedRetain, err := compileFailedPrivateStateForward(original, source, oldRetain, oldJournal, previous, previousJournal, corrected, captured, server, q.UpdateStage, q.RetainStage)
	if err != nil || expectedUpdate.SHA256() != update.SHA256() || expectedRetain.SHA256() != retain.SHA256() {
		return bad()
	}
	return e.Check(ctx)
}

func verifyInitializerMountForwardOwners(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createInitializerMountForwardProof, q1Update, update *deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("private state successor owner or recorded native update changed")
	}
	original, err := readEnvironmentCreateStageInput(store, e, "managed-application-owners")
	if err != nil || original == nil || original.SHA256() != q.OriginalPlanSHA256 || q1Update == nil || len(q1Update.Resources) != 1 || update == nil || len(update.Resources) != 1 {
		return bad()
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(source) != q.OriginalJournalSHA256 {
		return bad()
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-application-owners"); entry != nil && entry.State == "completed" {
		var alias createFailedForwardAlias
		if decodeEnvironmentJSON(entry.Receipt, &alias) != nil || alias.InitializerMountQualificationSHA256 != deploymentplan.Digest(q) || alias.OriginalPlanSHA256 != original.SHA256() || alias.OriginalJournalSHA256 != deploymentplan.Digest(source) {
			return bad()
		}
		var bases []activeTransportRenewalResource
		for _, resource := range original.Resources {
			bases = append(bases, sessionRecordedOwnerBase(resource.ID, source.Owners[resource.ID], resource.Desired))
		}
		pending, err := ordinaryRecordedPendingWrites(ctx, store, e, nil)
		if err != nil {
			return err
		}
		latest, err := completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, b, "kubernetes/managed-application-owners", bases, pending)
		if err != nil {
			return err
		}
		for _, owner := range latest {
			actual, err := b.Read(ctx, owner.ID)
			if err != nil {
				return err
			}
			if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, actual) {
				return bad()
			}
		}
		return e.Check(ctx)
	}
	recorded := false
	stage, err := readEnvironmentCreateStageInput(store, e, q.UpdateStage)
	if err != nil {
		return bad()
	}
	if stage != nil {
		if stage.SHA256() != update.SHA256() {
			return bad()
		}
		journal, exists, readErr := stage.ReadExecution(store.Root)
		if readErr != nil {
			return bad()
		}
		if exists {
			target := update.Resources[0].ID
			for _, id := range journal.Completed {
				if id == target && journal.Owners[target] == source.Owners[target] {
					recorded = true
				}
			}
			if journal.Failed == target && journal.Owners[target] == source.Owners[target] {
				recorded = true
			}
		}
	}
	prior, target := q1Update.Resources[0], update.Resources[0]
	if prior.ID != target.ID {
		return bad()
	}
	var bases []activeTransportRenewalResource
	baseDesired := map[string]deploymentplan.Object{}
	for _, resource := range original.Resources {
		expected := resource.Desired
		if resource.ID == prior.ID {
			expected = prior.Desired
			if recorded {
				expected = target.Desired
			}
		}
		bases = append(bases, sessionRecordedOwnerBase(resource.ID, source.Owners[resource.ID], expected))
		baseDesired[resource.ID] = expected
	}
	successors, err := completedSessionRecordedSuccessors(ctx, store, e, bases)
	if err != nil {
		return bad()
	}
	latest := make(map[string]deploymentplan.Object, len(successors))
	for _, successor := range successors {
		latest[successor.ID] = successor.Desired
	}
	for _, resource := range original.Resources {
		actual, readErr := b.Read(ctx, resource.ID)
		if readErr != nil {
			return readErr
		}
		if actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != source.Owners[resource.ID] {
			return bad()
		}
		expected := resource.Desired
		if resource.ID == prior.ID {
			expected = prior.Desired
		}
		// An accepted later session write supersedes the old template; do not
		// accept the old body once its replacement has been recorded.
		if !deploymentplan.Matches(latest[resource.ID], baseDesired[resource.ID]) {
			if !deploymentplan.Matches(latest[resource.ID], actual) {
				return bad()
			}
			continue
		}
		if deploymentplan.Matches(expected, actual) {
			continue
		}
		if resource.ID == target.ID && recorded && deploymentplan.Matches(target.Desired, actual) {
			continue
		}
		if !deploymentplan.Matches(latest[resource.ID], actual) {
			return bad()
		}
	}
	return e.Check(ctx)
}

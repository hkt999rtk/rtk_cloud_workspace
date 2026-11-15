package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// prepareInitializerForward qualifies an interrupted native retain without
// requiring the very workload being repaired to be ready. No write is made to
// the cluster; the update is server-side admitted against the observed owner.
func prepareInitializerForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, deadline time.Time, live []deploymentplan.Object, clock func() time.Time) (createInitializerForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	bad := func() (createInitializerForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return createInitializerForwardProof{}, nil, nil, createProviderRecoveryBlock("initializer repair requires its admitted original generation and exact failed retain")
	}
	if e == nil || e.Store != store || b == nil || clock == nil || e.Check(ctx) != nil || deadline.IsZero() || q0.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q0.OriginalAdmissionSHA256 != admissionSHA || !partialSessionJournalExtends(q0.SourceJournal, e.Journal) {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q0.FailedStage.Child.PlanSHA256 {
		return bad()
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(source) != q0.FailedStage.Child.JournalSHA256 {
		return bad()
	}
	crlStage, oldRetainStage := failedForwardStageNames(q0)
	crl, _, err := acceptedCreateStage(store, e, "kubernetes/"+crlStage)
	if err != nil {
		return bad()
	}
	oldRetain, err := readEnvironmentCreateStageInput(store, e, oldRetainStage)
	if err != nil || oldRetain == nil {
		return bad()
	}
	oldJournal, ok, err := readEnvironmentCreateStageProgress(store, e, oldRetainStage, oldRetain)
	if err != nil || !ok {
		return bad()
	}
	if validateCreateFailedForwardProofBindings(q0, admitted, admissionSHA, q0.FailedStage, crl, oldRetain, q0.QualifiedAt) != nil {
		return bad()
	}
	forwardEntry := createOpenBaoACKCompletionEntry(e.Journal, "failed-stage-forward-"+deploymentplan.Digest(q0))
	oldEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+oldRetainStage)
	forwardIntent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q0), "original_admission_sha256": admissionSHA, "parent_plan_sha256": q0.FailedStage.ParentSHA256, "original_failed_plan_sha256": q0.FailedStage.Child.PlanSHA256, "original_failed_journal_sha256": q0.FailedStage.Child.JournalSHA256, "repair_plan_sha256": q0.CandidateSHA256, "retain_plan_sha256": q0.RetainCandidateSHA256})
	forwardIntentSHA, intentErr := environmentCreatePlanSHA256(forwardIntent)
	if intentErr != nil || forwardEntry == nil || forwardEntry.IntentSHA256 != forwardIntentSHA || forwardEntry.Phase != "failed-stage-forward" || (forwardEntry.State != "pending" && forwardEntry.State != "allocated") || forwardEntry.StartedAt.Before(q0.QualifiedAt) || !forwardEntry.StartedAt.Before(q0.ValidUntil) || oldEntry == nil || oldEntry.Phase != oldRetainStage || (oldEntry.State != "pending" && oldEntry.State != "allocated") {
		return bad()
	}
	var corrected deploymentplan.Definition
	found := false
	for _, resource := range original.Resources {
		if resource.ID == oldJournal.Failed {
			if found {
				return bad()
			}
			corrected = deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase, Dependencies: resource.Dependencies}
			found = true
		}
	}
	if !found || !projectDeviceMemoryTrustInitializer(corrected.Object) {
		return bad()
	}
	suffix := original.SHA256()[:12] + "-" + oldRetain.SHA256()[:12]
	updateStage, retainStage := "initializer-update-"+suffix, "initializer-retain-"+suffix
	update, _, err := compileFailedInitializerForward(original, source, oldRetain, oldJournal, corrected, live, nil, "initialize-trust-state", updateStage, retainStage)
	if err != nil {
		return createInitializerForwardProof{}, nil, nil, err
	}
	if err = update.Admit(ctx, b); err != nil {
		return createInitializerForwardProof{}, nil, nil, err
	}
	var current deploymentplan.Object
	for _, object := range live {
		if deploymentplan.ID(object) == update.Resources[0].ID {
			current = object
			break
		}
	}
	body, err := update.Resources[0].Body(current)
	if err != nil || body == nil {
		return bad()
	}
	admittedBody, err := b.Write(ctx, update.Resources[0].Action, body, true)
	if err != nil || admittedBody == nil || !deploymentplan.Matches(body, admittedBody) {
		return bad()
	}
	update, retain, err := compileFailedInitializerForward(original, source, oldRetain, oldJournal, corrected, live, admittedBody, "initialize-trust-state", updateStage, retainStage)
	if err != nil {
		return createInitializerForwardProof{}, nil, nil, err
	}
	now := clock().UTC()
	if now.IsZero() || !now.Before(deadline) {
		return bad()
	}
	q := createInitializerForwardProof{Schema: initializerForwardProofSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), ParentPlanSHA256: e.Journal.PlanSHA256, SourceJournal: e.Journal, SourceJournalSHA256: deploymentplan.Digest(e.Journal), OriginalAdmissionSHA256: admissionSHA, FailedForwardSHA256: deploymentplan.Digest(q0), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(source), CRLStage: crlStage, CRLPlanSHA256: crl.SHA256(), FailedRetainStage: oldRetainStage, FailedRetainPlanSHA256: oldRetain.SHA256(), FailedRetainJournalSHA256: deploymentplan.Digest(oldJournal), UpdateStage: updateStage, UpdatePlanSHA256: update.SHA256(), RetainStage: retainStage, RetainPlanSHA256: retain.SHA256(), QualifiedAt: now, ExecutionDeadline: deadline}
	if e.Check(ctx) != nil {
		return bad()
	}
	if err = saveInitializerForwardBundle(store, e, q, update, retain); err != nil {
		return createInitializerForwardProof{}, nil, nil, err
	}
	return q, update, retain, nil
}

// validateInitializerForward reconstructs both native plans from the frozen
// source. The synthetic inventory is used only to re-render those plans; the
// native executor separately re-observes live UIDs and bodies before writes.
func validateInitializerForward(ctx context.Context, store secretStore, e *environmentCreateExecution, q createInitializerForwardProof, q0 createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, deadline time.Time, update, retain *deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("initializer successor no longer matches its admitted original owner, journals or exact native plans")
	}
	if !validInitializerForwardBundle(store, e, q, update, retain) || q.OriginalAdmissionSHA256 != admissionSHA || q.FailedForwardSHA256 != deploymentplan.Digest(q0) || q.SourceJournalSHA256 != deploymentplan.Digest(q.SourceJournal) || !q.ExecutionDeadline.Equal(deadline) || !q.QualifiedAt.Before(deadline) || e.Check(ctx) != nil {
		return bad()
	}
	original, err := readEnvironmentCreateStageInput(store, e, q0.FailedStage.Stage)
	if err != nil || original == nil || original.SHA256() != q.OriginalPlanSHA256 || q.OriginalPlanSHA256 != q0.FailedStage.Child.PlanSHA256 {
		return bad()
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(source) != q.OriginalJournalSHA256 || q.OriginalJournalSHA256 != q0.FailedStage.Child.JournalSHA256 {
		return bad()
	}
	crlStage, oldStage := failedForwardStageNames(q0)
	if q.CRLStage != crlStage || q.FailedRetainStage != oldStage {
		return bad()
	}
	crl, _, err := acceptedCreateStage(store, e, "kubernetes/"+crlStage)
	if err != nil || crl.SHA256() != q.CRLPlanSHA256 {
		return bad()
	}
	oldRetain, err := readEnvironmentCreateStageInput(store, e, oldStage)
	if err != nil || oldRetain == nil || oldRetain.SHA256() != q.FailedRetainPlanSHA256 {
		return bad()
	}
	oldJournal, ok, err := readEnvironmentCreateStageProgress(store, e, oldStage, oldRetain)
	if err != nil || !ok || deploymentplan.Digest(oldJournal) != q.FailedRetainJournalSHA256 {
		return bad()
	}
	if validateCreateFailedForwardProofBindings(q0, admitted, admissionSHA, q0.FailedStage, crl, oldRetain, q0.QualifiedAt) != nil || !partialSessionJournalExtends(q0.SourceJournal, e.Journal) {
		return bad()
	}
	prior := createOpenBaoACKCompletionEntry(q.SourceJournal, "failed-stage-forward-"+deploymentplan.Digest(q0))
	stage := createOpenBaoACKCompletionEntry(q.SourceJournal, "kubernetes/"+oldStage)
	forwardIntent := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q0), "original_admission_sha256": admissionSHA, "parent_plan_sha256": q0.FailedStage.ParentSHA256, "original_failed_plan_sha256": q0.FailedStage.Child.PlanSHA256, "original_failed_journal_sha256": q0.FailedStage.Child.JournalSHA256, "repair_plan_sha256": q0.CandidateSHA256, "retain_plan_sha256": q0.RetainCandidateSHA256})
	forwardIntentSHA, intentErr := environmentCreatePlanSHA256(forwardIntent)
	if intentErr != nil || prior == nil || prior.IntentSHA256 != forwardIntentSHA || prior.Phase != "failed-stage-forward" || (prior.State != "pending" && prior.State != "allocated") || prior.StartedAt.Before(q0.QualifiedAt) || !prior.StartedAt.Before(q0.ValidUntil) || stage == nil || stage.Phase != oldStage || (stage.State != "pending" && stage.State != "allocated") {
		return bad()
	}
	suffix := original.SHA256()[:12] + "-" + oldRetain.SHA256()[:12]
	if q.UpdateStage != "initializer-update-"+suffix || q.RetainStage != "initializer-retain-"+suffix {
		return bad()
	}
	var corrected deploymentplan.Definition
	var captured []deploymentplan.Object
	found := false
	baselines := make(map[string]deploymentplan.Object, len(retain.Resources))
	for _, resource := range retain.Resources {
		if resource.Baseline == nil || baselines[resource.ID] != nil {
			return bad()
		}
		baselines[resource.ID] = resource.Baseline
	}
	if len(update.Resources) != 1 || update.Resources[0].Baseline == nil {
		return bad()
	}
	for _, r := range original.Resources {
		uid := source.Owners[r.ID]
		if uid == "" {
			return bad()
		}
		baseline := baselines[r.ID]
		if r.ID == oldJournal.Failed {
			baseline = update.Resources[0].Baseline
		}
		if baseline == nil {
			return bad()
		}
		object := deploymentplan.Clone(baseline)
		deploymentplan.Map(object["metadata"])["uid"] = uid
		captured = append(captured, object)
		if r.ID == oldJournal.Failed {
			if found {
				return bad()
			}
			corrected = deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: r.Dependencies}
			found = true
		}
	}
	if !found || !projectDeviceMemoryTrustInitializer(corrected.Object) {
		return bad()
	}
	admittedBody := deploymentplan.Clone(baselines[oldJournal.Failed])
	if admittedBody == nil {
		return bad()
	}
	deploymentplan.Map(admittedBody["metadata"])["uid"] = source.Owners[oldJournal.Failed]
	expectedUpdate, expectedRetain, err := compileFailedInitializerForward(original, source, oldRetain, oldJournal, corrected, captured, admittedBody, "initialize-trust-state", q.UpdateStage, q.RetainStage)
	if err != nil || expectedUpdate.SHA256() != update.SHA256() || expectedRetain.SHA256() != retain.SHA256() {
		return bad()
	}
	return e.Check(ctx)
}

// verifyInitializerForwardOwners observes all source resources without waiting
// for workload readiness. A changed target is accepted only if the exact native
// update journal records that write as completed or in flight.
func verifyInitializerForwardOwners(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createInitializerForwardProof, update *deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("initializer successor owners or recorded update changed")
	}
	original, err := readEnvironmentCreateStageInput(store, e, "managed-application-owners")
	if err != nil || original == nil || original.SHA256() != q.OriginalPlanSHA256 || update == nil || len(update.Resources) != 1 {
		return bad()
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || deploymentplan.Digest(source) != q.OriginalJournalSHA256 {
		return bad()
	}
	updated := false
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
					updated = true
				}
			}
			// A successful API response records its owner before readiness.
			// If readiness then fails, Completed is still empty, but the
			// original exact owner and failed resource prove that write.
			if journal.Failed == target && journal.Owners[target] == source.Owners[target] {
				updated = true
			}
		}
	}
	target := update.Resources[0]
	for _, resource := range original.Resources {
		actual, readErr := b.Read(ctx, resource.ID)
		if readErr != nil {
			return readErr
		}
		if actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != source.Owners[resource.ID] {
			return bad()
		}
		if deploymentplan.Matches(resource.Desired, actual) {
			continue
		}
		if resource.ID != target.ID || !updated || !deploymentplan.Matches(target.Desired, actual) {
			return bad()
		}
	}
	return e.Check(ctx)
}

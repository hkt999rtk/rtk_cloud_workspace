package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// verifyInitializerSuccessorPlans verifies complete original owner continuity
// and the single known memory initializer change without mutating any input.
// Callers must also verify each child's accepted journal and current live UID.
func verifyInitializerSuccessorPlans(original *deploymentplan.Plan, originalJournal deploymentplan.Journal, update *deploymentplan.Plan, updateJournal deploymentplan.Journal, retain *deploymentplan.Plan, retainJournal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("initializer successor differs from the original resource owner or memory-only recipe")
	}
	if original == nil || update == nil || retain == nil || original.SHA256() != originalJournal.PlanSHA256 || originalJournal.Failed == "" || originalJournal.InFlight != "" || originalJournal.Accepted || originalJournal.AcceptanceStarted || len(originalJournal.Completed) != len(original.Resources) || len(originalJournal.Owners) != len(original.Resources) || len(update.Resources) != 1 || len(retain.Resources) != len(original.Resources) || !updateJournal.Accepted || !retainJournal.Accepted || updateJournal.PlanSHA256 != update.SHA256() || retainJournal.PlanSHA256 != retain.SHA256() {
		return bad()
	}
	changed := update.Resources[0]
	if changed.Action != "update" || changed.Desired["kind"] != "Deployment" || changed.UID == "" || updateJournal.Owners[changed.ID] != changed.UID || originalJournal.Owners[changed.ID] != changed.UID {
		return bad()
	}
	same := func(a, b deploymentplan.Object) bool {
		a = deploymentplan.Projection(a)
		b = deploymentplan.Projection(b)
		delete(deploymentplan.Map(a["metadata"]), "uid")
		delete(deploymentplan.Map(b["metadata"]), "uid")
		return deploymentplan.Digest(a) == deploymentplan.Digest(b)
	}
	count := 0
	for i, src := range original.Resources {
		uid := originalJournal.Owners[src.ID]
		next := retain.Resources[i]
		if originalJournal.Completed[i] != src.ID || uid == "" || next.ID != src.ID || next.Action != "retain" || next.UID != uid || retainJournal.Owners[next.ID] != uid || deploymentplan.Digest(next.Dependencies) != deploymentplan.Digest(src.Dependencies) {
			return bad()
		}
		expected := deploymentplan.Clone(src.Desired)
		if src.ID == changed.ID {
			count++
			if !deploymentplan.Matches(src.Desired, changed.Baseline) || !projectDeviceMemoryTrustInitializer(expected) || !same(changed.Desired, expected) {
				return bad()
			}
		}
		if !same(next.Desired, expected) {
			return bad()
		}
	}
	if count != 1 {
		return bad()
	}
	return nil
}

// verifyInitializerSuccessorAlias authenticates the original q0 admission,
// the separate q1 evidence, and each completed native child before a consumer
// may accept the updated resource body. It never changes any journal.
func verifyInitializerSuccessorAlias(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, alias createFailedForwardAlias, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, q0 createFailedForwardProof, repair, oldRetain *deploymentplan.Plan) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("initializer forward alias lacks its exact original and accepted successor owner chain")
	}
	if e == nil || b == nil || original == nil || repair == nil || oldRetain == nil || !environmentPreflightDigest.MatchString(alias.InitializerQualificationSHA256) || alias.QualificationSHA256 != deploymentplan.Digest(q0) || e.Check(ctx) != nil {
		return bad()
	}
	q1, update, retained, err := loadInitializerForwardBundle(store, e, alias.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	repairName, oldRetainName := failedForwardStageNames(q0)
	if q1.FailedForwardSHA256 != alias.QualificationSHA256 || q1.OriginalAdmissionSHA256 != q0.OriginalAdmissionSHA256 || q1.OriginalPlanSHA256 != original.SHA256() || q1.OriginalJournalSHA256 != deploymentplan.Digest(originalJournal) || q1.CRLStage != repairName || q1.CRLPlanSHA256 != repair.SHA256() || q1.FailedRetainStage != oldRetainName || q1.FailedRetainPlanSHA256 != oldRetain.SHA256() || alias.RepairStage != "kubernetes/"+repairName || alias.RepairPlanSHA256 != repair.SHA256() || alias.RetainStage != "kubernetes/"+q1.RetainStage || alias.RetainPlanSHA256 != retained.SHA256() {
		return bad()
	}
	acceptedRepair, _, err := acceptedCreateStage(store, e, "kubernetes/"+q1.CRLStage)
	if err != nil || acceptedRepair.SHA256() != repair.SHA256() {
		return bad()
	}
	prior, err := readEnvironmentCreateStageInput(store, e, q1.FailedRetainStage)
	if err != nil || prior == nil || prior.SHA256() != oldRetain.SHA256() {
		return bad()
	}
	oldJournal, exists, err := oldRetain.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(oldJournal) != q1.FailedRetainJournalSHA256 || len(update.Resources) != 1 || oldJournal.Failed != update.Resources[0].ID || oldJournal.Accepted || oldJournal.AcceptanceStarted || oldJournal.InFlight != "" || len(oldJournal.Completed) != len(oldRetain.Resources) {
		return bad()
	}
	actualUpdate, updateJournal, err := acceptedCreateStage(store, e, "kubernetes/"+q1.UpdateStage)
	if err != nil || actualUpdate.SHA256() != update.SHA256() {
		return bad()
	}
	actualRetain, retainedJournal, err := acceptedCreateStage(store, e, "kubernetes/"+q1.RetainStage)
	if err != nil || actualRetain.SHA256() != retained.SHA256() {
		return bad()
	}
	// The native alias fence or the application write observer reads each live
	// owner immediately afterward; avoid a duplicate full-resource read here.
	if verifyInitializerSuccessorPlans(original, originalJournal, update, updateJournal, retained, retainedJournal) != nil || e.Check(ctx) != nil {
		return bad()
	}
	return retained, nil
}

package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Observe the repaired logical application stage through its original alias.
func verifyCompletedApplicationForwardStage(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, backend deploymentplan.Backend, applicationForward *createFailedForwardProof, definitions []deploymentplan.Definition) error {
	stage := applicationForward.FailedStage.Stage
	original, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || original == nil || original.SHA256() != applicationForward.FailedStage.Child.PlanSHA256 {
		return createProviderRecoveryBlock("original application stage input changed after its repair")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	var alias createFailedForwardAlias
	if entry == nil || entry.State != "completed" || decodeEnvironmentJSON(entry.Receipt, &alias) != nil || alias.QualificationSHA256 != deploymentplan.Digest(*applicationForward) || alias.OriginalPlanSHA256 != original.SHA256() || alias.OriginalJournalSHA256 != applicationForward.FailedStage.Child.JournalSHA256 {
		return createProviderRecoveryBlock("original application repair alias is unavailable or changed")
	}
	if alias.ServiceSessionQualificationSHA256 != "" {
		// The original logical source is complete through a genuinely accepted
		// session successor. Validate that immutable alias and current owners;
		// a new session's renderer cannot reproduce the retired source template.
		if _, err := completedApplicationOwnerWrites(ctx, store, e, backend); err != nil {
			return err
		}
		retained, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, backend, alias.RetainStage)
		if err != nil {
			return err
		}
		if retained.SHA256() != alias.RetainPlanSHA256 {
			return createProviderRecoveryBlock("completed application session successor changed")
		}
		return e.Check(ctx)
	}
	projected, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil)
	if err != nil {
		return err
	}
	if applicationForward.initializer == nil {
		err = verifyFailedForwardDefinitions(original, projected)
	} else if applicationForward.initializer.mount == nil {
		_, update, _, loadErr := loadInitializerForwardBundle(store, e, deploymentplan.Digest(*applicationForward.initializer))
		if loadErr != nil {
			return loadErr
		}
		err = verifyInitializerForwardDefinitions(original, projected, update, "initialize-trust-state")
	} else {
		_, update, _, loadErr := loadInitializerMountForwardBundle(store, e, deploymentplan.Digest(*applicationForward.initializer.mount))
		if loadErr != nil {
			return loadErr
		}
		err = verifyInitializerMountForwardDefinitions(original, projected, update)
	}
	if err != nil {
		return err
	}
	return e.Check(ctx)
}

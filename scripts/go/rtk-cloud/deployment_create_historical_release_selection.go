package main

import (
	"context"
	"maps"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Historical bootstrap is qualified against its original immutable image, while
// the completed OpenBao successor remains the selected runtime authority. This
// never changes the environment selection or an executable release plan.
func createHistoricalReleaseSelection(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, original environmentCreatePlan) (environmentImageSelection, error) {
	selection, err := readEnvironmentImageSelection(store)
	if err != nil {
		return selection, err
	}
	if selection.Images["LKE_PKI_CONTROLLER_IMAGE"].Reference != "" || selection.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference != "" {
		_, operator, err := readEnvironmentCreateOperator(cfg)
		if err != nil {
			return selection, err
		}
		if _, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &original); err != nil {
			return selection, err
		}
		if e.versionState != nil && e.versionState.cache != nil && len(e.versionState.cache.RuntimeImages) != 0 {
			selection, err = parseEnvironmentImageSelection(cfg.Environment, e.versionState.cache.RuntimePrevious)
			if err != nil {
				return selection, err
			}
		}
	}
	const role = "LKE_OPENBAO_PKI_IMAGE"
	selected, explicit := selection.Images[role]
	if !explicit || deploymentplan.Digest(selected) == deploymentplan.Digest(original.Release.Images[role]) {
		return selection, nil
	}
	bad := func() (environmentImageSelection, error) {
		return environmentImageSelection{}, createProviderRecoveryBlock("historical OpenBao release requires its exact original admission and completed environment-selected successor")
	}
	bound, err := authenticatedCreateOperatorPlan(store, e)
	if err != nil || deploymentplan.Digest(*bound) != deploymentplan.Digest(original) {
		return bad()
	}
	receipt, found, err := completedOpenBaoInPlaceReceipt(ctx, store, e)
	if err != nil || !found {
		return bad()
	}
	history, err := verifyOpenBaoInPlaceHistory(ctx, cfg, store, e, receipt)
	if err != nil || deploymentplan.Digest(history.image) != deploymentplan.Digest(selected) || e.Check(ctx) != nil {
		return bad()
	}
	selection.Images = maps.Clone(selection.Images)
	selection.Images[role] = original.Release.Images[role]
	return selection, nil
}

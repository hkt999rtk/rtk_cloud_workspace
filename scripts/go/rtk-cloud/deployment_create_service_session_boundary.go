package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// serviceSessionBoundary detects the already-admitted q2 generation only
// after its retained original owners have actually entered native execution.
// A not-yet-started q2 remains with the existing private-state create flow.
func serviceSessionBoundary(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) (createInitializerMountForwardProof, bool, error) {
	return serviceSessionBoundaryWithAdmission(ctx, store, e, b, authenticatedInitializerForwardAdmission)
}

func serviceSessionBoundaryWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, load initializerForwardAdmissionLoader) (createInitializerMountForwardProof, bool, error) {
	bad := func() (createInitializerMountForwardProof, bool, error) {
		return createInitializerMountForwardProof{}, false, createProviderRecoveryBlock("original Service session q2 boundary cannot be authenticated")
	}
	if e == nil || e.Store != store || b == nil || load == nil || e.Check(ctx) != nil {
		return bad()
	}
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
		return createInitializerMountForwardProof{}, false, nil
	}
	q2, _, retain, err := loadInitializerMountForwardBundle(store, e, review)
	if err != nil || retain == nil {
		return bad()
	}
	input, err := readEnvironmentCreateStageInput(store, e, q2.RetainStage)
	if err != nil {
		return bad()
	}
	if input == nil {
		return createInitializerMountForwardProof{}, false, nil
	}
	if input.SHA256() != retain.SHA256() {
		return bad()
	}
	j, exists, err := readEnvironmentCreateStageProgress(store, e, q2.RetainStage, input)
	if err != nil {
		return bad()
	}
	if !exists {
		return createInitializerMountForwardProof{}, false, nil
	}
	if j.InFlight != "" {
		return bad()
	}
	writes, err := authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, b, load)
	if err != nil || !writes["kubernetes/managed-application-owners"].Written || e.Check(ctx) != nil {
		return bad()
	}
	return q2, true, nil
}

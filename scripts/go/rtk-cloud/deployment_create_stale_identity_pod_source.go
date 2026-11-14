package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Recover only the owner that actually blocked an admitted original q2 retain.
// The initial application, q0/q1 repairs and retained journals are untouched.
func staleIdentityPodSource(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) (createInitializerMountForwardProof, *deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Resource, error) {
	bad := func() (createInitializerMountForwardProof, *deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Resource, error) {
		return createInitializerMountForwardProof{}, nil, deploymentplan.Journal{}, deploymentplan.Resource{}, createProviderRecoveryBlock("stale identity Pod requires its exact original failed retained owner")
	}
	if e == nil || e.Store != store || b == nil || e.Check(ctx) != nil {
		return bad()
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "initializer-mount-forward-"
		if entry.Phase == "initializer-mount-forward" && strings.HasPrefix(entry.Key, prefix) && entry.State != "completed" {
			if review != "" {
				return bad()
			}
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if !environmentPreflightDigest.MatchString(review) {
		return bad()
	}
	q2, q2Update, q2Retain, err := loadInitializerMountForwardBundle(store, e, review)
	if err != nil {
		return bad()
	}
	q1, q1Update, q1Retain, err := loadInitializerForwardBundle(store, e, q2.InitializerQualificationSHA256)
	if err != nil {
		return bad()
	}
	q0, _, _, err := loadFailedForwardBundle(store, e, q1.FailedForwardSHA256)
	if err != nil {
		return bad()
	}
	admitted, admission, err := authenticatedInitializerForwardAdmission(store, e, q1)
	if err != nil {
		return bad()
	}
	if validateInitializerMountForward(ctx, store, e, q2, q1, q0, admitted, admission, q1Update, q1Retain, q2Update, q2Retain, q2.ExecutionDeadline) != nil || verifyInitializerMountForwardOwners(ctx, store, e, b, q2, q1Update, q2Update) != nil {
		return bad()
	}
	retain, err := readEnvironmentCreateStageInput(store, e, q2.RetainStage)
	if err != nil || retain == nil || retain.SHA256() != q2.RetainPlanSHA256 {
		return bad()
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, q2.RetainStage, retain)
	if err != nil || !exists || journal.Accepted || journal.InFlight != "" || journal.Failed == "" || len(journal.Completed) != len(retain.Resources) {
		return bad()
	}
	var resource deploymentplan.Resource
	for _, r := range retain.Resources {
		if r.ID == journal.Failed {
			if resource.ID != "" {
				return bad()
			}
			resource = r
		}
	}
	if resource.ID == "" || resource.Action != "retain" || resource.Desired["kind"] != "Deployment" || resource.UID == "" || journal.Owners[resource.ID] != resource.UID {
		return bad()
	}
	return q2, retain, journal, resource, nil
}

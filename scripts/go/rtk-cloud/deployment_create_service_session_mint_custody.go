package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native session mint already updates this one original custody reader,
// including its certificate path. Authenticate its exact previous owner so the
// workload batch does not race that native update or weaken release checks.
func verifyServiceSessionMintCustody(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q secondSessionAdmission) (serviceSessionHistoricalJob, error) {
	bad := func() (serviceSessionHistoricalJob, error) {
		return serviceSessionHistoricalJob{}, createProviderRecoveryBlock("original native session custody reader evidence changed")
	}
	if e == nil || b == nil || !validSecondSessionAdmission(store, e, q) || e.Check(ctx) != nil {
		return bad()
	}
	r := q.Renewal
	stage := "service-custody-reader-renewal-" + r.PreviousReview
	plan, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil || plan == nil || plan.SHA256() != r.PreviousCustodyPlanSHA || deploymentplan.Digest(journal) != r.PreviousCustodyJournalSHA || journal.Owners[r.PreviousCustodyID] != r.PreviousCustodyUID {
		return bad()
	}
	checked, found, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
	if err != nil || !found || deploymentplan.Digest(checked) != r.PreviousCustodyJournalSHA {
		return bad()
	}
	var desired deploymentplan.Object
	for n, resource := range plan.Resources {
		if resource.ID != r.PreviousCustodyID {
			continue
		}
		if desired != nil || resource.Desired["kind"] != "Deployment" || n >= len(journal.Completed) || journal.Completed[n] != resource.ID {
			return bad()
		}
		desired = resource.Desired
	}
	if desired == nil {
		return bad()
	}
	slots, err := serviceSessionSIDSlots(desired, r.Intent.SessionID, "")
	if err != nil || slots == 0 {
		return bad()
	}
	live, err := b.Read(ctx, r.PreviousCustodyID)
	if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != r.PreviousCustodyUID || !deploymentplan.Matches(desired, live) || e.Check(ctx) != nil {
		return bad()
	}
	return serviceSessionHistoricalJob{ID: r.PreviousCustodyID, UID: r.PreviousCustodyUID, Stage: stage, PlanSHA256: plan.SHA256(), JournalSHA256: deploymentplan.Digest(journal), verified: true, mintCustody: true, desired: desired}, nil
}

// After mint, the exact renewal child must be durably accepted and must have
// changed only the session and certificate path of the same original owner.
func verifyServiceSessionMintCustodyCompleted(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q secondSessionAdmission, review string) error {
	bad := func() error {
		return createProviderRecoveryBlock("native renewed custody reader is not the accepted original owner")
	}
	if e == nil || b == nil || deploymentplan.Digest(q) != review || !validSecondSessionAdmission(store, e, q) || e.Check(ctx) != nil {
		return bad()
	}
	r := q.Renewal
	old, oldJournal, err := acceptedCreateStage(store, e, "kubernetes/service-custody-reader-renewal-"+r.PreviousReview)
	if err != nil || old == nil || old.SHA256() != r.PreviousCustodyPlanSHA || deploymentplan.Digest(oldJournal) != r.PreviousCustodyJournalSHA || oldJournal.Owners[r.PreviousCustodyID] != r.PreviousCustodyUID {
		return bad()
	}
	var previous *deploymentplan.Resource
	for i := range old.Resources {
		if old.Resources[i].ID == r.PreviousCustodyID {
			if previous != nil {
				return bad()
			}
			previous = &old.Resources[i]
		}
	}
	if previous == nil {
		return bad()
	}
	stage := "service-custody-reader-renewal-" + review
	updated, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil || updated == nil || !journal.Accepted || journal.Owners[r.PreviousCustodyID] != r.PreviousCustodyUID {
		return bad()
	}
	checked, found, err := readEnvironmentCreateStageProgress(store, e, stage, updated)
	if err != nil || !found || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return bad()
	}
	reads := secondSessionReads{read: b.Read}
	if !secondSessionCustodyOwnerValid(ctx, store, e, reads, review, &r, *previous) || e.Check(ctx) != nil {
		return bad()
	}
	return nil
}

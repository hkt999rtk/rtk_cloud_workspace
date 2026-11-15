package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// serviceSessionHistoricalJob is evidence for an authenticated native
// bootstrap producer or mint custodian. It is not an installation ACK.
type serviceSessionHistoricalJob struct {
	ID, UID, Stage, PlanSHA256, JournalSHA256 string
	verified                                  bool
	mintCustody                               bool
	failedPodUID                              string // authenticated retired failure, never a mint or ACK
	desired                                   deploymentplan.Object
}

// verifyServiceSessionHistoricalJobs accepts only the original and immediate
// predecessor Jobs already bound by the admitted same-key/CSR lineage, plus
// the exact retained failure covered by an original trust-only forward. Native
// terminal state and original UIDs are checked without modifying any Job or
// treating an unknown Job as evidence.
func verifyServiceSessionHistoricalJobs(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, admission secondSessionAdmission) ([]serviceSessionHistoricalJob, error) {
	bad := func() ([]serviceSessionHistoricalJob, error) {
		return nil, createProviderRecoveryBlock("original Service session Job history lacks its admitted native lineage")
	}
	if e == nil || b == nil || !validSecondSessionAdmission(store, e, admission) || e.Check(ctx) != nil {
		return bad()
	}
	r := admission.Renewal
	if !environmentBootstrapOwnerUUID.MatchString(r.OriginalState.Session) || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || r.OriginalState.Session == r.Intent.SessionID {
		return bad()
	}
	expectedSessions := []string{r.OriginalState.Session, r.Intent.SessionID}
	inputs := []serviceSessionHistoricalJob{
		{ID: r.SourceJobID, UID: r.SourceJobUID, Stage: strings.TrimPrefix(r.SourceStage, "kubernetes/"), PlanSHA256: r.SourcePlanSHA, JournalSHA256: r.SourceJournalSHA},
		{ID: r.PreviousJobID, UID: r.PreviousJobUID, Stage: strings.TrimPrefix(r.PreviousStage, "kubernetes/"), PlanSHA256: r.PreviousPlanSHA, JournalSHA256: r.PreviousJournalSHA},
	}
	_, history, err := serviceSessionPriorLineage(store, e, admission)
	if err != nil {
		return bad()
	}
	for _, prior := range history {
		p := prior.Renewal
		if p.PreviousJobID == r.PreviousJobID || p.PreviousJobID == r.SourceJobID {
			continue
		}
		inputs = append(inputs, serviceSessionHistoricalJob{ID: p.PreviousJobID, UID: p.PreviousJobUID, Stage: strings.TrimPrefix(p.PreviousStage, "kubernetes/"), PlanSHA256: p.PreviousPlanSHA, JournalSHA256: p.PreviousJournalSHA})
		expectedSessions = append(expectedSessions, p.Intent.SessionID)
	}
	seen := map[string]bool{}
	for i, item := range inputs {
		if item.ID == "" || seen[item.ID] || !environmentBootstrapOwnerUUID.MatchString(item.UID) || !secretEnvironmentPattern.MatchString(item.Stage) {
			return bad()
		}
		seen[item.ID] = true
		plan, journal, err := acceptedCreateStage(store, e, "kubernetes/"+item.Stage)
		if err != nil || plan == nil || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack || plan.SHA256() != item.PlanSHA256 || deploymentplan.Digest(journal) != item.JournalSHA256 || journal.Owners[item.ID] != item.UID {
			return bad()
		}
		checked, exists, err := readEnvironmentCreateStageProgress(store, e, item.Stage, plan)
		if err != nil || !exists || deploymentplan.Digest(checked) != item.JournalSHA256 {
			return bad()
		}
		var resource *deploymentplan.Resource
		for n := range plan.Resources {
			if plan.Resources[n].ID == item.ID {
				if resource != nil || n >= len(journal.Completed) || journal.Completed[n] != item.ID {
					return bad()
				}
				resource = &plan.Resources[n]
			}
		}
		if resource == nil || resource.Action != "create" || resource.Desired["kind"] != "Job" || deploymentplan.ID(resource.Desired) != item.ID {
			return bad()
		}
		slots, err := serviceSessionSIDSlots(resource.Desired, expectedSessions[i], "")
		if err != nil || slots == 0 {
			return bad()
		}
		live, err := b.Read(ctx, item.ID)
		if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != item.UID || !deploymentplan.Matches(resource.Desired, live) || !serviceSessionJobComplete(live) {
			return bad()
		}
		inputs[i].verified = true
	}
	// A failed predecessor replaced by the admitted trust-only forward is
	// retained history, not a workload that can install the next session.
	original, originalReview, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || exists && originalReview != r.OriginalReview {
		return bad()
	}
	if exists {
		forward, _, found, err := loadPartialSessionTrustForwardAdmission(store, e, original, originalReview)
		if err != nil {
			return bad()
		}
		if found {
			f := forward.PartialTrustForward.Forward
			resource := f.FailedPlan.Resources[0]
			job, err := b.Read(ctx, resource.ID)
			if err != nil {
				return nil, secretCheckFailure(err, "original retained bootstrap Job read unavailable")
			}
			if seen[resource.ID] || job == nil || deploymentplan.Map(job["metadata"])["uid"] != f.FailedJobUID || !deploymentplan.Matches(resource.Desired, job) || !serviceSessionJobFailed(job) {
				return bad()
			}
			inputs = append(inputs, serviceSessionHistoricalJob{ID: resource.ID, UID: f.FailedJobUID, PlanSHA256: f.FailedPlanSHA, JournalSHA256: f.FailedJournalSHA, verified: true, failedPodUID: f.FailedPodUID, desired: resource.Desired})
		}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return inputs, nil
}

func serviceSessionJobFailed(job deploymentplan.Object) bool {
	status := deploymentplan.Map(job["status"])
	if job["kind"] != "Job" || status["failed"] != float64(1) {
		return false
	}
	for _, field := range []string{"active", "succeeded", "terminating"} {
		if status[field] != nil && status[field] != float64(0) {
			return false
		}
	}
	for _, raw := range certIssuerObjectList(status["conditions"]) {
		c := deploymentplan.Map(raw)
		if c["type"] == "Failed" && c["status"] == "True" {
			return true
		}
	}
	return false
}

func serviceSessionJobComplete(job deploymentplan.Object) bool {
	for _, raw := range certIssuerObjectList(deploymentplan.Map(job["status"])["conditions"]) {
		c := deploymentplan.Map(raw)
		if c["type"] == "Complete" && c["status"] == "True" {
			return true
		}
	}
	return false
}

package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// qualifyServiceSessionBatches only observes the original custody and owners,
// runs native admission and records immutable evidence. It does not sign a
// certificate, update a workload, or acknowledge a subject.
func qualifyServiceSessionBatches(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, parent *environmentCreatePlan, native environmentKubernetesBackend, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, readyOpenBao func(context.Context) error, now, deadline time.Time) (secondSessionAdmission, string, error) {
	bad := func() (secondSessionAdmission, string, error) {
		return secondSessionAdmission{}, "", createProviderRecoveryBlock("Service session qualification requires its original execution and a ready OpenBao host")
	}
	if e == nil || parent == nil || readyOpenBao == nil || now.IsZero() || !now.Before(deadline) || e.Check(ctx) != nil || parent.Environment != cfg.Environment || parent.Stack != e.Journal.Stack {
		return bad()
	}
	if err := readyOpenBao(ctx); err != nil {
		return secondSessionAdmission{}, "", err
	}
	renewal, err := prepareSecondSessionCandidate(ctx, cfg, store, e, native, *parent, service, openBaoRoot, now)
	if err != nil {
		return secondSessionAdmission{}, "", err
	}
	if _, err := qualifiedSecondSessionPlan(store, e, parent, renewal.Candidate); err != nil {
		return secondSessionAdmission{}, "", err
	}
	var snapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
		return bad()
	}
	q := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: cfg.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), SourceJournal: snapshot, SourceJournalSHA256: deploymentplan.Digest(snapshot), QualifiedAt: now, ExecutionDeadline: deadline, Renewal: *renewal}
	if !validSecondSessionAdmission(store, e, q) {
		return bad()
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	previous, history, err := serviceSessionPriorLineage(store, e, q)
	if err != nil {
		return secondSessionAdmission{}, "", err
	}
	if len(previous) > 2 {
		q.RequiredACK = serviceSessionRequiredACKs(q, history)
	}
	q.Batches, err = prepareRecordedServiceSessionBatches(ctx, store, e, backend, native.Inventory, q, previous, renewal.RenewedIntent.SessionID)
	if err != nil {
		return secondSessionAdmission{}, "", err
	}
	q.Reference, err = prepareServiceSessionReference(ctx, store, e, backend, renewal.OriginalState.Session, renewal.Intent.SessionID, renewal.RenewedIntent.SessionID, previous...)
	if err != nil {
		return secondSessionAdmission{}, "", err
	}
	if !validSecondSessionAdmission(store, e, q) || !validServiceSessionBatches(q) {
		return bad()
	}
	for _, batch := range q.Batches {
		for _, pending := range batch.Pending {
			if pending.Reason != "not-written" && (pending.Reason != "non-deployment" || pending.ID != "StatefulSet/"+parent.Stack+"-secrets/openbao") {
				return bad()
			}
		}
		if batch.Plan != nil {
			if err := batch.Plan.AdmitResumable(ctx, backend, store.Root); err != nil {
				return secondSessionAdmission{}, "", err
			}
		}
	}
	if err := renewal.Candidate.AdmitResumable(ctx, backend, store.Root); err != nil {
		return secondSessionAdmission{}, "", err
	}
	if err := q.Reference.Plan.AdmitResumable(ctx, backend, store.Root); err != nil {
		return secondSessionAdmission{}, "", err
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	if err := saveSecondSessionAdmission(store, e, q); err != nil {
		return secondSessionAdmission{}, "", err
	}
	return q, deploymentplan.Digest(q), nil
}

// A preflight after interruption reuses the already admitted batch and the
// native child journals; it never compiles a new plan from partially updated
// live owners or resets the original execution deadline.
func resumeServiceSessionBatchesQualification(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, readyOpenBao func(context.Context) error, now, deadline time.Time) (secondSessionAdmission, string, bool, error) {
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	return resumeServiceSessionBatchesQualificationWith(ctx, store, e, backend, readyOpenBao, now, deadline, sessionLineageHead)
}

func resumeServiceSessionBatchesQualificationWith(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, readyOpenBao func(context.Context) error, now, deadline time.Time, load func(secretStore, *environmentCreateExecution) (*secondSessionAdmission, string, error)) (secondSessionAdmission, string, bool, error) {
	bad := func() (secondSessionAdmission, string, bool, error) {
		return secondSessionAdmission{}, "", true, createProviderRecoveryBlock("admitted Service session continuation or its original deadline changed")
	}
	if e == nil || backend == nil || readyOpenBao == nil || load == nil || now.IsZero() || e.Check(ctx) != nil {
		return bad()
	}
	head, review, err := load(store, e)
	if err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if head == nil || head.Batches == nil {
		return secondSessionAdmission{}, "", false, nil
	}
	q := *head
	complete, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if complete {
		// The caller has independently observed expiry. A completed batch is
		// historical evidence; qualify the next successor instead of replaying it.
		return secondSessionAdmission{}, "", false, nil
	}
	if deploymentplan.Digest(q) != review || q.ExecutionDeadline.After(deadline) || now.Before(q.QualifiedAt) || !now.Before(deadline) || !validSecondSessionAdmission(store, e, q) || !validServiceSessionBatches(q) {
		return bad()
	}
	if !q.ExecutionDeadline.Equal(deadline) {
		effective, err := currentInitializerEffectiveDeadline(ctx, store, e, now)
		if err != nil || !effective.Equal(deadline) {
			return bad()
		}
	}
	if err := readyOpenBao(ctx); err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if _, err := verifyServiceSessionHistoricalJobs(ctx, store, e, backend, q); err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if err := verifyServiceSessionReferenceSource(store, e, q); err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if err := q.Reference.Plan.AdmitResumable(ctx, backend, store.Root); err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	if err := q.Renewal.Candidate.AdmitResumable(ctx, backend, store.Root); err != nil {
		return secondSessionAdmission{}, "", true, err
	}
	for _, batch := range q.Batches {
		if batch.Plan != nil {
			if err := batch.Plan.AdmitResumable(ctx, backend, store.Root); err != nil {
				return secondSessionAdmission{}, "", true, err
			}
		}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return q, review, true, nil
}

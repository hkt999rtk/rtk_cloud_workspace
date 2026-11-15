package main

import (
	"context"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These callbacks are the existing native session renewal, actual owner
// installation and registry ACK checks. The executor never fabricates ACKs.
type serviceSessionBatchExecution struct {
	Backend       deploymentplan.Backend
	Ready         func(context.Context) error
	VerifyHistory func(context.Context) error
	Mint          func(context.Context) error
	Pending       func(context.Context, serviceSessionBatch) error
	Accept        func(context.Context, *deploymentplan.Plan) error
	VerifyACKs    func(context.Context) (managedSessionState, error)
	Clock         func() time.Time
}

// executeServiceSessionBatches resumes the exact admitted same-key renewal and
// immutable workload plans under the original parent fence. Native child
// journals preserve each successful write across interrupted waits.
func executeServiceSessionBatches(ctx context.Context, store secretStore, e *environmentCreateExecution, review string, adapters serviceSessionBatchExecution, out io.Writer) error {
	bad := func() error {
		return createProviderRecoveryBlock("Service session batch execution requires its original qualification and native ACK checks")
	}
	if e == nil || e.Store != store || adapters.Backend == nil || adapters.Ready == nil || adapters.VerifyHistory == nil || adapters.Mint == nil || adapters.Pending == nil || adapters.Accept == nil || adapters.VerifyACKs == nil || out == nil || e.Check(ctx) != nil {
		return bad()
	}
	q, err := loadSecondSessionAdmission(store, e, review)
	if err != nil || len(q.Batches) < 2 || !validServiceSessionBatches(q) {
		return bad()
	}
	clock := adapters.Clock
	if clock == nil {
		clock = time.Now
	}
	executionDeadline, err := sessionEffectiveDeadline(ctx, store, e, q, clock().UTC())
	if err != nil {
		return err
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error {
		if e.Check(ctx) != nil || clock().Before(q.QualifiedAt) || !clock().Before(executionDeadline) {
			return bad()
		}
		if deadline, ok := ctx.Deadline(); !ok || deadline.After(executionDeadline) {
			return bad()
		}
		return nil
	}
	if err := check(ctx, nil); err != nil {
		return err
	}
	if err := adapters.Ready(ctx); err != nil {
		return err
	}
	if err := adapters.VerifyHistory(ctx); err != nil {
		return err
	}
	completed, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil {
		return err
	}
	if completed {
		state, err := adapters.VerifyACKs(ctx)
		if err != nil || !sessionExistingConsumersACKed(q, state, clock().UTC()) {
			if err != nil {
				return err
			}
			return bad()
		}
		return nil
	}
	// Reject stale or unknown consumer baselines before signing another seed.
	// Mint changes its separately authenticated custody owner, not these batches.
	for _, batch := range q.Batches {
		if batch.Plan != nil {
			if err := batch.Plan.AdmitResumable(ctx, adapters.Backend, store.Root); err != nil {
				return err
			}
		}
	}
	key := "service-session-batches-" + review
	intent := serviceSessionBatchIntent(review, q)
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if err := e.BeginEntry(key, "service-session-batches", intent, clock().UTC()); err != nil {
			return err
		}
	} else if entry.Phase != "service-session-batches" || entry.IntentSHA256 != intentSHA || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
		return bad()
	}
	if err := adapters.Mint(ctx); err != nil {
		return err
	}
	for _, batch := range q.Batches {
		if err := check(ctx, batch.Plan); err != nil {
			return err
		}
		if len(batch.Pending) != 0 {
			if err := adapters.Pending(ctx, batch); err != nil {
				return err
			}
		}
		if batch.Plan == nil {
			continue
		}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			if err := check(ctx, p); err != nil {
				return err
			}
			return p.AdmitResumable(ctx, adapters.Backend, store.Root)
		}
		if err := executeEnvironmentCreateStagePlan(ctx, store, e, batch.Stage, batch.Plan, adapters.Backend, qualify, check, adapters.Accept, func(context.Context) error { return nil }, out); err != nil {
			return err
		}
	}
	if err := check(ctx, nil); err != nil {
		return err
	}
	state, err := adapters.VerifyACKs(ctx)
	if err != nil {
		return err
	}
	observed := clock().UTC()
	if !sessionExistingConsumersACKed(q, state, observed) {
		return bad()
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	if entry.State == "pending" {
		receipt := serviceSessionBatchReceipt{Review: review, ReferencePlanSHA: q.Reference.Plan.SHA256(), ACK: state, ObservedAt: observed}
		for _, batch := range q.Batches {
			if batch.Plan != nil {
				receipt.BatchPlanSHA = append(receipt.BatchPlanSHA, batch.Plan.SHA256())
			}
		}
		if !validServiceSessionBatchReceipt(store, e, q, review, receipt) {
			return bad()
		}
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
			return err
		}
	}
	completed, err = completedServiceSessionBatch(store, e, q, review)
	if err != nil {
		return err
	}
	if completed {
		return nil
	}
	return e.CompleteEntry(key, observed)
}

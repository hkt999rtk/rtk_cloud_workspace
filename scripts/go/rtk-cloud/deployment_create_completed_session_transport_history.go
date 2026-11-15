package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func verifyCompletedSessionTransportHistory(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("historical transport template requires its completed managed OpenBao and original-key session handoff")
	}
	if e == nil || r == nil || !r.SecondSession || e.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	_, successor, err := readCompletedInitialTransportSuccessor(store, e.Journal.PlanSHA256)
	if err != nil || !successor {
		return bad()
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil || head == nil || review != r.Review || r.OriginalKeySHA != head.Renewal.KeySHA || r.OriginalCSRSHA != head.Renewal.CSRSHA || deploymentplan.Digest(r.RenewedIntent) != deploymentplan.Digest(head.Renewal.RenewedIntent) || !environmentPreflightDigest.MatchString(head.Renewal.OpenBaoTrustSHA) {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, *head, review)
	if err != nil || !done {
		return bad()
	}
	receipt, found, err := completedOpenBaoInPlaceReceipt(ctx, store, e)
	if err != nil || !found {
		return bad()
	}
	if _, err := verifyOpenBaoInPlaceHistory(ctx, cfg, store, e, receipt); err != nil {
		return err
	}
	return e.Check(ctx)
}

func recoveryTransportTemplateMaterials(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, minimum time.Duration) (map[string]string, environmentCreateTransport, error) {
	if r == nil || !r.SecondSession {
		return foundationRecoveryMaterials(store, e.Journal.PlanSHA256, minimum)
	}
	if err := verifyCompletedSessionTransportHistory(ctx, cfg, store, e, r); err != nil {
		return nil, environmentCreateTransport{}, err
	}
	return readFoundationRecoveryMaterials(store, e.Journal.PlanSHA256, e, true, minimum)
}

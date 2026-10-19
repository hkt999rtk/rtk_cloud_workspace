package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateNormalTailCallerTimeoutAllowed(timeout time.Duration, afterApplication bool) bool {
	return timeout > 0 && (timeout <= managedLateOverallLimit || afterApplication && timeout <= managedLateNormalTailExtensionLimit)
}

// A longer caller is only a bounded preparation context. Fresh six-gate
// admission must still establish the distinct supplemental execution pair.
func managedLateNormalTailSupplementCallerContext(ctx context.Context, store secretStore, e *environmentCreateExecution, timeout time.Duration, afterApplication bool) (context.Context, context.CancelFunc, error) {
	unchanged := func() {}
	if !managedLateNormalTailCallerTimeoutAllowed(timeout, afterApplication) {
		return nil, unchanged, createOpenBaoACKBlock("normal-tail-supplement-caller-limit")
	}
	if timeout <= managedLateOverallLimit {
		return ctx, unchanged, nil
	}
	bad := func() (context.Context, context.CancelFunc, error) {
		return nil, unchanged, createOpenBaoACKBlock("normal-tail-supplement-caller-source")
	}
	if ctx == nil || ctx.Err() != nil || e == nil || e.Store != store || store.Environment != "dev" || e.Check(ctx) != nil {
		return bad()
	}
	if managedLateRedisPrePauseSelected(e.Journal) {
		proof, _, err := readManagedLateRedisPrePauseSource(store, e)
		if err != nil {
			return nil, unchanged, err
		}
		authority, err := readManagedLateNormalTailExtensionAuthority(store, e.Journal.PlanSHA256)
		if err != nil || authority == nil || authority.SourceSHA != proof.SourceSHA || authority.OwnerSHA != deploymentplan.Digest(e.owner) || !time.Now().Before(authority.Deadline) || e.Check(ctx) != nil {
			return bad()
		}
		bounded, cancel := context.WithDeadline(ctx, authority.Deadline)
		if bounded.Err() != nil {
			cancel()
			return bad()
		}
		return bounded, cancel, nil
	}
	if !managedLateNormalTailControllerSelected(e.Journal) || len(e.Journal.Entries) != 434 || len(e.Journal.KubernetesLeases) != 149 || e.Journal.Entries[431].State != "completed" {
		return bad()
	}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" {
			return bad()
		}
	}
	if timeout > managedLateNormalTailSupplementLimit {
		approval, err := loadManagedLateNormalTailExtensionApproval(store, e.Journal.PlanSHA256)
		if err != nil || approval == nil || approval.SourceSHA != deploymentplan.Digest(e.Journal) || approval.OwnerSHA != deploymentplan.Digest(e.owner) || !time.Now().Before(approval.ExtendedDeadline) || e.Check(ctx) != nil {
			return bad()
		}
		bounded, cancel := context.WithDeadline(ctx, approval.ExtendedDeadline)
		if bounded.Err() != nil {
			cancel()
			return bad()
		}
		return bounded, cancel, nil
	}
	approval, err := loadManagedLateNormalTailSupplementApproval(store, e.Journal.PlanSHA256)
	if err != nil || approval == nil || approval.SourceSHA != deploymentplan.Digest(e.Journal) || approval.OwnerSHA != deploymentplan.Digest(e.owner) || !time.Now().Before(approval.ExtendedDeadline) || e.Check(ctx) != nil {
		return bad()
	}
	bounded, cancel := context.WithDeadline(ctx, approval.ExtendedDeadline)
	if bounded.Err() != nil {
		cancel()
		return bad()
	}
	return bounded, cancel, nil
}

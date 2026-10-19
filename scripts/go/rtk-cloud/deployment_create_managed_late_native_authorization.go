package main

// Private integration draft. Not part of a qualified checker or a live plan.

import (
	"context"
	"errors"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type managedLateNativeGuardKey struct{}

func checkManagedLateNativeGuard(ctx context.Context) error {
	if guard, ok := ctx.Value(managedLateNativeGuardKey{}).(func() error); ok {
		return guard()
	}
	return nil
}

// The caller also authenticates its typed source/current native role before
// constructing recipe. Every OpenBao command must call the context guard.
func managedLateNativeLease(ctx context.Context, cfg deploymentConfig,
	e *environmentCreateExecution, b environmentKubernetesBackend,
	review, stage string, recipe deploymentplan.Object,
	write func(context.Context) error) (resultErr error) {
	bad := func() error {
		return environmentCreateBootstrapBlocker("late native authorization requires its exact admitted recipe and original execution lease")
	}
	if e == nil || write == nil || cfg.Environment != "dev" || b.Store.Root != e.Store.Root || !environmentPreflightDigest.MatchString(review) || e.Check(ctx) != nil {
		return bad()
	}
	base := strings.TrimSuffix(stage, "-"+review)
	if stage != base+"-"+review || base != "late-seed-authorization" && base != "late-cleanup-authorization" {
		return bad()
	}
	admitted, source, err := loadAdmittedCreateRecovery(e.Store, e, review)
	if err != nil || admitted.Plan.Schema != managedLateSessionSchema && admitted.Plan.Schema != managedLateExpiredRenewalSchema || admitted.Plan.ParentJournalSHA != deploymentplan.Digest(source) || admitted.Plan.Materials["accepted-service-core-late-session-native-"+base] != deploymentplan.Digest(recipe) {
		return bad()
	}
	if err := e.BeginEntry(stage, "late-native-authorization", mustProviderRecoveryJSON(recipe), time.Now().UTC()); err != nil {
		return err
	}
	bindings := deploymentplan.Digest(map[string]string{"schema": admitted.Plan.Schema, "review": review, "stage": stage, "recipe": deploymentplan.Digest(recipe)})
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", BindingsSHA256: bindings}}
	lease, err := b.AcquireExecution(ctx, plan)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}
	if err := guard(); err != nil {
		return err
	}
	if err := write(context.WithValue(ctx, managedLateNativeGuardKey{}, guard)); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	if err := e.RecordReceipt(stage, mustProviderRecoveryJSON(recipe)); err != nil {
		return err
	}
	return e.CompleteEntry(stage, time.Now().UTC())
}

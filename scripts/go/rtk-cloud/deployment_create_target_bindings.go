package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Carry the accepted cleanup allocation into final create rendering. The typed
// adapter authenticates finalization, original UID, producer and actual mount.
func observeEnvironmentTargetBindings(ctx context.Context, cfg deploymentConfig, release environmentRelease, operation string, live []deploymentplan.Object) (environmentIdentityBindings, error) {
	bindings, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		return bindings, err
	}
	if operation == "create" {
		if handoff, ok := createNormalTailHandoffFromContext(ctx); ok {
			if err := applyCreateNormalTailCleanupBinding(ctx, cfg, handoff, &bindings, live); err != nil {
				return bindings, err
			}
		}
		if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok {
			if err := applyManagedLateBootstrapBindings(ctx, cfg, runtime, &bindings, live); err != nil {
				return bindings, err
			}
			if err := applyEnvironmentFreshProductBindings(ctx, cfg, runtime.Execution, &bindings); err != nil {
				return bindings, err
			}
		}
	}
	if operation != "create" {
		if err := observeEnvironmentCurrentCleanupBinding(&bindings, live); err != nil {
			return bindings, err
		}
		if err := observeEnvironmentManagedMQTTPlacements(&bindings, live); err != nil {
			return bindings, err
		}
	}
	return bindings, nil
}

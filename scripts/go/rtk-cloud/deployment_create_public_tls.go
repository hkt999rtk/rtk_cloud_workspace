package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreatePublicTLSKey struct{}

// Fresh qualification renders only the maintained public routes. It has no
// future Kubernetes owner, ACME account key, certificate or success receipt.
func prepareEnvironmentCreatePublicTLS(ctx context.Context, cfg deploymentConfig, store secretStore) (*environmentPublicTLSPlan, error) {
	values := deploymentEndpointValues(cfg)
	definitions, err := decodeEnvironmentCreateManifests(lkePublicHTTPSIngressManifests(values, deploymentPublicHTTPSRoutes(values))...)
	if err != nil {
		return nil, err
	}
	return prepareEnvironmentPublicTLS(ctx, cfg, store, definitions, nil, environmentIdentityBindings{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Owners: map[string]string{}})
}

func executeEnvironmentCreatePublicTLS(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, e *environmentCreateExecution, apply environmentCreateBootstrapApply) error {
	if plan.PublicTLS == nil {
		return nil
	}
	return executeEnvironmentPublicTLS(ctx, cfg, store, e, plan.PublicTLS, func(ctx context.Context, definition deploymentplan.Definition) (string, error) {
		if err := apply(ctx, "public-tls", []deploymentplan.Definition{definition}); err != nil {
			return "", err
		}
		return environmentCreatePublicTLSAcceptedUID(ctx, store, e, definition)
	})
}

func environmentCreatePublicTLSAcceptedUID(ctx context.Context, store secretStore, e *environmentCreateExecution, definition deploymentplan.Definition) (string, error) {
	if e == nil || e.Store != store || e.Check(ctx) != nil {
		return "", environmentCreateBlocker("deployment.create.public-tls", "public certificate readback lacks its held original create execution")
	}
	child, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/public-tls")
	if err != nil || len(child.Resources) != 1 || child.Resources[0].ID != deploymentplan.ID(definition.Object) || !deploymentplan.Matches(definition.Object, child.Resources[0].Desired) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[child.Resources[0].ID]) {
		return "", environmentCreateBlocker("deployment.create.public-tls", "published certificate lacks its genuine original accepted child and Secret UID")
	}
	return journal.Owners[child.Resources[0].ID], e.Check(ctx)
}

func bindEnvironmentCreatePublicTLSTarget(ctx context.Context, store secretStore, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	plan, ok := ctx.Value(environmentCreatePublicTLSKey{}).(*environmentPublicTLSPlan)
	if !ok || plan == nil {
		return definitions, nil
	}
	runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	if !ok || runtime.Execution == nil || runtime.Execution.Store != store || runtime.Execution.Check(ctx) != nil {
		return nil, environmentCreateBlocker("deployment.create.public-tls", "public certificate target lacks its held original create execution")
	}
	definitions, err := bindEnvironmentPublicTLSReferences(plan, definitions)
	if err != nil {
		return nil, err
	}
	return resolveEnvironmentPublicTLSDefinitions(store, plan, definitions)
}

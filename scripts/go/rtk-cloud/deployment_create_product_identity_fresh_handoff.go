package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A completed renewed session has its own immutable cleanup projection. Keep
// the original observation as identity evidence and select that session before
// any new remaining-bootstrap recipe is rendered.
func remainingFreshProductServiceCeremony(store secretStore, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.SecondSession || len(p.ProductIdentities) == 0 {
		return service, nil
	}
	if runtime.Execution == nil || runtime.Execution.Store != store {
		return service, freshProductResumeBlock("remaining-bootstrap-owner")
	}
	head, review, err := sessionLineageHead(store, runtime.Execution)
	if err != nil || head == nil || review != r.Review {
		return service, freshProductResumeBlock("remaining-bootstrap-session")
	}
	return freshProductResumeCurrentCeremony(store, runtime.Execution, *head, review, service)
}

// Preflight and execution render the same next issuer stage. Qualification
// performs Kubernetes admission only; no input, identity or receipt is written.
func remainingFreshProductIssuerDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	core, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, service)
	if err != nil {
		return nil, err
	}
	core, err = applyEnvironmentCreateManagedOpenBaoConsumers(p, service, roots, intermediates, core)
	if err != nil {
		return nil, err
	}
	return configureEnvironmentCreateFinalIssuer(p, roots, intermediates, core)
}

func qualifyRemainingFreshProductIssuer(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime) error {
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || r.ManagedTransportRecovery == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil {
		return freshProductResumeBlock("remaining-issuer-inputs")
	}
	if err := qualifyFreshProductSuccessorControls(ctx, cfg, store, runtime); err != nil {
		return err
	}
	const stage = "managed-complete-issuer"
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage); entry != nil && entry.State == "completed" {
		if currentCoreForwardReceiptMatches(entry.Receipt) {
			if _, _, err := acceptedCurrentCoreStage(store, e); err != nil {
				return err
			}
			var receipt currentCoreForwardReceipt
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil {
				return currentCoreForwardBlock("completed-logical-core")
			}
			q, err := readCurrentCoreForward(store, e, receipt.Qualification)
			if err != nil {
				return err
			}
			r.currentCore = q
		}
		return nil // The complete gate already observes accepted owner history.
	}
	service, err := remainingFreshProductServiceCeremony(store, parent.Bootstrap, runtime, r.Ceremony)
	if err != nil {
		return err
	}
	runtime.ServiceCeremony = &service
	ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	d := r.ManagedTransportRecovery
	roots := map[string]environmentCreateRegisteredIssuer{"service": service.Root, "openbao_tls": *r.OpenBaoRoot, "mqtt": *r.MQTTRoot, "app": d.AppRoot, "device": d.Device}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": service.Intermediate, "openbao_tls": *r.OpenBaoIntermediate, "mqtt": *r.MQTTIntermediate, "app": d.AppIntermediate}
	definitions, err := remainingFreshProductIssuerDefinitions(ctx, cfg, parent.Bootstrap, runtime, service, roots, intermediates)
	if err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateOpenBaoPublicCRLs(ctx, cfg, store, parent, runtime, service, definitions); err != nil {
		return err
	}
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return err
	}
	definitions, err = projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, plan)
	if err != nil {
		return err
	}
	var live []deploymentplan.Object
	if plan == nil {
		live, err = runtime.Backend.Inventory(ctx, parent.Stack)
		if err != nil {
			return err
		}
	} else {
		for _, resource := range plan.Resources {
			if resource.Baseline != nil {
				live = append(live, resource.Baseline)
			}
		}
	}
	if err := bindOpenBaoAllocations(definitions, live); err != nil {
		return err
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions)}
	if plan == nil {
		plan, err = deploymentplan.Compile(inputs, definitions, live)
		if err != nil {
			return err
		}
	}
	if plan.Inputs != inputs {
		q, _, err := prepareCurrentCoreForward(ctx, cfg, store, parent, runtime, service, definitions)
		if err != nil {
			return err
		}
		r.currentCore = q
		return nil
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
	return plan.AdmitResumable(ctx, backend, store.Root)
}

// Refresh the transport after workload acceptance while preserving the exact
// original renewed-seed cleanup and independently held KV authority.
func freshProductFinalizerRuntime(ctx context.Context, cfg deploymentConfig, l *createNormalTailProductLifecycle, e *environmentCreateExecution, backend environmentKubernetesBackend, client *environmentCreatePKIClient, s environmentCreateServiceCeremony) (environmentCreateBootstrapRuntime, error) {
	var empty environmentCreateBootstrapRuntime
	bad := func() (environmentCreateBootstrapRuntime, error) {
		return empty, environmentCreateBootstrapBlocker("fresh finalizer lost its original session or workload custody authority")
	}
	if e == nil || l == nil || backend.Store != e.Store || client == nil || client.Execution != e || cfg.Environment != e.Journal.Environment || e.Check(ctx) != nil {
		return bad()
	}
	original, exists := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	if exists && (original.Execution != e || original.ExecutionSHA256 != e.Journal.PlanSHA256 || original.Backend.Store != e.Store) {
		return bad()
	}
	if l.FreshResume != nil {
		r := original.ServiceSessionRecovery
		if !exists || r == nil || !r.Renewal || !r.SecondSession || r.qualifiedPlan == nil || r.RenewedIntent.SessionID != s.BootstrapSessionID {
			return bad()
		}
		if r.Review == l.FreshResume.SessionReviewSHA256 {
			if deploymentplan.Digest(r.qualifiedPlan) != l.FreshResume.SelectedPlanSHA256 {
				return bad()
			}
		} else {
			binding, err := readFreshProductSessionSuccessor(e.Store, e, l)
			if err != nil || binding == nil || binding.CurrentReview != r.Review || binding.SessionID != s.BootstrapSessionID || binding.CleanupBindingSHA256 != deploymentplan.Digest(s.CleanupBinding) {
				return bad()
			}
			programme, err := readFreshProductProgram(e.Store, e)
			if err != nil || programme.SelectedPlan == nil || r.qualifiedPlan.ConfigurationSHA256 != programme.SelectedPlan.ConfigurationSHA256 || deploymentplan.Digest(r.qualifiedPlan.Release) != deploymentplan.Digest(programme.SelectedPlan.Release) {
				return bad()
			}
			if err := verifyFreshProductSuccessorControls(ctx, cfg, e.Store, original, l); err != nil {
				return empty, err
			}
		}
	}
	if cfg.Values["PKI_STATE_BACKEND"] == "openbao" && (!exists || original.Custody == nil || original.Custody.Generation != e.Journal.PlanSHA256 || original.Custody.ServiceRootSHA256 != s.Root.RootFingerprint) {
		return bad()
	}
	original.Execution, original.ExecutionSHA256, original.Backend, original.PKIClient = e, e.Journal.PlanSHA256, backend, client
	original.ServiceCeremony = &s
	if client.Origin != nil {
		original.APIOrigin = client.Origin.String()
	}
	return original, nil
}

package main

// Private integration draft, unlinked until actual accepted Core and native CRL
// completion exist. No native operation in this file has been executed.
import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func executeManagedLateBootstrapSeed(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateSession == nil {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("exact late session source required")
	}
	if r.ManagedLateRemaining != nil {
		return restoreManagedLateRemainingCeremony(ctx, cfg, store, runtime)
	}
	if r.ManagedLatePostMint != nil {
		return restoreManagedLatePostMintCeremony(ctx, cfg, store, runtime)
	}
	native := func(guarded context.Context, stage string, recipe deploymentplan.Object, write func(context.Context) error) error {
		return managedLateNativeLease(guarded, cfg, runtime.Execution, runtime.Backend, r.ManagedLateSession.Review, stage, recipe, write)
	}
	return executeManagedLateSessionSeed(ctx, cfg, p, store, runtime, r.ManagedLateSession, apply, native)
}

func verifyManagedLateAcceptedOwners(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	if r := runtime.ServiceSessionRecovery; r != nil && r.normalTail != nil {
		return verifyManagedLateNormalTailAcceptedOwners(ctx, cfg, runtime)
	}
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateSession == nil || e == nil || e.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("late accepted owner source missing")
	}
	late := r.ManagedLateSession
	plan, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-late-session-owners-"+late.Review)
	if err != nil || !managedCoreSameQualifiedPlan(plan, managedLateExpectedOwnerCandidate(r)) {
		return environmentCreateBootstrapBlocker("late current owners are not the accepted qualified child")
	}
	updates := 0
	logID := "Deployment/" + e.Journal.Stack + "-video-cloud/video-cloud-logingester"
	logRetained := false
	for _, resource := range plan.Resources {
		if resource.Action == "update" {
			updates++
			source, ok := late.Owners[resource.ID]
			if !ok || source.UID != resource.UID {
				return environmentCreateBootstrapBlocker("late update escaped original native controllers")
			}
		}
		if resource.Action != "update" && resource.Action != "retain" {
			return environmentCreateBootstrapBlocker("late owners contain unqualified creation or retirement")
		}
		desired := resource.Desired
		if resource.ID == logID {
			if resource.Action != "retain" {
				return environmentCreateBootstrapBlocker("late owner child must retain original Log until its controlled helper")
			}
			logRetained = true
			if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-late-log-stop-"+late.Review); entry != nil {
				stop, sj, err := managedLateLogAccepted(e.Store, e, late, "stop", "")
				if err != nil || len(stop.Resources) != 1 || stop.Resources[0].ID != logID || sj.Owners[logID] != resource.UID {
					return environmentCreateBootstrapBlocker("late original Log stop is not genuinely accepted")
				}
				desired = stop.Resources[0].Desired
			}
			key := "kubernetes/managed-late-log-restore-" + late.Review
			if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry != nil {
				restore, rj, err := managedTransportPostAcceptedStage(e.Store, e, key)
				if err != nil || !managedCoreSameQualifiedPlan(restore, late.LogRestoreCandidate) || len(restore.Resources) != 1 || restore.Resources[0].ID != logID || restore.Resources[0].UID != resource.UID || rj.Owners[logID] != resource.UID {
					return environmentCreateBootstrapBlocker("late original Log restore is not genuinely accepted")
				}
				desired = restore.Resources[0].Desired
			}
		}
		body, err := runtime.Backend.Read(ctx, resource.ID)
		if err != nil || body == nil || j.Owners[resource.ID] != resource.UID || deploymentplan.Map(body["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(desired, body) {
			return environmentCreateBootstrapBlocker("late native original owner changed")
		}
	}
	if updates != 9 || !logRetained {
		return environmentCreateBootstrapBlocker("late accepted child requires nine native updates and original Log retention")
	}
	return e.Check(ctx)
}

// Registration consumes the bodies from the actually accepted Core and native
// owner children. Its renderer must preserve the independently qualified
// CertIssuer image and the new SID instead of rebuilding historical templates.
func managedLateCurrentCoreDefinitions(runtime environmentCreateBootstrapRuntime) ([]deploymentplan.Definition, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateSession == nil || e == nil {
		return nil, environmentCreateBootstrapBlocker("late current core source missing")
	}
	late := r.ManagedLateSession
	if r.completedTail != nil {
		if err := verifyManagedLateCompletedTailSource(context.Background(), late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair.Plan.Configuration, e.Store, e, r); err != nil {
			return nil, err
		}
		e = r.completedTail.view
	} else if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		if err := verifyManagedLateTerminalFinalizerSource(context.Background(), late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair.Plan.Configuration, e.Store, e, r); err != nil {
			return nil, err
		}
		if !managedLateSourceReuseActive(e) {
			if _, err := loadManagedLateTerminalFinalizerSource(context.Background(), late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair.Plan.Configuration, e.Store, e); err != nil {
				return nil, err
			}
		}
		view, viewErr := managedLateTerminalPureSourceView(context.Background(), e)
		if viewErr != nil {
			return nil, viewErr
		}
		e = view
	}
	owners, _, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-late-session-owners-"+late.Review)
	if err != nil || !managedCoreSameQualifiedPlan(owners, managedLateExpectedOwnerCandidate(r)) || late.AcceptedCore == nil {
		return nil, environmentCreateBootstrapBlocker("late current core owners not accepted")
	}
	current := map[string]deploymentplan.Resource{}
	for _, resource := range owners.Resources {
		current[resource.ID] = resource
	}
	logID := "Deployment/" + e.Journal.Stack + "-video-cloud/video-cloud-logingester"
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-late-log-restore-"+late.Review) != nil {
		plan, journal, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-late-log-restore-"+late.Review)
		if err != nil || !managedCoreSameQualifiedPlan(plan, late.LogRestoreCandidate) || len(plan.Resources) != 1 || plan.Resources[0].ID != logID || journal.Owners[logID] != late.Owners[logID].UID {
			return nil, environmentCreateBootstrapBlocker("late current Log restore not accepted")
		}
		current[logID] = plan.Resources[0]
	}
	children, _, _, err := managedLateCurrentChildren(e.Store, e, late)
	if err != nil {
		return nil, err
	}
	cleanup, err := managedLateAcceptedCleanupRBAC(late, children)
	if err != nil {
		return nil, err
	}
	for id, resource := range cleanup {
		if existing, ok := current[id]; ok && (existing.UID != resource.UID || !deploymentplan.Matches(resource.Desired, existing.Desired)) {
			return nil, environmentCreateBootstrapBlocker("late current core cleanup differs from accepted owner child")
		}
		current[id] = resource
	}
	return managedLateAcceptedCoreDefinitions(late, current)
}

func completeManagedLateBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	empty := environmentIdentityBindings{}
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateSession == nil || r.ManagedTransportRecovery == nil || apply == nil {
		return empty, environmentCreateBootstrapBlocker("late completion requires explicit source and native owners")
	}
	late := r.ManagedLateSession
	if r.ManagedLateRemaining != nil {
		if err := verifyManagedLateRemainingPreparationSource(ctx, cfg, store, runtime); err != nil {
			return empty, err
		}
	} else if err := verifyManagedLateSessionRuntimeSource(ctx, cfg, store, runtime, late); err != nil {
		return empty, err
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": service.Root, "device": r.ManagedTransportRecovery.Device, "app": r.ManagedTransportRecovery.AppRoot, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{"service": service.Intermediate, "app": r.ManagedTransportRecovery.AppIntermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, issuers, nil); err != nil {
		return empty, err
	}
	runtime.ServiceCeremony = &service
	ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.FinalizationOnly {
		return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, issuers, apply)
	}
	var defs []deploymentplan.Definition
	var err error
	if r.ManagedLatePostMint != nil {
		defs, err = managedLatePostMintDefinitions(late, r.ManagedLatePostMint)
	} else {
		defs, err = managedLateSessionOwnerDefinitions(late, service)
		if err == nil {
			defs, err = managedLateDefinitionClosure(defs, late.DependencyOwners)
		}
	}
	if err != nil {
		return empty, err
	}
	if r.ManagedLateRemaining == nil {
		if err = apply(ctx, "managed-late-session-owners-"+late.Review, defs); err != nil {
			return empty, err
		}
	}
	if err = verifyManagedLateAcceptedOwners(ctx, cfg, runtime); err != nil {
		return empty, err
	}
	check := func(checkCtx context.Context) error {
		return verifyManagedLateSessionRuntimeSource(checkCtx, cfg, store, runtime, late)
	}
	if _, err = executeManagedLateLogACK(ctx, cfg, runtime, late, apply, check); err != nil {
		return empty, err
	}
	if err = verifyManagedLateAcceptedOwners(ctx, cfg, runtime); err != nil {
		return empty, err
	}
	return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, issuers, apply)
}

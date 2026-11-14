package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The bootstrap/public-TLS readback and ordinary target compiler must classify
// the same original containers against the same environment-selected roles.
func observeEnvironmentCreateTargetBindings(ctx context.Context, cfg deploymentConfig, release environmentRelease, operator map[string]string, live []deploymentplan.Object) (environmentIdentityBindings, error) {
	selected, err := accountRegistrationCRLRefreshRelease(cfg, release, operator)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	return observeEnvironmentTargetBindings(ctx, cfg, selected, "create", live)
}

// Carry the accepted cleanup allocation into final create rendering. The typed
// adapter authenticates finalization, original UID, producer and actual mount.
func observeEnvironmentTargetBindings(ctx context.Context, cfg deploymentConfig, release environmentRelease, operation string, live []deploymentplan.Object) (environmentIdentityBindings, error) {
	observationRelease := release
	if operation == "create" {
		if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && runtime.Execution != nil {
			var err error
			observationRelease, err = createRuntimeImageObservationRelease(ctx, runtime.Execution.Store, runtime.Execution, runtime.Backend, release, live)
			if err != nil {
				return environmentIdentityBindings{}, err
			}
		}
	}
	bindings, err := observeEnvironmentBindings(cfg.Environment, observationRelease, live)
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
			if err := omitAuthenticatedFreshPreparations(ctx, cfg, runtime, &bindings, live); err != nil {
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
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return bindings, err
	}
	if err := restoreEnvironmentCustodyBindings(ctx, cfg, store, operation, &bindings); err != nil {
		return bindings, err
	}
	return bindings, nil
}

// Temporary enrollment owners are authenticated native producers, not target
// workloads. Keep their UIDs in Owners until the normal retirement stage.
func omitAuthenticatedFreshPreparations(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, bindings *environmentIdentityBindings, live []deploymentplan.Object) error {
	if bindings == nil {
		return nil
	}
	excluded, err := authenticatedFreshPreparationOwners(ctx, cfg, runtime, bindings.Owners, live)
	if err != nil {
		return err
	}
	pods := bindings.Pods[:0]
	for _, pod := range bindings.Pods {
		id := pod.Kind + "/" + bindings.Stack + "-" + pod.NamespaceSuffix + "/" + pod.Name
		if excluded[id] == "" {
			pods = append(pods, pod)
		}
	}
	bindings.Pods = pods
	return nil
}

func authenticatedFreshPreparationOwners(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, owners map[string]string, live []deploymentplan.Object) (map[string]string, error) {
	e := runtime.Execution
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil, nil
	}
	program, err := readFreshProductProgram(e.Store, e)
	if err != nil {
		return nil, err
	}
	l := &program.Lifecycle
	original, err := readProductInstalled(e.Store, l)
	if err != nil {
		return nil, err
	}
	definitions, err := productPreparationDefinitions(cfg, e.Store, e, l, original, false, true)
	if err != nil {
		return nil, err
	}
	stages := []string{"service-custody-reader"}
	if l.FreshResume != nil {
		stages[0] = l.FreshResume.CustodyStage
	}
	for _, output := range l.Outputs {
		stages = append(stages, "product-identities-"+productIdentityScope(l)[:12]+"-enroll-"+strings.TrimPrefix(output.Subject, "service:"))
	}
	birth := map[string]string{}
	for _, stage := range stages {
		plan, journal, err := acceptedCreateStage(e.Store, e, "kubernetes/"+stage)
		if err != nil {
			return nil, err
		}
		for _, resource := range plan.Resources {
			if resource.Desired["kind"] != "Deployment" {
				continue
			}
			if birth[resource.ID] != "" || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) {
				return nil, environmentCreateBootstrapBlocker("fresh product preparation owner is ambiguous")
			}
			birth[resource.ID] = "kubernetes/" + stage
		}
	}
	objects := map[string]deploymentplan.Object{}
	for _, object := range live {
		objects[deploymentplan.ID(object)] = object
	}
	excluded := map[string]string{}
	var history map[string]completedRecordedSuccessorStage
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		actual := objects[id]
		if actual == nil {
			continue
		} // Already retired preparations stay absent.
		uid := deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["uid"])
		if birth[id] == "" || owners[id] != uid || deploymentplan.Map(actual["metadata"])["uid"] != uid {
			return nil, environmentCreateBootstrapBlocker("fresh product preparation owner changed")
		}
		if history == nil {
			history, err = authenticatedRecordedPendingWrites(ctx, e.Store, e, runtime.Backend)
			if err != nil {
				return nil, err
			}
		}
		latest, err := completedRecordedOwnerSuccessorsAfterHistory(ctx, e.Store, e, runtime.Backend, birth[id], []activeTransportRenewalResource{sessionRecordedOwnerBase(id, uid, definition.Object)}, history)
		if err != nil || len(latest) != 1 || !deploymentplan.Matches(latest[0].Desired, actual) {
			return nil, environmentCreateBootstrapBlocker("fresh product preparation differs from its recorded native history")
		}
		excluded[id] = uid
	}
	return excluded, e.Check(ctx)
}

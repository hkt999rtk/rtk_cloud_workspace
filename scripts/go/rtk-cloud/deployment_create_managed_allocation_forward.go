package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedAllocationForwardStage(original *deploymentplan.Plan) string {
	return "transport-managed-storage-forward-" + original.SHA256()[:12]
}

// A stopped child may already have written its owners. Use only its original
// recorded completed writes as the next link in the resource history.
func completedManagedAllocationWrites(store secretStore, e *environmentCreateExecution) ([]activeTransportRenewalResource, error) {
	original, err := readEnvironmentCreateStageInput(store, e, "transport-managed-hosts")
	if err != nil || original == nil {
		return nil, err
	}
	stage := managedAllocationForwardStage(original)
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || plan == nil {
		return nil, err
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
	if err != nil || !exists || journal.InFlight != "" || journal.Failed != "" {
		return nil, createProviderRecoveryBlock("managed host writes cannot be reconciled with their original child journal")
	}
	var writes []activeTransportRenewalResource
	for _, resource := range plan.Resources {
		if !slices.Contains(journal.Completed, resource.ID) || resource.Action != "update" {
			continue
		}
		if resource.UID == "" || journal.Owners[resource.ID] != resource.UID || resource.Baseline == nil {
			return nil, createProviderRecoveryBlock("managed host write lacks its original owner: " + resource.ID)
		}
		writes = append(writes, activeTransportRenewalResource{ID: resource.ID, UID: resource.UID, Before: resource.Baseline, Desired: resource.Desired})
	}
	return writes, nil
}

// Earlier accepted stages remain evidence after a later recorded write. Check
// the exact owner chain and current state instead of requiring an old body.
func verifyAcceptedStageAfterRecordedHistory(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, key string) (*deploymentplan.Plan, error) {
	if key == "kubernetes/"+productPublicCRLStage {
		if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry != nil && productCRLForwardReceiptMatches(entry.Receipt) {
			source, err := readEnvironmentCreateStageInput(store, e, productPublicCRLStage)
			if err != nil || source == nil {
				return nil, createProviderRecoveryBlock("original product CRL failure input unavailable")
			}
			j, ok, err := source.ReadExecution(store.Root)
			if err != nil || !ok {
				return nil, createProviderRecoveryBlock("original product CRL failed journal unavailable")
			}
			if err := validateProductCRLForwardReceipt(store, e, source, j, entry.Receipt); err != nil {
				return nil, err
			}
			var receipt productCRLForwardReceipt
			_ = json.Unmarshal(entry.Receipt, &receipt)
			_, err = verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, receipt.ForwardStage)
			return source, err
		}
	}
	if key == "kubernetes/workload" {
		if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry != nil && workloadImageForwardReceiptMatches(entry.Receipt) {
			source, err := readEnvironmentCreateStageInput(store, e, "workload")
			if err != nil || source == nil {
				return nil, createProviderRecoveryBlock("original workload source unavailable")
			}
			j, exists, err := source.ReadExecution(store.Root)
			if err != nil || !exists {
				return nil, createProviderRecoveryBlock("original workload journal unavailable")
			}
			if err = validateWorkloadImageForwardReceipt(store, e, source, j, entry.Receipt); err != nil {
				return nil, err
			}
			_, err = verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, "kubernetes/"+createWorkloadImageForwardStage)
			return source, err
		}
	}
	plan, journal, err := acceptedCreateStage(store, e, key)
	if err != nil {
		return nil, err
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, strings.TrimPrefix(key, "kubernetes/"), plan)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return nil, createProviderRecoveryBlock("accepted owner history lacks its original native intent and execution lease")
	}
	owners := map[string]deploymentplan.Resource{}
	if err := managedLateRecordedOwners(plan, journal, owners); err != nil {
		return nil, err
	}
	// History proves owners and bodies, not a new rollout. Use one fresh native
	// inventory for this observation; immediate pre-write reads and ordinary
	// target readiness remain in their execution/qualification stages.
	read := b.Read
	if inventory, ok := b.(interface {
		Inventory(context.Context, string) ([]deploymentplan.Object, error)
	}); ok && len(plan.Resources) > environmentDeploymentCheckConcurrency {
		live, err := inventory.Inventory(ctx, e.Journal.Stack)
		if err != nil {
			return nil, err
		}
		objects := map[string]deploymentplan.Object{}
		for _, object := range live {
			id := deploymentplan.ID(object)
			if id == "" || objects[id] != nil {
				return nil, createProviderRecoveryBlock("accepted owner history has an invalid or duplicate native inventory resource")
			}
			objects[id] = object
		}
		read = func(ctx context.Context, id string) (deploymentplan.Object, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return deploymentplan.Clone(objects[id]), nil
		}
	}
	if err := verifyAcceptedStageAfterRecordedWrites(ctx, plan, journal, read, nil); err == nil {
		return plan, nil
	}
	var base []activeTransportRenewalResource
	for _, original := range plan.Resources {
		owner := owners[original.ID]
		base = append(base, sessionRecordedOwnerBase(owner.ID, owner.UID, owner.Desired))
	}
	retired := map[string]string{}
	writes, err := completedRecordedOwnerSuccessorsAfter(ctx, store, e, b, key, base, retired)
	if err != nil {
		return nil, err
	}
	copies, err := completedBootstrapCopyRetirementsAfter(ctx, e, key, owners)
	if err != nil {
		return nil, err
	}
	for id, uid := range copies {
		retired[id] = uid
	}
	if err := observeFreshFinalizedPrimary(ctx, store, e, b, owners, read, retired); err != nil {
		return nil, err
	}
	if err := verifyAcceptedStageAfterRecordedWritesAndRetirements(ctx, plan, journal, read, writes, retired); err != nil {
		return nil, err
	}
	return plan, nil
}

func verifyAcceptedStageAfterRecordedWrites(ctx context.Context, plan *deploymentplan.Plan, journal deploymentplan.Journal, read func(context.Context, string) (deploymentplan.Object, error), writes []activeTransportRenewalResource) error {
	return verifyAcceptedStageAfterRecordedWritesAndRetirements(ctx, plan, journal, read, writes, nil)
}

func verifyAcceptedStageAfterRecordedWritesAndRetirements(ctx context.Context, plan *deploymentplan.Plan, journal deploymentplan.Journal, read func(context.Context, string) (deploymentplan.Object, error), writes []activeTransportRenewalResource, retired map[string]string) error {
	if plan == nil || read == nil {
		return createProviderRecoveryBlock("original accepted stage and native owner reader are required")
	}
	owners := map[string]deploymentplan.Resource{}
	if err := managedLateRecordedOwners(plan, journal, owners); err != nil {
		return err
	}
	for _, original := range plan.Resources {
		resource := owners[original.ID]
		if original.Action == "retire" {
			actual, err := read(ctx, resource.ID)
			if err != nil || actual != nil {
				return createProviderRecoveryBlock("accepted retirement has an unverified or replacement current owner: " + resource.ID)
			}
			continue
		}
		for _, write := range writes {
			if write.ID != resource.ID {
				continue
			}
			if write.UID != resource.UID || !deploymentplan.Matches(resource.Desired, write.Before) {
				return createProviderRecoveryBlock("recorded later write does not follow the accepted owner: " + resource.ID)
			}
			resource.Desired = write.Desired
		}
		actual, err := read(ctx, resource.ID)
		if uid := retired[resource.ID]; uid != "" {
			if uid != resource.UID || err != nil || actual != nil {
				return createProviderRecoveryBlock("retired bootstrap copy has an unverified or replacement current owner: " + resource.ID)
			}
			continue
		}
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
			return createProviderRecoveryBlock("accepted owner differs from its recorded current state: " + resource.ID)
		}
	}
	return nil
}

// Kubernetes does not permit replacing an existing StatefulSet's immutable
// claim templates. Keep the same allocation while applying the new workload.
func compileManagedAllocationForward(original *deploymentplan.Plan, live []deploymentplan.Object) (*deploymentplan.Plan, error) {
	if original == nil {
		return nil, createProviderRecoveryBlock("original managed host plan is unavailable")
	}
	definitions := make([]deploymentplan.Definition, 0, len(original.Resources))
	for _, r := range original.Resources {
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: slices.Clone(r.Dependencies)})
	}
	if err := bindOpenBaoAllocations(definitions, live); err != nil {
		return nil, err
	}
	inputs := original.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(managedAllocationForwardStage(original), definitions)
	return deploymentplan.Compile(inputs, definitions, live)
}

func qualifyManagedAllocationForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
	original, err := readEnvironmentCreateStageInput(store, e, "transport-managed-hosts")
	if err != nil || original == nil {
		return original, nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/transport-managed-hosts")
	if entry == nil {
		return nil, nil, createProviderRecoveryBlock("original managed host stage entry is unavailable")
	}
	if entry.State == "pending" {
		_, exists, err := readEnvironmentCreateStageProgress(store, e, "transport-managed-hosts", original)
		if err != nil || exists {
			return nil, nil, createProviderRecoveryBlock("original managed host stage already has native writes or cannot be inspected")
		}
	} else {
		var receipt struct {
			Forward  string `json:"actual_qualified_forward_stage"`
			Original string `json:"original_failed_plan_sha256"`
		}
		if (entry.State != "allocated" && entry.State != "completed") || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Original != original.SHA256() || receipt.Forward != "kubernetes/"+managedAllocationForwardStage(original) {
			return nil, nil, createProviderRecoveryBlock("original managed host forward receipt changed")
		}
		if _, _, err := acceptedCreateStage(store, e, receipt.Forward); err != nil {
			return nil, nil, err
		}
	}
	live, err := b.Inventory(ctx, original.Stack)
	if err != nil {
		return nil, nil, err
	}
	forward, err := compileManagedAllocationForward(original, live)
	if err != nil {
		return nil, nil, err
	}
	previous, err := readEnvironmentCreateStageInput(store, e, managedAllocationForwardStage(original))
	if err != nil {
		return nil, nil, err
	}
	if previous != nil {
		if previous.Inputs != forward.Inputs {
			return nil, nil, createProviderRecoveryBlock("saved managed host continuation differs from the original allocation")
		}
		if entry.State == "completed" {
			if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, "kubernetes/"+managedAllocationForwardStage(original)); err != nil {
				return nil, nil, err
			}
			return original, previous, nil
		}
		return original, previous, previous.AdmitResumable(ctx, b, store.Root)
	}
	return original, forward, forward.Admit(ctx, b)
}

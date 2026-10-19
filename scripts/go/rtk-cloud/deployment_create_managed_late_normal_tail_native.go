package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

func projectManagedLateNormalTailOwners(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, owners map[string]deploymentplan.Resource, ready map[string]bool) error {
	bad := func() error { return createOpenBaoACKBlock("normal-tail-accepted-current-owners") }
	if r == nil || r.normalTail == nil || r.ManagedLateRemaining == nil || validateManagedLateNormalTailStages(store, e, r.ManagedLateRemaining) != nil {
		return bad()
	}
	p, j, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[428].Key)
	if err != nil {
		return err
	}
	account := "Deployment/" + e.Journal.Stack + "-account-manager/account-manager"
	secret := "Secret/" + e.Journal.Stack + "-account-manager/account-manager-service-registration-tls"
	for _, resource := range p.Resources {
		previous, exists := owners[resource.ID]
		switch resource.ID {
		case account:
			if !exists || resource.UID != previous.UID || !deploymentplan.Matches(previous.Desired, resource.Baseline) {
				return bad()
			}
		case secret:
			if exists || resource.Action != "create" || !environmentBootstrapOwnerUUID.MatchString(j.Owners[resource.ID]) {
				return bad()
			}
		default:
			if !exists || resource.Action != "retain" || resource.UID != previous.UID || !deploymentplan.Matches(previous.Desired, resource.Desired) {
				return bad()
			}
		}
		resource.UID = j.Owners[resource.ID]
		owners[resource.ID] = resource
		ready[resource.ID] = resource.ID != secret
	}
	id := "Deployment/" + r.Ceremony.BootstrapCredentialNamespace + "/" + r.Ceremony.CustodyReader
	retired, j, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[430].Key)
	old, exists := owners[id]
	if err != nil || len(retired.Resources) != 1 || !exists || retired.Resources[0].ID != id || retired.Resources[0].UID != old.UID || j.Owners[id] != old.UID || !deploymentplan.Matches(old.Desired, retired.Resources[0].Desired) {
		return bad()
	}
	identity, identityJournal, identityErr := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[432].Key)
	if identityErr != nil || len(identity.Resources) != 1 {
		return bad()
	}
	resource := identity.Resources[0]
	resource.UID = identityJournal.Owners[resource.ID]
	if !environmentBootstrapOwnerUUID.MatchString(resource.UID) || owners[resource.ID].ID != "" {
		return bad()
	}
	owners[resource.ID] = resource
	ready[resource.ID] = false
	if managedLateNormalTailControllerSelected(e.Journal) {
		controller, cj, controllerErr := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[433].Key)
		if controllerErr != nil {
			return controllerErr
		}
		for _, res := range controller.Resources {
			previous, exists := owners[res.ID]
			if exists {
				if res.Action != "retain" || res.UID != previous.UID || !deploymentplan.Matches(previous.Desired, res.Desired) {
					return bad()
				}
			} else if res.Action != "create" || !environmentBootstrapOwnerUUID.MatchString(cj.Owners[res.ID]) {
				return bad()
			}
			res.UID = cj.Owners[res.ID]
			owners[res.ID] = res
			ready[res.ID] = res.Desired["kind"] != "Secret"
		}
	}
	delete(owners, id)
	delete(ready, id)
	return nil
}

func verifyManagedLateNormalTailRetiredCustody(ctx context.Context, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.normalTail == nil || e == nil || e.Check(ctx) != nil {
		return createOpenBaoACKBlock("normal-tail-retired-custody-source")
	}
	id := "Deployment/" + r.Ceremony.BootstrapCredentialNamespace + "/" + r.Ceremony.CustodyReader
	p, j, err := managedTransportPostAcceptedStage(e.Store, e, e.Journal.Entries[430].Key)
	if err != nil || len(p.Resources) != 1 || p.Resources[0].ID != id || p.Resources[0].Action != "retire" || j.Owners[id] != p.Resources[0].UID {
		return createOpenBaoACKBlock("normal-tail-retired-custody-receipt")
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil || actual != nil {
		return createOpenBaoACKBlock("normal-tail-retired-custody-present")
	}
	return e.Check(ctx)
}

// The native writer uses json.Marshal(receipt) plus one newline. Reconstruct
// only that exact form and require the previously admitted raw tombstone hash.
func managedLateNormalTailArchivedTombstone(store secretStore, r *createServiceSessionRecovery) (managedLateCustodyTombstone, error) {
	var zero managedLateCustodyTombstone
	if r == nil || r.normalTail == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.PostNativeCleanup == nil {
		return zero, createOpenBaoACKBlock("normal-tail-tombstone-source")
	}
	var archived struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone json.RawMessage                        `json:"custody_tombstone"`
		UIDs      map[string]string                      `json:"credential_uids"`
	}
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", r.ManagedLateRemaining.ParentSHA, "bootstrap/service/finalized.json"), &archived) != nil || decodeEnvironmentJSON(archived.Tombstone, &zero) != nil || deploymentplan.Digest(archived.UIDs) != deploymentplan.Digest(r.Ceremony.CredentialUIDs) || !managedLateFinalizationEqual(archived.Native, r.ManagedLateRemaining.PostNativeCleanup.Native) || !managedLateFinalizationEqual(zero.Finalization, archived.Native) {
		return zero, createOpenBaoACKBlock("normal-tail-archived-tombstone")
	}
	raw, err := json.Marshal(zero)
	if err != nil || environmentPreflightBytesSHA256(append(raw, '\n')) != r.ManagedLateRemaining.PostNativeCleanup.TombstoneSHA {
		return zero, createOpenBaoACKBlock("normal-tail-original-tombstone-bytes")
	}
	return zero, nil
}
func verifyManagedLateNormalTailFinalizedCustody(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, seed createManagedLateSeedObservation, state managedSessionState, subjects []string, native environmentCreateBootstrapFinalization) error {
	r := runtime.ServiceSessionRecovery
	if err := verifyManagedLateNormalTailRetiredCustody(ctx, runtime); err != nil {
		return err
	}
	tombstone, err := managedLateNormalTailArchivedTombstone(runtime.Execution.Store, r)
	if err != nil {
		return err
	}
	if !managedLateFinalizedSessionCurrent(state, seed, r.ManagedLateSession, subjects, native, tombstone, time.Now().UTC()) || !managedLateFinalizationEqual(native, r.ManagedLateRemaining.PostNativeCleanup.Native) {
		return createOpenBaoACKBlock("normal-tail-current-finalized-session")
	}
	return runtime.Execution.Check(ctx)
}

func verifyManagedLateNormalTailAcceptedOwners(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.normalTail == nil || e == nil {
		return createOpenBaoACKBlock("normal-tail-current-owner-source")
	}
	if err := verifyManagedLateNormalTailRuntime(ctx, cfg, e.Store, runtime); err != nil {
		return err
	}
	children, _, _, err := managedLateCurrentChildren(e.Store, r.completedTail.view, r.ManagedLateSession)
	if err != nil {
		return err
	}
	owners, ready, err := managedLateCurrentOwners(r, r.ManagedLateSession, children)
	if err != nil {
		return err
	}
	if err := projectManagedLateNormalTailOwners(e.Store, e, r, owners, ready); err != nil {
		return err
	}
	objects, err := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return createOpenBaoACKBlock("normal-tail-current-owner-inventory")
	}
	if _, err := verifyManagedLateCurrentOwnerInventory(ctx, e.Store, r.ManagedLateSession.AcceptedCore.Inputs, owners, ready, objects); err != nil {
		return err
	}
	return verifyManagedLateNormalTailRetiredCustody(ctx, runtime)
}

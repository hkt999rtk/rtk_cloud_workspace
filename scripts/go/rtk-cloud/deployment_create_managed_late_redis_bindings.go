package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The accepted pre-pause allocation adds one owner to fresh inventory. Preserve
// the original bootstrap bindings until the genuine cutover publishes its own
// bindings; no other lifecycle reference or owner may change on this retry.
func readManagedLateRedisPrePauseBindings(ctx context.Context, store secretStore, e *environmentCreateExecution, observed environmentIdentityBindings) (environmentIdentityBindings, error) {
	bad := func(stage string) (environmentIdentityBindings, error) {
		return environmentIdentityBindings{}, managedLateRedisDurableError("pre-pause-bindings-" + stage)
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil || observed.Environment != store.Environment || observed.Stack != e.Journal.Stack {
		return bad("source")
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return environmentIdentityBindings{}, err
		}
	}
	resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	volumeRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, managedLateRedisDurableBase(e.Journal.PlanSHA256), "volume.json"), true)
	var volume environmentCreateVolumeOwner
	if err != nil || environmentPreflightBytesSHA256(volumeRaw) != resume.VolumeRawSHA || decodeEnvironmentJSON(volumeRaw, &volume) != nil {
		return bad("volume")
	}
	claimID := deploymentplan.ID(saved.Descriptor.Claim)
	if claimID != "PersistentVolumeClaim/"+e.Journal.Stack+"-platform/redis-data" || observed.Owners[claimID] != volume.PVCUID {
		return bad("owner")
	}
	relative := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/bindings.json")
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return bad("record")
	}
	originalRaw, err := readBoundedEnvironmentFile(path, true)
	var original environmentIdentityBindings
	if err != nil || len(originalRaw) > 256<<10 || decodeEnvironmentJSON(originalRaw, &original) != nil {
		return bad("record")
	}
	comparison := observed
	comparison.Owners = maps.Clone(observed.Owners)
	delete(comparison.Owners, claimID)
	comparisonRaw, err := json.Marshal(comparison)
	if err != nil || !bytes.Equal(originalRaw, comparisonRaw) {
		return bad("drift")
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return environmentIdentityBindings{}, err
		}
	}
	if err := e.Check(ctx); err != nil {
		return environmentIdentityBindings{}, err
	}
	return original, nil
}

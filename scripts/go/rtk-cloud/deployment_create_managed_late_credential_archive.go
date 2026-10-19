package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func writeEnvironmentCreateExpiredCredentialArchive(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, old environmentCreateServiceCeremony, namespace string, secret deploymentplan.Object) (string, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil {
		return "", managedHistoricalSeedBlock()
	}
	relative := filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "expired-credential-copies", old.BootstrapSessionID, namespace+".json")
	// Ordinary producers and a genuinely absent file keep their original
	// exclusive-create behavior. Only admitted post-native cleanup may reuse.
	if r.ManagedLateRemaining == nil || r.ManagedLateRemaining.PostNativeCleanup == nil {
		return writeEnvironmentCreateBootstrapJSON(store, relative, secret)
	}
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) {
		return "", managedHistoricalSeedBlock()
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		// This new immutable archive is an input of the next historical
		// verification. End the old read phase before publishing it; the
		// subsequent copy guard must rederive under the enlarged manifest.
		if runtime.Execution == nil {
			return "", managedHistoricalSeedBlock()
		}
		if err := runtime.Execution.Check(ctx); err != nil {
			return "", err
		}
		if !managedLateExpiredCredentialArchiveSourceMatches(r, old, namespace, secret, cfg.Values["CLOUD_STACK_NAME"]) {
			return "", managedHistoricalSeedBlock()
		}
		if managedLateSourceReuseActive(runtime.Execution) {
			if err := checkManagedLateSourceReuse(runtime.Execution); err != nil {
				return "", err
			}
		}
		invalidateManagedLateSourceReuse(runtime.Execution)
		return writeEnvironmentCreateBootstrapJSON(store, relative, secret)
	} else if err != nil {
		return "", managedHistoricalSeedBlock()
	}
	if err := verifyManagedLatePostNativeCleanupRuntime(ctx, cfg, store, runtime); err != nil {
		return "", err
	}
	if !managedLateExpiredCredentialArchiveSourceMatches(r, old, namespace, secret, cfg.Values["CLOUD_STACK_NAME"]) {
		return "", managedHistoricalSeedBlock()
	}
	return reuseManagedLateExpiredCredentialArchive(store, relative, secret)
}

func managedLateExpiredCredentialArchiveSourceMatches(r *createServiceSessionRecovery, old environmentCreateServiceCeremony, namespace string, secret deploymentplan.Object, stack string) bool {
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.PostNativeCleanup == nil || r.ManagedLateRemaining.StaleSeedsSHA != deploymentplan.Digest(r.StaleSeeds) || !slices.Contains(r.Intent.Namespaces, namespace) || !slices.Contains(old.BootstrapCredentialNamespaces, namespace) || old.BootstrapCredentialSecret != "service-bootstrap-"+old.BootstrapSessionID || !environmentBootstrapOwnerUUID.MatchString(old.CredentialUIDs[namespace]) {
		return false
	}
	meta := deploymentplan.Map(secret["metadata"])
	labels := deploymentplan.Map(meta["labels"])
	if deploymentplan.ID(secret) != "Secret/"+namespace+"/"+old.BootstrapCredentialSecret || meta["uid"] != old.CredentialUIDs[namespace] || deploymentplan.String(meta["resourceVersion"]) == "" || labels["rtk.cloud/bootstrap-session"] != old.BootstrapSessionID || labels["rtk.realtek.com/stack"] != stack {
		return false
	}
	count := 0
	for _, observed := range r.StaleSeeds {
		if observed.Cleanup.SessionID == old.BootstrapSessionID {
			if deploymentplan.Digest(observed.UIDs) != deploymentplan.Digest(old.CredentialUIDs) {
				return false
			}
			count++
		}
	}
	return count == 1
}

// The caller authenticates the admitted phase and live original UID first.
// Existing archives are never rewritten or accepted by semantic normalization.
func reuseManagedLateExpiredCredentialArchive(store secretStore, relative string, secret deploymentplan.Object) (string, error) {
	bad := func() (string, error) {
		return "", environmentCreateBootstrapBlocker("recorded expired credential archive differs from its exact admitted original copy")
	}
	raw, err := json.Marshal(secret)
	path, pathErr := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || len(raw) > 256<<10 || pathErr != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return bad()
	}
	prior, err := readBoundedEnvironmentFile(path, true)
	if err != nil || !bytes.Equal(prior, raw) {
		return bad()
	}
	return "/store/" + filepath.ToSlash(relative), nil
}

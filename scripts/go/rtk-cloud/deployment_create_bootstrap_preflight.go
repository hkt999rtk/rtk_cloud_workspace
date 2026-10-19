package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Qualify prerequisites that foundation construction would otherwise discover
// only after provider allocations. Historical teardown archives are evidence;
// only the selected active identity and bootstrap paths are inspected here.
func qualifyEnvironmentCreateBootstrapLocalState(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, err := newSecretStore("", cfg.Environment)
	if err != nil || store.Environment != cfg.Environment || store.Root != canonical.Root || store.ConfigRoot != canonical.ConfigRoot {
		return environmentCreateBlocker("deployment.create.inputs", "fresh bootstrap requires the exact canonical selected environment store")
	}
	values := environmentCreateProviderValues(cfg)
	if (cfg.Environment != "dev" && cfg.Environment != "staging") || values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || values["OPENBAO_STORAGE_BACKEND"] != "file" || values["OPENBAO_EFFECTIVE_REPLICAS"] != "1" {
		return environmentCreateBlocker("deployment.create.openbao", "initial file OpenBao requires the explicit non-production single-writer profile before provider allocation")
	}
	for _, relative := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return environmentCreateBlocker("deployment.create.openbao", "canonical original OpenBao initialization or operator material cannot be inspected")
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return environmentCreateBlocker("deployment.create.openbao", "original OpenBao initialization or operator material exists or cannot be inspected; qualify its original restore before fresh creation")
		}
	}
	retired, err := privateDeploymentStatePath(cfg.Environment, "deployment/account-bootstrap-retired.json")
	if err != nil {
		return environmentCreateBlocker("deployment.bootstrap.operator", "canonical Account Manager bootstrap lifecycle cannot be inspected")
	}
	if _, err := os.Lstat(retired); !errors.Is(err, os.ErrNotExist) {
		return environmentCreateBlocker("deployment.bootstrap.operator", "original Account Manager bootstrap is retired or its lifecycle cannot be inspected; fresh creation cannot reactivate its seed")
	}
	password := strings.TrimSpace(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"])
	if password == "" {
		return environmentCreateBlocker("deployment.bootstrap.operator", "an explicit initial platform administrator password is required before provider allocation")
	}
	path, err := privateDeploymentStatePath(cfg.Environment, "runtime/platform-admin")
	if err != nil {
		return environmentCreateBlocker("deployment.bootstrap.operator", "canonical original platform administrator credential cannot be inspected")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		inventory, err := privateDeploymentStatePath(cfg.Environment, "inventory.json")
		if err != nil {
			return environmentCreateBlocker("deployment.bootstrap.operator", "canonical runtime inventory cannot be inspected")
		}
		if _, err := os.Lstat(inventory); !errors.Is(err, os.ErrNotExist) {
			return environmentCreateBlocker("deployment.bootstrap.operator", "initialized runtime inventory lacks its original platform administrator credential; fresh creation cannot regenerate it")
		}
		return ctx.Err()
	} else if err != nil {
		return environmentCreateBlocker("deployment.bootstrap.operator", "canonical original platform administrator credential cannot be inspected")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || strings.TrimSpace(string(raw)) != password {
		return environmentCreateBlocker("deployment.bootstrap.operator", "canonical platform administrator credential is unreadable or differs from the explicit initial operator password")
	}
	if err := qualifyEnvironmentCreateRecordedRuntime(store); err != nil {
		return err
	}
	return ctx.Err()
}

// Compare only runtime entries already recorded by the original inventory.
// New catalog additions are provisioned by foundation; optional manually held
// credentials and future PKI/bootstrap material are qualified by their owners.
func qualifyEnvironmentCreateRecordedRuntime(store secretStore) error {
	path, err := privateDeploymentStatePath(store.Environment, "inventory.json")
	if err != nil {
		return environmentCreateBlocker("deployment.create.inputs", "canonical original runtime inventory cannot be inspected")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return environmentCreateBlocker("deployment.create.inputs", "canonical original runtime inventory cannot be inspected")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var inventory secretInventory
	if err != nil || json.Unmarshal(raw, &inventory) != nil || inventory.SchemaVersion != secretInventorySchemaVersion || inventory.Environment != store.Environment {
		return environmentCreateBlocker("deployment.create.inputs", "original runtime inventory is unreadable or has a foreign schema or environment")
	}
	catalog := map[string]secretCatalogEntry{}
	for _, entry := range rtkSecretCatalog() {
		catalog[entry.ID] = entry
	}
	seen := map[string]bool{}
	for _, recorded := range inventory.Entries {
		if seen[recorded.ID] {
			return environmentCreateBlocker("deployment.create.inputs", "original runtime inventory contains duplicate entries")
		}
		seen[recorded.ID] = true
		entry, known := catalog[recorded.ID]
		if !known {
			continue
		}
		if recorded.Path != filepath.ToSlash(filepath.Join("runtime", entry.ID)) || (recorded.Category != "" && recorded.Category != "runtime") || (recorded.Optional && !entry.Optional) || (recorded.ExplicitProvision && !entry.ExplicitProvision) {
			return environmentCreateBlocker("deployment.create.inputs", "original runtime inventory entry differs from its canonical maintained owner")
		}
		value, err := store.readRuntime(entry.ID)
		optional := entry.Optional || entry.ExplicitProvision || !supportSecretRequired(store, entry.ID)
		if optional && (err == nil || errors.Is(err, os.ErrNotExist)) {
			continue
		}
		if err != nil || value == "" {
			return environmentCreateBlocker("deployment.create.inputs", "original recorded runtime credential "+entry.ID+" is missing or unreadable; fresh creation cannot regenerate it")
		}
	}
	return nil
}

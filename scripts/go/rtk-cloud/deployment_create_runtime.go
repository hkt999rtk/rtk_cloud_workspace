package main

import (
	"errors"
	"io"
	"os"
	"strings"
)

// Inspect the original catalog before layout creation writes its inventory.
// An initialized store can add newly cataloged credentials, but a recorded
// credential missing from that store is never regenerated during fresh create.
func initializeEnvironmentCreateRuntime(store secretStore, operator map[string]string) error {
	password := strings.TrimSpace(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"])
	if password == "" {
		return environmentCreateBlocker("deployment.bootstrap.operator", "explicit initial operator credential is required before runtime initialization")
	}
	_, inventoryErr := store.read("inventory.json")
	if inventoryErr != nil && !errors.Is(inventoryErr, os.ErrNotExist) {
		return inventoryErr
	}
	if inventoryErr == nil {
		// Preserve the recorded catalog while finding additions. ensureLayout
		// would otherwise replace that catalog before this comparison.
		return ensureSecretStoreCatalogAdditions(io.Discard, store)
	}
	existing, readErr := store.readRuntime("platform-admin")
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) || readErr == nil && existing != password {
		return environmentCreateBlocker("deployment.bootstrap.operator", "retained platform administrator credential differs from the explicit initial operator")
	}
	if err := store.ensureLayout(); err != nil {
		return err
	}
	if errors.Is(readErr, os.ErrNotExist) {
		if err := store.write("runtime/platform-admin", []byte(password+"\n"), false); err != nil {
			return err
		}
	}
	return ensureMissingRuntimeSecrets(io.Discard, store)
}

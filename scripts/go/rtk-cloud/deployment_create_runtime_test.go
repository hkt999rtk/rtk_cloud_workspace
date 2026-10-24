package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFreshRuntimeInitializationSeedsExplicitOperatorBeforeCatalogCreation(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, store, operator := createBootstrapLocalFixture(t, environment)
			if _, err := os.Lstat(filepath.Join(store.Root, "inventory.json")); !os.IsNotExist(err) {
				t.Fatal("fresh fixture already recorded a runtime catalog")
			}
			if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
				t.Fatal(err)
			}
			password, err := store.readRuntime("platform-admin")
			if err != nil || password != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				t.Fatal("initial runtime generated a different operator credential")
			}
			for _, id := range []string{"postgres", "video-auth", "factory-enroll-auth"} {
				if value, err := store.readRuntime(id); err != nil || value == "" {
					t.Fatal("fresh initial runtime catalog values were skipped", id)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
				t.Fatal(err)
			}
			if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("initialized runtime values changed during repeated preparation")
			}
		})
	}
}

func TestFreshRuntimeInitializationPreservesRecordedMissingOriginals(t *testing.T) {
	_, store, operator := createBootstrapLocalFixture(t, "dev")
	if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.RuntimeDir(), "postgres")); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, store.Root)
	if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(store.RuntimeDir(), "postgres")); !os.IsNotExist(err) || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
		t.Fatal("fresh bootstrap regenerated a deleted original runtime credential")
	}
	if verifySecretStoreContents(store) == nil {
		t.Fatal("missing original credential unexpectedly passed runtime verification")
	}
}

func TestFreshRuntimeInitializationComparesOriginalCatalogBeforeWritingNewInventory(t *testing.T) {
	_, store, operator := createBootstrapLocalFixture(t, "dev")
	if err := store.write("runtime/platform-admin", []byte(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]), false); err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: "dev", Entries: []secretInventoryEntry{{ID: "platform-admin", Path: "runtime/platform-admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("inventory.json", old, false); err != nil {
		t.Fatal(err)
	}
	if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
		t.Fatal(err)
	}
	if value, err := store.readRuntime("postgres"); err != nil || value == "" {
		t.Fatal("new catalog entry was erased from the addition comparison")
	}
	if value, err := store.readRuntime("platform-admin"); err != nil || value != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
		t.Fatal("original operator credential changed during catalog addition")
	}
}

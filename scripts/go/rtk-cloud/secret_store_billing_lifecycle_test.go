package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBillingLifecycleCatalogOptionalExplicitProvisionAndNoPrivateCustody(t *testing.T) {
	for _, entry := range billingLifecycleSecretCatalog() {
		if !entry.Optional || !entry.ExplicitProvision || entry.Rotation != "explicit-manual" || len(entry.K8SBinding) == 0 || strings.Contains(entry.ID, "private") || strings.Contains(entry.ID, "identity") || strings.Contains(entry.ID, "signing-key") {
			t.Fatal("unsafe custody contract", entry)
		}
		for _, binding := range entry.K8SBinding {
			if binding.Secret == "billing-runtime" || binding.Secret == "video-cloud-workers-runtime" {
				t.Fatal("lifecycle authority is in shared worker secret", binding)
			}
		}
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	var output bytes.Buffer
	if err := ensureMissingRuntimeSecrets(&output, store); err != nil {
		t.Fatal(err)
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		if _, err := store.readRuntime(entry.ID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("ensure auto-provisioned sensitive lifecycle credential", entry.ID, err)
		}
		if supportSecretRequired(store, entry.ID) {
			t.Fatal("disabled lifecycle secret became mandatory", entry.ID)
		}
	}
	if err := verifySecretStoreContents(store); err != nil {
		t.Fatal("disabled store no longer verifies", err)
	}
	raw, err := store.read("inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory secretInventory
	if json.Unmarshal([]byte(raw), &inventory) != nil {
		t.Fatal("inventory decode")
	}
	for _, entry := range inventory.Entries {
		if billingLifecycleSecretID(entry.ID) && (!entry.Optional || !entry.ExplicitProvision) {
			t.Fatal("inventory lost optional provisioning metadata")
		}
	}
}

func TestBillingLifecycleCatalogUpgradeNeverGeneratesNewAuthorityOrUploader(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	var inventory secretInventory
	for _, entry := range rtkSecretCatalog() {
		if !entry.ExplicitProvision {
			if err := store.write(filepath.Join("runtime", entry.ID), []byte("fixture-existing-"+entry.ID+"\n"), true); err != nil {
				t.Fatal(err)
			}
			inventory.Entries = append(inventory.Entries, secretInventoryEntry{ID: entry.ID})
		}
	}
	inventory.SchemaVersion = 1
	inventory.Environment = "dev"
	raw, _ := json.Marshal(inventory)
	if err := store.write("inventory.json", raw, true); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := ensureSecretStoreCatalogAdditions(&output, store); err != nil {
		t.Fatal(err)
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		if _, err := store.readRuntime(entry.ID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("upgrade auto-provisioned lifecycle authority", entry.ID, err)
		}
	}
	if err := verifySecretStoreContents(store); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("upgrade reported credential provisioning", output.String())
	}
}

func TestBillingLifecycleSecretRequirementsTrackOperatorEnabledFeatures(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write(filepath.Join("operator", "env", "LKE_BILLING_BACKUP_ENABLED"), []byte("true\n"), true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"billing-backup-access-key-id", "billing-backup-secret-access-key", "cloud-logger-lifecycle-token", "cloud-logger-lifecycle-read-token"} {
		if !supportSecretRequired(store, id) {
			t.Fatal("enabled backup ignored required credential", id)
		}
	}
	if supportSecretRequired(store, "billing-raw-retention-financial") {
		t.Fatal("backup-only granted financial authority")
	}
	var output bytes.Buffer
	if err := ensureMissingRuntimeSecrets(&output, store); err == nil || !strings.Contains(err.Error(), "explicitly provisioned") {
		t.Fatal("ensure claimed enabled store was complete without manual credentials", err)
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		if _, err := store.readRuntime(entry.ID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("ensure generated enabled lifecycle authority", entry.ID)
		}
	}
	bindings := catalogK8SBindings("cloud-logger-lifecycle-read-token")
	for _, binding := range bindings {
		want := binding.Secret == "cloud-logger-runtime"
		if got := billingLifecycleBindingRequired(store, "cloud-logger-lifecycle-read-token", binding); got != want {
			t.Fatal("unused Billing binding became mandatory", binding)
		}
	}
	if err := store.write(filepath.Join("operator", "env", "LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED"), []byte("true\n"), true); err != nil {
		t.Fatal(err)
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		if !supportSecretRequired(store, entry.ID) {
			t.Fatal("authority ignored required secret", entry.ID)
		}
	}
}

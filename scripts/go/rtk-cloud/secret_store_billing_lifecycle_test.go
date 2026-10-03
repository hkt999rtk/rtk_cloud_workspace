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

func billingLifecycleAdapterStore(t *testing.T) (secretStore, string) {
	t.Helper()
	t.Setenv("RTK_CLOUD_WORKSPACE", "")
	defaults := readTestFile(t, filepath.Join(mustWorkspaceRoot(t), "cloud_deploy", "adapters", "lke", "defaults.env"))
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_deploy", "adapters", "lke", "defaults.env"), defaults)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	for key := range billingDeploymentDefaults() {
		t.Setenv(key, "")
	}
	return makeIsolatedTestSecretStore(t, "dev"), workspace
}

func TestBillingLifecycleAdapterOverrideRequiresCredentials(t *testing.T) {
	for _, tc := range []struct{ flag, secret, binding string }{
		{"LKE_BILLING_BACKUP_ENABLED", "cloud-logger-lifecycle-token", "cloud-logger-runtime"},
		{"LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED", "cloud-logger-lifecycle-read-token", "billing-raw-retention-runtime"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			store, workspace := billingLifecycleAdapterStore(t)
			var output bytes.Buffer
			if err := ensureMissingRuntimeSecrets(&output, store); err != nil {
				t.Fatal(err)
			}
			if err := verifySecretStoreContents(store); err != nil {
				t.Fatalf("disabled adapter defaults: %v", err)
			}
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "adapter.env"), tc.flag+"=true\n")
			if !billingLifecycleSecretRequired(store, tc.secret) || !billingLifecycleBindingRequired(store, tc.secret, secretK8SBinding{Secret: tc.binding}) {
				t.Fatal("adapter override did not require the lifecycle secret and binding")
			}
			if err := verifySecretStoreContents(store); err == nil || !strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("enabled adapter passed without dedicated credentials: %v", err)
			}
			if err := store.write(filepath.Join("operator", "env", tc.flag), []byte("false\n"), true); err != nil {
				t.Fatal(err)
			}
			if err := verifySecretStoreContents(store); err != nil {
				t.Fatalf("canonical operator override did not disable the stage: %v", err)
			}
			t.Setenv(tc.flag, "true")
			if !billingLifecycleSecretRequired(store, tc.secret) {
				t.Fatal("explicit process override lost precedence")
			}
			if err := store.write(filepath.Join("operator", "env", tc.flag), []byte("true\n"), true); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.flag, "false")
			if billingLifecycleSecretRequired(store, tc.secret) {
				t.Fatal("explicit process false did not override canonical operator true")
			}
		})
	}
}

func TestBillingLifecycleAdapterReadErrorsFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"missing defaults", "cloud_deploy/adapters/lke/defaults.env", ""},
		{"malformed defaults", "cloud_deploy/adapters/lke/defaults.env", "invalid\n"},
		{"malformed override", "cloud_env/dev/overrides/adapter.env", "invalid\n"},
		{"unknown override", "cloud_env/dev/overrides/adapter.env", "LKE_BILLING_BACKUP_ENABELD=true\n"},
		{"invalid flag", "cloud_env/dev/overrides/adapter.env", "LKE_BILLING_BACKUP_ENABLED=yes\n"},
		{"malformed selection", "cloud_env/dev/deployment.env", "invalid\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, workspace := billingLifecycleAdapterStore(t)
			path := filepath.Join(workspace, tc.path)
			if tc.body == "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, path, tc.body)
			}
			if _, err := billingLifecycleStoreFlags(store); err == nil {
				t.Fatal("invalid intent was treated as disabled")
			}
			if !billingLifecycleSecretRequired(store, "billing-backup-access-key-id") || !billingLifecycleBindingRequired(store, "billing-backup-access-key-id", secretK8SBinding{Secret: "cloud-logger-runtime"}) {
				t.Fatal("invalid intent bypassed required secret or binding")
			}
		})
	}
	t.Run("unreadable operator entry", func(t *testing.T) {
		store, _ := billingLifecycleAdapterStore(t)
		path := filepath.Join(store.Root, "operator", "env", "LKE_BILLING_BACKUP_ENABLED")
		if err := os.WriteFile(path, []byte("true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := billingLifecycleStoreFlags(store); err == nil {
			t.Fatal("unsafe operator entry was treated as disabled")
		}
		if !billingLifecycleSecretRequired(store, "billing-backup-access-key-id") {
			t.Fatal("unsafe operator entry bypassed a required secret")
		}
	})
}

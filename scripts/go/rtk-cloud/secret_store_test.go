package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Package tests use isolated fixture paths. Production execution never sets
	// this test-only marker and therefore uses the canonical SecretStore.
	_ = os.Setenv("RTK_CLOUD_TEST_MODE", "1")
	_ = os.Setenv("LKE_RUNTIME_SECRET_SEED", "rtk-cloud-go-test")
	os.Exit(m.Run())
}

func TestSecretStoreInitializesEnvironmentIsolatedSecureLayout(t *testing.T) {
	root := t.TempDir()
	store, err := newSecretStore(root, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"operator/env", "runtime", "kube", "pki/certissuer", "openbao", "test/databases"} {
		info, err := os.Stat(filepath.Join(store.Root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o", relative, info.Mode().Perm())
		}
	}
	info, err := os.Stat(filepath.Join(store.Root, "inventory.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("inventory mode = %v, err = %v", info.Mode().Perm(), err)
	}
	prod, err := newSecretStore(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Root == store.Root {
		t.Fatal("environment stores are not isolated")
	}
}

func TestSecretStoreRejectsTraversalSymlinkAndInsecureFile(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.safePath("../prod/runtime/postgres"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	path := filepath.Join(store.RuntimeDir(), "postgres")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readRuntime("postgres"); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("insecure secret read error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store.Root, "inventory.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readRuntime("postgres"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink secret read error = %v", err)
	}
}

func TestSecretInventoryNeverContainsValues(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.write("runtime/postgres", []byte("must-not-leak\n"), true); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printSecretInventory(&out, store); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "must-not-leak") {
		t.Fatal("inventory leaked a secret value")
	}
}

func TestSecretStoreVerifyFailsClosedForEmptyProd(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	err = verifySecretStoreContents(store)
	if err == nil || !strings.Contains(err.Error(), "required runtime secrets are missing") {
		t.Fatalf("verify error = %v", err)
	}
}

func TestSecretMigrationPlanPrintsNamesNotValues(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("TOKEN=must-not-leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore(filepath.Join(home, ".config", "rtk_cloud"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := planSecretMigration(&out, store, workspace); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "must-not-leak") {
		t.Fatal("migration plan leaked a value")
	}
}

func TestTrackedSecretCatalogMatchesRuntimeContract(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(workspace, "cloud_env", "secret-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tracked struct {
		RuntimeSecretIDs []string `json:"runtime_secret_ids"`
	}
	if err := json.Unmarshal(payload, &tracked); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, entry := range rtkSecretCatalog() {
		want[entry.ID] = true
	}
	for _, id := range tracked.RuntimeSecretIDs {
		if !want[id] {
			t.Fatalf("tracked catalog contains unknown runtime secret %q", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("tracked catalog is missing runtime secrets: %v", want)
	}
}

func TestSecretMigrationCutsOverAtomicallyAndRemovesLegacySources(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyRuntime := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	legacySecrets := filepath.Join(legacyRuntime, "state", "secrets")
	if err := os.MkdirAll(legacySecrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := os.WriteFile(filepath.Join(legacySecrets, entry.ID), []byte("fixture-"+entry.ID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("UNRELATED_PERSONAL_TOKEN=personal-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore(filepath.Join(home, ".config", "rtk_cloud"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSecrets(store, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf("legacy ~/.env still exists: %v", err)
	}
	if _, err := os.Stat(legacySecrets); !os.IsNotExist(err) {
		t.Fatalf("legacy runtime secrets still exist: %v", err)
	}
	if value, err := store.read("operator/env/UNRELATED_PERSONAL_TOKEN"); err != nil || value != "personal-fixture" {
		t.Fatalf("personal token migration value=%q err=%v", value, err)
	}
	if err := verifySecretStoreContents(store); err != nil {
		t.Fatal(err)
	}
	backups, err := os.ReadDir(filepath.Join(store.Root, "migration-backup"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup entries=%d err=%v", len(backups), err)
	}
}

func TestSecretMigrationFailureRollsBackAndRerunDoesNotOverwrite(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyEnv := filepath.Join(home, ".env")
	if err := os.WriteFile(legacyEnv, []byte("ONLY_ONE_VALUE=fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore(filepath.Join(home, ".config", "rtk_cloud"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSecrets(store, workspace); err == nil {
		t.Fatal("incomplete migration unexpectedly succeeded")
	}
	if _, err := os.Stat(store.Root); !os.IsNotExist(err) {
		t.Fatalf("failed migration left destination behind: %v", err)
	}
	if _, err := os.Stat(legacyEnv); err != nil {
		t.Fatalf("failed migration removed its source: %v", err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := migrateSecrets(store, workspace); err == nil || !strings.Contains(err.Error(), "refuses to overwrite") {
		t.Fatalf("rerun error = %v", err)
	}
}

func TestSecretMigrationPrefersLiveK8SValueAtCutover(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyRuntime := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	legacySecrets := filepath.Join(legacyRuntime, "state", "secrets")
	if err := os.MkdirAll(legacySecrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := os.WriteFile(filepath.Join(legacySecrets, entry.ID), []byte("local-"+entry.ID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kubeconfig := filepath.Join(legacyRuntime, "state", "kubeconfig.yaml")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *postgresql-runtime*) printf '%s\n' '{"data":{"POSTGRES_PASSWORD":"bGl2ZS1wb3N0Z3Jlcw=="}}' ;;
  *) exit 1 ;;
esac
`)
	if err := os.Chmod(kubectl, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	store, err := newSecretStore(filepath.Join(home, ".config", "rtk_cloud"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSecrets(store, workspace); err != nil {
		t.Fatal(err)
	}
	value, err := store.readRuntime("postgres")
	if err != nil || value != "live-postgres" {
		t.Fatalf("postgres cutover source mismatch, err=%v", err)
	}
}

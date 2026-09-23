package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeIsolatedTestSecretStore(t *testing.T, environment string) secretStore {
	t.Helper()
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	return store
}

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

func TestCredentialEnvironmentPreservesExplicitImageWhenOperatorBundleOmitsIt(t *testing.T) {
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/video-cloud:new")
	restore := installAllCredentialEnvironment(map[string]string{"LINODE_TOKEN": "fixture-token"})
	t.Cleanup(restore)
	if got := os.Getenv("LKE_VIDEO_CLOUD_IMAGE"); got != "registry.example.test/video-cloud:new" {
		t.Fatalf("explicit image override = %q, want process value", got)
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
		if entry.ID == "fleet-read-token" {
			continue
		}
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
	if value, err := store.readRuntime("fleet-read-token"); err != nil || value == "" {
		t.Fatalf("migration did not seed newly cataloged Fleet credential: value=%q err=%v", value, err)
	}
	if value, err := store.readRuntime("postgres"); err != nil || value != "fixture-postgres" {
		t.Fatalf("migration changed imported credential: value=%q err=%v", value, err)
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

func TestSecretStoreCommandsAndProvisionIntegration(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	workspace := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"init", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace}); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"init", "--environment", "qa", "--config-root", configRoot}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("fixture\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"LINODE_TOKEN": "linode-fixture", "GODADDY_KEY": "godaddy-fixture"} {
		if err := store.write(filepath.Join("operator", "env", key), []byte(value+"\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSecrets([]string{"inventory", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace}); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"plan", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"unknown", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace},
		{"migrate", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace},
		{"init", "--config-root", configRoot, "--workspace", workspace},
		{"init", "--environment", "../prod", "--config-root", configRoot, "--workspace", workspace},
	} {
		if err := runSecrets(args); err == nil {
			t.Fatalf("runSecrets(%v) unexpectedly succeeded", args)
		}
	}

	previousActiveRoot := activeSecretEnvironmentRoot
	previousStateDir := lkeRuntimeSecretStateDir
	defer func() {
		activeSecretEnvironmentRoot = previousActiveRoot
		lkeRuntimeSecretStateDir = previousStateDir
	}()
	t.Setenv("LINODE_TOKEN", "previous")
	configured, restore, err := configureProvisionSecretStore("staging")
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("LINODE_TOKEN"); got != "linode-fixture" {
		t.Fatalf("LINODE_TOKEN = %q", got)
	}
	if got := sensitiveEnvironmentPath(provisionPaths{EnvRoot: "legacy"}, "kube", "kubeconfig.yaml"); got != configured.KubeconfigPath() {
		t.Fatalf("canonical kubeconfig path = %q", got)
	}
	for category, suffix := range map[string]string{
		"certissuer":   filepath.Join("pki", "certissuer", "ca.key"),
		"mqtt-tls":     filepath.Join("pki", "mqtt", "tls.key"),
		"public-https": filepath.Join("pki", "public-https", "tls.key"),
		"openbao":      filepath.Join("openbao", "unseal.json"),
	} {
		if got := sensitiveEnvironmentPath(provisionPaths{}, category, filepath.Base(suffix)); got != filepath.Join(configured.Root, suffix) {
			t.Fatalf("%s path = %q", category, got)
		}
	}
	restore()
	if got := os.Getenv("LINODE_TOKEN"); got != "previous" {
		t.Fatalf("restored LINODE_TOKEN = %q", got)
	}

	activeSecretEnvironmentRoot = ""
	if got := sensitiveEnvironmentPath(provisionPaths{EnvRoot: "legacy"}, "kube", "kubeconfig.yaml"); got != filepath.Join("legacy", "state", "kubeconfig.yaml") {
		t.Fatalf("legacy kubeconfig path = %q", got)
	}
	if got := sensitiveEnvironmentPath(provisionPaths{EnvRoot: "legacy"}, "openbao", "data"); got != filepath.Join("legacy", "state", "openbao", "data") {
		t.Fatalf("legacy OpenBao path = %q", got)
	}
	if _, err := store.runtimePath("../bad"); err == nil {
		t.Fatal("invalid runtime ID was accepted")
	}
	if _, err := store.operatorPath("bad-key"); err == nil {
		t.Fatal("invalid operator key was accepted")
	}
	if _, err := store.operatorPath("VALID_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.safePath(filepath.Join(string(filepath.Separator), "tmp", "secret")); err == nil {
		t.Fatal("absolute secret path was accepted")
	}
	if err := store.write("runtime/postgres", []byte("replacement"), false); err == nil {
		t.Fatal("non-replacing write overwrote a secret")
	}
	if err := runSecrets([]string{"migrate", "--environment", "staging", "--config-root", configRoot, "--workspace", workspace, "--confirm", "video-cloud-staging"}); err == nil || !strings.Contains(err.Error(), "refuses to overwrite") {
		t.Fatalf("confirmed migration error = %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("unsupported sensitive path category did not panic")
			}
		}()
		activeSecretEnvironmentRoot = store.Root
		_ = sensitiveEnvironmentPath(provisionPaths{}, "unsupported", "secret")
	}()
}

func TestConfigureProvisionSecretStoreAddsOnlyNewCatalogCredentials(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	for _, entry := range rtkSecretCatalog() {
		if entry.ID == "fleet-read-token" {
			continue
		}
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("fixture-"+entry.ID+"\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := store.read("inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory secretInventory
	if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
		t.Fatal(err)
	}
	entries := inventory.Entries[:0]
	for _, entry := range inventory.Entries {
		if entry.ID != "fleet-read-token" {
			entries = append(entries, entry)
		}
	}
	inventory.Entries = entries
	payload, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := store.write("inventory.json", payload, true); err != nil {
		t.Fatal(err)
	}

	configured, restore, err := configureProvisionSecretStore("staging")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if value, err := configured.readRuntime("fleet-read-token"); err != nil || value == "" {
		t.Fatalf("new catalog credential value=%q err=%v", value, err)
	}
	if value, err := configured.readRuntime("postgres"); err != nil || value != "fixture-postgres" {
		t.Fatalf("existing credential was changed: value=%q err=%v", value, err)
	}
}

func TestConfigureProvisionSecretStoreDoesNotRepairDeletedRecordedCredential(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	for _, entry := range rtkSecretCatalog() {
		if entry.ID == "fleet-read-token" {
			continue
		}
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("fixture\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := configureProvisionSecretStore("staging"); err == nil || !strings.Contains(err.Error(), "fleet-read-token") {
		t.Fatalf("deleted recorded credential should remain a hard failure, got %v", err)
	}
}

func TestSecretStoreMigrationHelpers(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	fixtureRoot := t.TempDir()
	envFile := filepath.Join(fixtureRoot, "operator.env")
	if err := os.WriteFile(envFile, []byte(strings.Join([]string{
		"# comment", "export FIRST_TOKEN='first'", `SECOND_TOKEN="second"`, "invalid-key=ignored", "MISSING_EQUALS", "",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importEnvFileToStore(store, envFile); err != nil {
		t.Fatal(err)
	}
	values, err := store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	if values["FIRST_TOKEN"] != "first" || values["SECOND_TOKEN"] != "second" {
		t.Fatalf("imported operator values = %#v", values)
	}
	if err := importEnvFileToStore(store, filepath.Join(fixtureRoot, "missing.env")); err != nil {
		t.Fatal(err)
	}

	single := filepath.Join(fixtureRoot, "kubeconfig.yaml")
	if err := os.WriteFile(single, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySensitivePath(store, single, "kube/kubeconfig.yaml"); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(fixtureRoot, "pki")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "ca.key"), []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySensitivePath(store, directory, "pki/certissuer"); err != nil {
		t.Fatal(err)
	}

	devices := filepath.Join(fixtureRoot, "devices")
	if err := os.MkdirAll(devices, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devices, "credential.json"), []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devices, "runtime.log"), []byte("quarantine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTestDeviceMaterial(store, devices); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("test/devices/test_device/credential.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("test/archive/quarantine/devices/test_device/runtime.log"); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(fixtureRoot, "workspace")
	legacy := filepath.Join(fixtureRoot, "legacy")
	for _, path := range []string{
		filepath.Join(workspace, ".artifacts", "runs", "credentials.sqlite"),
		filepath.Join(workspace, ".artifacts", "runs", "public.txt"),
		filepath.Join(legacy, "artifacts", "credential-bundles", "devices.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copySensitiveArtifacts(store, workspace, legacy, "test/archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("test/archive/workspace-artifacts/runs/credentials.sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("test/archive/environment-artifacts/credential-bundles/devices.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "test", "archive", "workspace-artifacts", "runs", "public.txt")); !os.IsNotExist(err) {
		t.Fatalf("non-sensitive artifact was copied: %v", err)
	}
	for path, want := range map[string]bool{"bundle.sqlite": true, "bundle.sqlite.gz": true, "bundle.db": true, "notes.txt": false} {
		if got := isSensitiveArtifactPath(path); got != want {
			t.Fatalf("isSensitiveArtifactPath(%q) = %v", path, got)
		}
	}

	symlink := filepath.Join(fixtureRoot, "secret-link")
	if err := os.Symlink(single, symlink); err != nil {
		t.Fatal(err)
	}
	if err := copySensitivePath(store, symlink, "test/archive/link"); err == nil {
		t.Fatal("sensitive symlink was accepted")
	}
	if err := copySensitivePath(store, filepath.Join(fixtureRoot, "missing"), "test/archive/missing"); err != nil {
		t.Fatal(err)
	}
	if err := copyTestDeviceMaterial(store, filepath.Join(fixtureRoot, "missing-devices")); err != nil {
		t.Fatal(err)
	}
	symlinkTree := filepath.Join(fixtureRoot, "symlink-tree")
	if err := os.MkdirAll(symlinkTree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(single, filepath.Join(symlinkTree, "linked-secret")); err != nil {
		t.Fatal(err)
	}
	if err := copySensitivePath(store, symlinkTree, "test/archive/symlink-tree"); err == nil {
		t.Fatal("nested sensitive symlink was accepted")
	}
	if err := copyTestDeviceMaterial(store, symlinkTree); err == nil {
		t.Fatal("nested test-device symlink was accepted")
	}
	symlinkWorkspace := filepath.Join(fixtureRoot, "symlink-workspace")
	symlinkArtifactRoot := filepath.Join(symlinkWorkspace, ".artifacts")
	if err := os.MkdirAll(symlinkArtifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(single, filepath.Join(symlinkArtifactRoot, "credentials.db")); err != nil {
		t.Fatal(err)
	}
	if err := copySensitiveArtifacts(store, symlinkWorkspace, filepath.Join(fixtureRoot, "missing-legacy"), "test/archive"); err == nil {
		t.Fatal("sensitive artifact symlink was accepted")
	}
}

func TestSecretStoreVerifiesK8SMirrorBindings(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\ncase \"$*\" in\n")
	secretData := map[string]map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		for _, binding := range entry.K8SBinding {
			if secretData[binding.Secret] == nil {
				secretData[binding.Secret] = map[string]string{}
			}
			secretData[binding.Secret][binding.Key] = "Y2Fub25pY2Fs"
		}
	}
	for secret, data := range secretData {
		payload, err := json.Marshal(map[string]any{"data": data})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&script, "  *%s*) printf '%%s\\n' '%s' ;;\n", secret, payload)
	}
	script.WriteString("  *) exit 1 ;;\nesac\n")
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifySecretStoreK8SBindings(store); err != nil {
		t.Fatal(err)
	}
	if err := store.write("runtime/postgres", []byte("different\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStoreK8SBindings(store); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("K8s mirror mismatch error = %v", err)
	}
}

func TestSecretsEnsureAddsMissingCredentialsWithoutRotation(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	workspace := t.TempDir()
	store, err := newSecretStore(configRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"init", "--environment", "dev", "--config-root", configRoot, "--workspace", workspace}); err != nil {
		t.Fatal(err)
	}
	if err := store.write("runtime/postgres", []byte("keep-existing\n"), false); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"ensure", "--environment", "dev", "--config-root", configRoot, "--workspace", workspace}); err == nil {
		t.Fatal("ensure accepted a missing stack confirmation")
	}
	if err := runSecrets([]string{"ensure", "--environment", "dev", "--config-root", configRoot, "--workspace", workspace, "--confirm", "video-cloud-dev"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.readRuntime("postgres"); err != nil || got != "keep-existing" {
		t.Fatalf("existing credential changed: got=%q err=%v", got, err)
	}
	for _, id := range []string{"fleet-read-token", "job-authorization-token"} {
		if value, err := store.readRuntime(id); err != nil || value == "" {
			t.Fatalf("missing ensured credential %s: err=%v", id, err)
		}
	}
	if err := runSecrets([]string{"ensure", "--environment", "dev", "--config-root", configRoot, "--workspace", workspace, "--confirm", "video-cloud-dev"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.readRuntime("postgres"); err != nil || got != "keep-existing" {
		t.Fatalf("idempotent ensure rotated credential: got=%q err=%v", got, err)
	}
}

func TestSecretStoreVerificationAndErrorBranches(t *testing.T) {
	workspace := t.TempDir()
	store := makeIsolatedTestSecretStore(t, "staging")
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := verifySecretStore(&out, store, workspace); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verified staging") {
		t.Fatalf("verification output = %q", out.String())
	}
	if err := runSecrets([]string{"verify", "--environment", "staging", "--config-root", store.ConfigRoot, "--workspace", workspace}); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets(nil); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets([]string{"init", "--bad-flag"}); err == nil {
		t.Fatal("invalid secrets flag was accepted")
	}
	missingStore, err := newSecretStore(filepath.Join(t.TempDir(), "missing"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStorePermissionsOnly(missingStore); err == nil {
		t.Fatal("uninitialized store passed permission verification")
	}
	if err := printSecretInventory(io.Discard, missingStore); err == nil {
		t.Fatal("missing inventory was accepted")
	}

	legacySecretDir := filepath.Join(workspace, "cloud_env", "staging", "runtime", "state", "secrets")
	if err := os.MkdirAll(legacySecretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStore(io.Discard, store, workspace); err == nil || !strings.Contains(err.Error(), "legacy sensitive path") {
		t.Fatalf("legacy verification error = %v", err)
	}

	badEntry := filepath.Join(store.Root, "operator", "env", "bad-key")
	if err := os.WriteFile(badEntry, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readOperator(); err == nil {
		t.Fatal("invalid operator entry was accepted")
	}
	if err := os.Remove(badEntry); err != nil {
		t.Fatal(err)
	}

	badDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDirectory, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(badDirectory); err == nil {
		t.Fatal("regular file was accepted as a private directory")
	}
	if err := os.Chmod(store.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStorePermissionsOnly(store); err == nil {
		t.Fatal("insecure environment root was accepted")
	}
	if err := ensurePrivateDirectory(store.Root); err != nil {
		t.Fatal(err)
	}
	invalidOperatorDirectory := filepath.Join(store.Root, "operator", "env", "INVALID_DIRECTORY")
	if err := os.Mkdir(invalidOperatorDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readOperator(); err == nil {
		t.Fatal("operator directory entry was accepted")
	}
	if err := os.Remove(invalidOperatorDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(store.Root, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStoreContents(store); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("insecure directory error = %v", err)
	}
	if _, err := store.read("runtime/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing secret read error = %v", err)
	}
	if _, err := store.readRuntime("bad/id"); err == nil {
		t.Fatal("invalid runtime ID was accepted")
	}
	emptyStore, err := newSecretStore(filepath.Join(t.TempDir(), "empty"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(emptyStore.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := emptyStore.readOperator(); err == nil {
		t.Fatal("missing operator directory was accepted")
	}
	if err := store.write("../escaped", []byte("secret"), true); err == nil {
		t.Fatal("escaping write path was accepted")
	}
	if _, err := store.read("../escaped"); err == nil {
		t.Fatal("escaping read path was accepted")
	}
	insecureOperator := filepath.Join(store.Root, "operator", "env", "INSECURE_TOKEN")
	if err := os.WriteFile(insecureOperator, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readOperator(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("insecure operator error = %v", err)
	}
	fileConfigRoot := filepath.Join(t.TempDir(), "config-file")
	if err := os.WriteFile(fileConfigRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileRootStore, err := newSecretStore(fileConfigRoot, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := fileRootStore.ensureLayout(); err == nil {
		t.Fatal("file config root was accepted")
	}
	brokenLayoutStore, err := newSecretStore(filepath.Join(t.TempDir(), "config"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(brokenLayoutStore.ConfigRoot); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(brokenLayoutStore.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenLayoutStore.Root, "operator"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := brokenLayoutStore.ensureLayout(); err == nil {
		t.Fatal("broken layout path was accepted")
	}
}

func TestSecretStoreK8SBindingFailureModes(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := verifySecretStoreK8SBindings(store); err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' 'not-json'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifySecretStoreK8SBindings(store); err == nil || !strings.Contains(err.Error(), "invalid metadata") {
		t.Fatalf("invalid K8s metadata error = %v", err)
	}
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStoreK8SBindings(store); err == nil || !strings.Contains(err.Error(), "binding is missing") {
		t.Fatalf("missing K8s binding error = %v", err)
	}
	if err := os.Remove(filepath.Join(store.RuntimeDir(), "postgres")); err != nil {
		t.Fatal(err)
	}
	if err := verifySecretStoreK8SBindings(store); err == nil || !strings.Contains(err.Error(), "read canonical secret postgres") {
		t.Fatalf("missing canonical secret error = %v", err)
	}
}

func TestSecretStoreK8SBindingsRequireEveryConsumer(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	allKeys := map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		for _, binding := range entry.K8SBinding {
			allKeys[binding.Key] = base64.StdEncoding.EncodeToString([]byte("canonical"))
		}
	}
	withoutAdminJobToken := map[string]string{}
	for key, value := range allKeys {
		if key != "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN" {
			withoutAdminJobToken[key] = value
		}
	}
	good, _ := json.Marshal(map[string]any{"data": allKeys})
	bad, _ := json.Marshal(map[string]any{"data": withoutAdminJobToken})
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *cloud-admin-billing-client*) printf '%%s\\n' '%s' ;;\n  *) printf '%%s\\n' '%s' ;;\nesac\n", bad, good)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifySecretStoreK8SBindings(store); err == nil || !strings.Contains(err.Error(), "job-authorization-token") || !strings.Contains(err.Error(), "cloud-admin-billing-client") {
		t.Fatalf("second consumer with missing binding was accepted: %v", err)
	}
}

func TestSyncMissingSecretBindingsPreservesExistingValues(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	allKeys := map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		for _, binding := range entry.K8SBinding {
			allKeys[binding.Key] = base64.StdEncoding.EncodeToString([]byte("canonical"))
		}
	}
	before := map[string]string{}
	for key, value := range allKeys {
		if key != "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN" {
			before[key] = value
		}
	}
	good, _ := json.Marshal(map[string]any{"data": allKeys})
	missing, _ := json.Marshal(map[string]any{"data": before})
	dir := t.TempDir()
	marker, patchPath := filepath.Join(dir, "patched"), filepath.Join(dir, "patch.json")
	kubectl := filepath.Join(dir, "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'patch secret account-manager-runtime'*) cat > '%s'; touch '%s' ;;\n  *'get secret account-manager-runtime -o json'*) if [ -f '%s' ]; then printf '%%s' '%s'; else printf '%%s' '%s'; fi ;;\n  *'get secret '*'-o json'*) printf '%%s' '%s' ;;\nesac\n", patchPath, marker, marker, good, missing, good)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	var output bytes.Buffer
	if err := syncMissingSecretBindings(&output, store, true); err != nil || !strings.Contains(output.String(), "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN") {
		t.Fatalf("dry-run result = %q, %v", output.String(), err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run patched a Secret", err)
	}
	output.Reset()
	if err := syncMissingSecretBindings(&output, store, false); err != nil {
		t.Fatal(err)
	}
	var patch struct {
		Data map[string]string `json:"data"`
	}
	raw, err := os.ReadFile(patchPath)
	if err != nil || json.Unmarshal(raw, &patch) != nil || len(patch.Data) != 1 || patch.Data["ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"] != allKeys["ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"] {
		t.Fatal("sync did not patch exactly the missing key", err)
	}
	output.Reset()
	if err := syncMissingSecretBindings(&output, store, false); err != nil || output.Len() != 0 {
		t.Fatalf("repeat sync changed Kubernetes Secret: %q, %v", output.String(), err)
	}
	allKeys["ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"] = base64.StdEncoding.EncodeToString([]byte("different"))
	conflict, _ := json.Marshal(map[string]any{"data": allKeys})
	script = fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'patch secret '*) exit 99 ;;\n  *'get secret '*'-o json'*) printf '%%s' '%s' ;;\nesac\n", conflict)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syncMissingSecretBindings(io.Discard, store, false); err == nil || !strings.Contains(err.Error(), "automatic replacement is refused") {
		t.Fatalf("conflicting binding was overwritten: %v", err)
	}
}

func TestSecretStoreK8SRuntimeValidatesCertificatesAndPKIWorkloads(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	valid := rolloutTLSFixture(t, time.Now().Add(30*24*time.Hour), x509.ExtKeyUsageServerAuth)
	certPEM, err := os.ReadFile(valid.cert)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(valid.key)
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "pki-controller-tls"},
		"data":     map[string]string{"tls.crt": base64.StdEncoding.EncodeToString(certPEM), "tls.key": base64.StdEncoding.EncodeToString(keyPEM)},
	}}})
	deployments, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": "video-cloud-api-pki", "generation": 2},
		"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"env": []any{map[string]any{"name": "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"}}}}}}},
		"status":   map[string]any{"observedGeneration": 2, "updatedReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 0},
	}}})
	kubectl := writeSecretRuntimeKubectl(t, secrets, deployments)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifySecretStoreK8SRuntime(store, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestSecretStoreK8SRuntimeReportsCertificateAndIdentityFailuresTogether(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	expired := rolloutTLSFixture(t, time.Now().Add(-time.Hour), x509.ExtKeyUsageServerAuth)
	certPEM, err := os.ReadFile(expired.cert)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(expired.key)
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "pki-controller-tls"},
		"data":     map[string]string{"tls.crt": base64.StdEncoding.EncodeToString(certPEM), "tls.key": base64.StdEncoding.EncodeToString(keyPEM)},
	}}})
	deployments, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": "video-cloud-api-pki", "generation": 3},
		"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"env": []any{map[string]any{"name": "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"}}}}}}},
		"status":   map[string]any{"observedGeneration": 3, "updatedReplicas": 1, "availableReplicas": 0, "unavailableReplicas": 1},
	}}})
	kubectl := writeSecretRuntimeKubectl(t, secrets, deployments)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	err = verifySecretStoreK8SRuntime(store, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not currently valid") || !strings.Contains(err.Error(), "identity is not proven") {
		t.Fatalf("combined live validation error = %v", err)
	}
}

func TestSecretStoreK8SRuntimeRejectsIncompleteServiceClientInventory(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	secrets := []byte(`{"items":[]}`)
	deployments := []byte(`{"items":[{"metadata":{"name":"pki-controller","generation":1},"spec":{"replicas":1,"template":{"spec":{"containers":[{"env":[{"name":"RTK_DEPLOYMENT_SERVICE_ISSUER_ID","value":"intermediate-id"},{"name":"PKI_SERVICE_CLIENT_ROOT_SHA256","value":"0123456789abcdef"},{"name":"PKI_REQUIRED_CONSUMERS_SERVICE","value":"api,controller"}]}]}}},"status":{"observedGeneration":1,"updatedReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}`)
	pods := []byte(`{"items":[{"metadata":{"name":"pki-controller-1"}}]}`)
	report := []byte("pki authorization denied\n" + `{"status":"service-client-registry-inventory-incomplete","issuances":0,"pending_issuances":0,"invalid_records":0,"unpublished_revocations":0,"missing_acknowledgments":0}`)
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'get secrets --all-namespaces -o json'*) printf '%%s' '%s' ;;\n  *'get deployments -o json'*) printf '%%s' '%s' ;;\n  *'get pods -l app.kubernetes.io/name=pki-controller -o json'*) printf '%%s' '%s' ;;\n  *'recovery-inventory-service-client'*) printf '%%s' '%s'; exit 1 ;;\n  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%%s' '{\"items\":[{\"metadata\":{\"name\":\"postgresql-0\"}}]}' ;;\n  *'pki_deployment_bootstrap_sessions'*) printf '%%s' '{\"sessions\":[],\"active_caller_index\":true,\"legacy_caller_constraint\":false}' ;;\n  *) exit 1 ;;\nesac\n", secrets, deployments, pods, report)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	err := verifySecretStoreK8SRuntime(store, time.Now())
	if err == nil || !strings.Contains(err.Error(), "inventory did not complete") {
		t.Fatalf("empty registry error = %v", err)
	}
}

func TestAccountManagerPKIPrecheckRejectsOldConsumerAndSocketMismatch(t *testing.T) {
	decode := func(t *testing.T, sidecarEnv string) liveDeploymentList {
		t.Helper()
		var deployments liveDeploymentList
		raw := fmt.Sprintf(`{"items":[{"metadata":{"name":"account-manager"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"APP_CERT_ISSUER_SOCKET","value":"/run/account-pki/private/controller.sock"}]},{"name":"pkimanagement","env":%s}]}}}}]}`, sidecarEnv)
		if err := json.Unmarshal([]byte(raw), &deployments); err != nil {
			t.Fatal(err)
		}
		return deployments
	}
	old := decode(t, `[{"name":"PKI_MANAGEMENT_SOCKET","value":"/run/account-pki/private/controller.sock"},{"name":"PKI_MANAGEMENT_ISSUER_SERVER_CRL_MANIFEST","value":"/run/crls.json"}]`)
	if err := verifyAccountManagerPKIConfiguration(old); err == nil || !strings.Contains(err.Error(), "PKI_MANAGEMENT_ISSUER_SERVER_CRL_MANIFEST") {
		t.Fatalf("old CRL consumer precheck error = %v", err)
	}
	mismatch := decode(t, `[{"name":"PKI_MANAGEMENT_SOCKET","value":"/run/other.sock"}]`)
	if err := verifyAccountManagerPKIConfiguration(mismatch); err == nil || !strings.Contains(err.Error(), "different managed socket") {
		t.Fatalf("managed socket mismatch precheck error = %v", err)
	}
	good := decode(t, `[{"name":"PKI_MANAGEMENT_SOCKET","value":"/run/account-pki/private/controller.sock"},{"name":"PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST","value":"/run/bundles.json"},{"name":"PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_PKI_CONTROLLER_URL","value":"https://controller.example.test"},{"name":"PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_MANAGEMENT_CA","value":"/run/root.pem"}]`)
	if err := verifyAccountManagerPKIConfiguration(good); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDeploymentBootstrapSessionCheckRejectsActiveAndExpiredOwners(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sessions, err := json.Marshal(liveDeploymentBootstrapReport{ActiveCallerIndex: true, Sessions: []liveDeploymentBootstrapSession{
		{Caller: "service:pki-controller", DeploymentID: "old-deployment", ExpiresAt: now.Add(-time.Minute)},
		{Caller: "service:certissuer", DeploymentID: "current-deployment", ExpiresAt: now.Add(time.Minute)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%%s' '{\"items\":[{\"metadata\":{\"name\":\"postgresql-0\"}}]}' ;;\n  *'pki_deployment_bootstrap_sessions'*) printf '%%s' '%s' ;;\n  *) exit 1 ;;\nesac\n", sessions)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	err = verifyLiveDeploymentBootstrapSessions("/tmp/kubeconfig", "video-cloud-dev-platform", now)
	if err == nil || !strings.Contains(err.Error(), "expired but still active") || !strings.Contains(err.Error(), "service:certissuer") {
		t.Fatalf("bootstrap ownership error = %v", err)
	}
}

func TestLiveDeploymentBootstrapSessionCheckRejectsPendingServiceClientIssuance(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	report, err := json.Marshal(liveDeploymentBootstrapReport{ActiveCallerIndex: true, PendingCount: 2, PendingIssuances: []livePendingServiceClientIssuance{
		{Caller: "service:certissuer", RequestID: "retained-request", CreatedAt: now.Add(-time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%%s' '{\"items\":[{\"metadata\":{\"name\":\"postgresql-0\"}}]}' ;;\n  *'pki_deployment_bootstrap_sessions'*) printf '%%s' '%s' ;;\n  *) exit 1 ;;\nesac\n", report)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	err = verifyLiveDeploymentBootstrapSessions("/tmp/kubeconfig", "video-cloud-dev-platform", now)
	if err == nil || !strings.Contains(err.Error(), "pending Service client issuance caller=service:certissuer request=retained-request") || !strings.Contains(err.Error(), "1 additional pending") {
		t.Fatalf("pending signing error = %v", err)
	}
}

func TestLiveRootPolicyPrecheckRejectsMissingPolicy(t *testing.T) {
	const rootID = "697e8e86-5af6-4580-8456-7f91d17634f2"
	var deployments liveDeploymentList
	raw := `{"items":[{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_ACCOUNT_MANAGER_SERVICE_ROOT_ID","value":"` + rootID + `"},{"name":"VIDEO_CLOUD_ACCOUNT_MANAGER_SERVICE_ROOT_STATE","value":"/private/root.json"}]}]}}}}]}`
	if err := json.Unmarshal([]byte(raw), &deployments); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\ncase \"$*\" in\n  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%s' '{\"items\":[{\"metadata\":{\"name\":\"postgresql-0\"}}]}' ;;\n  *'pki_root_distrust'*) printf '%s' '[]' ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	err := verifyLiveRootPolicyReferences("/tmp/kubeconfig", "video-cloud-dev-platform", deployments)
	if err == nil || !strings.Contains(err.Error(), rootID) || !strings.Contains(err.Error(), "video-cloud-api/app") {
		t.Fatalf("missing root policy error = %v", err)
	}
	script = strings.Replace(script, "'[]'", "'[\""+rootID+"\"]'", 1)
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveRootPolicyReferences("/tmp/kubeconfig", "video-cloud-dev-platform", deployments); err != nil {
		t.Fatalf("present root policy: %v", err)
	}
	deployments.Items[0].Spec.Template.Spec.Containers[0].Env[1].Value = ""
	if err := verifyLiveRootPolicyReferences("/tmp/kubeconfig", "video-cloud-dev-platform", deployments); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete root policy error = %v", err)
	}
}

func TestCertIssuerBootstrapPrecheckRejectsUnsupportedAndUnreachableConfiguration(t *testing.T) {
	decode := func(t *testing.T, env string) liveDeploymentList {
		t.Helper()
		var deployments liveDeploymentList
		raw := fmt.Sprintf(`{"items":[{"metadata":{"name":"certissuer"},"spec":{"template":{"spec":{"containers":[{"env":%s}]}}}}]}`, env)
		if err := json.Unmarshal([]byte(raw), &deployments); err != nil {
			t.Fatal(err)
		}
		return deployments
	}
	crl := decode(t, `[{"name":"OPENBAO_SERVER_CRL_MANIFEST","value":"/run/crl.json"}]`)
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", crl); err == nil || !strings.Contains(err.Error(), "before that deployment contract exists") {
		t.Fatalf("CRL precheck error = %v", err)
	}
	base := `[{"name":"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER","value":"service:certissuer"},{"name":"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECT","value":"service:certissuer"},{"name":"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA","value":"/run/root.pem"},{"name":"CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT","value":"/run/bootstrap.crt"},{"name":"CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY","value":"/run/bootstrap.key"},{"name":"PKI_BOOTSTRAP_SESSION_ID","value":"session"},{"name":"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID","value":"session"},{"name":"CERT_ISSUER_HOST_RENEWAL_URL","value":"https://127.0.0.1:9443"},{"name":"CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN","value":"^service-provisioner$"}]`
	mismatchedSession := decode(t, strings.Replace(base, `"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID","value":"session"`, `"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID","value":"other"`, 1))
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", mismatchedSession); err == nil || !strings.Contains(err.Error(), "same session") {
		t.Fatalf("session binding precheck error = %v", err)
	}
	serviceEndpoint := decode(t, strings.Replace(base, `https://127.0.0.1:9443`, `https://certissuer.video-cloud-dev-video-cloud.svc:9443`, 1))
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", serviceEndpoint); err == nil || !strings.Contains(err.Error(), "readiness") {
		t.Fatalf("self-enrollment endpoint precheck error = %v", err)
	}
	invalid := decode(t, strings.Replace(base, `^service-provisioner$`, `[`, 1))
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", invalid); err == nil || !strings.Contains(err.Error(), "pattern is invalid") {
		t.Fatalf("caller pattern precheck error = %v", err)
	}
	allowed := decode(t, base)
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s' '{\"items\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", allowed); err == nil || !strings.Contains(err.Error(), "NetworkPolicy") {
		t.Fatalf("network policy precheck error = %v", err)
	}
	policy := `{"items":[{"metadata":{"name":"allow-service-host-renewal"},"spec":{"ingress":[{"from":[{"podSelector":{"matchLabels":{"rtk.realtek.com/pki-bootstrap":"true"}}}]}]}}]}`
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s' '"+policy+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyCertIssuerBootstrapConfiguration("/tmp/kubeconfig", "video-cloud-dev-video-cloud", allowed); err != nil {
		t.Fatal(err)
	}
}

func TestCertIssuerStaticServingChainPrecheck(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := rolloutTLSFixture(t, now.Add(24*time.Hour), x509.ExtKeyUsageServerAuth)
	leaf, err := os.ReadFile(fixture.cert)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := os.ReadFile(fixture.ca)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(t *testing.T, ca []byte, managed bool) (liveSecretList, liveDeploymentList) {
		t.Helper()
		secretsRaw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
			"metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "certissuer-runtime"},
			"data": map[string]string{
				"tls.crt": base64.StdEncoding.EncodeToString(leaf),
				"tls.key": base64.StdEncoding.EncodeToString(key),
				"ca.crt":  base64.StdEncoding.EncodeToString(ca),
			},
		}}})
		env := []any{map[string]any{"name": "CERT_ISSUER_SERVER_CERT", "value": "/etc/video-cloud/certissuer/tls.crt"}}
		if managed {
			env = append(env, map[string]any{"name": "CERT_ISSUER_HOST_IDENTITY_STATE", "value": "/var/lib/pki-host/identity/state.json"})
		}
		deploymentsRaw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
			"metadata": map[string]any{"name": "certissuer"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"env": env, "volumeMounts": []any{map[string]any{"name": "runtime", "mountPath": "/etc/video-cloud/certissuer"}}}},
				"volumes":    []any{map[string]any{"name": "runtime", "secret": map[string]any{"secretName": "certissuer-runtime"}}},
			}}},
		}}})
		var secrets liveSecretList
		var deployments liveDeploymentList
		if json.Unmarshal(secretsRaw, &secrets) != nil || json.Unmarshal(deploymentsRaw, &deployments) != nil {
			t.Fatal("cannot decode static serving fixtures")
		}
		return secrets, deployments
	}
	secrets, deployments := decode(t, issuer, false)
	if err := verifyCertIssuerStaticServingChain("video-cloud-dev-video-cloud", secrets, deployments, now); err != nil {
		t.Fatal(err)
	}
	other := rolloutTLSFixture(t, now.Add(24*time.Hour), x509.ExtKeyUsageServerAuth)
	unrelatedCA, err := os.ReadFile(other.ca)
	if err != nil {
		t.Fatal(err)
	}
	secrets, deployments = decode(t, unrelatedCA, false)
	if err := verifyCertIssuerStaticServingChain("video-cloud-dev-video-cloud", secrets, deployments, now); err == nil || !strings.Contains(err.Error(), "no usable issuer chain") {
		t.Fatalf("untrusted static cert error = %v", err)
	}
	secrets, deployments = decode(t, unrelatedCA, true)
	if err := verifyCertIssuerStaticServingChain("video-cloud-dev-video-cloud", secrets, deployments, now); err != nil {
		t.Fatalf("managed host identity must not depend on the static seed chain: %v", err)
	}
}

func writeSecretRuntimeKubectl(t *testing.T, secrets, deployments []byte) string {
	t.Helper()
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'get secrets --all-namespaces -o json'*) printf '%%s' '%s' ;;\n  *'get deployments -o json'*) printf '%%s' '%s' ;;\n  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%%s' '{\"items\":[{\"metadata\":{\"name\":\"postgresql-0\"}}]}' ;;\n  *'pki_deployment_bootstrap_sessions'*) printf '%%s' '{\"sessions\":[],\"active_caller_index\":true,\"legacy_caller_constraint\":false}' ;;\n  *) exit 1 ;;\nesac\n", string(secrets), string(deployments))
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return kubectl
}

func TestSecretMigrationRejectsInvalidLiveK8SMetadata(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	legacyRoot := t.TempDir()
	kubeconfig := filepath.Join(legacyRoot, "state", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' 'not-json'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := importLiveK8SRuntimeSecrets(store, legacyRoot); err == nil || !strings.Contains(err.Error(), "decode K8s secret metadata") {
		t.Fatalf("invalid live K8s metadata error = %v", err)
	}
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' '{\"data\":{\"POSTGRES_PASSWORD\":\"%%%\"}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := importLiveK8SRuntimeSecrets(store, legacyRoot); err == nil || !strings.Contains(err.Error(), "decode live K8s value") {
		t.Fatalf("invalid live K8s value error = %v", err)
	}
}

func TestKubernetesProvisionProductionSecretStoreResetFailsClosed(t *testing.T) {
	t.Setenv("RTK_CLOUD_TEST_MODE", "0")
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("canonical\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	ctx := provisionContext{
		Paths: provisionPaths{EnvRoot: t.TempDir()},
		Env:   map[string]string{"CLOUD_ENV_NAME": "staging"},
		Opts:  provisionOptions{mode: provisionMode{reset: true}},
	}
	err = runKubernetesProvision(lkeCloudProvider{}, ctx)
	if err == nil || !strings.Contains(err.Error(), "reset is not implemented") {
		t.Fatalf("production reset error = %v", err)
	}
	if activeCanonicalSecretStore || activeSecretEnvironmentRoot != "" {
		t.Fatal("canonical SecretStore state was not restored")
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createBootstrapLocalFixture(t *testing.T, environment string) (deploymentConfig, secretStore, map[string]string) {
	t.Helper()
	// A fresh selected store has no initialized runtime inventory. The common
	// initialized-store fixture records the complete catalog before any values
	// are installed, which intentionally represents missing original material.
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, environment)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{configRoot, filepath.Join(store.Root, "runtime"), filepath.Join(store.Root, "openbao")} {
		if err := ensurePrivateDirectory(path); err != nil {
			t.Fatal(err)
		}
	}
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "DEPLOYMENT_PROTECTION_PROFILE": "standard", "OPENBAO_STORAGE_BACKEND": "file", "OPENBAO_EFFECTIVE_REPLICAS": "1"}}
	return cfg, store, map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "private-initial-operator-password"}
}

func TestFreshBootstrapRecordedRuntimeBlocksMissingOriginalAndAllowsOnlyNewCatalogAdditions(t *testing.T) {
	for _, mode := range []string{"original-present-new-catalog-absent", "manual-optional-blank", "manual-optional-absent", "future-pki-absent", "original-postgres-missing", "original-postgres-empty", "original-postgres-permissions", "original-postgres-symlink", "foreign-inventory", "malformed-inventory", "duplicate-original", "foreign-original-path", "forged-optional-original"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator := createBootstrapLocalFixture(t, "dev")
			if err := store.write("runtime/platform-admin", []byte(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]+"\n"), false); err != nil {
				t.Fatal(err)
			}
			if err := store.write("runtime/postgres", []byte("private-original-database-password\n"), false); err != nil {
				t.Fatal(err)
			}
			inventory := secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: "dev", Entries: []secretInventoryEntry{
				{ID: "platform-admin", Category: "runtime", Path: "runtime/platform-admin"},
				{ID: "postgres", Category: "runtime", Path: "runtime/postgres"},
			}}
			switch mode {
			case "manual-optional-blank", "manual-optional-absent":
				inventory.Entries = append(inventory.Entries, secretInventoryEntry{ID: "billing-backup-secret-access-key", Category: "runtime", Path: "runtime/billing-backup-secret-access-key", Optional: true, ExplicitProvision: true})
				if mode == "manual-optional-blank" {
					if err := store.write("runtime/billing-backup-secret-access-key", []byte("\n"), false); err != nil {
						t.Fatal(err)
					}
				}
			case "future-pki-absent":
				inventory.Entries = append(inventory.Entries, secretInventoryEntry{ID: "future-device-root", Category: "pki", Path: "pki/device/future-root.json"})
			case "original-postgres-missing", "original-postgres-symlink":
				if err := os.Remove(filepath.Join(store.Root, "runtime", "postgres")); err != nil {
					t.Fatal(err)
				}
				if mode == "original-postgres-symlink" {
					if err := os.Symlink(filepath.Join(store.Root, "runtime", "platform-admin"), filepath.Join(store.Root, "runtime", "postgres")); err != nil {
						t.Fatal(err)
					}
				}
			case "original-postgres-empty":
				if err := store.write("runtime/postgres", []byte("\n"), true); err != nil {
					t.Fatal(err)
				}
			case "original-postgres-permissions":
				if err := os.Chmod(filepath.Join(store.Root, "runtime", "postgres"), 0644); err != nil {
					t.Fatal(err)
				}
			case "foreign-inventory":
				inventory.Environment = "staging"
			case "duplicate-original":
				inventory.Entries = append(inventory.Entries, inventory.Entries[1])
			case "foreign-original-path":
				inventory.Entries[1].Path = "migration-backup/runtime/postgres"
			case "forged-optional-original":
				inventory.Entries[1].Optional = true
			}
			raw, _ := json.Marshal(inventory)
			if mode == "malformed-inventory" {
				raw = []byte("invalid original inventory")
			}
			if err := store.write("inventory.json", raw, false); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := qualifyEnvironmentCreateBootstrapLocalState(context.Background(), cfg, store, operator)
			allowed := mode == "original-present-new-catalog-absent" || mode == "manual-optional-blank" || mode == "manual-optional-absent" || mode == "future-pki-absent"
			if (err == nil) != allowed {
				t.Fatal("original inventory prerequisite result", mode, err)
			}
			if err != nil {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.create.inputs" || strings.Contains(err.Error(), "private-") {
					t.Fatal("original inventory failure lost safe pre-allocation boundary", err)
				}
			}
			if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("read-only qualification regenerated, repaired or changed original runtime material")
			}
			if _, err := os.Stat(filepath.Join(store.Root, "runtime", "video-auth")); !os.IsNotExist(err) {
				t.Fatal("qualification seeded a new catalog addition")
			}
		})
	}
}

func createBootstrapStateSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := fmt.Sprint(info.Mode())
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf("/%x", sha256.Sum256(raw))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += "/" + target
		}
		state[relative] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFreshBootstrapLocalPrerequisitesDoNotRequireFutureMaterialOrReviveArchives(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		for _, mode := range []string{"empty-active-state", "matching-original-admin", "historical-teardown-only"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, operator := createBootstrapLocalFixture(t, environment)
				if mode == "matching-original-admin" {
					if err := store.write("runtime/platform-admin", []byte(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]+"\n"), false); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "historical-teardown-only" {
					for _, relative := range []string{"migration-backup/destroy-lke-original/openbao/root-token", "migration-backup/destroy-lke-original/runtime/platform-admin", "deployment/reports/remove-original.json", "deployment/remove-volume-ownership.json"} {
						if err := store.write(relative, []byte("private-retained-teardown-evidence"), false); err != nil {
							t.Fatal(err)
						}
					}
				}
				before := createBootstrapStateSnapshot(t, store.Root)
				if err := qualifyEnvironmentCreateBootstrapLocalState(context.Background(), cfg, store, operator); err != nil {
					t.Fatal("genuine fresh prerequisites were rejected", err)
				}
				if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("qualification modified retained state or manufactured future material")
				}
			})
		}
	}
}

func TestFreshBootstrapLocalPrerequisitesBlockBeforeAllocationsAndPreserveIdentity(t *testing.T) {
	for _, mode := range []string{"production", "protection-profile", "storage-backend", "replicas", "retained-init", "retained-root", "retained-unseal", "openbao-symlink", "openbao-parent-permissions", "mismatched-admin", "empty-admin", "admin-permissions", "admin-symlink", "missing-recorded-admin", "retired-bootstrap", "missing-operator-password", "wrong-store", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			environment := "dev"
			if mode == "production" {
				environment = "prod"
			}
			cfg, store, operator := createBootstrapLocalFixture(t, environment)
			ctx := context.Background()
			expectedID := "deployment.create.openbao"
			switch mode {
			case "protection-profile":
				cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "production"
			case "storage-backend":
				cfg.Values["OPENBAO_STORAGE_BACKEND"] = "raft"
			case "replicas":
				cfg.Values["OPENBAO_EFFECTIVE_REPLICAS"] = "3"
			case "retained-init", "retained-root", "retained-unseal":
				relative := map[string]string{"retained-init": "openbao/init.json", "retained-root": "openbao/root-token", "retained-unseal": "openbao/unseal-key"}[mode]
				if err := store.write(relative, []byte("private-original-openbao-material"), false); err != nil {
					t.Fatal(err)
				}
			case "openbao-symlink":
				if err := os.Symlink(filepath.Join(store.Root, "absent-original"), filepath.Join(store.Root, "openbao", "root-token")); err != nil {
					t.Fatal(err)
				}
			case "openbao-parent-permissions":
				if err := os.Chmod(filepath.Join(store.Root, "openbao"), 0755); err != nil {
					t.Fatal(err)
				}
			case "mismatched-admin", "empty-admin", "admin-permissions":
				expectedID = "deployment.bootstrap.operator"
				value := "private-different-admin-password"
				if mode == "empty-admin" {
					value = "\n"
				}
				if err := store.write("runtime/platform-admin", []byte(value), false); err != nil {
					t.Fatal(err)
				}
				if mode == "admin-permissions" {
					if err := os.Chmod(filepath.Join(store.Root, "runtime", "platform-admin"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			case "admin-symlink":
				expectedID = "deployment.bootstrap.operator"
				if err := os.Symlink(filepath.Join(store.Root, "absent-original"), filepath.Join(store.Root, "runtime", "platform-admin")); err != nil {
					t.Fatal(err)
				}
			case "missing-recorded-admin":
				expectedID = "deployment.bootstrap.operator"
				if err := store.write("inventory.json", []byte(`{"schema_version":1,"environment":"dev","entries":[{"id":"platform-admin","path":"runtime/platform-admin"}]}`), false); err != nil {
					t.Fatal(err)
				}
			case "retired-bootstrap":
				expectedID = "deployment.bootstrap.operator"
				if err := store.write("deployment/account-bootstrap-retired.json", []byte(`{"retired":true}`), false); err != nil {
					t.Fatal(err)
				}
			case "missing-operator-password":
				expectedID = "deployment.bootstrap.operator"
				delete(operator, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD")
			case "wrong-store":
				expectedID = "deployment.create.inputs"
				store.Environment = "staging"
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := qualifyEnvironmentCreateBootstrapLocalState(ctx, cfg, store, operator)
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation did not stop qualification", err)
				}
			} else {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != expectedID || strings.Contains(err.Error(), "private-") {
					t.Fatal("unsafe initial prerequisite passed or leaked original private material", err)
				}
			}
			if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("blocked qualification altered original identity or history")
			}
		})
	}
}

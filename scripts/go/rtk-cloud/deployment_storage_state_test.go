package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustStorageStatePath(t *testing.T, cfg deploymentConfig, name string) string {
	t.Helper()
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "storage")} {
		if err := ensurePrivateDirectory(path); err != nil {
			t.Fatal(err)
		}
	}
	path, err := deploymentStorageStatePath(cfg.Environment, name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStorageImportPreservesProofsAndRejectsUnverifiedOrConflictingState(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"verified", "retry", "wrong-environment", "unfinished-journal", "changed-proof", "conflicting-canonical", "source-symlink", "public-source"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				cfg := deploymentConfig{Environment: environment, Storage: deploymentStoragePlan{RuntimeMediaCutoverRequired: true, RuntimeMedia: deploymentStorageTarget{Bucket: "target-" + environment, Region: "us-sea", Prefix: "environments/stack"}}}
				source := t.TempDir()
				proof := []byte(fmt.Sprintf(`{"environment":%q,"purpose":"media","destination_bucket":%q,"destination_region":"us-sea"}`, environment, cfg.Storage.RuntimeMedia.Bucket))
				digest := fmt.Sprintf("%x", sha256.Sum256(proof))
				journal := storageCutoverJournal{Environment: environment, Purpose: "media", Status: "complete", ID: "original-cutover", MigrationSHA256: digest}
				if mode == "unfinished-journal" {
					journal.Status = "applied"
				}
				if err := saveStorageCutoverJournal(store, journal, false); err != nil {
					t.Fatal(err)
				}
				receipt := map[string]any{"environment": environment, "bucket": cfg.Storage.RuntimeMedia.Bucket, "region": "us-sea", "prefix": cfg.Storage.RuntimeMedia.Prefix, "cutover_id": journal.ID, "cutover_at": time.Now().UTC().Add(-time.Hour).Truncate(time.Second), "migration_receipt_sha256": digest, "rollback_credentials_retained": true}
				if mode == "wrong-environment" {
					receipt["environment"] = "other"
				}
				if mode == "changed-proof" {
					proof = append(proof, '\n')
				}
				proofPath := filepath.Join(source, "state", "storage-migration.json")
				if err := os.MkdirAll(filepath.Dir(proofPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(proofPath, proof, 0600); err != nil {
					t.Fatal(err)
				}
				receiptPath := filepath.Join(source, "state", "storage-cutover.json")
				original, _ := json.Marshal(receipt)
				if err := os.WriteFile(receiptPath, original, 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "conflicting-canonical" {
					if err := writeDeploymentStorageState(environment, "storage-cutover.json", map[string]string{"existing": "retain"}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "source-symlink" {
					if err := os.Remove(receiptPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(proofPath, receiptPath); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "public-source" {
					if err := os.Chmod(receiptPath, 0644); err != nil {
						t.Fatal(err)
					}
				}
				_, err := importDeploymentStorageState(cfg, source, "media")
				want := mode == "verified" || mode == "retry"
				if (err == nil) != want {
					t.Fatal("unexpected import decision", mode, err)
				}
				if want {
					if err := validateDeploymentStorageActivation(cfg); err != nil {
						t.Fatal("imported original evidence did not qualify", err)
					}
					got, err := readDeploymentStorageState(environment, "storage-cutover.json")
					if err != nil || !bytes.Equal(got, original) {
						t.Fatal("import rewrote the original completion time or receipt")
					}
					if mode == "retry" {
						if _, err := importDeploymentStorageState(cfg, source, "media"); err != nil {
							t.Fatal("identical original import is not idempotent", err)
						}
					}
				} else if mode != "conflicting-canonical" {
					if _, err := readDeploymentStorageState(environment, "storage-cutover.json"); !os.IsNotExist(err) {
						t.Fatal("rejected import published completion evidence", err)
					}
				} else {
					got, err := readDeploymentStorageState(environment, "storage-cutover.json")
					if err != nil || !bytes.Contains(got, []byte("retain")) {
						t.Fatal("import overwrote concurrent canonical state")
					}
				}
			})
		}
	}
}

func TestStorageStateIsEnvironmentOwnedAndPreflightCannotCreateCompletion(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
			cfg := deploymentConfig{Environment: environment, RuntimeRoot: t.TempDir(), Values: map[string]string{}, Storage: deploymentStoragePlan{RuntimeMediaCutoverRequired: true, RuntimeMedia: deploymentStorageTarget{Bucket: "target", Region: "us-sea", Prefix: "environments/stack"}}}
			// A receipt in another checkout is never a lookup fallback or proof.
			if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), map[string]any{"environment": environment}); err != nil {
				t.Fatal(err)
			}
			path := mustStorageStatePath(t, cfg, "storage-cutover.json")
			before, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateDeploymentStorageActivation(cfg); err == nil {
				t.Fatal("legacy checkout receipt authorized deployment")
			}
			var output bytes.Buffer
			checks := deploymentPreflightChecks{lookPath: func(string) (string, error) {
				t.Fatal("storage blocker should precede provider/tool probes")
				return "", nil
			}}
			if err := runDeploymentPreflightWithChecksContext(context.Background(), cfg, "provision", checks, &output); err == nil || !strings.Contains(output.String(), "deployment.storage.activation") {
				t.Fatal("preflight did not expose execution's storage blocker", err)
			}
			after, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(after) != len(before) {
				t.Fatal("preflight created completion state")
			}
			if err := writeDeploymentStorageState(environment, "storage-preflight.json", map[string]string{"environment": environment}); err != nil {
				t.Fatal(err)
			}
			cfg.RuntimeRoot = t.TempDir()
			body, err := readDeploymentStorageState(environment, "storage-preflight.json")
			if err != nil || !bytes.Contains(body, []byte(environment)) {
				t.Fatal("switching checkouts lost environment state", err)
			}
			for _, other := range []string{"dev", "staging", "prod"} {
				if other != environment {
					if _, err := readDeploymentStorageState(other, "storage-preflight.json"); err == nil {
						t.Fatal("another environment borrowed storage evidence")
					}
				}
			}
			if _, err := deploymentStorageStatePath(environment, "../storage-cutover.json"); err == nil {
				t.Fatal("storage state path escaped its allowlist")
			}
			if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := readDeploymentStorageState(environment, "storage-preflight.json"); err == nil {
				t.Fatal("public storage state directory accepted")
			}
		})
	}
}

func TestStorageImportCLIUsesExplicitSourceAndExitCodes(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := writeStorageState(filepath.Join(source, "state", "storage-preflight.json"), deploymentStorageReceipt{Environment: cfg.Environment, Purpose: cfg.Storage.RuntimeMedia.Purpose, Bucket: cfg.Storage.RuntimeMedia.Bucket, Region: cfg.Storage.RuntimeMedia.Region}); err != nil {
		t.Fatal(err)
	}
	base := []string{"--workspace", workspace, "--environment", "staging", "--source-runtime", source, "--confirm", cfg.Values["CLOUD_STACK_NAME"]}
	if err := runDeploymentStorageImport(base); err != nil {
		t.Fatal("valid explicit evidence import failed", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "deployment", "storage", "storage-preflight.json")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--environment", "../dev", "--source-runtime", source}, {"--environment", "staging", "--source-runtime", "relative"}, append(append([]string{}, base...), "--purpose", "unknown"), append(append([]string{}, base...), "--confirm", "other"), {"--unknown"}} {
		if err := runDeploymentStorageImport(args); err != exitCode(2) {
			t.Fatal("invalid import flags were not exit 2", err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "state", "storage-preflight.json"), []byte(`{"environment":"staging","bucket":"private-payload-never-print"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentStorageImport(base); err != exitCode(1) {
		t.Fatal("unverified evidence was not exit 1", err)
	}
	if err := runDeploymentStorageImport([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

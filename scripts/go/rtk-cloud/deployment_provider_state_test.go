package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testLKEStatePath(t *testing.T, environment string) string {
	t.Helper()
	path, err := deploymentLKEStatePath(environment, "state.env")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderReadsRefuseLegacyWrongOwnerAndUnsafePrivateFiles(t *testing.T) {
	for _, mode := range []string{"legacy-only", "wrong-environment", "wrong-stack", "duplicate-field", "unknown-field", "insecure-file", "symlink-file", "conflicting-selection"} {
		t.Run(mode, func(t *testing.T) {
			makeIsolatedTestSecretStore(t, "staging")
			paths := provisionPaths{EnvRoot: t.TempDir()}
			values := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": "video-cloud-staging"}
			path := testLKEStatePath(t, "staging")
			if mode == "legacy-only" {
				writeTestFile(t, filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=123\n")
			} else {
				writeTestLKEState(t, "staging", "LKE_CLUSTER_ID=123\n")
				raw := readTestFile(t, path)
				switch mode {
				case "wrong-environment":
					raw = "CLOUD_ENV_NAME=dev\nCLOUD_STACK_NAME=video-cloud-staging\nLKE_CLUSTER_ID=123\n"
				case "wrong-stack":
					raw = "CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=other\nLKE_CLUSTER_ID=123\n"
				case "duplicate-field":
					raw += "LKE_CLUSTER_ID=789\n"
				case "unknown-field":
					raw += "PRIVATE_TOKEN=never-public\n"
				case "conflicting-selection":
					t.Setenv("LKE_CLUSTER_ID", "789")
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "insecure-file" {
					os.Chmod(path, 0644)
				}
				if mode == "symlink-file" {
					os.Remove(path)
					os.Symlink(filepath.Join(t.TempDir(), "unknown"), path)
				}
			}
			if _, err := lkeClusterID(paths, values); err == nil {
				t.Fatal("unsafe or conflicting provider owner selected")
			}
			if mode == "legacy-only" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("read-only check imported legacy provider state")
				}
			}
		})
	}
}

func TestDestroyedProviderStatePreservesConcurrentWriterAndOriginalOwner(t *testing.T) {
	for _, mode := range []string{"valid", "concurrent", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=video-cloud-staging\n")
			writeTestLKEState(t, "staging", "LKE_CLUSTER_ID=123\n")
			writeTestCanonicalKube(t, "staging", "original-private-kubeconfig\n")
			plan := linodeDestroyPlan{Stack: "video-cloud-staging", LKEClusters: []linodeDestroyResource{{ID: "123"}}}
			if mode == "wrong-owner" {
				plan.LKEClusters[0].ID = "789"
			}
			capturedStore, files, err := snapshotDestroyedLKEState(root, plan)
			if mode == "wrong-owner" {
				if err == nil {
					t.Fatal("destruction did not include the recorded original cluster")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "concurrent" {
				writeTestLKEState(t, "staging", "LKE_CLUSTER_ID=789\n")
			}
			err = removeDestroyedLKEState(capturedStore, files)
			if (err == nil) != (mode == "valid") {
				t.Fatal("wrong archive decision", err)
			}
			if mode == "concurrent" {
				state, err := readLKEProviderState("staging", "video-cloud-staging")
				if err != nil || state["LKE_CLUSTER_ID"] != "789" || readTestFile(t, store.KubeconfigPath()) != "original-private-kubeconfig\n" {
					t.Fatal("cleanup removed a concurrent provider owner or its credentials", err)
				}
				return
			}
			archives, _ := filepath.Glob(testDestroyedLKEArchiveGlob(t, "staging"))
			if len(archives) != 1 || !bytes.Equal([]byte(readTestFile(t, archives[0])), files[0].Raw) {
				t.Fatal("original provider source was not archived")
			}
			if _, err := os.Stat(store.KubeconfigPath()); !os.IsNotExist(err) {
				t.Fatal("approved destroyed identity was not retired")
			}
		})
	}
}

func writeTestLKEState(t *testing.T, environment, raw string) {
	t.Helper()
	state, err := parseStrictEnv([]byte(raw), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLKEProviderState(environment, "video-cloud-"+environment, state); err != nil {
		t.Fatal(err)
	}
}

func writeTestLKEAccount(t *testing.T, environment, raw string) {
	t.Helper()
	if err := writePrivateDeploymentState(environment, filepath.Join("deployment", "adapters", "lke", "account.env"), []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func testCanonicalKubePath(t *testing.T, environment string) string {
	t.Helper()
	store, err := newSecretStore("", environment)
	if err != nil {
		t.Fatal(err)
	}
	return store.KubeconfigPath()
}

func writeTestCanonicalKube(t *testing.T, environment, raw string) {
	t.Helper()
	if err := writePrivateDeploymentState(environment, "kube/kubeconfig.yaml", []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func testDestroyedLKEArchiveGlob(t *testing.T, environment string) string {
	t.Helper()
	store, err := newSecretStore("", environment)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.Root, "migration-backup", "destroy-lke-*", "deployment", "adapters", "lke", "state.env")
}

func TestLKEProviderStateRetainsAllocationsAndRejectsWrongEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			stack := "video-cloud-" + environment
			paths := provisionPaths{EnvRoot: t.TempDir()}
			values := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack}
			writeTestLKEState(t, environment, "LKE_CLUSTER_ID=123\nLKE_POSTGRES_NODE_POOL_ID=456\nLKE_POSTGRES_NODE_COUNT=2\n")
			if err := writeLKEState(paths, values, lkeCluster{ID: 123, Label: stack + "-lke", Region: "us-sea"}); err != nil {
				t.Fatal(err)
			}
			state, err := readLKEProviderState(environment, stack)
			if err != nil || state["LKE_POSTGRES_NODE_POOL_ID"] != "456" || state["LKE_POSTGRES_NODE_COUNT"] != "2" {
				t.Fatal("metadata refresh lost existing provider allocations", err)
			}
			if err := writeLKEState(paths, values, lkeCluster{ID: 789, Label: stack + "-lke"}); err == nil {
				t.Fatal("metadata refresh replaced the owned cluster")
			}
			if _, err := readLKEProviderState(environment, "another-stack"); err == nil {
				t.Fatal("provider state authorized another stack")
			}
			if _, err := os.Stat(filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env")); !os.IsNotExist(err) {
				t.Fatal("metadata refresh created operational state in configuration")
			}
			info, err := os.Stat(filepath.Join(store.Root, "deployment", "adapters", "lke", "state.env"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("provider state escaped private environment storage", err)
			}
		})
	}
}

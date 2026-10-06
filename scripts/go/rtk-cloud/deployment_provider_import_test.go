package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProviderImportVerifiesOriginalAPIOwnerAndPreservesSourcesForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "wrong-id", "wrong-region", "wrong-ca", "missing-pool", "denied", "foreign-source", "unknown-field", "existing-conflict"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				kube := frozenKubernetesFixture(t)
				kubeRaw, _ := yaml.Marshal(kube)
				store := makeIsolatedTestSecretStore(t, environment)
				workspace := writeDeploymentFixture(t, environment, "lke")
				cfg, err := resolveDeploymentConfig(workspace, environment, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.write("operator/env/LINODE_TOKEN", []byte("private-provider-token"), true); err != nil {
					t.Fatal(err)
				}
				if err := store.write("kube/kubeconfig.yaml", kubeRaw, true); err != nil {
					t.Fatal(err)
				}
				source := t.TempDir()
				writeTestFile(t, filepath.Join(source, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\nCLOUD_STACK_NAME="+cfg.Values["CLOUD_STACK_NAME"]+"\n")
				originalState := "LKE_CLUSTER_ID=123\nLKE_CLUSTER_LABEL=" + cfg.Values["CLOUD_STACK_NAME"] + "-lke\nLKE_CLUSTER_REGION=us-sea\nLKE_POSTGRES_NODE_POOL_ID=456\nLKE_POSTGRES_NODE_TYPE=g6-standard-4\nLKE_POSTGRES_NODE_COUNT=2\n"
				if mode == "unknown-field" {
					originalState += "PRIVATE_KEY=must-not-be-imported\n"
				}
				if mode == "foreign-source" {
					writeTestFile(t, filepath.Join(source, "env", "stack.env"), "CLOUD_ENV_NAME=other\n")
				}
				for name, raw := range map[string]string{"state.env": originalState, "account.env": "# original operator quota\nLKE_ACTIVE_SERVICE_LIMIT=20\n"} {
					path := filepath.Join(source, "adapters", "lke", name)
					writeTestFile(t, path, raw)
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "existing-conflict" {
					writeTestLKEState(t, environment, "LKE_CLUSTER_ID=789\n")
				}
				cluster := lkeCluster{ID: 123, Label: cfg.Values["CLOUD_STACK_NAME"] + "-lke", Region: "us-sea"}
				if mode == "wrong-id" {
					cluster.ID = 789
				}
				if mode == "wrong-region" {
					cluster.Region = "sg-sin-2"
				}
				clusterRaw, _ := json.Marshal(cluster)
				nativeKube := kubeRaw
				if mode == "wrong-ca" {
					cluster := kube["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)
					cluster["certificate-authority-data"] = base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
					nativeKube, _ = yaml.Marshal(kube)
				}
				responses := map[string]string{
					"/lke/clusters/123":            string(clusterRaw),
					"/lke/clusters/123/kubeconfig": `{"kubeconfig":"` + base64.StdEncoding.EncodeToString(nativeKube) + `"}`,
					"/lke/clusters/123/pools":      `{"data":[{"id":456,"type":"g6-standard-4","count":2}]}`,
				}
				if mode == "missing-pool" {
					responses["/lke/clusters/123/pools"] = `{"data":[]}`
				}
				if mode == "denied" {
					responses["/lke/clusters/123"] = "__ERROR_403__"
				}
				log := fakeDeploymentPreflightLKERead(t, responses)
				count := 0
				if mode == "valid" {
					err = runDeploymentProviderImport([]string{"--workspace", workspace, "--environment", environment, "--source-runtime", source, "--confirm", cfg.Values["CLOUD_STACK_NAME"]})
					count = 2
				} else {
					count, err = importDeploymentProviderState(context.Background(), cfg, source, store.KubeconfigPath())
				}
				if (err == nil) != (mode == "valid") {
					t.Fatal("incorrect provider import decision", mode, err)
				}
				if err != nil && (strings.Contains(err.Error(), "private-provider-token") || strings.Contains(err.Error(), "must-not-be-imported")) {
					t.Fatal("import error exposed private source material")
				}
				if got := readTestFile(t, filepath.Join(source, "adapters", "lke", "state.env")); got != originalState {
					t.Fatal("import changed original provider evidence")
				}
				if mode == "valid" {
					if count != 2 {
						t.Fatal("partial import")
					}
					state, err := readLKEProviderState(environment, cfg.Values["CLOUD_STACK_NAME"])
					if err != nil || state["LKE_CLUSTER_ID"] != "123" || state["LKE_POSTGRES_NODE_POOL_ID"] != "456" {
						t.Fatal("import changed original allocated IDs", err)
					}
					path, _ := deploymentLKEStatePath(environment, "account.env")
					if readTestFile(t, path) != "# original operator quota\nLKE_ACTIVE_SERVICE_LIMIT=20\n" {
						t.Fatal("quota source bytes were rewritten")
					}
					archives, _ := filepath.Glob(filepath.Join(store.Root, "migration-backup", "provider-state", "*", "state.env"))
					if len(archives) != 1 || readTestFile(t, archives[0]) != originalState {
						t.Fatal("original provider source was not privately archived")
					}
					if count, err := importDeploymentProviderState(context.Background(), cfg, source, store.KubeconfigPath()); err != nil || count != 2 {
						t.Fatal("same-source retry changed identity or failed", err)
					}
				} else {
					path, _ := deploymentLKEStatePath(environment, "account.env")
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("failed owner verification published partial account state")
					}
				}
				if raw, _ := os.ReadFile(log); strings.Contains(string(raw), "POST ") || strings.Contains(string(raw), "PUT ") || strings.Contains(string(raw), "DELETE ") {
					t.Fatal("baseline migration mutated provider resources")
				}
				if got := readTestFile(t, store.KubeconfigPath()); got != string(kubeRaw) {
					t.Fatal("baseline migration replaced original Kubernetes credentials")
				}
			})
		}
	}
}

func TestProviderImportCLIRejectsMissingSourceAndWrongConfirmationBeforePublishing(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	workspace := writeDeploymentFixture(t, "staging", "lke")
	for _, args := range [][]string{
		{"--environment", "staging"},
		{"--environment", "staging", "--source-runtime", "relative", "--confirm", "video-cloud-staging"},
		{"--environment", "staging", "--source-runtime", t.TempDir(), "--confirm", "wrong-stack"},
		{"--environment", "staging", "--source-runtime", t.TempDir(), "--confirm", "video-cloud-staging", "--source-kubeconfig", "relative"},
	} {
		args = append(args, "--workspace", workspace)
		if err := runDeploymentProviderImport(args); err != exitCode(2) {
			t.Fatal("invalid import input reached provider migration", err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Root, "deployment", "adapters", "lke")); !os.IsNotExist(err) {
		t.Fatal("invalid CLI wrote provider state")
	}
}

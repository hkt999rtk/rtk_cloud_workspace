package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func maintainedDeploymentSnapshotFixture(t *testing.T, environment string) (deploymentConfig, secretStore, environmentRelease, environmentIdentityBindings) {
	t.Helper()
	kube := frozenKubernetesFixture(t)
	store := makeIsolatedTestSecretStore(t, environment)
	workspace := writeDeploymentFixture(t, environment, "lke")
	cfg, err := resolveDeploymentConfig(workspace, environment, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte(strings.Repeat("a", 32)), true); err != nil {
			t.Fatal(err)
		}
	}
	kubeRaw, _ := yaml.Marshal(kube)
	if err := store.write("kube/kubeconfig.yaml", kubeRaw, true); err != nil {
		t.Fatal(err)
	}
	profile := postgresBackupTestConfig(t)
	profile.Environment, profile.Stack, profile.Namespace = environment, cfg.Values["CLOUD_STACK_NAME"], cfg.Values["CLOUD_STACK_NAME"]+"-platform"
	profile.Worker.Environment, profile.Worker.Stack, profile.Worker.Remote.Prefix = environment, profile.Stack, environment+"/postgres"
	profile.Worker.Source.Host = "postgresql." + profile.Namespace + ".svc.cluster.local"
	raw, _ := json.Marshal(profile)
	writeTestFile(t, filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), string(raw))
	if err := writeLKEProviderState(environment, profile.Stack, map[string]string{"LKE_CLUSTER_ID": "123"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateDeploymentState(environment, "deployment/adapters/lke/account.env", []byte("LKE_ACTIVE_SERVICE_LIMIT=unlimited\n")); err != nil {
		t.Fatal(err)
	}
	_, bindings, release := completeEnvironmentRenderFixture(environment)
	raw, _ = json.Marshal(bindings)
	if err := store.write("deployment/identity-bindings.json", raw, true); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(release)
	if err := store.write("deployment/release.json", raw, true); err != nil {
		t.Fatal(err)
	}
	return cfg, store, release, bindings
}

func TestMaintainedSnapshotAndNativeFenceRejectInputDriftAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "runtime-drift", "operator-drift", "release-drift", "binding-drift", "provider-drift", "config-drift", "unknown-workload", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, release, bindings := maintainedDeploymentSnapshotFixture(t, environment)
				snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
				if err != nil {
					t.Fatal("maintained snapshot unavailable", err)
				}
				backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, environment)
				if err != nil {
					t.Fatal(err)
				}
				object := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"namespace": bindings.Stack + "-platform", "name": "unchanged", "uid": "owner", "resourceVersion": "1", "labels": map[string]any{"rtk.realtek.com/stack": bindings.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": map[string]any{"type": "ClusterIP"}}
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: bindings.Stack, Operation: "upgrade", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, []deploymentplan.Definition{{Object: deploymentplan.Projection(object), Phase: "configuration"}}, []deploymentplan.Object{object})
				if err != nil {
					t.Fatal(err)
				}
				storage, err := backend.storageBindings(context.Background(), plan)
				if err != nil {
					t.Fatal(err)
				}
				plan.StorageSHA256 = deploymentplan.Digest(storage)
				compiled := &compiledEnvironmentDeployment{Plan: plan, Configuration: cfg, Store: store, Backend: backend, Release: release, Bindings: bindings}
				directory := t.TempDir()
				inventory := []deploymentplan.Object{object}
				if mode == "unknown-workload" {
					inventory = append(inventory, deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": bindings.Stack + "-platform", "name": "unknown", "uid": "unknown"}})
				}
				raw, _ := json.Marshal(map[string]any{"items": inventory})
				writeTestFile(t, filepath.Join(directory, "inventory.json"), string(raw))
				raw, _ = json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": bindings.Stack + "-platform"}}}})
				writeTestFile(t, filepath.Join(directory, "namespaces.json"), string(raw))
				writeTestFile(t, filepath.Join(directory, "kubectl"), "#!/bin/sh\ncase \"$*\" in\n *' exec '*) printf '{\"system_identifier\":\"123456789\",\"sealed\":true}' ;;\n *'get namespace '*) cat \"$RTK_SNAPSHOT_NAMESPACES\";;\n *'get clusterrole,'*) printf '{\"items\":[]}' ;;\n *'get deployment,'*) cat \"$RTK_SNAPSHOT_INVENTORY\";;\n *) exit 1;;\nesac\n")
				os.Chmod(filepath.Join(directory, "kubectl"), 0755)
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_SNAPSHOT_NAMESPACES", filepath.Join(directory, "namespaces.json"))
				t.Setenv("RTK_SNAPSHOT_INVENTORY", filepath.Join(directory, "inventory.json"))
				switch mode {
				case "runtime-drift":
					store.write("runtime/postgres", []byte("changed"), true)
				case "operator-drift":
					store.write("operator/env/LINODE_TOKEN", []byte("changed"), true)
				case "release-drift":
					release.WorkspaceRevision = strings.Repeat("c", 40)
					raw, _ = json.Marshal(release)
					store.write("deployment/release.json", raw, true)
				case "binding-drift":
					bindings.Owners["Service/"+bindings.Stack+"-platform/unchanged"] = "changed"
					raw, _ = json.Marshal(bindings)
					store.write("deployment/identity-bindings.json", raw, true)
				case "provider-drift":
					writePrivateDeploymentState(environment, "deployment/adapters/lke/account.env", []byte("LKE_ACTIVE_SERVICE_LIMIT=10\n"))
				case "config-drift":
					appendFile(t, filepath.Join(cfg.EnvironmentRoot, "environment.env"), "UNREVIEWED_SETTING=true\n")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				err = compiled.Fence(ctx, plan)
				if (err == nil) != (mode == "valid") {
					t.Fatal("wrong native input fence outcome", err)
				}
			})
		}
	}
}

func TestOrdinarySnapshotDoesNotRequireOrCreateDisabledBackupState(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, _, _ := maintainedDeploymentSnapshotFixture(t, environment)
			path := filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(store.RuntimeDir(), "platform-admin"))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
			if err != nil || snapshot.PostgresProtection.Environment != "" {
				t.Fatal("ordinary update requires a backup project", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("snapshot generated backup configuration")
			}
			after, err := os.ReadFile(filepath.Join(store.RuntimeDir(), "platform-admin"))
			if err != nil || string(after) != string(before) {
				t.Fatal("snapshot changed existing credentials")
			}
			cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] = "true"
			if _, err := readEnvironmentDeploymentSnapshot(cfg, store); err == nil {
				t.Fatal("enabled backup lacks required maintained configuration")
			}
			cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] = "invalid"
			if _, err := readEnvironmentDeploymentSnapshot(cfg, store); err == nil {
				t.Fatal("invalid backup selection ignored")
			}
		})
	}
}

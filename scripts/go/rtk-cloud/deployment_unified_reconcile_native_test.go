package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPublicReconciliationVerifiesNativeOwnerAndPreservesUncertainWrites(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"no-writes", "accepted", "uncertain-write", "live-owner"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				config := frozenKubernetesFixture(t)
				store := makeIsolatedTestSecretStore(t, environment)
				raw, _ := json.Marshal(config)
				if err := store.write("kube/kubeconfig.yaml", raw, true); err != nil {
					t.Fatal(err)
				}
				workspace := writeDeploymentFixture(t, environment, "lke")
				stack := "video-cloud-" + environment
				live := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "original", "namespace": stack + "-platform", "uid": "original-owner", "resourceVersion": "4", "labels": map[string]any{"rtk.realtek.com/stack": stack}}, "data": map[string]any{"value": "old"}}
				wanted := deploymentplan.Clone(live)
				deploymentplan.Map(wanted["data"])["value"] = "new"
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, []deploymentplan.Definition{{Phase: "configuration", Object: wanted}}, []deploymentplan.Object{live})
				if err != nil {
					t.Fatal(err)
				}
				sha := plan.SHA256()
				lease := environmentExecutionLeaseBody(plan, sha+":original")
				deploymentplan.Map(lease["metadata"])["uid"], deploymentplan.Map(lease["metadata"])["resourceVersion"] = "original-lease", "7"
				host, err := os.Hostname()
				if err != nil {
					t.Fatal(err)
				}
				owner := deploymentplan.ExecutionOwner{PlanSHA256: sha, Environment: environment, Stack: stack, Hostname: host, Lease: deploymentplan.LeaseOwner{ResourceID: deploymentplan.ID(lease), UID: "original-lease", Holder: sha + ":original"}}
				journal := deploymentplan.Journal{PlanSHA256: sha, Owners: map[string]string{}}
				if mode == "accepted" {
					journal.Accepted, journal.AcceptanceStarted = true, true
					journal.Completed = []string{deploymentplan.ID(live)}
					journal.Owners[deploymentplan.ID(live)] = "original-owner"
					live = deploymentplan.Clone(plan.Resources[0].Desired)
					deploymentplan.Map(live["metadata"])["uid"], deploymentplan.Map(live["metadata"])["resourceVersion"] = "original-owner", "8"
				} else if mode == "uncertain-write" {
					journal.InFlight = deploymentplan.ID(live)
				}
				active := filepath.Join(store.Root, "deployment", ".execution-active")
				rollback := filepath.Join(store.Root, "deployment", "rollback", sha)
				for _, directory := range []string{active, filepath.Join(rollback, ".active")} {
					if err := os.MkdirAll(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				lock, err := deploymentplan.LockExecutionOwner(active, true)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "live-owner" {
					t.Cleanup(func() { lock.Close() })
				} else if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				leasePath, resourcePath := filepath.Join(dir, "lease.json"), filepath.Join(dir, "resource.json")
				for path, value := range map[string]any{filepath.Join(active, "owner.json"): owner, filepath.Join(rollback, "plan.json"): plan, filepath.Join(rollback, "journal.json"): journal, leasePath: lease, resourcePath: live} {
					raw, _ := json.Marshal(value)
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := readTestFile(t, resourcePath)
				writeTestFile(t, filepath.Join(dir, "kubectl"), `#!/bin/sh
shift 3
case "$*" in
 'get Lease rtk-deployment-execution --namespace '* ) test ! -f "$RTK_RECONCILE_LEASE" || cat "$RTK_RECONCILE_LEASE";;
 'get ConfigMap original --namespace '* ) cat "$RTK_RECONCILE_RESOURCE";;
 'delete --raw=/apis/coordination.k8s.io/v1/namespaces/'*'/leases/rtk-deployment-execution -f -' )
 python3 -c 'import json,sys; body=json.load(sys.stdin); assert body["kind"]=="DeleteOptions" and body["preconditions"]=={"uid":"original-lease","resourceVersion":"7"}' || exit 1
 rm "$RTK_RECONCILE_LEASE";;
 *) exit 1;;
esac
`)
				if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_RECONCILE_LEASE", leasePath)
				t.Setenv("RTK_RECONCILE_RESOURCE", resourcePath)
				err = runEnvironmentExecutionReconcile([]string{"--workspace", workspace, "--environment", environment, "--confirm", stack, "--plan-sha256", sha})
				completed := mode == "no-writes" || mode == "accepted"
				if (err == nil) != completed {
					t.Fatal("public native reconciliation made the wrong decision", err)
				}
				if readTestFile(t, resourcePath) != before {
					t.Fatal("reconciliation rolled back or overwrote a resource")
				}
				for _, path := range []string{active, leasePath} {
					_, err := os.Lstat(path)
					if !completed && err != nil || completed && !os.IsNotExist(err) {
						t.Fatal("original execution fence was stolen or left unresolved", path, err)
					}
				}
				if _, err := os.Stat(filepath.Join(rollback, "journal.json")); err != nil {
					t.Fatal("private original recovery evidence was discarded", err)
				}
			})
		}
	}
}

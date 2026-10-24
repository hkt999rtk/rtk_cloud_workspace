package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeFenceSharesFreshInventoryAndPreservesMissingOwnerReads(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing-claim", "missing-secret", "volume-drift", "owner-drift", "unknown-workload", "config-drift"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, release, bindings := maintainedDeploymentSnapshotFixture(t, environment)
				ns := bindings.Stack + "-platform"
				meta := func(name, uid string) deploymentplan.Object {
					return deploymentplan.Object{"namespace": ns, "name": name, "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": bindings.Stack}}
				}
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta("data", "claim-owner"), "spec": deploymentplan.Object{"volumeName": "original-pv", "storageClassName": "fixture", "accessModes": []any{"ReadWriteOnce"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": meta("original-identity", "secret-owner"), "data": deploymentplan.Object{"key": "private-fence-canary"}}
				role := deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta("outside-inventory", "role-owner")}
				pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "original-pv", "uid": "volume-owner", "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": ns, "name": "data", "uid": "claim-owner"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				bindings.Owners = map[string]string{deploymentplan.ID(claim): "claim-owner", deploymentplan.ID(secret): "secret-owner", deploymentplan.ID(role): "role-owner"}
				writeJSON := func(path string, value any) {
					t.Helper()
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					writeTestFile(t, path, string(raw))
				}
				snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
				if err != nil {
					t.Fatal(err)
				}
				backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, environment)
				if err != nil {
					t.Fatal(err)
				}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: bindings.Stack, Operation: "upgrade", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}}
				for _, object := range []deploymentplan.Object{claim, secret} {
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(object), UID: deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]), Action: "retain", Phase: "configuration", Desired: deploymentplan.Projection(object), Baseline: deploymentplan.Projection(object)})
				}
				plan.StorageSHA256 = deploymentplan.Digest([]environmentStorageBinding{{deploymentplan.ID(claim), "claim-owner", deploymentplan.ID(pv), "volume-owner", deploymentplan.Digest(pv["spec"])}})
				compiled := &compiledEnvironmentDeployment{Plan: plan, Configuration: cfg, Store: store, Backend: backend, Release: release, Bindings: bindings}
				directory := t.TempDir()
				objects := map[string]deploymentplan.Object{deploymentplan.ID(claim): claim, deploymentplan.ID(secret): secret, deploymentplan.ID(role): role, deploymentplan.ID(pv): pv}
				inventory := []deploymentplan.Object{claim, secret}
				switch mode {
				case "missing-claim":
					inventory = []deploymentplan.Object{secret}
				case "missing-secret":
					inventory = []deploymentplan.Object{claim}
				case "volume-drift":
					deploymentplan.Map(pv["spec"])["storageClassName"] = "private-fence-canary"
				case "owner-drift":
					deploymentplan.Map(secret["metadata"])["uid"] = "replacement-owner"
				case "unknown-workload":
					inventory = append(inventory, deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("unplanned", "unplanned-owner")})
				case "config-drift":
					appendFile(t, filepath.Join(cfg.EnvironmentRoot, "environment.env"), "UNREVIEWED_SETTING=true\n")
				}
				writeJSON(filepath.Join(directory, "objects.json"), objects)
				writeJSON(filepath.Join(directory, "inventory.json"), map[string]any{"items": inventory})
				writeJSON(filepath.Join(directory, "namespaces.json"), map[string]any{"items": []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "uid": "namespace-owner"}}}})
				kubectl := filepath.Join(directory, "kubectl")
				writeTestFile(t, kubectl, `#!/bin/sh
printf '%s\n' "$*" >> "$RTK_FENCE_CALLS"
case "$*" in
 *'get namespace '*) cat "$RTK_FENCE_DIR/namespaces.json";;
 *'get deployment,'*) cat "$RTK_FENCE_DIR/inventory.json";;
 *'get clusterrole,'*|*'get validatingwebhookconfiguration '*) printf '{"items":[]}' ;;
 *' get '*) python3 -c 'import json,sys; a=sys.argv[1:]; i=a.index("get"); ns=a[a.index("--namespace")+1] if "--namespace" in a else ""; o=json.load(open(sys.argv[-1])).get(a[i+1]+"/"+ns+"/"+a[i+2]); print(json.dumps(o) if o else "",end="")' "$@" "$RTK_FENCE_DIR/objects.json";;
 *) exit 2;;
esac
`)
				if err := os.Chmod(kubectl, 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_FENCE_DIR", directory)
				t.Setenv("RTK_FENCE_CALLS", filepath.Join(directory, "calls"))
				before := plan.SHA256()
				observed, err := compiled.FenceInventory(context.Background(), plan)
				wantSuccess := mode == "valid" || strings.HasPrefix(mode, "missing-")
				if (err == nil) != wantSuccess || plan.SHA256() != before {
					t.Fatal("wrong fence outcome or changed plan", err)
				}
				if err != nil && strings.Contains(err.Error(), "private-fence-canary") {
					t.Fatal("fence exposed private state")
				}
				if wantID := map[string]string{"volume-drift": "deployment.execution.storage", "owner-drift": "deployment.ownership.drift", "unknown-workload": "deployment.inventory"}[mode]; wantID != "" {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != wantID {
						t.Fatal("fence hid the original storage, owner or workload blocker", err)
					}
				}
				if wantSuccess {
					for _, object := range []deploymentplan.Object{claim, secret, role} {
						if deploymentplan.Digest(observed[deploymentplan.ID(object)]) != deploymentplan.Digest(object) {
							t.Fatal("phase observation omitted or changed an owner")
						}
					}
				}
				calls, _ := os.ReadFile(filepath.Join(directory, "calls"))
				if mode == "config-drift" && len(calls) != 0 {
					t.Fatal("known local drift triggered cluster scans")
				}
				if mode == "valid" && (strings.Contains(string(calls), "get PersistentVolumeClaim ") || strings.Contains(string(calls), "get Secret ") || strings.Count(string(calls), "get PersistentVolume ") != 1 || strings.Count(string(calls), "get Role ") != 1) {
					t.Fatal("fresh inventory owners were redundantly reread or missing owner was not read")
				}
				if mode == "valid" {
					// A subsequent fence must see replacement owners, not reuse this map.
					deploymentplan.Map(secret["metadata"])["uid"] = "replacement-owner"
					writeJSON(filepath.Join(directory, "inventory.json"), map[string]any{"items": inventory})
					if _, err := compiled.FenceInventory(context.Background(), plan); err == nil {
						t.Fatal("later fence reused a stale observation")
					}
				}
			})
		}
	}
}

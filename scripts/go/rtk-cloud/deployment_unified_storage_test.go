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

func TestNativeStorageProofRejectsReboundLostAndChangedVolumesForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "claim-replaced", "claim-pending", "claim-lost", "volume-replaced", "volume-handle-changed", "claimref-uid", "claimref-namespace", "volume-pending", "volume-terminating", "status-only"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				kube := frozenKubernetesFixture(t)
				makeIsolatedTestSecretStore(t, environment)
				raw, _ := yaml.Marshal(kube)
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				namespace := "video-cloud-" + environment + "-platform"
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"volumeName": "pv-original", "storageClassName": "fast", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}, "status": deploymentplan.Object{"phase": "Bound"}}
				pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original"}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "original-data"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}, Resources: []deploymentplan.Resource{{ID: "PersistentVolumeClaim/" + namespace + "/data", UID: "claim-original", Action: "retain", Desired: deploymentplan.Projection(claim), Baseline: deploymentplan.Projection(claim)}}}
				directory := t.TempDir()
				claimPath, pvPath := filepath.Join(directory, "claim.json"), filepath.Join(directory, "pv.json")
				publish := func() {
					claimRaw, _ := json.Marshal(claim)
					pvRaw, _ := json.Marshal(pv)
					writeTestFile(t, claimPath, string(claimRaw))
					writeTestFile(t, pvPath, string(pvRaw))
				}
				writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
case "$*" in
 *'get PersistentVolumeClaim data --namespace '*) cat "$RTK_TEST_STORAGE_CLAIM";;
 *'get PersistentVolume pv-original '*) cat "$RTK_TEST_STORAGE_PV";;
 *) exit 1;;
esac
`)
				if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_TEST_STORAGE_CLAIM", claimPath)
				t.Setenv("RTK_TEST_STORAGE_PV", pvPath)
				publish()
				bindings, err := backend.storageBindings(context.Background(), plan)
				if err != nil || len(bindings) != 1 {
					t.Fatal("original native storage proof unavailable", err)
				}
				plan.StorageSHA256 = deploymentplan.Digest(bindings)
				approved := plan.SHA256()
				switch mode {
				case "claim-replaced":
					deploymentplan.Map(claim["metadata"])["uid"] = "claim-new"
				case "claim-pending":
					deploymentplan.Map(claim["status"])["phase"] = "Pending"
				case "claim-lost":
					deploymentplan.Map(claim["status"])["phase"] = "Lost"
				case "volume-replaced":
					deploymentplan.Map(pv["metadata"])["uid"] = "volume-new"
				case "volume-handle-changed":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["volumeHandle"] = "different-data"
				case "claimref-uid":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["uid"] = "other-claim"
				case "claimref-namespace":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["namespace"] = "another-environment"
				case "volume-pending":
					deploymentplan.Map(pv["status"])["phase"] = "Available"
				case "volume-terminating":
					deploymentplan.Map(pv["metadata"])["deletionTimestamp"] = "now"
				case "status-only":
					deploymentplan.Map(pv["metadata"])["resourceVersion"] = "2"
					deploymentplan.Map(pv["status"])["lastPhaseTransitionTime"] = "later"
				}
				publish()
				err = backend.CheckStorageBindings(context.Background(), plan)
				allowed := mode == "valid" || mode == "status-only"
				if (err == nil) != allowed || plan.SHA256() != approved {
					t.Fatal("incorrect original-volume qualification or mutated plan", err)
				}
				if err != nil && strings.Contains(err.Error(), "original-data") {
					t.Fatal("storage identity leaked through report")
				}
			})
		}
	}
}

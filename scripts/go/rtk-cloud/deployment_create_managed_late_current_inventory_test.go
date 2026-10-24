package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateCurrentInventoryFixture(t *testing.T) (secretStore, deploymentplan.Inputs, map[string]deploymentplan.Resource, map[string]bool, []deploymentplan.Object) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	for key, value := range map[string]string{"GHCR_PULL_USERNAME": "isolated-user", "GHCR_PULL_TOKEN": "isolated-token"} {
		if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	plan, objects, originalDigest := targetWorkloadFixture(t, "dev", "Deployment")
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:` + strings.Repeat("c", 64) + `"},"layers":[]}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(strings.ReplaceAll(string(raw), originalDigest, digest)), plan) != nil {
		t.Fatal("rewrite isolated selected image")
	}
	for i, object := range objects {
		raw, _ = json.Marshal(object)
		if json.Unmarshal([]byte(strings.ReplaceAll(string(raw), originalDigest, digest)), &objects[i]) != nil {
			t.Fatal("rewrite isolated runtime image")
		}
	}
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "manifest.json"), string(manifest))
	writeTestFile(t, filepath.Join(directory, "docker"), `#!/bin/sh
if test "$1" = info; then printf '[{"Name":"buildx","Path":"/isolated/docker-buildx"}]'; exit 0; fi
test "$1" = --config && test -f "$2/config.json" || exit 1
case "$2" in "$RTK_TEST_STORE_ROOT"/deployment/tmp/image-metadata-*) ;; *) exit 1;; esac
test "$DOCKER_CONFIG" = "$2" && test "$BUILDX_CONFIG" = "$2/buildx" || exit 1
test "$3" = buildx && test "$4" = imagetools && test "$5" = inspect && test "$6" = --raw || exit 1
test "$7" = "$RTK_TEST_INVENTORY_IMAGE" || exit 1
cat "$RTK_TEST_INVENTORY_MANIFEST"
`)
	if err := os.Chmod(filepath.Join(directory, "docker"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_STORE_ROOT", store.Root)
	t.Setenv("RTK_TEST_INVENTORY_IMAGE", "ghcr.io/hkt999rtk/runtime@"+digest)
	t.Setenv("RTK_TEST_INVENTORY_MANIFEST", filepath.Join(directory, "manifest.json"))
	owners := map[string]deploymentplan.Resource{}
	ready := map[string]bool{}
	for _, r := range plan.Resources {
		owners[r.ID] = r
		ready[r.ID] = true
	}
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "retained-config", "namespace": plan.Stack + "-application", "uid": "original-config"}, "data": deploymentplan.Object{"public": "original"}}
	id := deploymentplan.ID(cm)
	owners[id] = deploymentplan.Resource{ID: id, UID: "original-config", Action: "retain", Desired: deploymentplan.Projection(cm)}
	ready[id] = true
	objects = append(objects, cm)
	return store, plan.Inputs, owners, ready, objects
}

func TestManagedLateCurrentInventoryKeepsOwnerReadinessAndRuntimeProof(t *testing.T) {
	for _, mode := range []string{"valid", "body-drift", "owner-replaced", "owner-deleting", "config-deleting", "duplicate", "not-ready", "controller-unobserved", "pod-missing", "pod-owner-replaced", "wrong-pod-image", "wrong-image-digest", "failed-init", "pending-not-ready", "pending-body-drift"} {
		t.Run(mode, func(t *testing.T) {
			store, inputs, owners, ready, objects := managedLateCurrentInventoryFixture(t)
			controller, pod := objects[0], objects[1]
			controllerID := deploymentplan.ID(controller)
			cm := objects[len(objects)-1]
			switch mode {
			case "body-drift":
				deploymentplan.Map(controller["spec"])["replicas"] = float64(2)
			case "owner-replaced":
				deploymentplan.Map(controller["metadata"])["uid"] = "replacement"
			case "owner-deleting":
				deploymentplan.Map(controller["metadata"])["deletionTimestamp"] = "2026-10-14T00:00:00Z"
			case "config-deleting":
				deploymentplan.Map(cm["metadata"])["deletionTimestamp"] = "2026-10-14T00:00:00Z"
			case "duplicate":
				objects = append(objects, deploymentplan.Clone(cm))
			case "not-ready":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["ready"] = false
			case "controller-unobserved":
				deploymentplan.Map(controller["status"])["observedGeneration"] = float64(1)
			case "pod-missing":
				objects = append(objects[:1], objects[2:]...)
			case "pod-owner-replaced":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			case "wrong-pod-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "ghcr.io/hkt999rtk/runtime:foreign"
			case "wrong-image-digest":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["imageID"] = "containerd://sha256:" + strings.Repeat("f", 64)
			case "failed-init":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["initContainerStatuses"])[0])["state"])["terminated"])["exitCode"] = float64(1)
			case "pending-not-ready":
				ready[controllerID] = false
				deploymentplan.Map(pod["status"])["phase"] = "Pending"
			case "pending-body-drift":
				ready[controllerID] = false
				deploymentplan.Map(controller["spec"])["replicas"] = float64(2)
			}
			before := deploymentplan.Digest(owners)
			fingerprints, err := verifyManagedLateCurrentOwnerInventory(context.Background(), store, inputs, owners, ready, objects)
			valid := mode == "valid" || mode == "pending-not-ready"
			if (err == nil) != valid {
				t.Fatalf("unexpected %s acceptance: %v", mode, err)
			}
			if valid && (len(fingerprints) != len(owners) || fingerprints[controllerID] != deploymentplan.Digest(deploymentplan.Projection(controller))) {
				t.Fatal("all owner projections not preserved")
			}
			if deploymentplan.Digest(owners) != before {
				t.Fatal("immutable current authority mutated")
			}
			entries, _ := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
			if len(entries) != 0 {
				t.Fatal("temporary image credentials retained")
			}
		})
	}
}

func TestManagedLateCurrentInventoryDoesNotReuseEarlierBoundary(t *testing.T) {
	store, inputs, owners, ready, objects := managedLateCurrentInventoryFixture(t)
	if _, err := verifyManagedLateCurrentOwnerInventory(context.Background(), store, inputs, owners, ready, objects); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(objects[len(objects)-1]["data"])["public"] = "changed-at-next-boundary"
	if _, err := verifyManagedLateCurrentOwnerInventory(context.Background(), store, inputs, owners, ready, objects); err == nil {
		t.Fatal("earlier mutable inventory accepted at a new boundary")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestQualifiedControllerIdentityPermissionsRefreshOriginalFilesystemOnFrozenCluster(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			kube := frozenKubernetesFixture(t)
			store := makeIsolatedTestSecretStore(t, environment)
			raw, _ := yaml.Marshal(kube)
			backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write("kube/kubeconfig.yaml", []byte("foreign-permissions-authentication-canary"), true); err != nil {
				t.Fatal(err)
			}
			resource := identityPermissionFixture(environment)
			pod := deploymentplan.Object{"metadata": deploymentplan.Object{"name": "original-pod", "uid": "original-pod-uid", "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "uid": "original-rs", "controller": true}}}, "spec": environmentPKIPod(resource.Desired), "status": deploymentplan.Object{"containerStatuses": []any{deploymentplan.Object{"name": "pki-controller", "ready": true, "state": deploymentplan.Object{"running": deploymentplan.Object{}}}}}}
			directory := t.TempDir()
			writeJSON := func(name string, value any) {
				raw, _ := json.Marshal(value)
				writeTestFile(t, filepath.Join(directory, name), string(raw))
			}
			writeJSON("replicasets.json", deploymentplan.Object{"items": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-rs", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "uid": resource.UID, "controller": true}}}}}})
			writeJSON("pods.json", deploymentplan.Object{"items": []any{pod}})
			writeJSON("pod.json", pod)
			writeTestFile(t, filepath.Join(directory, "mode"), "700:10001\n")
			calls := deploymentRuntimeTestKubectl(t, `
cp "$2" "$RTK_PERMISSIONS_FROZEN_CAPTURE"
case "$*" in
 *'get replicasets '*) cat "$RTK_PERMISSIONS_DIR/replicasets.json" ;;
 *'get pods '*) cat "$RTK_PERMISSIONS_DIR/pods.json" ;;
 *'get pod original-pod '*) cat "$RTK_PERMISSIONS_DIR/pod.json" ;;
 *' exec '*" stat -c %a:%u /var/lib/identity") cat "$RTK_PERMISSIONS_DIR/mode" ;;
 *) exit 2 ;;
esac
`)
			t.Setenv("PATH", filepath.Dir(calls)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_PERMISSIONS_DIR", directory)
			t.Setenv("RTK_PERMISSIONS_FROZEN_CAPTURE", filepath.Join(directory, "capture"))
			c := &compiledEnvironmentDeployment{Plan: &deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}, Backend: backend, Store: store, Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}}}}
			if err := verifyQualifiedControllerIdentityPermissions(context.Background(), c); err != nil {
				t.Fatal("original identity filesystem could not be qualified", err)
			}
			writeTestFile(t, filepath.Join(directory, "mode"), "700:0\n")
			err = verifyQualifiedControllerIdentityPermissions(context.Background(), c)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.pki.identity-permissions" || strings.Contains(err.Error(), "authentication-canary") {
				t.Fatal("changed original filesystem ownership was reused or exposed", err)
			}
			captured, err := os.ReadFile(filepath.Join(directory, "capture"))
			if err != nil || string(captured) != string(backend.KubeconfigContent) {
				t.Fatal("current filesystem check read another Kubernetes identity", err)
			}
		})
	}
}

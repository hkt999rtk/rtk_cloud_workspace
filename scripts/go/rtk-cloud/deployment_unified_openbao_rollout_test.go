package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoOnDeleteRestartUsesExactOwnerAndStopsOnDrift(t *testing.T) {
	for _, mode := range []string{"restart", "current", "controller-drift", "foreign-pod", "delete-rejected", "replacement-drift", "sealed"} {
		t.Run(mode, func(t *testing.T) {
			env, release, live := existingFileOpenBaoFixture("dev", false)
			bindings, err := observeEnvironmentBindings("dev", release, live)
			if err != nil {
				t.Fatal(err)
			}
			want, err := renderEnvironmentOpenBao(env, bindings.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(want, bindings, release); err != nil {
				t.Fatal(err)
			}
			if err := bindOpenBaoAllocations([]deploymentplan.Definition{{Object: want}}, live); err != nil {
				t.Fatal(err)
			}
			controller := deploymentplan.Clone(want)
			meta := deploymentplan.Map(controller["metadata"])
			meta["uid"], meta["resourceVersion"], meta["generation"] = "original-controller", "2", float64(2)
			controller["status"] = map[string]any{"observedGeneration": float64(2), "updateRevision": "target-revision"}
			if mode == "controller-drift" {
				meta["uid"] = "different-controller"
			}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "openbao-0", "namespace": meta["namespace"], "uid": "old-pod", "resourceVersion": "4", "labels": map[string]any{"controller-revision-hash": "old-revision"}, "ownerReferences": []any{map[string]any{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "original-controller"}}}, "spec": environmentPKIPod(want), "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}
			if mode == "foreign-pod" {
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "other-owner"
			}
			if mode == "current" {
				deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"] = "target-revision"
			}
			directory := t.TempDir()
			for name, object := range map[string]deploymentplan.Object{"controller.json": controller, "pod.json": pod} {
				raw, _ := json.Marshal(object)
				writeTestFile(t, filepath.Join(directory, name), string(raw))
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			stub := "#!" + python + "\n" + `import sys,json,os
from pathlib import Path
root=Path(os.environ['RTK_OPENBAO_TEST_ROOT'])
mode=os.environ['RTK_OPENBAO_TEST_MODE']
args=sys.argv[4:]
if args[0]=='get':
 if args[1]=='StatefulSet': print((root/'controller.json').read_text())
 elif args[1]=='Pod':
  pod=json.loads((root/'pod.json').read_text())
  if (root/'deleted').exists():
   pod['metadata']['uid']='replacement-pod'
   pod['metadata']['labels']['controller-revision-hash']='target-revision'
   if mode=='replacement-drift': pod['spec']['containers'][0]['image']='unreviewed:latest'
   if mode=='sealed': pod['status']['conditions'][0]['status']='False'
  print(json.dumps(pod))
 else: sys.exit(2)
elif args[0]=='delete':
 body=json.load(sys.stdin)
 assert body['preconditions']=={'uid':'old-pod','resourceVersion':'4'}
 assert '/api/v1/namespaces/video-cloud-dev-secrets/pods/openbao-0' in args[1]
 if mode=='delete-rejected': sys.exit(1)
 if 'dryRun=All' in args[1]: (root/'dry-run').touch()
 else: (root/'deleted').touch()
 print('{}')
else: sys.exit(2)
`
			writeTestFile(t, filepath.Join(directory, "kubectl"), stub)
			if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_OPENBAO_TEST_ROOT", directory)
			t.Setenv("RTK_OPENBAO_TEST_MODE", mode)
			kube := frozenKubernetesFixture(t)
			raw, _ := json.Marshal(kube)
			backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			resource := deploymentplan.Resource{ID: deploymentplan.ID(want), UID: "original-controller", Action: "update", Desired: want}
			plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}
			requests, err := environmentPlanAccessRequirements(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, verb := range []string{"get", "delete", "watch"} {
				found := false
				for _, r := range requests {
					if r.Resource == "pods" && r.Name == "openbao-0" && r.Verb == verb {
						found = true
					}
				}
				if !found {
					t.Fatal("preflight omitted actual Pod restart permission", verb)
				}
			}
			if mode == "restart" || mode == "current" {
				if err := backend.checkOpenBaoRestartAdmission(context.Background(), plan); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(directory, "deleted")); !os.IsNotExist(err) {
					t.Fatal("preflight restarted a real Pod")
				}
			}
			ctx := context.Background()
			if mode == "sealed" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 1500*time.Millisecond)
				defer cancel()
			}
			err = backend.Wait(ctx, resource)
			if (err == nil) != (mode == "restart" || mode == "current") {
				t.Fatal("restart decision incorrect", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "credential=") {
				t.Fatal("private API error escaped")
			}
			_, deletedErr := os.Stat(filepath.Join(directory, "deleted"))
			if mode == "controller-drift" || mode == "foreign-pod" || mode == "current" {
				if !os.IsNotExist(deletedErr) {
					t.Fatal("restart deleted a foreign/current Pod")
				}
			}
			if mode == "restart" && deletedErr != nil {
				t.Fatal("OnDelete update did not restart its old Pod")
			}
		})
	}
}

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
	for _, mode := range []string{"restart", "current", "omitted-readonly", "readonly-drift", "controller-drift", "foreign-pod", "delete-rejected", "replacement-drift", "sealed"} {
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
			if mode == "omitted-readonly" || mode == "readonly-drift" {
				for _, group := range []string{"containers", "initContainers"} {
					for _, value := range certIssuerObjectList(environmentPKIPod(want)[group]) {
						container := deploymentplan.Map(value)
						container["volumeMounts"] = append(certIssuerObjectList(container["volumeMounts"]), deploymentplan.Object{"name": "original-state", "mountPath": "/var/lib/original", "readOnly": false})
					}
				}
			}
			controller := deploymentplan.Clone(want)
			meta := deploymentplan.Map(controller["metadata"])
			meta["uid"], meta["resourceVersion"], meta["generation"] = "original-controller", "2", float64(2)
			controller["status"] = map[string]any{"observedGeneration": float64(2), "updateRevision": "target-revision"}
			if mode == "controller-drift" {
				meta["uid"] = "different-controller"
			}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "openbao-0", "namespace": meta["namespace"], "uid": "old-pod", "resourceVersion": "4", "labels": map[string]any{"controller-revision-hash": "old-revision"}, "ownerReferences": []any{map[string]any{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "original-controller"}}}, "spec": deploymentplan.Clone(environmentPKIPod(want)), "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}
			if mode == "omitted-readonly" || mode == "readonly-drift" {
				for _, group := range []string{"containers", "initContainers"} {
					for _, value := range certIssuerObjectList(deploymentplan.Map(pod["spec"])[group]) {
						for _, mount := range certIssuerObjectList(deploymentplan.Map(value)["volumeMounts"]) {
							if deploymentplan.Map(mount)["readOnly"] == false {
								delete(deploymentplan.Map(mount), "readOnly")
							}
						}
					}
				}
			}
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
   if mode=='readonly-drift': pod['spec']['containers'][0]['volumeMounts'][-1]['readOnly']=True
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
			backend.RolloutTimeout = 90 * time.Second
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
			if mode == "restart" || mode == "current" || mode == "omitted-readonly" {
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
			if (err == nil) != (mode == "restart" || mode == "current" || mode == "omitted-readonly") {
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
			if (mode == "restart" || mode == "omitted-readonly") && deletedErr != nil {
				t.Fatal("OnDelete update did not restart its old Pod")
			}
		})
	}
}

func TestOpenBaoOnDeleteRetainVerifiesOriginalCurrentPodWithoutMutation(t *testing.T) {
	for _, mode := range []string{"current", "omitted-readonly", "readonly-drift", "old-revision", "controller-drift", "foreign-pod", "wrong-image", "wrong-init", "not-ready", "unobserved", "missing-revision", "absent-pod", "pod-replaced", "revision-drift", "generation-drift", "create-native"} {
		t.Run(mode, func(t *testing.T) {
			image := "ghcr.io/hkt999rtk/openbao@sha256:" + strings.Repeat("a", 64)
			want := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": "openbao", "namespace": "video-cloud-dev-secrets"}, "spec": map[string]any{"replicas": float64(1), "updateStrategy": map[string]any{"type": "OnDelete"}, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "openbao", "image": image}}, "initContainers": []any{map[string]any{"name": "install", "image": image}}}}}}
			if mode == "omitted-readonly" || mode == "readonly-drift" {
				for _, group := range []string{"containers", "initContainers"} {
					deploymentplan.Map(certIssuerObjectList(environmentPKIPod(want)[group])[0])["volumeMounts"] = []any{deploymentplan.Object{"name": "original-state", "mountPath": "/var/lib/original", "readOnly": false}}
				}
			}
			controller := deploymentplan.Clone(want)
			meta := deploymentplan.Map(controller["metadata"])
			meta["uid"], meta["resourceVersion"], meta["generation"] = "original-controller", "2", float64(2)
			// OnDelete may retain an old currentRevision after its actual Pod was
			// explicitly replaced. The Pod's revision, not this field, proves target.
			controller["status"] = map[string]any{"observedGeneration": float64(2), "currentRevision": "historical-revision", "updateRevision": "target-revision"}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "openbao-0", "namespace": meta["namespace"], "uid": "original-pod", "resourceVersion": "4", "labels": map[string]any{"controller-revision-hash": "target-revision"}, "ownerReferences": []any{map[string]any{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "original-controller"}}}, "spec": deploymentplan.Clone(environmentPKIPod(want)), "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}
			switch mode {
			case "omitted-readonly", "readonly-drift":
				for _, group := range []string{"containers", "initContainers"} {
					mount := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])[group])[0])["volumeMounts"])[0])
					delete(mount, "readOnly")
					if mode == "readonly-drift" {
						mount["readOnly"] = true
					}
				}
			case "old-revision":
				deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"] = "historical-revision"
			case "controller-drift":
				meta["uid"] = "foreign-controller"
			case "foreign-pod":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "foreign-controller"
			case "wrong-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "unreviewed:latest"
			case "wrong-init":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"])[0])["image"] = "unreviewed:latest"
			case "not-ready", "pod-replaced", "revision-drift", "generation-drift":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"])[0])["status"] = "False"
			case "unobserved":
				deploymentplan.Map(controller["status"])["observedGeneration"] = float64(1)
			case "missing-revision":
				delete(deploymentplan.Map(controller["status"]), "updateRevision")
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
with (root/'calls').open('a') as f: f.write(' '.join(args)+'\n')
if 'rollout' in args:
 (root/'native-rollout').touch()
 print('rollout status is only available for RollingUpdate strategy type',file=sys.stderr)
 sys.exit(1)
if args[0]!='get':
 (root/'mutation').touch()
 sys.exit(2)
if args[1]=='StatefulSet':
 controller=json.loads((root/'controller.json').read_text())
 if (root/'pod-read').exists():
  if mode=='revision-drift': controller['status']['updateRevision']='foreign-revision'
  if mode=='generation-drift':
   controller['metadata']['generation']=3
   controller['status']['observedGeneration']=3
 print(json.dumps(controller))
elif args[1]=='Pod':
 if mode=='absent-pod': print('null'); sys.exit(0)
 pod=json.loads((root/'pod.json').read_text())
 if (root/'pod-read').exists() and mode=='pod-replaced': pod['metadata']['uid']='foreign-pod'
 (root/'pod-read').touch()
 print(json.dumps(pod))
else: sys.exit(2)
`
			writeTestFile(t, filepath.Join(directory, "kubectl"), stub)
			if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_OPENBAO_TEST_ROOT", directory)
			t.Setenv("RTK_OPENBAO_TEST_MODE", mode)
			raw, _ := json.Marshal(frozenKubernetesFixture(t))
			backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			backend.RolloutTimeout = 3 * time.Second
			if mode == "not-ready" {
				backend.RolloutTimeout = 300 * time.Millisecond
			}
			resource := deploymentplan.Resource{ID: deploymentplan.ID(want), UID: "original-controller", Action: "retain", Desired: want}
			if mode == "create-native" {
				resource.Action = "create"
			}
			if !environmentOnDeleteOpenBao(resource) || environmentOnDeleteUpdate(resource) {
				t.Fatal("profile classification changed update-only restart permission")
			}
			plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}
			if err := backend.checkOpenBaoRestartAdmission(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(directory, "calls")); !os.IsNotExist(err) {
				t.Fatal("retained/create target requested restart admission")
			}
			requests, err := environmentPlanAccessRequirements(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, request := range requests {
				if request.Resource == "pods" && request.Verb == "delete" {
					t.Fatal("retained/create target requested Pod delete permission")
				}
			}
			err = backend.Wait(context.Background(), resource)
			if (err == nil) != (mode == "current" || mode == "omitted-readonly") {
				t.Fatal("retained OpenBao decision incorrect", mode, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "mutation")); !os.IsNotExist(err) {
				t.Fatal("retained OpenBao issued a mutation")
			}
			_, nativeErr := os.Stat(filepath.Join(directory, "native-rollout"))
			if mode == "create-native" {
				if nativeErr != nil {
					t.Fatal("create's existing native routing changed")
				}
			} else if !os.IsNotExist(nativeErr) {
				t.Fatal("retained OnDelete called unsupported native rollout")
			}
		})
	}
}

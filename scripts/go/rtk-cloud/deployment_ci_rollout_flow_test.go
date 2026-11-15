package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This executes the real rollout, native Kubernetes adapter and private journal
// against an isolated stateful API simulator; it never touches a real cluster.
func TestCIRolloutNativeFlowAndRecovery(t *testing.T) {
	for _, mode := range []string{"success", "readiness-failed", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			config := frozenKubernetesFixture(t)
			encoded, _ := yaml.Marshal(config)
			backend, err := newEnvironmentKubernetesBackend(string(encoded), "dev")
			if err != nil {
				t.Fatal(err)
			}
			backend.RolloutTimeout = 15 * time.Second
			repo := "hkt999rtk/rtk_cloud_frontend"
			imageRepo := "ghcr.io/" + repo + "/frontend"
			old := imageRepo + "@sha256:" + strings.Repeat("a", 64)
			next := imageRepo + "@sha256:" + strings.Repeat("b", 64)
			rev := strings.Repeat("c", 40)
			object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-frontend", "name": "frontend", "uid": "uid", "resourceVersion": "1"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "frontend", "image": old}}, "volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "original-pvc"}}}}}}}
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "uid", Desired: object}}}
			manifest := []byte(fmt.Sprintf(`{"schema":"rtk-service-image/v1","repository":%q,"image":%q,"digest":%q,"source_commit":%q,"run_id":"3","run_attempt":"1"}`, repo, imageRepo, "sha256:"+strings.Repeat("b", 64), rev))
			archive := publicationZIP(t, "image-manifest.json", manifest)
			runJSON := func(id int, workflow string) []byte {
				return []byte(fmt.Sprintf(`{"id":%d,"head_sha":%q,"head_branch":"main","path":%q,"event":"push","status":"completed","conclusion":"success","run_attempt":1}`, id, rev, ".github/workflows/"+workflow))
			}
			responses := map[string][]byte{
				"repos/" + repo + "/actions/runs/10/attempts/1":                                           []byte(`{"id":10,"run_attempt":1,"status":"completed"}`),
				"repos/" + repo + "/commits/main":                                                         []byte(`{"sha":"` + rev + `"}`),
				"repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + rev + "&per_page=100":      []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(2, "ci.yml"))),
				"repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + rev + "&per_page=100": []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(3, "release.yml"))),
				"repos/" + repo + "/actions/runs/2":                                                       runJSON(2, "ci.yml"), "repos/" + repo + "/actions/runs/3": runJSON(3, "release.yml"),
				"repos/" + repo + "/actions/runs/3/jobs?per_page=100":      []byte(`{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`),
				"repos/" + repo + "/actions/runs/3/artifacts?per_page=100": []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":4,"name":"image-manifest","expired":false,"size_in_bytes":%d}]}`, len(archive))),
				"repos/" + repo + "/actions/artifacts/4/zip":               archive,
			}
			run := func(_ context.Context, command string, args ...string) ([]byte, error) {
				if command != "gh" || len(args) != 2 || args[0] != "api" {
					return nil, fmt.Errorf("unexpected provider request")
				}
				value, ok := responses[args[1]]
				if !ok {
					return nil, fmt.Errorf("missing provider response")
				}
				return value, nil
			}
			image, err := resolveCIRolloutImage(context.Background(), ciBranchSelection{repo: "main"}, repo, imageRepo, run)
			if err != nil || image.Reference != next {
				t.Fatalf("CI source resolution: %v", err)
			}
			dir := t.TempDir()
			statePath := filepath.Join(dir, "state.json")
			tool := filepath.Join(dir, "kubectl")
			configMap := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-ci-state-rtk-cloud-frontend", "uid": "state-uid", "resourceVersion": "1"}, "data": map[string]any{"state.json": ""}}
			var previousLease any
			if mode == "interrupted" {
				pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
				deploymentplan.Map(pod["containers"].([]any)[0])["image"] = next
				pending := &ciRolloutPending{Repository: repo, Branch: "main", RunID: 10, Attempt: 1, LeaseUID: "previous-lease", Holder: "previous-holder", Changes: []ciRolloutPendingChange{{Namespace: "video-cloud-dev-frontend", Name: "frontend", UID: "uid", Container: "frontend", PreviousImage: old, DesiredImage: next}}}
				priorState, _ := json.Marshal(ciRolloutState{Environment: "dev", Repository: repo, Branch: "main", Images: map[string]environmentReleaseImage{}, Pending: pending})
				deploymentplan.Map(configMap["data"])["state.json"] = string(priorState)
				previousLease = deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-deployment-execution", "uid": "previous-lease", "resourceVersion": "1", "annotations": map[string]any{"rtk.realtek.com/ci-repository": repo, "rtk.realtek.com/ci-run-id": "10", "rtk.realtek.com/ci-run-attempt": "1"}}, "spec": map[string]any{"holderIdentity": "previous-holder"}}
			}
			state, _ := json.Marshal(map[string]any{"deployment": object, "configmap": configMap, "lease": previousLease, "fail": mode == "readiness-failed"})
			if err = os.WriteFile(statePath, state, 0600); err != nil {
				t.Fatal(err)
			}
			program := ciRolloutFakeKubectl
			if err = os.WriteFile(tool, []byte(program), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", tool)
			t.Setenv("CI_TEST_STATE", statePath)
			t.Setenv("GITHUB_RUN_ID", "101")
			t.Setenv("GITHUB_RUN_ATTEMPT", "1")
			images := map[string]environmentReleaseImage{imageRepo: image}
			journal, err := newCIRolloutBatchJournal(backend.Store, repo, "main", images, "flow-1")
			if err != nil {
				t.Fatal(err)
			}
			hooks := ciRolloutNativeBatchHooks(backend, plan, journal, run, time.Minute)
			err = executeCIRolloutBatch(context.Background(), plan, ciBranchSelection{repo: "main"}, repo, "main", images, hooks)
			raw, readErr := os.ReadFile(statePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var result struct {
				Deployment deploymentplan.Object `json:"deployment"`
				ConfigMap  deploymentplan.Object `json:"configmap"`
				Lease      any                   `json:"lease"`
			}
			if json.Unmarshal(raw, &result) != nil {
				t.Fatal("bad simulated state")
			}
			current := environmentReleaseSlotImage(result.Deployment, "frontend")
			want := next
			if mode == "readiness-failed" {
				want = old
			}
			if mode != "readiness-failed" && !strings.Contains(deploymentplan.String(deploymentplan.Map(result.ConfigMap["data"])["state.json"]), next) {
				t.Fatal("completed digest was not recorded in the environment")
			}
			if (mode != "readiness-failed" && err != nil) || (mode == "readiness-failed" && err == nil) || current != want || result.Lease != nil || !strings.Contains(string(raw), "original-pvc") {
				t.Fatalf("result=%v image=%s lease=%v state=%s", err, current, result.Lease, raw)
			}
			log, readErr := os.ReadFile(filepath.Join(backend.Store.Root, journal.relative))
			if mode == "interrupted" && !strings.Contains(string(log), `"status":"recovering"`) {
				t.Fatal("interrupted prior run recovery was not recorded")
			}
			if readErr != nil || !strings.Contains(string(log), old) {
				t.Fatalf("missing recovery journal: %v", readErr)
			}
		})
	}
}

// Stateful Kubernetes fixture shared by rollout integration tests.
const ciRolloutFakeKubectl = `#!/usr/bin/env python3
import json,os,sys
p=os.environ['CI_TEST_STATE']
with open(p) as f: state=json.load(f)
a=sys.argv[1:]; a=a[2:]; a=a[1:]
if a[0]=='--namespace': a=a[2:]
verb=a[0]; dry='--dry-run=server' in a
if verb=='get':
 obj=state['lease'] if a[1]=='Lease' else state['configmap'] if a[1]=='ConfigMap' else state['deployment']
 if obj is not None: print(json.dumps(obj))
elif verb=='create':
 obj=json.load(sys.stdin); obj['metadata']['uid']='lease-uid'; obj['metadata']['resourceVersion']='1'
 if not dry:
  if state['lease'] is not None: sys.exit(1)
  state['lease']=obj
 print(json.dumps(obj))
elif verb=='delete':
 body=json.load(sys.stdin); lease=state['lease']
 if lease is None or body['preconditions']['uid']!=lease['metadata']['uid']:sys.exit(1)
 state['lease']=None
elif verb=='patch':
 patch=json.load(sys.stdin);obj=state['configmap'] if a[1]=='configmap' else state['deployment']
 for op in patch:
  parts=op['path'].strip('/').split('/'); cur=obj
  for part in parts[:-1]: cur=cur[int(part)] if isinstance(cur,list) else cur[part]
  key=int(parts[-1]) if isinstance(cur,list) else parts[-1]
  if op['op']=='test' and cur[key]!=op['value']:sys.exit(1)
  if op['op']=='replace':cur[key]=op['value']
 if not dry:obj['metadata']['resourceVersion']=str(int(obj['metadata']['resourceVersion'])+1)
 print(json.dumps(obj))
elif verb=='rollout':
 if state['fail']:
  state['fail']=False
  with open(p,'w') as f:json.dump(state,f)
  sys.exit(1)
 print('ready')
else:sys.exit(1)
if not dry:
 with open(p,'w') as f:json.dump(state,f)
`

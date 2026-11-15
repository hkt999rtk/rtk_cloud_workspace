package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the real preparation command against isolated Kubernetes and GitHub
// simulations. No credential, state, or network access to a real environment.
func TestCIRolloutPrepareCreatesRestrictedIdentityAndIsIdempotent(t *testing.T) {
	kube := frozenKubernetesFixture(t)
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	rawKube, _ := yaml.Marshal(kube)
	if err := store.write("kube/kubeconfig.yaml", rawKube, true); err != nil {
		t.Fatal(err)
	}
	workspace := writeDeploymentFixture(t, "dev", "lke")
	repo := "hkt999rtk/rtk_cloud_frontend"
	writeTestFile(t, filepath.Join(workspace, "cloud_env/dev/ci-rollout.env"), "CI_ROLLOUT_BRANCHES={\""+repo+"\":\"main\"}\n")
	image := "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("a", 64)
	original := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-frontend", "name": "frontend", "uid": "frontend-original", "resourceVersion": "1", "labels": map[string]any{"rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}}}}}
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.json")
	state, _ := json.Marshal(map[string]any{"objects": map[string]any{"Deployment/video-cloud-dev-frontend/frontend": original}})
	if err := os.WriteFile(statePath, state, 0600); err != nil {
		t.Fatal(err)
	}
	kubectl := `#!/usr/bin/env python3
import base64,json,os,sys
p=os.environ['CI_PREP_STATE']
with open(p) as f: s=json.load(f)
a=sys.argv[4:]
if a[0]=='auth':
 resource=a[3]
 if resource in ('secrets','pods','deployments.apps/rtk-ci-unrelated-probe'):
  print('no');sys.exit(1)
 print('yes');sys.exit(0)
if a[0]=='get' and a[1]=='deployments':
 print(json.dumps({'apiVersion':'v1','kind':'List','items':[s['objects']['Deployment/video-cloud-dev-frontend/frontend']]}));sys.exit(0)
if a[0]=='get':
 ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 key=a[1]+'/'+ns+'/'+a[2]
 obj=s['objects'].get(key)
 if obj is not None: print(json.dumps(obj))
 sys.exit(0)
if a[0] in ('create','replace'):
 obj=json.load(sys.stdin);m=obj['metadata']; key=obj['kind']+'/'+m.get('namespace','')+'/'+m['name']
 if '--dry-run=server' not in a:
  m.setdefault('uid','uid-'+m['name']);m.setdefault('resourceVersion','1')
  if obj['kind']=='Secret':obj['data']={'token':base64.b64encode(b'restricted-fixture-token').decode()}
  s['objects'][key]=obj
  with open(p,'w') as f:json.dump(s,f)
 print(json.dumps(obj));sys.exit(0)
if a[0]=='delete':
 opts=json.load(sys.stdin);key='Lease/video-cloud-dev-video-cloud/rtk-deployment-execution';obj=s['objects'].get(key)
 if obj is None or opts['preconditions']['uid']!=obj['metadata']['uid'] or opts['preconditions']['resourceVersion']!=obj['metadata']['resourceVersion']:sys.exit(1)
 del s['objects'][key]
 with open(p,'w') as f:json.dump(s,f)
 print('{}');sys.exit(0)
sys.exit(2)
`
	tool := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(tool, []byte(kubectl), 0700); err != nil {
		t.Fatal(err)
	}
	github := `#!/bin/sh
if [ "$1 $2" = 'auth token' ]; then echo fixture-github-token; exit 0; fi
if [ "$1" = api ]; then
 case "$2" in
  */commits/main) printf '{"sha":"cccccccccccccccccccccccccccccccccccccccc"}' ;;
  *actions/workflows/*) printf '{"total_count":0,"workflow_runs":[]}' ;;
  */environments/dev) printf '{}' ;;
  *) exit 3 ;;
 esac
 exit 0
fi
if [ "$1 $2" = 'secret set' ]; then cat >/dev/null; printf '%s\n' "$3" >> "$CI_PREP_GH_LOG"; exit 0; fi
exit 4
`
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte(github), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("CI_PREP_STATE", statePath)
	t.Setenv("CI_PREP_GH_LOG", filepath.Join(directory, "gh.log"))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{"--environment", "dev", "--workspace", workspace}
	if err := runCIRolloutPrepare(args); err != nil {
		t.Fatal("dry run:", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "gh.log")); !os.IsNotExist(err) {
		t.Fatal("dry run modified GitHub")
	}
	for i := 0; i < 2; i++ {
		if err := runCIRolloutPrepare(append(args, "--apply")); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	name, _ := ciRolloutStateName(repo)
	saved, err := store.read(filepath.Join("deployment/ci-rollout/identities", name, "kubeconfig.yaml"))
	if err != nil || !strings.Contains(saved, "restricted-fixture-token") || strings.Contains(saved, "private-frozen-token") {
		t.Fatal("restricted kubeconfig was not isolated", err)
	}
	log, err := os.ReadFile(filepath.Join(directory, "gh.log"))
	if err != nil || strings.Count(string(log), "RTK_DEV_ROLLOUT_KUBECONFIG") != 2 || strings.Count(string(log), "RTK_DEV_ROLLOUT_TARGETS") != 2 {
		t.Fatal("expected both GitHub secret destinations on each explicit apply", err)
	}
	final, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Objects map[string]deploymentplan.Object `json:"objects"`
	}
	if json.Unmarshal(final, &result) != nil || deploymentplan.Digest(result.Objects["Deployment/video-cloud-dev-frontend/frontend"]) != deploymentplan.Digest(original) || result.Objects["Lease/video-cloud-dev-video-cloud/rtk-deployment-execution"] != nil {
		t.Fatal("preparation changed the workload or retained its lease")
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestCustodyInspectionJobExecutionFencesOriginalJobPodAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "lost-create-response", "lost-delete-response", "foreign-pod", "bad-public-output", "replaced-job-after-log", "failed-job"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, "dev")
			fixture := t.TempDir()
			source := deploymentplan.Object{"securityContext": environmentCreateServicePodSecurity(), "containers": []any{deploymentplan.Object{"securityContext": environmentCreateServiceContainerSecurity()}}}
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "60", "600"
			binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), ServiceRootSHA256: strings.Repeat("c", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api", StorageAliases: []string{"api-state"}}}}
			ref, err := newEnvironmentCustodyStateReference(cfg, binding, "video-cloud-dev-video-cloud", "api-state", "/identity/private/client.json", binding.ServiceRootSHA256)
			if err != nil {
				t.Fatal(err)
			}
			job, err := environmentCustodyInspectionJob(cfg, binding, ref, source, "ghcr.io/fixture/native@sha256:"+strings.Repeat("d", 64), "", "", "service:api", nil)
			if err != nil {
				t.Fatal(err)
			}
			installCustodyInspectionProcessFixture(t, fixture, mode)
			backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-inspection-kubeconfig"), RolloutTimeout: 60 * time.Second}
			observed, err := runEnvironmentCustodyInspection(context.Background(), backend, job, ref)
			success := mode == "success" || mode == "lost-create-response" || mode == "lost-delete-response"
			if (err == nil) != success || success && (observed.Subject != "service:api" || !observed.Pending) {
				t.Fatal("inspection outcome disagrees with original reader proof", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-error-marker") {
				t.Fatal("private helper output leaked")
			}
			raw, readErr := os.ReadFile(filepath.Join(fixture, "delete.json"))
			var deletion struct {
				Preconditions map[string]string `json:"preconditions"`
			}
			if readErr != nil || json.Unmarshal(raw, &deletion) != nil || deletion.Preconditions["uid"] != "11111111-1111-4111-8111-111111111111" {
				t.Fatal("cleanup did not fence the original helper UID", readErr)
			}
			_, left := os.Stat(filepath.Join(fixture, "job.json"))
			if mode == "replaced-job-after-log" {
				if left != nil {
					t.Fatal("cleanup removed replacement owner")
				}
			} else if !os.IsNotExist(left) {
				t.Fatal("original inspection Job was not removed")
			}
		})
	}
}

func installCustodyInspectionProcessFixture(t *testing.T, fixture, mode string) {
	t.Helper()
	script := `#!/usr/bin/env python3
import sys,json,os,copy
from pathlib import Path
root=Path(os.environ['RTK_INSPECTION_FIXTURE']); mode=os.environ['RTK_INSPECTION_MODE']; args=sys.argv[1:]
def save(name,value): (root/name).write_text(json.dumps(value))
def load(name): return json.loads((root/name).read_text())
if 'create' in args:
 job=json.load(sys.stdin);job['metadata']['uid']='11111111-1111-4111-8111-111111111111';job['metadata']['resourceVersion']='1';job['status']={'conditions':[{'type':'Complete','status':'True'}]};save('job.json',job)
 pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':'original-inspection-pod','namespace':job['metadata']['namespace'],'uid':'22222222-2222-4222-8222-222222222222','ownerReferences':[{'kind':'Job','name':job['metadata']['name'],'uid':job['metadata']['uid'],'controller':True}],'labels':job['spec']['template']['metadata']['labels']},'spec':copy.deepcopy(job['spec']['template']['spec']),'status':{'phase':'Succeeded'}}
 if mode=='foreign-pod':pod['metadata']['ownerReferences'][0]['uid']='foreign-job'
 if mode.startswith('public-'):pod['status']={'phase':'Running','containerStatuses':[{'name':'inspect','ready':True,'state':{'running':{}}}]}
 save('pod.json',pod)
 if mode=='lost-create-response':sys.exit(1)
 print(json.dumps(job))
elif 'get' in args:
 kind=args[args.index('get')+1]
 if kind.lower()=='job':
  if (root/'job.json').exists():print(json.dumps(load('job.json')))
 elif kind.lower()=='secret':print(json.dumps(load('mirror.json')))
 elif kind=='pods':print(json.dumps({'items':[load('pod.json')]}))
 elif kind.lower()=='pod':print(json.dumps(load('pod.json')))
 else:sys.exit(90)
elif 'wait' in args:
 if '--for=delete' in args:sys.exit(0)
 sys.exit(1 if mode=='failed-job' else 0)
elif 'exec' in args:
 save('exec.json',args)
 save('input.json',sys.stdin.read() if '-i' in args else '')
 if mode=='public-exec-failed':sys.exit(1)
 if mode=='public-replaced-after-exec':
  job=load('job.json');job['metadata']['uid']='33333333-3333-4333-8333-333333333333';save('job.json',job)
 if mode=='public-bad-output':print('private-error-marker')
 else:print(json.dumps(load('public.json')))
elif 'logs' in args:
 if mode=='replaced-job-after-log':
  job=load('job.json');job['metadata']['uid']='33333333-3333-4333-8333-333333333333';save('job.json',job)
 if mode=='bad-public-output':print('private-error-marker')
 else:
  command=load('job.json')['spec']['template']['spec']['containers'][0]['command']
  print(json.dumps({'subject':command[4],'fingerprint':'e'*64,'public_key_sha256':'f'*64,'root_sha256':command[5],'state_sha256':'a'*64,'pending':True}))
elif 'delete' in args:
 request=json.load(sys.stdin);save('delete.json',request)
 if request['preconditions']['uid']!=load('job.json')['metadata']['uid']:sys.exit(1)
 (root/'job.json').unlink();(root/'pod.json').unlink()
 if mode=='lost-delete-response':sys.exit(1)
else:sys.exit(91)
`
	kubectl := filepath.Join(fixture, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_INSPECTION_FIXTURE", fixture)
	t.Setenv("RTK_INSPECTION_MODE", mode)
}

func TestProductCustodyObservationAcceptsCurrentNativeProofWithoutReadingOldMirrorKeyOrPVC(t *testing.T) {
	_, bootstrap, _, service := createServiceRecipeFixture(t, "dev")
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "60", "600"
	cfg.Values["SHADOW_WORKER_REGISTRATION_ENABLED"] = "true"
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.ProductIdentities = outputs
	binding, err := createEnvironmentCustodyBinding(cfg, bootstrap, e.Journal.PlanSHA256, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	ref, err := newEnvironmentCustodyStateReference(cfg, binding, "video-cloud-dev-video-cloud", outputs[0].Claim, environmentCreateServiceOwnerIdentityPath("shadow", "client.json"), service.Root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	l := &createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, Root: service.Root, Bootstrap: bootstrap, Outputs: outputs}
	source := deploymentplan.Object{"securityContext": environmentCreateServicePodSecurity(), "serviceAccountName": ref.ServiceAccount, "containers": []any{deploymentplan.Object{"name": "enroll", "image": bootstrap.Image, "securityContext": environmentCreateServiceContainerSecurity(), "env": environmentCreateEnv(map[string]string{"PKI_CLIENT_IDENTITY_STATE": ref.StatePath, "PKI_STATE_RECORD_PREFIX": "dev/" + ref.Generation + "/" + ref.Owner, "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": ref.Mount})}}}
	owner := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "original-shadow-enrollment", "namespace": ref.Namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": source}}}
	child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: bootstrap.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(bootstrap.Image), BindingsSHA256: deploymentplan.Digest(ref)}, []deploymentplan.Definition{{Object: owner, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: child.SHA256(), Accepted: true, Completed: []string{deploymentplan.ID(owner)}, Owners: map[string]string{deploymentplan.ID(owner): "55555555-5555-4555-8555-555555555555"}}
	for name, value := range map[string]any{"plan.json": child, "journal.json": journal} {
		raw, _ := json.Marshal(value)
		if err := store.write(filepath.Join("deployment/rollback", child.SHA256(), name), raw, false); err != nil {
			t.Fatal(err)
		}
	}
	stage := "kubernetes/product-identities-" + productIdentityScope(l)[:12] + "-enroll-shadow"
	intent, err := environmentCreateKubernetesStageIntent(child)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry(stage, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(map[string]string{"plan_sha256": child.SHA256()})
	if err := e.RecordReceipt(stage, receipt); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(stage, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create/material", ref.Generation, "openbao-transport/ca.crt"), []byte(service.Root.Certificate), false); err != nil {
		t.Fatal(err)
	}
	trust, err := renderEnvironmentCustodyTrust(cfg, ref.Generation, service.Root.Certificate, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create", ref.Generation, "custody/transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	installCustodyInspectionProcessFixture(t, fixture, "success")
	mirrorUID := "44444444-4444-4444-8444-444444444444"
	mirror := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": strings.Split(outputs[0].ID, "/")[2], "namespace": ref.Namespace, "uid": mirrorUID}, "immutable": true, "data": deploymentplan.Object{"stale-bootstrap-mirror": "unused-private-delivery"}}
	raw, _ := json.Marshal(mirror)
	if err := os.WriteFile(filepath.Join(fixture, "mirror.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	installed := &environmentProductIdentitySession{Ceremony: service, Outputs: []environmentProductIdentityReceipt{{ResourceID: outputs[0].ID, Subject: outputs[0].Subject, SecretUID: mirrorUID, CertificateSHA256: strings.Repeat("f", 64), Custody: ref}}}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: ref.Generation, Custody: binding, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-custody-observer")}}
	if err := verifyProductIdentityOutputs(context.Background(), cfg, runtime, l, installed); err != nil {
		t.Fatal("current KV identity was blocked by old delivery mirror, fingerprint or claim", err)
	}
}

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type stagingNativeRecoveryState struct {
	Objects                            map[string]deploymentplan.Object `json:"objects"`
	Calls                              []string                         `json:"calls"`
	Schema                             environmentSchemaState           `json:"schema"`
	Registry                           any                              `json:"registry"`
	Sealed                             bool                             `json:"sealed"`
	Mode                               string                           `json:"mode"`
	Deletes, Unseals, Leases, Releases int
}

type stagingNativeRecoveryProtocol struct {
	path    string
	backend environmentKubernetesBackend
}

func (p stagingNativeRecoveryProtocol) save(t *testing.T, s stagingNativeRecoveryState) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func (p stagingNativeRecoveryProtocol) read(t *testing.T) stagingNativeRecoveryState {
	t.Helper()
	raw, err := os.ReadFile(p.path)
	if err != nil {
		t.Fatal(err)
	}
	var s stagingNativeRecoveryState
	if json.Unmarshal(raw, &s) != nil {
		t.Fatal("invalid isolated native state")
	}
	return s
}

// Only the Kubernetes/native process boundary is simulated. Owner checks,
// conditional deletes, lease fencing, private custody and journal writes are real.
func newStagingNativeRecoveryProtocol(t *testing.T, store secretStore, objects map[string]deploymentplan.Object) stagingNativeRecoveryProtocol {
	t.Helper()
	dir := t.TempDir()
	p := stagingNativeRecoveryProtocol{path: filepath.Join(dir, "state.json"), backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-native-recovery"), RolloutTimeout: 5 * time.Second}}
	p.save(t, stagingNativeRecoveryState{Objects: objects, Schema: environmentSchemaState{Database: "rtk_billing", Schema: "public"}})
	script := `#!/usr/bin/env python3
import fcntl,json,os,sys,uuid
p=os.environ['RTK_STAGING_NATIVE_RECOVERY_TEST']
with open(p+'.lock','a') as lock:
 fcntl.flock(lock,fcntl.LOCK_EX)
 d=json.load(open(p));a=sys.argv[1:]
 while a and a[0].startswith('--'):
  if a[0]=='--kubeconfig':a=a[2:]
  else:a=a[1:]
 # exec commands can start with --namespace before their verb.
 original=sys.argv[1:]
 if 'exec' in original:a=original[original.index('exec'):]
 d['calls'].append(' '.join(a)) if d['calls'] is not None else d.update(calls=[' '.join(a)])
 def save():
  with open(p,'w') as out:json.dump(d,out)
 def emit(o):print(json.dumps(o))
 save()
 if a[0]=='get':
  kind=a[1];ns=original[original.index('--namespace')+1] if '--namespace' in original else ''
  if len(a)>2 and not a[2].startswith('-'):
   o=d['objects'].get(kind+'/'+ns+'/'+a[2])
   if o is not None:emit(o)
  else:
   kinds=kind.lower().split(',');kinds=['persistentvolume' if x=='persistentvolumes' else x for x in kinds]
   items=[o for o in d['objects'].values() if o['kind'].lower() in kinds and o['metadata'].get('namespace','')==ns]
   if '-l' in a:
    key,value=a[a.index('-l')+1].split('=',1);items=[o for o in items if o['metadata'].get('labels',{}).get(key)==value]
   emit({'apiVersion':'v1','kind':'List','items':items})
  sys.exit()
 if a[0] in ('create','replace'):
  o=json.load(sys.stdin);m=o['metadata'];key=o['kind']+'/'+m.get('namespace','')+'/'+m['name']
  assert o['kind']=='Lease'
  old=d['objects'].get(key)
  assert (a[0]=='create' and old is None) or (a[0]=='replace' and old['metadata']['uid']==m['uid'] and old['metadata']['resourceVersion']==m['resourceVersion'])
  if '--dry-run=server' not in a:
   m['uid']=str(uuid.uuid4());m['resourceVersion']='1';d['objects'][key]=o;d['Leases']+=1;save()
  emit(o);sys.exit()
 if a[0]=='delete':
  body=json.load(sys.stdin);assert body['kind']=='DeleteOptions'
  raw=next(x for x in a if x.startswith('--raw='));ns,name=raw.split('/namespaces/')[1].split('/')[0],raw.split('/')[-1].split('?')[0]
  kind='Lease' if '/leases/' in raw else 'Pod';key=kind+'/'+ns+'/'+name
  o=d['objects'][key];m=o['metadata'];assert body['preconditions']=={'uid':m['uid'],'resourceVersion':m['resourceVersion']}
  if 'dryRun=All' not in raw:
   if kind=='Lease':del d['objects'][key];d['Releases']+=1
   else:
    assert name=='openbao-0';d['Deletes']+=1;m['uid']='99999999-9999-4999-8999-999999999999';m['resourceVersion']='9'
    controller=d['objects']['StatefulSet/'+ns+'/openbao'];m['labels']['controller-revision-hash']=controller['status']['updateRevision']
    if d['mode']=='replacement-owner':m['ownerReferences'][0]['uid']='foreign-controller'
    d['sealed']=True
   save()
  emit({'status':'Success'});sys.exit()
 if a[0]=='exec':
  script=' '.join(a)
  if "json_build_object('database'" in script:
   assert 'BEGIN TRANSACTION READ ONLY;' in script
   emit(d['schema']);sys.exit()
  if 'registry-read' in a:
   assert 'PGOPTIONS=-c default_transaction_read_only=on' in script and a[-1].startswith('SELECT ')
   emit(d['registry']);sys.exit()
  if 'bao status -format=json' in script:
   emit({'initialized':True,'sealed':d['sealed']});sys.exit()
  if 'bao write -format=json sys/unseal -' in script:
   body=json.load(sys.stdin);assert body=={'key':os.environ['RTK_STAGING_NATIVE_ORIGINAL_SHARE']}
   d['Unseals']+=1;d['sealed']=False;save();emit({'initialized':True,'sealed':False});sys.exit()
 print('unsupported isolated operation '+repr(a),file=sys.stderr);sys.exit(2)
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_STAGING_NATIVE_RECOVERY_TEST", p.path)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}

func TestStagingBillingQualificationReconcilesOnlyAbsentJobAndEmptyOriginalDatabase(t *testing.T) {
	for _, mode := range []string{"original", "nonempty-database", "database-owner-changed", "query-pod-changed", "job-already-exists", "different-write"} {
		t.Run(mode, func(t *testing.T) {
			schema := newSchemaOwnerFixture(t, "staging")
			f := newStagingWorkloadQualificationFixture(t, schema.plan.Resources, func(cfg *deploymentConfig, parent *environmentCreatePlan, _ secretStore) {
				cfg.Workspace = schema.cfg.Workspace
				cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"] = "selected-pull"
				parent.Release.Images["LKE_BILLING_IMAGE"] = schema.release.Images["LKE_BILLING_IMAGE"]
			})
			jobID := deploymentplan.ID(schema.job.Object)
			for _, r := range f.child.Resources {
				if r.ID == jobID {
					break
				}
				uid := r.UID
				if uid == "" {
					uid = "77777777-7777-4777-8777-777777777777"
				}
				f.journal.Completed = append(f.journal.Completed, r.ID)
				f.journal.Owners[r.ID] = uid
			}
			f.journal.InFlight, f.journal.Failed = jobID, jobID
			if mode == "different-write" {
				f.journal.InFlight = f.child.Resources[len(f.child.Resources)-1].ID
			}
			f.saveProgress(t, f.child, f.journal)
			protocol := newStagingNativeRecoveryProtocol(t, f.store, schema.objects)
			state := protocol.read(t)
			switch mode {
			case "nonempty-database":
				state.Schema.Objects = 1
			case "database-owner-changed":
				deploymentplan.Map(state.Objects["StatefulSet/"+f.parent.Stack+"-platform/postgresql"]["metadata"])["uid"] = "foreign"
			case "query-pod-changed":
				deploymentplan.Map(state.Objects["Pod/"+f.parent.Stack+"-platform/postgresql-0"]["metadata"])["ownerReferences"] = []any{}
			case "job-already-exists":
				state.Objects[jobID] = deploymentplan.Clone(schema.job.Object)
				deploymentplan.Map(state.Objects[jobID]["metadata"])["uid"] = "unknown-job"
			}
			protocol.save(t, state)
			journalPath := filepath.Join(f.store.Root, "deployment/rollback", f.child.SHA256(), "journal.json")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			err = recoverStagingInitialBillingWrite(t.Context(), f.cfg, f.store, &f.parent, f.e, protocol.backend)
			after, _ := os.ReadFile(journalPath)
			if mode != "original" {
				if err == nil || string(before) != string(after) {
					t.Fatal("unsafe initial migration reconciled or history changed", err)
				}
			} else {
				if err != nil {
					t.Fatal("exact unwritten initial migration could not reconcile", err)
				}
				var got deploymentplan.Journal
				if json.Unmarshal(after, &got) != nil {
					t.Fatal("invalid reconciled journal")
				}
				expected := f.journal
				expected.InFlight = ""
				if deploymentplan.Digest(got) != deploymentplan.Digest(expected) {
					t.Fatal("reconciliation changed known writes")
				}
				archive := filepath.Join(filepath.Dir(journalPath), "unwritten-create-"+deploymentplan.Digest(f.journal)+".json")
				raw, err := os.ReadFile(archive)
				if err != nil || string(raw) != string(before) {
					t.Fatal("unknown-write evidence was not archived exactly", err)
				}
				if err := recoverStagingInitialBillingWrite(t.Context(), f.cfg, f.store, &f.parent, f.e, protocol.backend); err != nil {
					t.Fatal("completed reconciliation was not repeatable", err)
				}
			}
			for _, call := range protocol.read(t).Calls {
				if !strings.HasPrefix(call, "get ") && !strings.HasPrefix(call, "exec ") {
					t.Fatalf("Billing reconciliation mutated native state: %s", call)
				}
			}
		})
	}
}

func stagingNativeOriginalCustody(t *testing.T, f *stagingWorkloadQualificationFixture, uid string) {
	t.Helper()
	share := base64.StdEncoding.EncodeToString([]byte("isolated-original-openbao-share"))
	raw := mustProviderRecoveryJSON(map[string]any{"unseal_keys_b64": []string{share}, "root_token": "isolated-original-root"})
	for path, value := range map[string][]byte{"openbao/init.json": raw, "openbao/unseal-key": []byte(share), "openbao/root-token": []byte("isolated-original-root")} {
		if err := f.store.write(path, value, false); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_STAGING_NATIVE_ORIGINAL_SHARE", share)
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(raw), "statefulset_uid": uid, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"})
	if err := f.e.BeginEntry("openbao-initialize", "openbao", receipt, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.RecordReceipt("openbao-initialize", receipt); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestStagingOpenBaoQualificationCompletesNativeRolloutWithOriginalCustody(t *testing.T) {
	for _, mode := range []string{"original", "pending-after-delete", "completed", "controller-replaced", "different-target", "changed-custody", "replacement-owner"} {
		t.Run(mode, func(t *testing.T) {
			ns := "video-cloud-staging-secrets"
			uid := "55555555-5555-4555-8555-555555555555"
			controller := stagingQualificationObject("StatefulSet", ns, "openbao", uid, deploymentplan.Object{"replicas": float64(1), "updateStrategy": deploymentplan.Object{"type": "OnDelete"}, "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "openbao:original"}}}}})
			deploymentplan.Map(controller["metadata"])["generation"] = float64(2)
			deploymentplan.Map(controller["metadata"])["resourceVersion"] = "2"
			target := deploymentplan.Resource{ID: deploymentplan.ID(controller), UID: uid, Phase: "workload", Action: "update", Desired: deploymentplan.Clone(controller), Baseline: deploymentplan.Clone(controller)}
			resources := stagingRuntimeQualificationResources()
			resources = append(resources[:1], append([]deploymentplan.Resource{target}, resources[1:]...)...)
			f := newStagingWorkloadQualificationFixture(t, resources)
			for i, r := range f.child.Resources {
				if r.Phase == "routing" {
					continue
				}
				owner := r.UID
				if owner == "" {
					owner = []string{"11111111-1111-4111-8111-111111111111", "55555555-5555-4555-8555-555555555555", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
				}
				f.journal.Completed = append(f.journal.Completed, r.ID)
				f.journal.Owners[r.ID] = owner
			}
			f.journal.Failed = "Deployment/" + f.parent.Stack + "-video-cloud/video-cloud-api"
			f.saveProgress(t, f.child, f.journal)
			runtimeStage := stagingWorkloadRuntimePrefix + f.child.SHA256()[:12]
			runtimeChild := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: f.child.Inputs, Resources: []deploymentplan.Resource{target}}
			runtimeChild.Resources[0].Action = "retain"
			runtimeChild.BindingsSHA256 = deploymentplan.Digest(runtimeStage)
			if mode == "different-target" {
				deploymentplan.Map(runtimeChild.Resources[0].Desired["spec"])["replicas"] = float64(2)
			}
			f.persistStage(t, runtimeStage, runtimeChild)
			controller["status"] = deploymentplan.Object{"observedGeneration": float64(2), "updateRevision": "target-revision", "currentRevision": "old-revision"}
			pod := stagingQualificationObject("Pod", ns, "openbao-0", "66666666-6666-4666-8666-666666666666", deploymentplan.Clone(environmentPKIPod(controller)))
			deploymentplan.Map(pod["metadata"])["resourceVersion"] = "7"
			deploymentplan.Map(pod["metadata"])["labels"] = deploymentplan.Object{"controller-revision-hash": "old-revision"}
			deploymentplan.Map(pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": uid, "controller": true}}
			pod["status"] = deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}
			pv := stagingQualificationObject("PersistentVolume", "", "original-openbao-volume", "77777777-7777-4777-8777-777777777777", deploymentplan.Object{"csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "501"}, "claimRef": deploymentplan.Object{"namespace": ns, "name": "data-openbao-0", "uid": "88888888-8888-4888-8888-888888888888"}})
			protocol := newStagingNativeRecoveryProtocol(t, f.store, map[string]deploymentplan.Object{target.ID: controller, deploymentplan.ID(pod): pod, deploymentplan.ID(pv): pv})
			stagingNativeOriginalCustody(t, f, uid)
			if mode == "changed-custody" {
				if err := f.store.write("openbao/unseal-key", []byte("replacement-share"), true); err != nil {
					t.Fatal(err)
				}
			}
			state := protocol.read(t)
			state.Mode = mode
			if mode == "controller-replaced" {
				deploymentplan.Map(state.Objects[target.ID]["metadata"])["uid"] = "foreign-controller"
			}
			key := "openbao/workload-native-rollout-" + f.child.SHA256()[:12]
			intent := mustProviderRecoveryJSON(deploymentplan.Object{"original_child": f.child.SHA256(), "controller_uid": uid, "update_revision": "target-revision"})
			if mode == "pending-after-delete" || mode == "completed" {
				deploymentplan.Map(deploymentplan.Map(state.Objects[deploymentplan.ID(pod)]["metadata"])["labels"])["controller-revision-hash"] = "target-revision"
				if err := f.e.BeginEntry(key, "workload", intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if mode == "completed" {
					if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"pod_uid": deploymentplan.Map(pod["metadata"])["uid"], "controller_uid": uid, "revision": "target-revision"})); err != nil {
						t.Fatal(err)
					}
					if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				} else {
					state.Sealed = true
				}
			}
			protocol.save(t, state)
			custody := map[string]string{}
			for _, name := range []string{"init.json", "unseal-key", "root-token"} {
				raw, err := os.ReadFile(filepath.Join(f.store.Root, "openbao", name))
				if err != nil {
					t.Fatal(err)
				}
				custody[name] = string(raw)
			}
			err := resumeStagingOriginalOpenBaoRollout(t.Context(), f.cfg, f.store, f.e, protocol.backend)
			success := mode == "original" || mode == "pending-after-delete" || mode == "completed"
			if success && err != nil || !success && err == nil {
				t.Fatalf("rollout outcome for %s: %v; calls=%v", mode, err, protocol.read(t).Calls)
			}
			after := protocol.read(t)
			if after.Leases != after.Releases {
				t.Fatal("native recovery leaked original execution lease")
			}
			if success {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
				if entry == nil || entry.State != "completed" {
					t.Fatal("native rollout did not complete original checkpoint")
				}
				wantDeletes, wantUnseals := 0, 0
				if mode == "original" {
					wantDeletes = 1
				}
				if mode != "completed" {
					wantUnseals = 1
				}
				if after.Deletes != wantDeletes || after.Unseals != wantUnseals {
					t.Fatalf("native deletes/unseals %d/%d, want %d/%d", after.Deletes, after.Unseals, wantDeletes, wantUnseals)
				}
				before := deploymentplan.Digest(*entry)
				if err := resumeStagingOriginalOpenBaoRollout(t.Context(), f.cfg, f.store, f.e, protocol.backend); err != nil {
					t.Fatal("completed native rollout did not verify", err)
				}
				repeated := protocol.read(t)
				if repeated.Deletes != after.Deletes || repeated.Unseals != after.Unseals || deploymentplan.Digest(*createOpenBaoACKCompletionEntry(f.e.Journal, key)) != before {
					t.Fatal("completed rollout replayed deletion/unseal")
				}
				if mode != "completed" {
					ledger, err := readEnvironmentCreateLedger(f.e)
					if err != nil || len(ledger.Volumes) != 1 || ledger.Volumes[0].NativeID != "501" || ledger.Volumes[0].PVCUID != "88888888-8888-4888-8888-888888888888" {
						t.Fatal("native rollout did not preserve original volume", err)
					}
				}
			} else if mode != "replacement-owner" && (after.Deletes != 0 || after.Unseals != 0) {
				t.Fatal("unsafe rollout changed native owner or unsealed")
			}
			for name, before := range custody {
				raw, _ := os.ReadFile(filepath.Join(f.store.Root, "openbao", name))
				if string(raw) != before {
					t.Fatal("native recovery replaced original custody")
				}
			}
		})
	}
}

func TestStagingAllocationQualificationForwardsOnlyOriginalPostgresAllocation(t *testing.T) {
	for _, mode := range []string{"original", "postgres-replaced", "claim-template-changed"} {
		t.Run(mode, func(t *testing.T) {
			uid := "55555555-5555-4555-8555-555555555555"
			postgres := stagingQualificationObject("StatefulSet", "video-cloud-staging-platform", "postgresql", uid, deploymentplan.Object{"replicas": float64(1), "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}, "spec": deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "20Gi"}}}}}})
			desired := deploymentplan.Clone(postgres)
			delete(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(desired["spec"])["volumeClaimTemplates"])[0])["spec"]), "storageClassName")
			resources := stagingRuntimeQualificationResources()
			resources = append(resources[:1], append([]deploymentplan.Resource{{ID: deploymentplan.ID(postgres), UID: uid, Phase: "workload", Action: "update", Baseline: postgres, Desired: desired}}, resources[1:]...)...)
			f := newStagingWorkloadQualificationFixture(t, resources)
			for i, r := range f.child.Resources {
				if r.Phase == "routing" {
					continue
				}
				owner := r.UID
				if owner == "" {
					owner = []string{"11111111-1111-4111-8111-111111111111", "55555555-5555-4555-8555-555555555555", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
				}
				f.journal.Completed = append(f.journal.Completed, r.ID)
				f.journal.Owners[r.ID] = owner
			}
			f.journal.Failed = "Deployment/" + f.parent.Stack + "-video-cloud/video-cloud-api"
			f.saveProgress(t, f.child, f.journal)
			initial, steady, err := stagingWorkloadRuntimeDefinitions(f.child, f.journal)
			if err != nil {
				t.Fatal(err)
			}
			f.acceptDefinitions(t, stagingLoggerInitPrefix+f.child.SHA256()[:12], initial)
			runtimeStage := stagingWorkloadRuntimePrefix + f.child.SHA256()[:12]
			accepted, _ := f.acceptDefinitions(t, runtimeStage, steady)
			actual := deploymentplan.Clone(postgres)
			if mode == "postgres-replaced" {
				deploymentplan.Map(actual["metadata"])["uid"] = "foreign"
			}
			if mode == "claim-template-changed" {
				deploymentplan.Map(actual["spec"])["volumeClaimTemplates"] = []any{}
			}
			backend := stagingQualificationBackend(t, f.store, map[string]deploymentplan.Object{deploymentplan.ID(postgres): actual})
			handled, err := applyStagingWorkloadAllocationForward(t.Context(), f.cfg, f.store, &f.parent, f.e, backend)
			if !handled {
				t.Fatal("original allocation continuation not recognized")
			}
			if mode != "original" {
				if err == nil {
					t.Fatal("foreign PostgreSQL allocation admitted")
				}
				return
			}
			if err != nil {
				t.Fatal("original allocation could not complete runtime continuation", err)
			}
			stage, err := freshCreateWorkloadStage(f.store, f.e)
			if err != nil || stage != runtimeStage {
				t.Fatal("completed allocation lost actual runtime successor", err)
			}
			var retained deploymentplan.Resource
			for _, r := range accepted.Resources {
				if r.ID == deploymentplan.ID(postgres) {
					retained = r
				}
			}
			if retained.Action != "retain" || retained.UID != uid || deploymentplan.Digest(deploymentplan.Map(retained.Desired["spec"])["volumeClaimTemplates"]) != deploymentplan.Digest(deploymentplan.Map(postgres["spec"])["volumeClaimTemplates"]) {
				t.Fatal("successor did not retain original PostgreSQL storage allocation")
			}
		})
	}
}

func TestStagingIssuerQualificationUsesOriginalNativeRegistryDuringHandoff(t *testing.T) {
	for _, mode := range []string{"original", "different-issuer", "different-environment", "invalid-id"} {
		t.Run(mode, func(t *testing.T) {
			f := stagingRuntimeQualificationFixture(t)
			id := "11111111-1111-4111-8111-111111111111"
			record := map[string]any{"issuer_id": id, "environment": "staging", "status": "active"}
			protocol := newStagingNativeRecoveryProtocol(t, f.store, map[string]deploymentplan.Object{})
			if mode == "different-issuer" {
				record["issuer_id"] = "22222222-2222-4222-8222-222222222222"
			}
			if mode == "different-environment" {
				record["environment"] = "dev"
			}
			if mode == "invalid-id" {
				id = "not-an-issuer' OR true"
			}
			state := protocol.read(t)
			state.Registry = record
			protocol.save(t, state)
			result, err := readStagingResumeIssuer(t.Context(), f.cfg, environmentCreateBootstrapRuntime{Execution: f.e, Backend: protocol.backend, StagingControllerHandoff: true}, id)
			if mode == "original" {
				if err != nil || deploymentplan.Digest(result) != deploymentplan.Digest(record) {
					t.Fatal("original issuer could not be read through temporary Controller handoff", err)
				}
			} else if err == nil {
				t.Fatal("foreign issuer qualified through native registry")
			}
			calls := protocol.read(t).Calls
			if mode == "invalid-id" {
				if len(calls) != 0 {
					t.Fatal("unvalidated issuer reached native SQL")
				}
				return
			}
			if len(calls) != 1 || !strings.Contains(calls[0], "SELECT document FROM pki_issuers WHERE id='11111111-1111-4111-8111-111111111111'") {
				t.Fatalf("registry query escaped original issuer scope: %v", calls)
			}
		})
	}
}

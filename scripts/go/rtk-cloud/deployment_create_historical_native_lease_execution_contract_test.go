package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type historicalNativeLeaseState struct {
	Lease   deploymentplan.Object `json:"lease"`
	Creates int                   `json:"creates"`
	Deletes int                   `json:"deletes"`
}

// Only the external Lease API is modeled. All other native protocols continue
// through their original endpoint. Production owns admission, UID/version
// preconditions, context fencing, parent receipts and bounded release.
func installHistoricalNativeLeaseProtocol(t *testing.T, f *acceptedCoreExecutionContract) (string, func()) {
	t.Helper()
	original, originalPath := os.Getenv("RTK_CLOUD_KUBECTL"), os.Getenv("PATH")
	dir := t.TempDir()
	statePath := filepath.Join(dir, "lease.json")
	if err := os.WriteFile(statePath, mustProviderRecoveryJSON(historicalNativeLeaseState{}), 0600); err != nil {
		t.Fatal(err)
	}
	literal := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	script := `#!/usr/bin/env python3
import hashlib,json,os,subprocess,sys
path=` + literal(statePath) + `
original=` + literal(original) + `
resource=` + literal("Lease/"+f.e.Journal.Stack+"-video-cloud/rtk-deployment-execution") + `
parent=` + literal(f.e.Journal.PlanSHA256) + `
with open(path) as source:s=json.load(source)
raw=sys.argv[1:];a=list(raw)
while a and a[0].startswith('--'):
 if a[0] in ('--kubeconfig','--namespace'):a=a[2:]
 else:a=a[1:]
def save():
 with open(path,'w') as target:json.dump(s,target)
def emit(value):print(json.dumps(value))
def fail(reason):sys.exit(reason)
if a[:1]==['get'] and a[1].lower()=='lease':
 ns=raw[raw.index('--namespace')+1] if '--namespace' in raw else ''
 if 'Lease/'+ns+'/'+a[2]!=resource:fail('foreign Lease selected')
 if s['lease'] is not None:emit(s['lease'])
 sys.exit(0)
if a[:1] in (['create'],['replace']):
 body=sys.stdin.read();obj=json.loads(body)
 if obj.get('kind')!='Lease':sys.exit(subprocess.run([original]+raw,input=body,text=True).returncode)
 meta=obj['metadata'];key='Lease/'+meta.get('namespace','')+'/'+meta['name']
 if key!=resource or obj.get('apiVersion')!='coordination.k8s.io/v1':fail('foreign Lease body')
 if '--dry-run=server' not in a:
  old=s['lease']
  if a[0]=='create':
   if old is not None:fail('native Lease already owned')
   s['creates']+=1;sha=hashlib.sha256((parent+str(s['creates'])).encode()).hexdigest()
   meta['uid']=sha[:8]+'-'+sha[8:12]+'-4'+sha[13:16]+'-8'+sha[17:20]+'-'+sha[20:32];meta['resourceVersion']='1'
  else:
   if old is None or meta.get('uid')!=old['metadata']['uid'] or meta.get('resourceVersion')!=old['metadata']['resourceVersion']:fail('native Lease write precondition differs')
   meta['resourceVersion']=str(int(meta['resourceVersion'])+1)
  s['lease']=obj;save()
 emit(obj);sys.exit(0)
if a[:1]==['delete'] and any('/leases/rtk-deployment-execution' in arg for arg in a):
 expected='--raw=/apis/coordination.k8s.io/v1/namespaces/'+resource.split('/')[1]+'/leases/rtk-deployment-execution'
 if expected not in a:fail('foreign Lease delete path')
 body=json.load(sys.stdin);old=s['lease']
 if old is None or body.get('preconditions')!={'uid':old['metadata']['uid'],'resourceVersion':old['metadata']['resourceVersion']}:fail('native Lease delete precondition differs')
 s['lease']=None;s['deletes']+=1;save();emit({'status':'Success'});sys.exit(0)
if not original:fail('unexpected non-Lease native operation')
os.execv(original,[original]+raw)
`
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath)
	return statePath, func() {
		t.Setenv("RTK_CLOUD_KUBECTL", original)
		t.Setenv("PATH", originalPath)
	}
}

// Historical admission is an explicit fixture input. This test qualifies only
// the native authorization lease adapter, including its callback and every
// durable outcome. It does not claim source qualification or policy issuance.
func TestHistoricalLateNativeAuthorizationPreservesActualLeaseAndParentFences(t *testing.T) {
	for _, mode := range []string{"complete", "native-failure", "lease-replaced", "changed-recipe", "foreign-stage", "foreign-owner", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, memory := freshStageFixture(t)
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
			f := &acceptedCoreExecutionContract{cfg: cfg, store: store, e: e, native: memory.normalTailE2EBackend, p: createFoundationRecoveryPlan{Plan: environmentCreatePlan{Environment: "dev", Stack: e.Journal.Stack}}}
			recipe := deploymentplan.Object{"policy_intent": deploymentplan.Object{"policy": "isolated-original-session", "policy_sha256": deploymentplan.Digest("reviewed native policy")}}
			review, _ := f.admitRecovery(t, managedLateSessionSchema, map[string]string{"accepted-service-core-late-session-native-late-seed-authorization": deploymentplan.Digest(recipe)})
			statePath, restore := installHistoricalNativeLeaseProtocol(t, f)
			defer restore()
			backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-original-kubernetes-access")}
			stage := "late-seed-authorization-" + review
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "changed-recipe":
				recipe["unreviewed"] = true
			case "foreign-stage":
				stage = "foreign-authorization-" + review
			case "foreign-owner":
				owner := e.owner
				owner.OwnerToken = "foreign"
				if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(owner), true); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			before := deploymentplan.Digest(e.Journal)
			nativeCalls := 0
			failure := errors.New("isolated native authorization failure")
			err := managedLateNativeLease(ctx, cfg, e, backend, review, stage, recipe, func(guarded context.Context) error {
				nativeCalls++
				if _, ok := guarded.Value(managedLateNativeGuardKey{}).(func() error); !ok || checkManagedLateNativeGuard(guarded) != nil {
					t.Fatal("native callback lost its actual lease guard")
				}
				if mode == "native-failure" {
					return failure
				}
				if mode == "lease-replaced" {
					var state historicalNativeLeaseState
					if err := readExecutionJSON(statePath, &state); err != nil || state.Lease == nil {
						t.Fatal("actual native lease missing", err)
					}
					deploymentplan.Map(state.Lease["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
					if err := os.WriteFile(statePath, mustProviderRecoveryJSON(state), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			var state historicalNativeLeaseState
			if readErr := readExecutionJSON(statePath, &state); readErr != nil {
				t.Fatal(readErr)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, stage)
			if mode == "complete" {
				if err != nil || nativeCalls != 1 || state.Creates != 1 || state.Deletes != 1 || state.Lease != nil || entry == nil || entry.State != "completed" || len(e.Journal.KubernetesLeases) != 1 || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(recipe) {
					t.Fatal("native completion lost actual receipt/lease lifecycle", err, nativeCalls, state.Creates, state.Deletes)
				}
			} else if mode == "native-failure" || mode == "lease-replaced" {
				if err == nil || nativeCalls != 1 || state.Creates != 1 || entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
					t.Fatal("unconfirmed native outcome became completed", err)
				}
				if mode == "native-failure" && (!errors.Is(err, failure) || state.Deletes != 1 || state.Lease != nil) || mode == "lease-replaced" && (state.Deletes != 0 || state.Lease == nil) {
					t.Fatal("failed operation leaked its owner or deleted a replacement", err)
				}
			} else if err == nil || nativeCalls != 0 || state.Creates != 0 || state.Deletes != 0 || entry != nil || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("unreviewed authorization reached a native callback or changed source", err)
			}
		})
	}
}

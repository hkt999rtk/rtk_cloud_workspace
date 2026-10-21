package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type nativeCreateStageContract struct {
	cfg                 deploymentConfig
	store               secretStore
	e                   *environmentCreateExecution
	parent              environmentCreatePlan
	definitions         []deploymentplan.Definition
	state, architecture string
}

type nativeCreateStageState struct {
	Objects                               map[string]deploymentplan.Object `json:"objects"`
	Mode                                  string                           `json:"mode"`
	Writes, Leases, Releases, VolumeReads int
}

// The admission is an explicit isolated parent-stage boundary. Configuration,
// private operator inputs, original child evidence and every execution fence
// are real. Only Kubernetes API responses are supplied by the native protocol.
func newNativeCreateStageContract(t *testing.T, mode string, setup ...func(string)) *nativeCreateStageContract {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	architecture := filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	writeTestFile(t, architecture, readTestFile(t, architecture)+"\nDEPLOYMENT_PROTECTION_PROFILE=standard\n")
	for _, configure := range setup {
		configure(architecture)
	}
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, e, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"LINODE_TOKEN": "isolated-native-stage-operator"}, kubeconfig: raw})
	if fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot); err != nil || deploymentplan.Digest(fresh) != parent.ConfigurationSHA256 {
		t.Fatal("original maintained configuration", err)
	}
	if _, operator, err := readEnvironmentCreateOperator(cfg); err != nil || deploymentplan.Digest(operator) != parent.OperatorSHA256 {
		t.Fatal("original canonical operator", err)
	}
	ns := parent.Stack + "-video-cloud"
	body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "isolated-stage-input", "namespace": ns}, "data": deploymentplan.Object{"selected": "original"}}
	namespace := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns, "uid": "11111111-1111-4111-8111-111111111111", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": parent.Stack}}}
	f := &nativeCreateStageContract{cfg: cfg, store: store, e: e, parent: parent, definitions: []deploymentplan.Definition{{Phase: "configuration", Object: body}}, state: filepath.Join(t.TempDir(), "native.json"), architecture: architecture}
	f.save(t, nativeCreateStageState{Objects: map[string]deploymentplan.Object{deploymentplan.ID(namespace): namespace}, Mode: mode})
	// Read/modify/write API transitions are serialized just as they are at the
	// server. Inventory is filtered by the actual requested kind and namespace;
	// an unknown operation fails instead of silently reporting successful access.
	script := `#!/usr/bin/env python3
import fcntl,json,os,sys,uuid
p=os.environ['RTK_NATIVE_CREATE_STAGE_STATE']
with open(p+'.lock','a') as lock:
 fcntl.flock(lock,fcntl.LOCK_EX)
 with open(p) as src:d=json.load(src)
 b=list(sys.argv[1:])
 while b and b[0].startswith('--'):
  if b[0]=='--kubeconfig':b=b[2:]
  else:b=b[1:]
 def save():
  with open(p,'w') as dst:json.dump(d,dst)
 def emit(o):print(json.dumps(o))
 if not b:sys.exit(2)
 if b[0]=='get':
  kind=b[1];ns=b[b.index('--namespace')+1] if '--namespace' in b else ''
  if len(b)>2 and not b[2].startswith('-'):
   key=kind+'/'+ns+'/'+b[2]
   if key in d['objects']:emit(d['objects'][key])
   sys.exit()
  if kind=='persistentvolumes':
   d['VolumeReads']+=1
   if d['mode']=='capture-failure':
    d['mode']='normal';save();emit({'items':None});sys.exit()
  kinds=kind.lower().split(',');kinds=['persistentvolume' if k=='persistentvolumes' else k for k in kinds]
  selector=b[b.index('-l')+1].split('=',1) if '-l' in b else None
  items=[]
  for o in d['objects'].values():
   m=o['metadata']
   if o['kind'].lower() not in kinds or m.get('namespace','')!=ns:continue
   if selector and m.get('labels',{}).get(selector[0])!=selector[1]:continue
   items.append(o)
  save();emit({'apiVersion':'v1','kind':'List','items':items});sys.exit()
 if b[0] in ('create','replace'):
  o=json.load(sys.stdin);m=o['metadata'];key=o['kind']+'/'+m.get('namespace','')+'/'+m['name'];dry='--dry-run=server' in b
  assert o['kind'] in ('Lease','ConfigMap')
  assert 'Namespace//'+m['namespace'] in d['objects']
  old=d['objects'].get(key)
  if b[0]=='create':assert old is None
  else:assert old and m['uid']==old['metadata']['uid'] and m['resourceVersion']==old['metadata']['resourceVersion']
  if not dry:
   m.setdefault('uid',str(uuid.uuid4()));m['resourceVersion']=str(int(m.get('resourceVersion','0'))+1)
   d['objects'][key]=o
   d['Leases' if o['kind']=='Lease' else 'Writes']+=1
   save()
   if o['kind']=='ConfigMap' and d['mode']=='response-lost':sys.exit(1)
  emit(o);sys.exit()
 if b[0]=='delete':
  request=json.load(sys.stdin);leases=[k for k in d['objects'] if k.startswith('Lease/')];assert len(leases)==1
  key=leases[0];m=d['objects'][key]['metadata'];pre=request['preconditions']
  expected='--raw=/apis/coordination.k8s.io/v1/namespaces/'+m['namespace']+'/leases/'+m['name']
  assert b==['delete',expected,'-f','-'] and request['apiVersion']=='v1' and request['kind']=='DeleteOptions'
  assert pre['uid']==m['uid'] and pre['resourceVersion']==m['resourceVersion']
  del d['objects'][key];d['Releases']+=1;save();emit({'status':'Success'});sys.exit()
 print('unsupported isolated native operation: '+repr(b),file=sys.stderr);sys.exit(2)
`
	bin := t.TempDir()
	path := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_NATIVE_CREATE_STAGE_STATE", f.state)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f *nativeCreateStageContract) read(t *testing.T) nativeCreateStageState {
	t.Helper()
	raw, err := os.ReadFile(f.state)
	if err != nil {
		t.Fatal(err)
	}
	var state nativeCreateStageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func (f *nativeCreateStageContract) save(t *testing.T, state nativeCreateStageState) {
	t.Helper()
	if err := os.WriteFile(f.state, mustProviderRecoveryJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *nativeCreateStageContract) apply(t *testing.T) error {
	t.Helper()
	return applyEnvironmentCreateStage(t.Context(), f.cfg, f.store, &f.parent, f.e, "native-contract", f.definitions, io.Discard)
}
func (f *nativeCreateStageContract) accepted(t *testing.T) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/native-contract")
	if err != nil || len(p.Resources) != 1 || len(j.Owners) != 1 || len(j.Completed) != 1 || !j.Accepted {
		t.Fatal("actual accepted child", err)
	}
	state := f.read(t)
	if state.Writes != 1 || state.Leases == 0 || state.Leases != state.Releases || len(state.Objects) != 2 || state.Objects[p.Resources[0].ID] == nil || j.Owners[p.Resources[0].ID] != deploymentplan.Map(state.Objects[p.Resources[0].ID]["metadata"])["uid"] {
		t.Fatalf("native write/lease/owner mismatch: %+v", state)
	}
	if err := f.e.Check(t.Context()); err != nil {
		t.Fatal("original parent fence", err)
	}
	return p, j
}

func TestCreateStageNativeWrapperAcceptsAndRevalidatesKnownOwner(t *testing.T) {
	f := newNativeCreateStageContract(t, "normal")
	if err := f.apply(t); err != nil {
		t.Fatal("actual native stage", err)
	}
	p, j := f.accepted(t)
	initial := f.read(t)
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil || initial.VolumeReads != 1 || ledger.UpdatedAt.IsZero() || len(ledger.Volumes) != 0 {
		t.Fatal("actual empty native volume capture", err)
	}
	receipt := append([]byte(nil), createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/native-contract").Receipt...)
	originalInput, err := os.ReadFile(environmentCreateStageInputPath(f.e, "native-contract"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unchanged", "restored-derived-input"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "restored-derived-input" {
				if err := os.Remove(environmentCreateStageInputPath(f.e, "native-contract")); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.apply(t); err != nil {
				t.Fatal("completed native retry", err)
			}
			again, actual := f.accepted(t)
			if again.SHA256() != p.SHA256() || deploymentplan.Digest(actual.Owners) != deploymentplan.Digest(j.Owners) || f.read(t).VolumeReads != initial.VolumeReads || !bytes.Equal(receipt, createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/native-contract").Receipt) {
				t.Fatal("completed retry replayed work or changed its original receipt")
			}
			restored, err := os.ReadFile(environmentCreateStageInputPath(f.e, "native-contract"))
			if err != nil || !bytes.Equal(originalInput, restored) {
				t.Fatal("derived input restoration lost original plan", err)
			}
		})
	}
	for _, mode := range []string{"configuration", "operator", "definitions", "native-uid", "native-body"} {
		t.Run(mode, func(t *testing.T) {
			before := f.read(t)
			oldDefs := f.definitions
			switch mode {
			case "configuration":
				original := readTestFile(t, f.architecture)
				writeTestFile(t, f.architecture, original+"\nDEPLOYMENT_UPGRADE_WINDOW_SECONDS=3601\n")
				t.Cleanup(func() { writeTestFile(t, f.architecture, original) })
			case "operator":
				path := filepath.Join(f.store.Root, "operator/env/LINODE_TOKEN")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("changed-isolated-operator"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
				})
			case "definitions":
				f.definitions = []deploymentplan.Definition{{Phase: oldDefs[0].Phase, Object: deploymentplan.Clone(oldDefs[0].Object)}}
				deploymentplan.Map(f.definitions[0].Object["data"])["selected"] = "changed"
				t.Cleanup(func() { f.definitions = oldDefs })
			default:
				changed := f.read(t)
				object := changed.Objects[p.Resources[0].ID]
				if mode == "native-uid" {
					deploymentplan.Map(object["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
				} else {
					deploymentplan.Map(object["data"])["selected"] = "changed"
				}
				f.save(t, changed)
				t.Cleanup(func() { current := f.read(t); current.Objects = before.Objects; f.save(t, current) })
			}
			err := f.apply(t)
			cause := map[string]string{"configuration": "maintained intent or operator credentials changed", "operator": "maintained intent or operator credentials changed", "definitions": "stage changed its qualified original input", "native-uid": "deployment.drift", "native-body": "deployment.drift"}[mode]
			if err == nil || !strings.Contains(err.Error(), cause) {
				t.Fatalf("%s did not refuse at the actual boundary: %v", mode, err)
			}
			if current := f.read(t); current.Writes != before.Writes || current.Leases != current.Releases {
				t.Fatal("refused drift changed native writes or leaked lease")
			}
			if !bytes.Equal(receipt, createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/native-contract").Receipt) {
				t.Fatal("refused drift changed accepted parent receipt")
			}
		})
	}
	if err := f.apply(t); err != nil {
		t.Fatal("restored original inputs should still revalidate", err)
	}
	f.accepted(t)
}

func TestCreateStageNativeWrapperResumesAcceptedChildAfterCaptureFailure(t *testing.T) {
	f := newNativeCreateStageContract(t, "capture-failure")
	if err := f.apply(t); err == nil || !strings.Contains(err.Error(), "persistent volume inventory is incomplete") {
		t.Fatal("actual post-acceptance interruption", err)
	}
	p, err := readEnvironmentCreateStageInput(f.store, f.e, "native-contract")
	if err != nil || p == nil {
		t.Fatal(err)
	}
	j, exists, err := p.ReadExecution(f.store.Root)
	if err != nil || !exists || !j.Accepted || len(j.Owners) != 1 || createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/native-contract").State != "allocated" {
		t.Fatal("interruption did not retain exact accepted child and allocated parent", err)
	}
	original := deploymentplan.Digest(f.read(t).Objects)
	if err := f.apply(t); err != nil {
		t.Fatal("resume original captured write", err)
	}
	_, accepted := f.accepted(t)
	if deploymentplan.Digest(f.read(t).Objects) != original || deploymentplan.Digest(j.Owners) != deploymentplan.Digest(accepted.Owners) || f.read(t).VolumeReads != 2 {
		t.Fatal("capture retry replayed known write or skipped fresh capture")
	}
}

func TestCreateStageNativeWrapperRefusesUnknownPersistedWrite(t *testing.T) {
	f := newNativeCreateStageContract(t, "response-lost")
	if err := f.apply(t); err == nil {
		t.Fatal("lost actual create response was accepted")
	}
	before := f.read(t)
	if before.Writes != 1 || before.Leases != before.Releases {
		t.Fatal("native write interruption not observed")
	}
	p, err := readEnvironmentCreateStageInput(f.store, f.e, "native-contract")
	if err != nil || p == nil {
		t.Fatal(err)
	}
	// The API persisted the object but its response never supplied an owner
	// receipt. The real resumable reader must not infer one from a matching body.
	raw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var interrupted deploymentplan.Journal
	if json.Unmarshal(raw, &interrupted) != nil || interrupted.InFlight != p.Resources[0].ID || len(interrupted.Owners) != 0 || interrupted.Accepted {
		t.Fatal("unknown write was converted into a known owner")
	}
	if err := f.apply(t); err == nil || !strings.Contains(err.Error(), "deployment.recovery") {
		t.Fatal("unknown persisted owner was adopted", err)
	}
	after := f.read(t)
	if after.Writes != 1 || deploymentplan.Digest(after.Objects) != deploymentplan.Digest(before.Objects) || after.Leases != before.Leases {
		t.Fatal("unknown outcome retry performed native operations")
	}
	final, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json"))
	if err != nil || !bytes.Equal(raw, final) {
		t.Fatal("unknown outcome retry fabricated original evidence", err)
	}
	if err := f.e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

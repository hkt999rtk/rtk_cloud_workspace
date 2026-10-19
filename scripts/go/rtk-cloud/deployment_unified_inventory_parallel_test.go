package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type inventoryProcessCall struct {
	Kind, Namespace string
	PID             int
}

type inventoryProcessState struct {
	Calls                     []inventoryProcessCall
	Active, Maximum, Finished int
}

type inventoryProcessFixture struct {
	backend    environmentKubernetesBackend
	stack      string
	namespaces []string
	statePath  string
	release    string
}

func nativeInventoryProcessFixture(t *testing.T, environment, mode string) inventoryProcessFixture {
	t.Helper()
	if environmentDeploymentCheckConcurrency != 12 {
		t.Fatal("native independent query groups must share the limit of twelve")
	}
	kubeconfig, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	makeIsolatedTestSecretStore(t, environment)
	backend, err := newEnvironmentKubernetesBackend(string(kubeconfig), environment)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	fixture := inventoryProcessFixture{backend: backend, stack: "video-cloud-" + environment, statePath: filepath.Join(directory, "state.json"), release: filepath.Join(directory, "release")}
	for _, suffix := range []int{7, 0, 6, 1, 4, 2, 5, 3, 8, 16, 9, 15, 10, 14, 11, 13, 12} {
		fixture.namespaces = append(fixture.namespaces, fmt.Sprintf("%s-ns-%02d", fixture.stack, suffix))
	}
	writeTestFile(t, fixture.statePath, `{"Calls":[],"Active":0,"Maximum":0,"Finished":0}`)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	stub := `import fcntl,hashlib,json,os,stat,sys,time
from pathlib import Path
sys.stderr=open(os.environ['RTK_INVENTORY_PROCESS_ERRORS'],'a')
args=sys.argv[1:]
assert args[0]=='--kubeconfig'
identity=Path(args[1])
root=Path(os.environ['RTK_INVENTORY_PROCESS_ROOT'])
assert identity.parent.parent==root/'deployment'/'tmp'
assert identity.name=='kubeconfig.yaml'
assert stat.S_IMODE(identity.stat().st_mode)==0o600
assert stat.S_IMODE(identity.parent.stat().st_mode)==0o700
assert hashlib.sha256(identity.read_bytes()).hexdigest()==os.environ['RTK_INVENTORY_PROCESS_IDENTITY']
assert args[2]=='--request-timeout=30s'
args=args[3:]
stack=os.environ['RTK_INVENTORY_PROCESS_STACK']
namespaces=json.loads(os.environ['RTK_INVENTORY_PROCESS_NAMESPACES'])
statepath=os.environ['RTK_INVENTORY_PROCESS_STATE']
mode=os.environ['RTK_INVENTORY_PROCESS_MODE']
def update(kind,namespace='',finish=False):
 with open(statepath,'r+') as stream:
  fcntl.flock(stream,fcntl.LOCK_EX)
  state=json.load(stream)
  if finish:
   state['Active']-=1
   state['Finished']+=1
  else:
   state['Calls'].append({'Kind':kind,'Namespace':namespace,'PID':os.getpid()})
   if namespace:
    state['Active']+=1
    state['Maximum']=max(state['Maximum'],state['Active'])
   elif kind!='namespace':
    assert state['Active']==0
    assert state['Finished']==len(namespaces)
  stream.seek(0)
  json.dump(state,stream)
  stream.truncate()
  stream.flush()
  fcntl.flock(stream,fcntl.LOCK_UN)
def obj(kind,name,namespace=''):
 metadata={'name':name,'uid':'original-'+name,'resourceVersion':'4'}
 if namespace: metadata['namespace']=namespace
 return {'apiVersion':'v1','kind':kind,'metadata':metadata}
if args==['get','namespace','-o','json']:
 update('namespace')
 names=['unrelated']+namespaces+[stack+'x-foreign']
 names += ['video-cloud-'+env+'-foreign' for env in ['dev','staging','prod'] if stack!='video-cloud-'+env]
 print(json.dumps({'items':[obj('Namespace',name) for name in names]}))
elif args[:2]==['get',os.environ['RTK_INVENTORY_PROCESS_NAMESPACED_KINDS']]:
 assert len(args)==6 and args[2]=='--namespace' and args[4:]==['-o','json']
 namespace=args[3]
 assert namespace in namespaces
 index=namespaces.index(namespace)
 update('namespaced',namespace)
 while not Path(os.environ['RTK_INVENTORY_PROCESS_RELEASE']).exists(): time.sleep(0.01)
 if mode in ['failure','invalid']:
  time.sleep(0.01 if index==1 else 0.2 if index==0 else 0.3)
 else:
  time.sleep(0.12 if index==0 else 0.01)
 update('namespaced',namespace,True)
 if mode=='failure' and index in [0,1]:
  print('private-inventory-marker')
  sys.stderr.write('private-inventory-marker')
  sys.exit(1)
 if mode=='invalid' and index==1:
  print('private-inventory-marker')
 else:
  print(json.dumps({'items':[obj('ConfigMap','first',namespace),obj('ConfigMap','second',namespace)]}))
elif args==['get',os.environ['RTK_INVENTORY_PROCESS_CLUSTER_KINDS'],'-l','rtk.realtek.com/stack='+stack,'-o','json']:
 update('cluster')
 print(json.dumps({'items':[obj('ClusterRole','selected-cluster-role')]}))
elif args==['get','validatingwebhookconfiguration','-o','json']:
 update('webhook')
 hook=obj('ValidatingWebhookConfiguration','selected-ingress-admission')
 hook['webhooks']=[{'clientConfig':{'service':{'namespace':stack+'-ingress','name':'ingress-nginx-controller-admission'}}}]
 foreign=obj('ValidatingWebhookConfiguration','foreign-ingress-admission')
 foreign['webhooks']=[{'clientConfig':{'service':{'namespace':'foreign-ingress','name':'ingress-nginx-controller-admission'}}}]
 print(json.dumps({'items':[foreign,hook]}))
else:
 raise Exception('unexpected inventory request')
`
	script := filepath.Join(directory, "kubectl.py")
	writeTestFile(t, script, stub)
	path := filepath.Join(directory, "kubectl")
	writeTestFile(t, path, "#!/bin/sh\nexec "+shellQuote(python)+" "+shellQuote(script)+" \"$@\"\n")
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_INVENTORY_PROCESS_ROOT", backend.Store.Root)
	digest := sha256.Sum256(backend.KubeconfigContent)
	t.Setenv("RTK_INVENTORY_PROCESS_IDENTITY", hex.EncodeToString(digest[:]))
	t.Setenv("RTK_INVENTORY_PROCESS_STACK", fixture.stack)
	names, _ := json.Marshal(fixture.namespaces)
	t.Setenv("RTK_INVENTORY_PROCESS_NAMESPACES", string(names))
	t.Setenv("RTK_INVENTORY_PROCESS_STATE", fixture.statePath)
	t.Setenv("RTK_INVENTORY_PROCESS_RELEASE", fixture.release)
	t.Setenv("RTK_INVENTORY_PROCESS_MODE", mode)
	t.Setenv("RTK_INVENTORY_PROCESS_NAMESPACED_KINDS", environmentNamespacedInventoryKinds)
	t.Setenv("RTK_INVENTORY_PROCESS_CLUSTER_KINDS", environmentClusterInventoryKinds)
	errorPath := filepath.Join(directory, "fixture-errors")
	t.Setenv("RTK_INVENTORY_PROCESS_ERRORS", errorPath)
	t.Cleanup(func() {
		if t.Failed() {
			raw, _ := os.ReadFile(errorPath)
			t.Log(string(raw))
		}
	})
	return fixture
}

func (f inventoryProcessFixture) state(t *testing.T) inventoryProcessState {
	t.Helper()
	raw, err := os.ReadFile(f.statePath)
	var state inventoryProcessState
	if err != nil || json.Unmarshal(raw, &state) != nil {
		t.Fatal("cannot read completed inventory process fixture", err)
	}
	return state
}

func (f inventoryProcessFixture) waitForLimit(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(f.statePath)
		var state inventoryProcessState
		if json.Unmarshal(raw, &state) == nil && state.Active == environmentDeploymentCheckConcurrency {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("namespace inventory did not overlap twelve independent reads")
}

func (f inventoryProcessFixture) assertJoined(t *testing.T) {
	t.Helper()
	for _, call := range f.state(t).Calls {
		if err := syscall.Kill(call.PID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("inventory returned while an API subprocess remained alive", call, err)
		}
	}
	assertPlanAccessPrivateFilesRemoved(t, f.backend)
}

func TestNativeInventoryParallelNamespacesPreserveOrderAndScope(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := nativeInventoryProcessFixture(t, environment, "success")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			type result struct {
				objects []deploymentplan.Object
				err     error
			}
			done := make(chan result, 1)
			go func() {
				objects, err := fixture.backend.Inventory(ctx, fixture.stack)
				done <- result{objects, err}
			}()
			fixture.waitForLimit(t)
			writeTestFile(t, fixture.release, "release")
			finished := <-done
			if finished.err != nil {
				t.Fatal(finished.err)
			}
			var want, got []string
			for _, namespace := range fixture.namespaces {
				want = append(want, "Namespace//"+namespace, "ConfigMap/"+namespace+"/first", "ConfigMap/"+namespace+"/second")
			}
			want = append(want, "ClusterRole//selected-cluster-role", "ValidatingWebhookConfiguration//selected-ingress-admission")
			for _, object := range finished.objects {
				got = append(got, deploymentplan.ID(object))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("namespace order, selected stack or existing cluster/webhook sequence changed:\ngot %v\nwant %v", got, want)
			}
			state := fixture.state(t)
			if state.Maximum != environmentDeploymentCheckConcurrency || state.Active != 0 || state.Finished != len(fixture.namespaces) || len(state.Calls) != len(fixture.namespaces)+3 || state.Calls[len(state.Calls)-2].Kind != "cluster" || state.Calls[len(state.Calls)-1].Kind != "webhook" {
				t.Fatal("inventory exceeded its bound, skipped scope or overlapped dependent cluster checks", state)
			}
			fixture.assertJoined(t)
		})
	}
}

func TestNativeInventoryParallelFailureStopsDispatchAndJoins(t *testing.T) {
	for _, mode := range []string{"failure", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			fixture := nativeInventoryProcessFixture(t, "dev", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := fixture.backend.Inventory(ctx, fixture.stack); done <- err }()
			fixture.waitForLimit(t)
			writeTestFile(t, fixture.release, "release")
			err := <-done
			want := "deployment.inventory: cannot enumerate resources in " + fixture.namespaces[0]
			if mode == "invalid" {
				want = "cannot decode complete environment inventory"
			}
			if err == nil || err.Error() != want || strings.Contains(err.Error(), "private-inventory-marker") {
				t.Fatal("inventory error lost original namespace order or exposed private output", err)
			}
			state := fixture.state(t)
			if len(state.Calls) != environmentDeploymentCheckConcurrency+1 || state.Active != 0 || state.Finished != environmentDeploymentCheckConcurrency {
				t.Fatal("failed inventory dispatched additional namespace/cluster reads or failed to join active calls", state)
			}
			fixture.assertJoined(t)
		})
	}
}

func TestNativeInventoryParallelCancellationJoinsBeforeReturn(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := nativeInventoryProcessFixture(t, environment, "cancel")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := fixture.backend.Inventory(ctx, fixture.stack); done <- err }()
			fixture.waitForLimit(t)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("inventory cancellation was not preserved", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled inventory left active API processes")
			}
			if state := fixture.state(t); len(state.Calls) != environmentDeploymentCheckConcurrency+1 {
				t.Fatal("cancelled inventory dispatched more namespace/cluster requests", state)
			}
			fixture.assertJoined(t)
		})
	}
}

func TestNativeInventoryProgressShowsParallelQueryTargets(t *testing.T) {
	fixture := nativeInventoryProcessFixture(t, "dev", "success")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runEnvironmentProgressInterval(ctx, &out, "deployment.inventory.read", 5*time.Millisecond, func(ctx context.Context) error {
			_, err := fixture.backend.Inventory(ctx, fixture.stack)
			return err
		})
	}()
	fixture.waitForLimit(t)
	// Keep all real mock queries active long enough to observe a heartbeat.
	time.Sleep(20 * time.Millisecond)
	writeTestFile(t, fixture.release, "release")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"active=12", "Inspect resource inventory Namespace//" + fixture.stack + "-ns-", "+9 more", "queries-finished="} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("parallel query work was hidden from the heartbeat", want, out.String())
		}
	}
	if strings.Contains(out.String(), "private-frozen-token") || strings.Contains(out.String(), environmentNamespacedInventoryKinds) {
		t.Fatal("inventory progress printed private data or raw command arguments")
	}
	fixture.assertJoined(t)
}

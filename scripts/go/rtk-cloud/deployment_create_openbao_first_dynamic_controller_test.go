package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// isolatedOpenBaoFirstController is a test-only loopback process bridge. It
// creates only an explicitly synthetic Pod observation after verifying a real
// process and TLS status. It does not create PVCs, ACKs or journal entries;
// the observation is not evidence of real Kubernetes or CSI behavior.
type isolatedOpenBaoFirstController struct {
	t                                                                           *testing.T
	listener                                                                    net.Listener
	capability, namespace, objects                                              string
	cfg                                                                         deploymentConfig
	store                                                                       secretStore
	mu                                                                          sync.Mutex
	rpc                                                                         *isolatedOpenBaoRPC
	cleanup                                                                     func()
	podUID                                                                      string
	volumeRoot                                                                  string
	volumeParent                                                                string
	mockVolumes                                                                 map[string]isolatedOpenBaoMockVolume
	redisVolumeRoot, redisPVCUID, redisPVUID                                    string
	foundationNetwork, foundationOwner, postgresVolumeRoot                      string
	postgresPVCUID, postgresPVUID, redisReplicaUID, redisPodUID, postgresPodUID string
	foundationProcesses                                                         map[string]isolatedFoundationProcess
}
type isolatedOpenBaoFirstRequest struct {
	Capability string `json:"capability"`
	Action     string `json:"action"`
	Namespace  string `json:"namespace"`
	Script     string `json:"script,omitempty"`
	Input      string `json:"input,omitempty"`
	ReadOnly   bool   `json:"read_only,omitempty"`
}

func newIsolatedOpenBaoFirstController(t *testing.T, cfg deploymentConfig, store secretStore, objects string) *isolatedOpenBaoFirstController {
	t.Helper()
	if cfg.Environment != "dev" || cfg.Environment != store.Environment || !isolatedScriptTempPath(store.Root) || !isolatedScriptTempPath(objects) {
		t.Fatal("only isolated test paths are allowed")
	}
	if err := os.Chmod(objects, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	volumeParent := t.TempDir()
	if err := os.Chmod(volumeParent, 0700); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	c := &isolatedOpenBaoFirstController{t: t, volumeParent: volumeParent, listener: listener, capability: hex.EncodeToString(nonce[:]), namespace: cfg.Values["CLOUD_STACK_NAME"] + "-secrets", objects: objects, cfg: cfg, store: store}
	go c.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cleanup != nil {
			c.cleanup()
			c.cleanup = nil
		}
	})
	return c
}
func (c *isolatedOpenBaoFirstController) serve() {
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		go c.handle(conn)
	}
}
func (c *isolatedOpenBaoFirstController) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	var request isolatedOpenBaoFirstRequest
	decoder := json.NewDecoder(io.LimitReader(conn, 2<<20))
	decoder.DisallowUnknownFields()
	response := isolatedOpenBaoRPCResponse{Code: 1, Error: true}
	if decoder.Decode(&request) != nil || request.Capability != c.capability || ((request.Action == "redis-pvc" || request.Action == "redis-pvc-read" || request.Action == "foundation-start" || request.Action == "foundation-read") && request.Namespace != c.cfg.Values["CLOUD_STACK_NAME"]+"-platform") || (request.Action != "redis-pvc" && request.Action != "redis-pvc-read" && request.Action != "foundation-start" && request.Action != "foundation-read" && request.Namespace != c.namespace) {
		_ = json.NewEncoder(conn).Encode(response)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	defer cancel()
	switch request.Action {
	case "start":
		if request.Script != "" || request.Input != "" {
			break
		}
		newProcess := c.rpc == nil
		if c.rpc == nil {
			load := func(kind, name string) (deploymentplan.Object, error) {
				var obj deploymentplan.Object
				path := filepath.Join(c.objects, kind+"_"+c.namespace+"_"+name+".json")
				err := readExecutionJSON(path, &obj)
				return obj, err
			}
			set, e1 := load("StatefulSet", "openbao")
			secret, e2 := load("Secret", "openbao-transport-tls")
			config, e3 := load("ConfigMap", "openbao-file")
			if e1 != nil || e2 != nil || e3 != nil {
				c.t.Log("isolated OpenBao controller: stage-object-read")
				break
			}
			container, owner, volumeRoot, cleanup, err := launchIsolatedOpenBaoFromOriginalStage(ctx, c.cfg, c.store, c.volumeParent, set, secret, config)
			if err != nil {
				c.t.Log("isolated OpenBao controller:", err.Error()) // fixed test-only stage labels only
				break
			}
			docker, err := exec.LookPath("docker")
			if err != nil {
				cleanup()
				break
			}
			rpc := &isolatedOpenBaoRPC{capability: c.capability, namespace: c.namespace, owner: owner, container: container, docker: docker, allowed: isolatedOpenBaoInitializerScripts()}
			if !rpc.containerOwned() {
				c.t.Log("isolated OpenBao controller: stage-container-owner")
				cleanup()
				break
			}
			c.rpc, c.cleanup, c.volumeRoot = rpc, cleanup, volumeRoot
		}
		// This result only represents the real isolated process and TLS status,
		// not Kubernetes rollout or PV/CSI readiness.
		status := isolatedOpenBaoInitializerScripts()
		var script string
		for s := range status {
			if strings.HasSuffix(s, "result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]") {
				script = s
				break
			}
		}
		if script == "" {
			break
		}
		ready := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			r := c.forward(ctx, isolatedOpenBaoRPCRequest{Capability: c.capability, Namespace: c.namespace, Script: script})
			var current struct {
				Initialized *bool `json:"initialized"`
				Sealed      *bool `json:"sealed"`
			}
			payload, err := base64.StdEncoding.DecodeString(r.Output)
			if !r.Error && r.Code == 0 && err == nil && json.Unmarshal(payload, &current) == nil && current.Initialized != nil && current.Sealed != nil && (!newProcess || !*current.Initialized && *current.Sealed) {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				break
			case <-time.After(200 * time.Millisecond):
			}
		}
		if !ready {
			c.t.Log("isolated OpenBao controller: stage-tls-status")
		} else if err := c.observeRealProcess(); err != nil {
			c.t.Log("isolated OpenBao controller: stage-observation")
		} else {
			response = isolatedOpenBaoRPCResponse{Code: 0, Error: false}
		}
	case "openbao-read":
		if request.Script == "" && request.Input == "" && c.verifyOpenBaoReadOnly(ctx) == nil {
			response = isolatedOpenBaoRPCResponse{Code: 0, Error: false}
		}
	case "foundation-read":
		if request.Script == "" && request.Input == "" && c.verifyFoundationReadOnly(ctx) == nil {
			response = isolatedOpenBaoRPCResponse{Code: 0, Error: false}
		}
	case "foundation-start":
		if request.Script == "" && request.Input == "" {
			if err := c.observeFoundationProcesses(ctx); err != nil {
				c.t.Log("isolated foundation:", err.Error())
			} else {
				response = isolatedOpenBaoRPCResponse{Code: 0, Error: false}
			}
		}
	case "redis-pvc", "redis-pvc-read":
		if request.Script == "" && request.Input == "" && c.observeMockRedisClaim(request.Action == "redis-pvc-read") == nil {
			response = isolatedOpenBaoRPCResponse{Code: 0, Error: false}
		}
	case "exec":
		if c.rpc == nil {
			break
		}
		if c.rpc.allowed[request.Script] {
			if request.ReadOnly && !isolatedOpenBaoInitializerReadOnly(request.Script) {
				break
			}
			response = c.forward(ctx, isolatedOpenBaoRPCRequest{Capability: c.capability, Namespace: c.namespace, Script: request.Script, Input: request.Input})
			break
		}
		custodyAction, allowed := c.authorizeIsolatedCustodyScript(ctx, request)
		if !allowed {
			c.t.Log("isolated custody: request-denied")
			break
		}
		// Authorization is scoped to this single exact request; never leave a
		// dynamically authorized write in the persistent initializer allowlist.
		c.rpc.allowed[request.Script] = true
		response = c.forward(ctx, isolatedOpenBaoRPCRequest{Capability: c.capability, Namespace: c.namespace, Script: request.Script, Input: request.Input})
		if response.Error || response.Code != 0 {
			c.t.Log("isolated custody:", custodyAction, "failed")
		} else {
			c.t.Log("isolated custody:", custodyAction, "passed")
		}
		delete(c.rpc.allowed, request.Script)
	}
	_ = json.NewEncoder(conn).Encode(response)
}

// observeRealProcess publishes only a synthetic Kubernetes observation. The
// original child journal and owner are revalidated, including after restarts;
// no persistent volume, receipt, ACK, or real Kubernetes state is fabricated.
func (c *isolatedOpenBaoFirstController) observeRealProcess() error {
	bad := errors.New("synthetic observation rejected")
	load := func(kind, name string) (deploymentplan.Object, error) {
		var obj deploymentplan.Object
		err := readExecutionJSON(filepath.Join(c.objects, kind+"_"+c.namespace+"_"+name+".json"), &obj)
		return obj, err
	}
	set, e1 := load("StatefulSet", "openbao")
	secret, e2 := load("Secret", "openbao-transport-tls")
	config, e3 := load("ConfigMap", "openbao-file")
	if e1 != nil || e2 != nil || e3 != nil || c.rpc == nil || !c.rpc.containerOwned() {
		return bad
	}
	_, owner, _, err := isolatedOpenBaoStageMaterial(c.cfg, c.store, set, secret, config)
	if err != nil {
		return bad
	}
	meta := deploymentplan.Map(set["metadata"])
	if deploymentplan.String(meta["uid"]) != owner {
		return bad
	}
	podPath := filepath.Join(c.objects, "Pod_"+c.namespace+"_openbao-0.json")
	if c.podUID == "" {
		if _, err := os.Lstat(podPath); !os.IsNotExist(err) {
			return bad
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return bad
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		c.podUID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	} else {
		pod, err := load("Pod", "openbao-0")
		if err != nil || deploymentplan.Map(pod["metadata"])["uid"] != c.podUID || validateOpenBaoPodOwner(set, pod) != nil {
			return bad
		}
	}
	generation, ok := meta["generation"].(float64)
	if !ok || generation <= 0 {
		generation = 1
		meta["generation"] = generation
	}
	podVolumes, err := c.observeMockVolumes(set, owner, false)
	if err != nil {
		c.t.Log("isolated mock CSI:", err.Error())
		return bad
	}
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := exec.CommandContext(inspectCtx, c.rpc.docker, "inspect", "--format", "{{.Id}}|{{.Image}}|{{.State.Running}}|{{.State.StartedAt}}", c.rpc.container).Output()
	if err != nil {
		return bad
	}
	parts := strings.Split(strings.TrimSpace(string(inspected)), "|")
	const approvedConfig = "sha256:5cdbfe3c85e965315cd3220d2836b8d54b55f95715c551a46e233e0e6e6c9b03"
	if len(parts) != 4 || parts[0] != c.rpc.container || parts[1] != approvedConfig || parts[2] != "true" {
		return bad
	}
	if _, err := time.Parse(time.RFC3339Nano, parts[3]); err != nil {
		return bad
	}
	containers := certIssuerObjectList(environmentPKIPod(set)["containers"])
	if len(containers) != 1 {
		return bad
	}
	container := deploymentplan.Map(containers[0])
	image := deploymentplan.String(container["image"])
	if deploymentplan.String(container["name"]) != "openbao" || !strings.HasSuffix(image, "@sha256:436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115") {
		return bad
	}
	podSpec := deploymentplan.Clone(environmentPKIPod(set))
	podSpec["volumes"] = append(certIssuerObjectList(podSpec["volumes"]), podVolumes...)
	revision := "isolated-" + strings.ReplaceAll(owner, "-", "")
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": c.namespace, "uid": c.podUID, "resourceVersion": "isolated-observation", "labels": deploymentplan.Object{"controller-revision-hash": revision}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": owner, "controller": true}}}, "spec": podSpec, "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": "openbao", "image": image, "imageID": parts[1], "containerID": "docker://" + parts[0], "ready": true, "started": true, "state": deploymentplan.Object{"running": deploymentplan.Object{"startedAt": parts[3]}}}}}}
	set["status"] = deploymentplan.Object{"observedGeneration": generation, "updateRevision": revision, "currentRevision": revision, "readyReplicas": float64(1), "updatedReplicas": float64(1)}
	write := func(path string, value deploymentplan.Object) error {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(c.objects, ".isolated-observation-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err := tmp.Chmod(0600); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := tmp.Write(b); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(name, path)
	}
	if err := write(podPath, pod); err != nil {
		return err
	}
	return write(filepath.Join(c.objects, "StatefulSet_"+c.namespace+"_openbao.json"), set)
}

func (c *isolatedOpenBaoFirstController) forward(ctx context.Context, request isolatedOpenBaoRPCRequest) isolatedOpenBaoRPCResponse {
	fail := isolatedOpenBaoRPCResponse{Code: 1, Error: true}
	if c.rpc == nil || ctx.Err() != nil {
		return fail
	}
	server, client := net.Pipe()
	defer client.Close()
	go c.rpc.handle(server)
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	if json.NewEncoder(client).Encode(request) != nil {
		return fail
	}
	var response isolatedOpenBaoRPCResponse
	if json.NewDecoder(io.LimitReader(client, 4<<20)).Decode(&response) != nil {
		return fail
	}
	return response
}

func TestIsolatedOpenBaoFirstObservationRejectsUnavailableProcess(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	objects := t.TempDir()
	c := newIsolatedOpenBaoFirstController(t, cfg, store, objects)
	if err := c.observeRealProcess(); err == nil {
		t.Fatal("missing real process accepted")
	}
	if _, err := os.Lstat(filepath.Join(objects, "Pod_"+c.namespace+"_openbao-0.json")); !os.IsNotExist(err) {
		t.Fatal("unavailable process produced synthetic readiness")
	}
}

func TestIsolatedOpenBaoFirstControllerRejectsForeignInput(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	objects := t.TempDir()
	c := newIsolatedOpenBaoFirstController(t, cfg, store, objects)
	call := func(req isolatedOpenBaoFirstRequest) isolatedOpenBaoRPCResponse {
		conn, err := net.DialTimeout("tcp4", c.listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := json.NewEncoder(conn).Encode(req); err != nil {
			t.Fatal(err)
		}
		var response isolatedOpenBaoRPCResponse
		if err := json.NewDecoder(conn).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, req := range []isolatedOpenBaoFirstRequest{
		{Capability: "foreign", Action: "start", Namespace: c.namespace},
		{Capability: c.capability, Action: "start", Namespace: "foreign"},
		{Capability: c.capability, Action: "start", Namespace: c.namespace, Script: "unexpected"},
		{Capability: c.capability, Action: "start", Namespace: c.namespace},
		{Capability: c.capability, Action: "exec", Namespace: c.namespace, Script: "cat /etc/passwd"},
	} {
		if response := call(req); !response.Error {
			t.Fatal("unverified stage or foreign input accepted")
		}
	}
	if c.rpc != nil || c.cleanup != nil {
		t.Fatal("rejected requests started a container")
	}
}

// installIsolatedOpenBaoFirstKubectl wraps only the selected OpenBao rollout
// and initializer exec. It returns process status, not Kubernetes ownership or
// CSI readiness; all other calls continue to the existing fail-closed fake API.
func installIsolatedOpenBaoFirstKubectl(t *testing.T, bin, expectedKube string, c *isolatedOpenBaoFirstController) map[string]string {
	t.Helper()
	if c == nil || !isolatedScriptTempPath(bin) || !isolatedScriptTempPath(expectedKube) {
		t.Fatal("isolated kubectl inputs required")
	}
	original := filepath.Join(bin, "kubectl")
	fallback := filepath.Join(bin, "kubectl-before-openbao-first")
	if err := os.Rename(original, fallback); err != nil {
		t.Fatal(err)
	}
	const shim = `#!/usr/bin/env python3
import base64,hmac,json,os,socket,sys
args=sys.argv[1:]
def trace(stage):
 try:
  with open(os.environ['RTK_FIRST_BAO_TRACE'],'a') as f:f.write(stage+'\n')
 except (OSError,KeyError):pass
if len(args)<4 or args[0]!='--kubeconfig' or not args[2].startswith('--request-timeout='): sys.exit(71)
try:
 with open(args[1],'rb') as actual,open(os.environ['RTK_FIRST_BAO_KUBECONFIG'],'rb') as expected:
  if not hmac.compare_digest(actual.read(),expected.read()):sys.exit(72)
except OSError:sys.exit(72)
a=args[3:]; ns=os.environ['RTK_FIRST_BAO_NAMESPACE']
start=len(a)==6 and a[:5]==['--namespace',ns,'rollout','status','StatefulSet/openbao'] and a[5].startswith('--timeout=')
exec_prefix=['--namespace',ns,'exec','-i','openbao-0','--container','openbao','--','env','BAO_ADDR=https://127.0.0.1:8200','BAO_TLS_SERVER_NAME=openbao.'+ns+'.svc','/bin/sh','-ec']
is_exec=len(a)==len(exec_prefix)+1 and a[:-1]==exec_prefix
is_set_get=a==['get','StatefulSet','openbao','--namespace',ns,'--ignore-not-found=true','-o','json']
platform_ns=os.environ['RTK_FIRST_BAO_PLATFORM_NAMESPACE']
is_redis_pvc=len(a)==6 and a[:5]==['--namespace',platform_ns,'wait','--for=jsonpath={.status.phase}=Bound','PersistentVolumeClaim/redis-data'] and a[5].startswith('--timeout=')
is_foundation_rollout=len(a)==6 and a[:4]==['--namespace',platform_ns,'rollout','status'] and a[4] in ('Deployment/redis','StatefulSet/postgresql') and a[5].startswith('--timeout=')
if not start and not is_exec and not is_set_get and not is_redis_pvc and not is_foundation_rollout:
 os.execv(os.environ['RTK_FIRST_BAO_FALLBACK'],[os.environ['RTK_FIRST_BAO_FALLBACK']]+args)
trace('wrapper-redis-pvc' if is_redis_pvc else ('wrapper-foundation' if is_foundation_rollout else ('wrapper-start' if start or is_set_get else 'wrapper-exec')))
payload=sys.stdin.buffer.read(1048577) if is_exec else b''
if len(payload)>1048576:sys.exit(75)
req={'capability':os.environ['RTK_FIRST_BAO_CAPABILITY'],'action':('redis-pvc-read' if os.environ.get('RTK_FIRST_BAO_READ_ONLY')=='1' else 'redis-pvc') if is_redis_pvc else (('foundation-read' if os.environ.get('RTK_FIRST_BAO_READ_ONLY')=='1' else 'foundation-start') if is_foundation_rollout else (('openbao-read' if os.environ.get('RTK_FIRST_BAO_READ_ONLY')=='1' else 'start') if start or is_set_get else 'exec')),'namespace':platform_ns if is_redis_pvc or is_foundation_rollout else ns,'script':a[-1] if is_exec else '', 'input':base64.b64encode(payload).decode() if is_exec else '', 'read_only':os.environ.get('RTK_FIRST_BAO_READ_ONLY')=='1'}
try:
 host,port=os.environ['RTK_FIRST_BAO_BRIDGE'].split(':')
 with socket.create_connection((host,int(port)),timeout=10) as conn:
  conn.settimeout(90);conn.sendall((json.dumps(req)+'\n').encode());data=b''
  while b'\n' not in data and len(data)<4194304:
   chunk=conn.recv(65536)
   if not chunk:break
   data+=chunk
 result=json.loads(data)
 if result.get('error'):
  trace('controller-rejected')
  if is_set_get and os.environ.get('RTK_FIRST_BAO_READ_ONLY')!='1':os.execv(os.environ['RTK_FIRST_BAO_FALLBACK'],[os.environ['RTK_FIRST_BAO_FALLBACK']]+args)
  sys.exit(75)
 trace('controller-accepted')
 if is_set_get:os.execv(os.environ['RTK_FIRST_BAO_FALLBACK'],[os.environ['RTK_FIRST_BAO_FALLBACK']]+args)
 if is_exec:sys.stdout.buffer.write(base64.b64decode(result['output'],validate=True))
 sys.exit(result['code'])
except Exception:
 trace('bridge-error')
 if is_set_get and os.environ.get('RTK_FIRST_BAO_READ_ONLY')!='1':os.execv(os.environ['RTK_FIRST_BAO_FALLBACK'],[os.environ['RTK_FIRST_BAO_FALLBACK']]+args)
 sys.exit(75)
`
	if err := os.WriteFile(original, []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(filepath.Join(c.objects, "openbao-trace.log")); err == nil {
			t.Log("isolated OpenBao wrapper trace (fixed labels):", string(data))
		}
	})
	return map[string]string{"RTK_FIRST_BAO_FALLBACK": fallback, "RTK_FIRST_BAO_KUBECONFIG": expectedKube, "RTK_FIRST_BAO_NAMESPACE": c.namespace, "RTK_FIRST_BAO_PLATFORM_NAMESPACE": c.cfg.Values["CLOUD_STACK_NAME"] + "-platform", "RTK_FIRST_BAO_CAPABILITY": c.capability, "RTK_FIRST_BAO_BRIDGE": c.listener.Addr().String(), "RTK_FIRST_BAO_TRACE": filepath.Join(c.objects, "openbao-trace.log")}
}

func TestIsolatedOpenBaoFirstKubectlWrapperFailsClosed(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	objects, bin := t.TempDir(), t.TempDir()
	kube := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kube, []byte("synthetic-test-kubeconfig"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!/bin/sh\nprintf fallback\\n\n"), 0700); err != nil {
		t.Fatal(err)
	}
	controller := newIsolatedOpenBaoFirstController(t, cfg, store, objects)
	vars := installIsolatedOpenBaoFirstKubectl(t, bin, kube, controller)
	call := func(args ...string) ([]byte, error) {
		cmd := exec.Command(filepath.Join(bin, "kubectl"), append([]string{"--kubeconfig", kube, "--request-timeout=5s"}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		for k, v := range vars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return cmd.CombinedOutput()
	}
	if out, err := call("get", "Namespace", "test"); err != nil || !strings.Contains(string(out), "fallback") {
		t.Fatal("unrelated call did not retain original fake", err)
	}
	if _, err := call("--namespace", controller.namespace, "rollout", "status", "StatefulSet/openbao", "--timeout=10s"); err == nil {
		t.Fatal("missing original stage was reported ready")
	}
	if controller.rpc != nil || controller.cleanup != nil {
		t.Fatal("missing stage started a process")
	}
}

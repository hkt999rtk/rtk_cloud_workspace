package main

import (
	"bufio"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type isolatedMigrationResult struct {
	Code  int  `json:"code"`
	Error bool `json:"error"`
}
type isolatedMigrationPending struct {
	done     chan struct{}
	snapshot string
	result   isolatedMigrationResult
}
type isolatedMigrationController struct {
	listener                                                 net.Listener
	capability, objects, root, docker, network, owner, stack string
	selected                                                 map[string]string
	mu                                                       sync.Mutex
	runs                                                     map[string]*isolatedMigrationPending
	traceMu                                                  sync.Mutex
	requests, authenticated, eligible, executed, succeeded   int
}

func isolatedMigrationControllerRootValid(root string) bool {
	info, err := os.Lstat(root)
	return err == nil && isolatedScriptTempPath(root) && info.IsDir() && info.Mode().Perm() == 0700 && info.Mode()&os.ModeSymlink == 0
}

// installIsolatedMigrationController overlays only the four early migration
// Job waits on the existing file-backed Kubernetes simulator. A private TCP
// port can be allowlisted in the subprocess sandbox; Docker is invoked only by
// this parent process, never exposed to the sandboxed script.
func installIsolatedMigrationController(t *testing.T, store secretStore, objects, docker, network, owner, stack string, selected map[string]string, roots ...string) (*isolatedMigrationController, int) {
	t.Helper()
	fallback := os.Getenv("RTK_CLOUD_KUBECTL")
	if !filepath.IsAbs(fallback) || !filepath.IsAbs(objects) || stack == "" || len(selected) != 4 {
		t.Fatal("exact isolated migration controller inputs are required")
	}
	for _, name := range []string{"pki-migrate", "pki-grants", "pki-create-database-logins", "account-manager-migrate"} {
		if !immutableEnvironmentImage.MatchString(selected[name]) {
			t.Fatal("selected immutable Job image is unavailable")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The same private fake inventory must retain an in-flight intent even if
	// this test controller process is restarted. A new controller may not
	// replay a Job whose outcome is unknown.
	if len(roots) > 1 {
		listener.Close()
		t.Fatal("one isolated controller root is required")
	}
	root := ""
	if len(roots) == 1 {
		root = roots[0]
	} else {
		root = t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			listener.Close()
			t.Fatal(err)
		}
	}
	if !isolatedMigrationControllerRootValid(root) {
		listener.Close()
		t.Fatal("private isolated controller state is unavailable")
	}
	var capBytes [32]byte
	if _, err := rand.Read(capBytes[:]); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	c := &isolatedMigrationController{listener: listener, capability: hex.EncodeToString(capBytes[:]), objects: objects, root: root, docker: docker, network: network, owner: owner, stack: stack, selected: selected, runs: map[string]*isolatedMigrationPending{}}
	go c.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		// t.TempDir owns cleanup; re-created controllers reuse this exact root.
	})
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "kubectl")
	program := `#!/usr/bin/env python3
import hmac,json,os,re,socket,sys
args=sys.argv[1:]
if len(args)<4 or args[0]!='--kubeconfig' or not args[2].startswith('--request-timeout='): sys.exit(71)
try:
 with open(args[1],'rb') as a, open(os.environ['RTK_TEST_JOB_KUBECONFIG'],'rb') as b:
  if not hmac.compare_digest(a.read(),b.read()):sys.exit(72)
except OSError:sys.exit(72)
a=args[3:]
if len(a)==6 and a[0]=='--namespace' and a[2]=='wait' and a[3]=='--for=condition=complete' and a[4].startswith('Job/') and a[5].startswith('--timeout='):
 try:
  match=re.fullmatch(r'--timeout=([1-9][0-9]*)s',a[5])
  if not match:sys.exit(74)
  timeout=int(match.group(1))
  host,port=os.environ['RTK_TEST_JOB_BRIDGE'].split(':')
  with socket.create_connection((host,int(port)),timeout=min(10,timeout)) as sock:
   sock.settimeout(timeout)
   sock.sendall((json.dumps({'capability':os.environ['RTK_TEST_JOB_CAPABILITY'],'namespace':a[1],'name':a[4][4:]})+'\n').encode())
   data=b''
   while b'\n' not in data and len(data)<4096:
    chunk=sock.recv(4096)
    if not chunk:break
    data+=chunk
  result=json.loads(data)
  if result.get('error') or result.get('code') != 0:sys.exit(1)
  sys.exit(0)
 except Exception:sys.exit(73)
# Only these exact foundation operations may reach the earlier owner-checked
# process controller. Unknown waits and rollouts remain fail-closed.
if 'wait' in a or ('rollout' in a and 'status' in a):
 ns=os.environ['RTK_TEST_JOB_PLATFORM_NAMESPACE']
 pvc=len(a)==6 and a[:5]==['--namespace',ns,'wait','--for=jsonpath={.status.phase}=Bound','PersistentVolumeClaim/redis-data'] and a[5].startswith('--timeout=')
 rollout=len(a)==6 and a[:4]==['--namespace',ns,'rollout','status'] and a[4] in ('Deployment/redis','StatefulSet/postgresql') and a[5].startswith('--timeout=')
 if not pvc and not rollout:sys.exit(75)
os.execv(os.environ['RTK_TEST_JOB_FALLBACK'],[os.environ['RTK_TEST_JOB_FALLBACK']]+args)
`
	if err := os.WriteFile(shim, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_TEST_JOB_FALLBACK", fallback)
	t.Setenv("RTK_TEST_JOB_KUBECONFIG", store.KubeconfigPath())
	t.Setenv("RTK_TEST_JOB_BRIDGE", listener.Addr().String())
	t.Setenv("RTK_TEST_JOB_CAPABILITY", c.capability)
	t.Setenv("RTK_TEST_JOB_PLATFORM_NAMESPACE", stack+"-platform")
	t.Setenv("RTK_CLOUD_KUBECTL", shim)
	_ = python // shebang selects the same isolated environment python3.
	return c, listener.Addr().(*net.TCPAddr).Port
}

func (c *isolatedMigrationController) serve() {
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		go c.handle(conn)
	}
}
func (c *isolatedMigrationController) handle(conn net.Conn) {
	defer conn.Close()
	c.traceMu.Lock()
	c.requests++
	c.traceMu.Unlock()
	_ = conn.SetDeadline(time.Now().Add(6 * time.Minute))
	var req struct {
		Capability string `json:"capability"`
		Namespace  string `json:"namespace"`
		Name       string `json:"name"`
	}
	dec := json.NewDecoder(io.LimitReader(bufio.NewReader(conn), 4096))
	dec.DisallowUnknownFields()
	result := isolatedMigrationResult{Code: -1, Error: true}
	if dec.Decode(&req) == nil && req.Capability == c.capability {
		c.traceMu.Lock()
		c.authenticated++
		c.traceMu.Unlock()
		result = c.wait(req.Namespace, req.Name)
	}
	_ = json.NewEncoder(conn).Encode(result)
}

func (c *isolatedMigrationController) wait(namespace, name string) isolatedMigrationResult {
	bad := isolatedMigrationResult{Code: -1, Error: true}
	image, allowed := c.selected[name]
	expectedNS := c.stack + "-video-cloud"
	if name == "account-manager-migrate" {
		expectedNS = c.stack + "-account-manager"
	}
	if !allowed || namespace != expectedNS {
		return bad
	}
	c.traceMu.Lock()
	c.eligible++
	c.traceMu.Unlock()
	component := regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	if !component.MatchString(name) || !component.MatchString(namespace) {
		return bad
	}
	path := filepath.Join(c.objects, "Job_"+namespace+"_"+name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return bad
	}
	var job deploymentplan.Object
	if json.Unmarshal(data, &job) != nil {
		return bad
	}
	meta := deploymentplan.Map(job["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if job["kind"] != "Job" || meta["name"] != name || meta["namespace"] != namespace || !component.MatchString(uid) {
		return bad
	}
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(job["spec"])["template"])["spec"])
	cs := certIssuerObjectList(pod["containers"])
	if len(cs) != 1 || deploymentplan.Map(cs[0])["image"] != image {
		return bad
	}
	key := namespace + "/" + name + "/" + uid
	body := deploymentplan.Clone(job)
	delete(body, "status")
	snapshot := deploymentplan.Digest(body)
	c.mu.Lock()
	if prior := c.runs[key]; prior != nil {
		c.mu.Unlock()
		if prior.snapshot != snapshot {
			return bad
		}
		<-prior.done
		return prior.result
	}
	intent := filepath.Join(c.root, "intent-"+uid)
	f, err := os.OpenFile(intent, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		c.mu.Unlock()
		return bad
	}
	_ = f.Close()
	pending := &isolatedMigrationPending{done: make(chan struct{}), snapshot: snapshot, result: bad}
	c.runs[key] = pending
	c.mu.Unlock()
	defer close(pending.done)
	secrets := map[string]map[string]string{}
	for _, x := range certIssuerObjectList(deploymentplan.Map(cs[0])["envFrom"]) {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(x)["secretRef"])["name"])
		if name == "" {
			return bad
		}
		if err := c.loadSecret(namespace, name, secrets); err != nil {
			return bad
		}
	}
	for _, x := range certIssuerObjectList(deploymentplan.Map(cs[0])["env"]) {
		item := deploymentplan.Map(x)
		ref := deploymentplan.Map(deploymentplan.Map(item["valueFrom"])["secretKeyRef"])
		if len(ref) > 0 {
			if err := c.loadSecret(namespace, deploymentplan.String(ref["name"]), secrets); err != nil {
				return bad
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c.traceMu.Lock()
	c.executed++
	c.traceMu.Unlock()
	var observed isolatedMigrationRuntime
	code, err := runIsolatedMigrationJob(ctx, c.docker, c.root, c.network, c.owner, job, map[string]bool{image: true}, secrets, filepath.Join(c.root, "job-"+uid+".log"), func(runtime isolatedMigrationRuntime) error {
		observed = runtime
		return nil
	})
	if err != nil {
		return bad
	}
	if observed.ExitCode != code {
		return bad
	}
	if err = c.recordActualJobStatus(path, job, observed); err != nil {
		return bad
	}
	if code == 0 {
		c.traceMu.Lock()
		c.succeeded++
		c.traceMu.Unlock()
	}
	pending.result = isolatedMigrationResult{Code: code}
	return pending.result
}

// Only aggregate fixed counters; never expose requests, command output or secrets.
func (c *isolatedMigrationController) safeProgress() (int, int, int, int, int) {
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	return c.requests, c.authenticated, c.eligible, c.executed, c.succeeded
}

func (c *isolatedMigrationController) loadSecret(namespace, name string, dest map[string]map[string]string) error {
	if _, ok := dest[name]; ok {
		return nil
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`).MatchString(name) {
		return errors.New("invalid synthetic secret")
	}
	data, err := os.ReadFile(filepath.Join(c.objects, "Secret_"+namespace+"_"+name+".json"))
	if err != nil {
		return err
	}
	var object deploymentplan.Object
	if json.Unmarshal(data, &object) != nil || object["kind"] != "Secret" {
		return errors.New("invalid synthetic secret")
	}
	meta := deploymentplan.Map(object["metadata"])
	if meta["namespace"] != namespace || meta["name"] != name {
		return errors.New("foreign synthetic secret")
	}
	values := map[string]string{}
	for key, value := range deploymentplan.Map(object["data"]) {
		raw, err := base64.StdEncoding.DecodeString(deploymentplan.String(value))
		if err != nil {
			return err
		}
		values[key] = string(raw)
	}
	for key, value := range deploymentplan.Map(object["stringData"]) {
		if _, duplicate := values[key]; duplicate {
			return errors.New("ambiguous synthetic Secret")
		}
		text, ok := value.(string)
		if !ok {
			return errors.New("invalid synthetic Secret")
		}
		values[key] = text
	}
	dest[name] = values
	return nil
}

func (c *isolatedMigrationController) recordActualJobStatus(path string, original deploymentplan.Object, runtime isolatedMigrationRuntime) error {
	id := strings.TrimPrefix(runtime.ContainerID, "docker://")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) || !deploymentImageDigestPattern.MatchString(runtime.ImageID) {
		return errors.New("actual completed migration container evidence unavailable")
	}
	code := runtime.ExitCode
	lock, err := os.OpenFile(filepath.Join(c.objects, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var current deploymentplan.Object
	if json.Unmarshal(raw, &current) != nil || deploymentplan.Digest(current) != deploymentplan.Digest(original) {
		return errors.New("original Job changed during real execution")
	}
	meta := deploymentplan.Map(current["metadata"])
	template := deploymentplan.Map(deploymentplan.Map(current["spec"])["template"])
	spec := deploymentplan.Map(template["spec"])
	containers := certIssuerObjectList(spec["containers"])
	if len(containers) != 1 {
		return errors.New("original migration container inventory changed")
	}
	phase := "Succeeded"
	if code != 0 {
		phase = "Failed"
	}
	// Kubernetes ownership is explicitly synthetic. Runtime IDs, image ID and
	// exit code come only from the inspected, owner-checked real container.
	name := deploymentplan.String(meta["name"]) + "-runtime"
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": name, "namespace": meta["namespace"], "uid": "isolated-" + id[:32], "labels": deploymentplan.Object{"rtk.realtek.com/stack": c.stack}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": current["apiVersion"], "kind": "Job", "name": meta["name"], "uid": meta["uid"], "controller": true}}}, "spec": deploymentplan.Clone(spec), "status": deploymentplan.Object{"phase": phase, "containerStatuses": []any{deploymentplan.Object{"name": deploymentplan.Map(containers[0])["name"], "containerID": runtime.ContainerID, "imageID": runtime.ImageID, "ready": false, "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": code}}}}}}
	podBytes, err := json.Marshal(pod)
	if err != nil {
		return err
	}
	podFile, err := os.OpenFile(filepath.Join(c.objects, "Pod_"+deploymentplan.String(meta["namespace"])+"_"+name+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := podFile.Write(podBytes)
	closeErr := podFile.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("actual migration observation could not be retained")
	}
	condition := "Complete"
	if code != 0 {
		condition = "Failed"
	}
	current["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": condition, "status": "True"}}}
	body, err := json.Marshal(current)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.objects, ".job-status-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func TestIsolatedMigrationRuntimeObservationMeetsNativeAcceptance(t *testing.T) {
	for _, code := range []int{0, 17} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)
			job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-migrate", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "migrate", "image": image}}}}}}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "bindings"}, []deploymentplan.Definition{{Object: job, Phase: "migration"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			job = deploymentplan.Clone(plan.Resources[0].Desired)
			deploymentplan.Map(job["metadata"])["uid"] = "original-job-owner"
			root := t.TempDir()
			path := filepath.Join(root, "Job_video-cloud-dev-video-cloud_pki-migrate.json")
			raw, _ := json.Marshal(job)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			c := isolatedMigrationController{objects: root, stack: "video-cloud-dev"}
			if err := c.recordActualJobStatus(path, job, isolatedMigrationRuntime{ExitCode: code}); err == nil {
				t.Fatal("missing actual runtime evidence accepted")
			}
			runtime := isolatedMigrationRuntime{ContainerID: "docker://" + strings.Repeat("b", 64), ImageID: "sha256:" + strings.Repeat("c", 64), ExitCode: code}
			if err := c.recordActualJobStatus(path, job, runtime); err != nil {
				t.Fatal(err)
			}
			var objects []deploymentplan.Object
			paths, _ := filepath.Glob(filepath.Join(root, "*.json"))
			for _, p := range paths {
				var object deploymentplan.Object
				if err := readExecutionJSON(p, &object); err != nil {
					t.Fatal(err)
				}
				objects = append(objects, object)
			}
			err = verifyEnvironmentTargetWorkloads(t.Context(), plan, objects, func(context.Context, string) (map[string]bool, error) {
				return map[string]bool{runtime.ImageID: true}, nil
			})
			if (err == nil) != (code == 0) {
				t.Fatal("native acceptance did not follow the recorded exit status", err)
			}
		})
	}
}

// Verify the exact sandbox policy can reach only the test controller's port;
// no Docker or arbitrary Unix socket becomes available to the shell process.
func verifyIsolatedMigrationControllerSandbox(t *testing.T, controller *isolatedMigrationController, port int) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Fatal("isolated shell sandbox requires macOS")
	}
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unixPath := filepath.Join(controller.root, "sandbox-probe.sock")
	local, err := net.Listen("unix", unixPath)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	profile := "(version 1)(allow default)(deny network-outbound)" + fmt.Sprintf("(allow network-outbound (remote ip %s))", strconv.Quote(fmt.Sprintf("localhost:%d", port)))
	probe := `import json,socket,sys
out={'allowed':False,'other_denied':False,'unix_denied':False}
with socket.create_connection(('127.0.0.1',int(sys.argv[1])),timeout=2) as s:
 s.sendall(b'{"capability":"invalid","namespace":"invalid","name":"invalid"}\n')
 out['allowed']=json.loads(s.recv(4096)).get('error') is True
try:
 with socket.create_connection(('127.0.0.1',int(sys.argv[2])),timeout=2):pass
except PermissionError:out['other_denied']=True
try:
 with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as s:s.connect(sys.argv[3])
except PermissionError:out['unix_denied']=True
print(json.dumps(out))`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, python, "-c", probe, strconv.Itoa(port), strconv.Itoa(other.Addr().(*net.TCPAddr).Port), unixPath).Output()
	var result struct {
		Allowed     bool `json:"allowed"`
		OtherDenied bool `json:"other_denied"`
		UnixDenied  bool `json:"unix_denied"`
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || !result.Allowed || !result.OtherDenied || !result.UnixDenied {
		t.Fatal("isolated controller sandbox port boundary was not enforced", err)
	}
}

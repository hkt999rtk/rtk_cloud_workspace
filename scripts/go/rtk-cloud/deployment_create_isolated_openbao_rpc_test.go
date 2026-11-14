package main

import (
	"bytes"
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
	"strings"
	"testing"
	"time"
)

type isolatedOpenBaoRPC struct {
	listener                                        net.Listener
	capability, namespace, owner, container, docker string
	allowed                                         map[string]bool
}

type isolatedOpenBaoRPCRequest struct {
	Capability string `json:"capability"`
	Namespace  string `json:"namespace"`
	Script     string `json:"script"`
	Input      string `json:"input"`
}
type isolatedOpenBaoRPCResponse struct {
	Code   int    `json:"code"`
	Output string `json:"output"`
	Error  bool   `json:"error"`
}

func isolatedOpenBaoInitializerScripts() map[string]bool {
	prefix := "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n"
	header := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	out := map[string]bool{}
	for _, script := range []string{
		"set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]",
		"bao operator init -key-shares=1 -key-threshold=1 -format=json",
		"bao write -format=json sys/unseal -",
		header + "bao audit list -format=json\n",
		header + "bao auth enable -path=kubernetes kubernetes >/dev/null\n",
		header + "bao write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443 kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt disable_local_ca_jwt=false >/dev/null\n",
		header + "bao auth list -format=json\n",
		header + "bao read -format=json auth/kubernetes/config\n",
	} {
		out[prefix+script] = true
	}
	return out
}

// installIsolatedOpenBaoRPC exposes only the exact initial OpenBao commands
// needed by the formal initializer. Docker is called by the test process, not
// by the sandboxed shell. No result, credential, or stdout is saved in a log.
func installIsolatedOpenBaoRPC(t *testing.T, store secretStore, namespace, containerID, owner string) (*isolatedOpenBaoRPC, int) {
	t.Helper()
	fallback := os.Getenv("RTK_CLOUD_KUBECTL")
	docker, err := exec.LookPath("docker")
	if err != nil || !filepath.IsAbs(fallback) || !filepath.IsAbs(docker) || namespace == "" || owner == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(containerID) {
		t.Fatal("isolated OpenBao RPC inputs are invalid")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var capability [32]byte
	if _, err := rand.Read(capability[:]); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	rpc := &isolatedOpenBaoRPC{listener: listener, capability: hex.EncodeToString(capability[:]), namespace: namespace, owner: owner, container: containerID, docker: docker, allowed: isolatedOpenBaoInitializerScripts()}
	if !rpc.containerOwned() {
		listener.Close()
		t.Fatal("isolated OpenBao container owner or network is invalid")
	}
	go rpc.serve()
	t.Cleanup(func() { _ = listener.Close() })
	dir := t.TempDir()
	shim := filepath.Join(dir, "kubectl")
	program := `#!/usr/bin/env python3
import base64,hmac,json,os,socket,sys
a=sys.argv[1:]
if len(a)<4 or a[0]!='--kubeconfig' or not a[2].startswith('--request-timeout='):sys.exit(71)
try:
 with open(a[1],'rb') as x,open(os.environ['RTK_TEST_BAO_KUBECONFIG'],'rb') as y:
  if not hmac.compare_digest(x.read(),y.read()):sys.exit(72)
except OSError:sys.exit(72)
b=a[3:]
if 'exec' in b and any('openbao' in x for x in b):
 try:
  expected=['--namespace',os.environ['RTK_TEST_BAO_NAMESPACE'],'exec','-i','openbao-0','--container','openbao','--','env','BAO_ADDR=https://127.0.0.1:8200','BAO_TLS_SERVER_NAME=openbao.'+os.environ['RTK_TEST_BAO_NAMESPACE']+'.svc','/bin/sh','-ec']
  if len(b)!=len(expected)+1 or b[:-1]!=expected:sys.exit(75)
  payload=sys.stdin.buffer.read(1048577)
  if len(payload)>1048576:sys.exit(75)
  req={'capability':os.environ['RTK_TEST_BAO_CAPABILITY'],'namespace':b[1],'script':b[-1],'input':base64.b64encode(payload).decode()}
  host,port=os.environ['RTK_TEST_BAO_BRIDGE'].split(':')
  with socket.create_connection((host,int(port)),timeout=10) as s:
   s.settimeout(120);s.sendall((json.dumps(req)+'\n').encode());data=b''
   while b'\n' not in data and len(data)<4194304:
    chunk=s.recv(65536)
    if not chunk:break
    data+=chunk
  r=json.loads(data)
  if r.get('error'):sys.exit(75)
  sys.stdout.buffer.write(base64.b64decode(r['output'],validate=True));sys.exit(r['code'])
 except Exception:sys.exit(75)
os.execv(os.environ['RTK_TEST_BAO_FALLBACK'],[os.environ['RTK_TEST_BAO_FALLBACK']]+a)
`
	if err := os.WriteFile(shim, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_TEST_BAO_FALLBACK", fallback)
	t.Setenv("RTK_TEST_BAO_KUBECONFIG", store.KubeconfigPath())
	t.Setenv("RTK_TEST_BAO_NAMESPACE", namespace)
	t.Setenv("RTK_TEST_BAO_BRIDGE", listener.Addr().String())
	t.Setenv("RTK_TEST_BAO_CAPABILITY", rpc.capability)
	t.Setenv("RTK_CLOUD_KUBECTL", shim)
	return rpc, listener.Addr().(*net.TCPAddr).Port
}

func (r *isolatedOpenBaoRPC) containerOwned() bool {
	out, err := exec.Command(r.docker, "inspect", "--format", `{{index .Config.Labels "rtk.test.owner"}}|{{.HostConfig.NetworkMode}}|{{.State.Running}}`, r.container).Output()
	return err == nil && strings.TrimSpace(string(out)) == r.owner+"|none|true"
}
func (r *isolatedOpenBaoRPC) serve() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}
func (r *isolatedOpenBaoRPC) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	var request isolatedOpenBaoRPCRequest
	decoder := json.NewDecoder(io.LimitReader(conn, 2<<20))
	decoder.DisallowUnknownFields()
	response := isolatedOpenBaoRPCResponse{Code: 1, Error: true}
	if decoder.Decode(&request) == nil && request.Capability == r.capability && request.Namespace == r.namespace && r.allowed[request.Script] && r.containerOwned() {
		input, err := base64.StdEncoding.DecodeString(request.Input)
		if err == nil && len(input) <= 1<<20 {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			cmd := exec.CommandContext(ctx, r.docker, "exec", "-i", r.container, "env", "BAO_ADDR=https://127.0.0.1:8200", "BAO_TLS_SERVER_NAME=openbao."+r.namespace+".svc", "/bin/sh", "-ec", request.Script)
			cmd.Stdin = bytes.NewReader(input)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = io.Discard
			err = cmd.Run()
			cancel()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitCode()
				} else {
					code = -1
				}
			}
			if code >= 0 && out.Len() <= 2<<20 {
				response = isolatedOpenBaoRPCResponse{Code: code, Output: base64.StdEncoding.EncodeToString(out.Bytes())}
			}
		}
	}
	_ = json.NewEncoder(conn).Encode(response)
}

// Exercise the real OpenBao status through the actual OS sandbox and verify
// that a shell command outside the fixed initializer list cannot be proxied.
func verifyIsolatedOpenBaoRPCSandbox(t *testing.T, r *isolatedOpenBaoRPC, port int, store secretStore) {
	t.Helper()
	profile := fmt.Sprintf("(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip %q))", fmt.Sprintf("localhost:%d", port))
	shim := os.Getenv("RTK_CLOUD_KUBECTL")
	prefix := "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n"
	status := prefix + "set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]"
	invoke := func(script string) ([]byte, error) {
		cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, shim, "--kubeconfig", store.KubeconfigPath(), "--request-timeout=90s", "--namespace", r.namespace, "exec", "-i", "openbao-0", "--container", "openbao", "--", "env", "BAO_ADDR=https://127.0.0.1:8200", "BAO_TLS_SERVER_NAME=openbao."+r.namespace+".svc", "/bin/sh", "-ec", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		for _, key := range []string{"RTK_TEST_BAO_FALLBACK", "RTK_TEST_BAO_KUBECONFIG", "RTK_TEST_BAO_NAMESPACE", "RTK_TEST_BAO_BRIDGE", "RTK_TEST_BAO_CAPABILITY"} {
			cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
		}
		return cmd.Output()
	}
	out, err := invoke(status)
	var observed struct {
		Initialized *bool `json:"initialized"`
		Sealed      *bool `json:"sealed"`
	}
	if err != nil || json.Unmarshal(out, &observed) != nil || observed.Initialized == nil || *observed.Initialized || observed.Sealed == nil || !*observed.Sealed {
		t.Fatal("sandbox could not verify real sealed OpenBao via restricted RPC", err)
	}
	if _, err = invoke(prefix + "cat /etc/passwd"); err == nil {
		t.Fatal("sandbox proxied unregistered OpenBao command")
	}
}

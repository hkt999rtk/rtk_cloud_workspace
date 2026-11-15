package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestIsolatedScriptWrapperFencesAndSelectedArguments(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS sandbox-exec isolation")
	}
	for _, operation := range []string{"preflight", "create"} {
		t.Run(operation, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
			environmentRoot := filepath.Join(workspace, "cloud_env", "dev")
			cfg, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
			if err != nil {
				t.Fatal(err)
			}
			cfg, store, original, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"ISOLATED_OPERATOR": "synthetic"}})
			parent, _ := providerExecutionContractFork(t, original, plan, "isolated-script-argument-test")
			actual, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
			if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(cfg) {
				t.Fatal("original config changed", err)
			}
			bin := t.TempDir()
			const fakeGo = `#!/bin/sh
set -eu
[ "$1" = build ] && [ "$2" = -o ] || exit 81
cat > "$3" <<'INNER'
#!/bin/sh
printf 'ARG:%s\n' "$@"
printf 'STORE:%s\n' "$RTK_CLOUD_CONFIG_ROOT"
selected_workspace= selected_environment_root=
while [ "$#" -gt 0 ]; do
 case "$1" in
  --workspace) selected_workspace="$2"; shift 2;;
  --environment-root) selected_environment_root="$2"; shift 2;;
  *) shift;;
 esac
done
printf 'SELECTED_WORKSPACE:%s\n' "$selected_workspace"
printf 'SELECTED_ENVIRONMENT_ROOT:%s\n' "$selected_environment_root"
/usr/bin/python3 "$RTK_ISOLATED_PROBE" "$RTK_ISOLATED_ALLOWED" "$RTK_ISOLATED_DENIED" "$RTK_ISOLATED_UNIX" "$RTK_ISOLATED_LOCK" || exit $?
if [ "${RTK_ISOLATED_FAIL:-0}" = 1 ]; then exit 23; fi
INNER
chmod 700 "$3"
`
			for _, name := range []string{"go", "kubectl", "gh", "docker", "curl"} {
				body := "#!/bin/sh\nexit 99\n"
				if name == "go" {
					body = fakeGo
				}
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			probe := filepath.Join(t.TempDir(), "probe.py")
			const py = `import errno,fcntl,os,socket,sys
fd=os.open(sys.argv[4],os.O_RDWR)
fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
print("OWNER_LOCK_ACQUIRED")
fcntl.flock(fd,fcntl.LOCK_UN)
os.close(fd)
for i,port in enumerate(map(int,sys.argv[1:3])):
 s=socket.socket(); s.settimeout(2)
 try:
  s.connect(('127.0.0.1',port))
  if i!=0: raise SystemExit('unselected loopback accepted')
  print('ALLOWED_LOOPBACK')
 except OSError as e:
  if i!=1 or e.errno!=errno.EPERM: raise
  print('DENIED_LOOPBACK')
 s.close()
s=socket.socket(socket.AF_UNIX)
try: s.connect(sys.argv[3]); raise SystemExit('host unix socket accepted')
except OSError as e:
 if e.errno!=errno.EPERM: raise
 print('DENIED_UNIX')
s.close()
s=socket.socket();s.settimeout(2)
try: s.connect(('203.0.113.1',443)); raise SystemExit('external address accepted')
except OSError as e:
 if e.errno!=errno.EPERM: raise
 print('DENIED_EXTERNAL')
s.close()
`
			if err := os.WriteFile(probe, []byte(py), 0600); err != nil {
				t.Fatal(err)
			}
			allowed, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer allowed.Close()
			denied, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer denied.Close()
			// A short POSIX temporary path keeps the Unix socket within its
			// platform length limit, including on macOS with a long TMPDIR.
			unixDir, err := os.MkdirTemp("/tmp", "rtk-sandbox-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(unixDir) })
			unixPath := filepath.Join(unixDir, "probe.sock")
			unix, err := net.Listen("unix", unixPath)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close()
			go func() {
				for {
					c, e := allowed.Accept()
					if e != nil {
						return
					}
					_ = c.Close()
				}
			}()
			a := allowed.Addr().(*net.TCPAddr).Port
			b := denied.Addr().(*net.TCPAddr).Port
			f := isolatedScriptInvocation{workspace: workspace, environmentRoot: environmentRoot, fakeBin: bin, store: store, execution: parent, ports: []int{a}, env: map[string]string{"RTK_ISOLATED_PROBE": probe, "RTK_ISOLATED_ALLOWED": fmt.Sprint(a), "RTK_ISOLATED_DENIED": fmt.Sprint(b), "RTK_ISOLATED_UNIX": unixPath, "RTK_ISOLATED_LOCK": filepath.Join(store.Root, "deployment/.execution-active/running.lock")}}
			output, err := f.run(t, operation, "--operation", "create")
			if err != nil {
				t.Fatalf("isolated wrapper: %v: %s", err, output)
			}
			for _, want := range []string{"SELECTED_WORKSPACE:" + workspace, "SELECTED_ENVIRONMENT_ROOT:" + environmentRoot, "STORE:" + store.ConfigRoot, "ALLOWED_LOOPBACK", "DENIED_LOOPBACK", "DENIED_UNIX", "DENIED_EXTERNAL", "OWNER_LOCK_ACQUIRED"} {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %s in %s", want, output)
				}
			}
			if operation == "preflight" {
				before, err := os.ReadFile(parent.JournalPath)
				if err != nil {
					t.Fatal(err)
				}
				f.env["RTK_ISOLATED_FAIL"] = "1"
				if out, err := f.run(t, "create", "--confirm", parent.Journal.Stack); err == nil || !strings.Contains(out, "OWNER_LOCK_ACQUIRED") {
					t.Fatalf("controlled interruption did not release same owner: %v %s", err, out)
				}
				delete(f.env, "RTK_ISOLATED_FAIL")
				if out, err := f.run(t, "create", "--confirm", parent.Journal.Stack); err != nil || !strings.Contains(out, "OWNER_LOCK_ACQUIRED") {
					t.Fatalf("same parent retry did not reacquire original lock: %v %s", err, out)
				}
				after, err := os.ReadFile(parent.JournalPath)
				if err != nil || string(after) != string(before) {
					t.Fatal("controlled subprocess changed original journal", err)
				}
			}
		})
	}
}

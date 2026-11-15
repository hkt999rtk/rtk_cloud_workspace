package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Keep any existing isolated Job wrapper as fallback; the shell can reach only
// this OpenBao RPC's explicit port, and cannot reach the Docker Unix socket.
func attachIsolatedOpenBaoRPCToScript(t *testing.T, f *isolatedScriptInvocation, rpc *isolatedOpenBaoRPC, port int) {
	t.Helper()
	if f == nil || rpc == nil || f.detached || !isolatedScriptTempPath(f.fakeBin) || !isolatedScriptTempPath(f.store.Root) || f.store.KubeconfigPath() != os.Getenv("RTK_TEST_BAO_KUBECONFIG") || rpc.listener.Addr().String() != os.Getenv("RTK_TEST_BAO_BRIDGE") || rpc.capability != os.Getenv("RTK_TEST_BAO_CAPABILITY") {
		t.Fatal("isolated shell OpenBao RPC inputs differ")
	}
	shim := os.Getenv("RTK_CLOUD_KUBECTL")
	if !isolatedScriptTempPath(filepath.Dir(shim)) {
		t.Fatal("OpenBao RPC wrapper is not isolated")
	}
	wrapper, err := os.ReadFile(shim)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.fakeBin, "kubectl")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("isolated kubectl placeholder unavailable")
	}
	fallback := filepath.Join(f.fakeBin, "kubectl-before-openbao")
	file, err := os.OpenFile(fallback, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(original); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	if f.env == nil {
		f.env = map[string]string{}
	}
	for _, name := range []string{"RTK_TEST_BAO_KUBECONFIG", "RTK_TEST_BAO_NAMESPACE", "RTK_TEST_BAO_BRIDGE", "RTK_TEST_BAO_CAPABILITY"} {
		value := os.Getenv(name)
		if value == "" {
			t.Fatal("isolated shell OpenBao input unavailable", name)
		}
		if old, exists := f.env[name]; exists && old != value {
			t.Fatal("isolated shell OpenBao input changed", name)
		}
		f.env[name] = value
	}
	if _, exists := f.env["RTK_TEST_BAO_FALLBACK"]; exists {
		t.Fatal("OpenBao shell fallback already selected")
	}
	f.env["RTK_TEST_BAO_FALLBACK"] = fallback
	if !slices.Contains(f.ports, port) {
		f.ports = append(f.ports, port)
	}
}

// This is a shell wiring probe using a synthetic Go CLI; it does not claim a
// real preflight or create. OpenBao is real, private, TLS verified, and only
// fixed initialization operations may cross the restricted loopback bridge.
func verifyIsolatedOpenBaoRPCThroughShell(t *testing.T, containerID, owner string) {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	environmentRoot := filepath.Join(workspace, "cloud_env", "dev")
	cfg, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, original, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"ISOLATED_OPERATOR": "synthetic"}})
	parent, _ := providerExecutionContractFork(t, original, plan, "isolated-openbao-shell")
	actual, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(cfg) {
		t.Fatal("isolated selected parent changed", err)
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("isolated-openbao-shell-kubeconfig"), false); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	const fakeGo = `#!/bin/sh
set -eu
[ "$1" = build ] && [ "$2" = -o ] || exit 81
cat > "$3" <<'INNER'
#!/bin/sh
exec "$RTK_CLOUD_KUBECTL" --kubeconfig "$RTK_TEST_BAO_KUBECONFIG" --request-timeout=90s --namespace "$RTK_TEST_BAO_NAMESPACE" exec -i openbao-0 --container openbao -- env BAO_ADDR=https://127.0.0.1:8200 "BAO_TLS_SERVER_NAME=openbao.$RTK_TEST_BAO_NAMESPACE.svc" /bin/sh -ec "$RTK_SHELL_OPENBAO_SCRIPT"
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
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	rpc, port := installIsolatedOpenBaoRPC(t, store, "video-cloud-dev-secrets", containerID, owner)
	prefix := "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n"
	status := prefix + "set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]"
	f := isolatedScriptInvocation{workspace: workspace, environmentRoot: environmentRoot, fakeBin: bin, store: store, execution: parent, env: map[string]string{"RTK_SHELL_OPENBAO_SCRIPT": status}}
	attachIsolatedOpenBaoRPCToScript(t, &f, rpc, port)
	if out, err := f.run(t, "preflight", "--operation", "create"); err != nil || len(out) == 0 {
		t.Fatal("shell could not read the real OpenBao status through restricted RPC", err)
	}
	f.env["RTK_SHELL_OPENBAO_SCRIPT"] = prefix + "cat /etc/passwd"
	if _, err := f.run(t, "create", "--confirm", parent.Journal.Stack); err == nil {
		t.Fatal("shell proxied an unapproved OpenBao command")
	}
}

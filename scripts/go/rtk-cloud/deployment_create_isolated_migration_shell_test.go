package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The shell inherits only explicit fixture variables, and the operating system
// sandbox allows only the controller's already-bound loopback port.
func attachIsolatedMigrationControllerToScript(t *testing.T, f *isolatedScriptInvocation, controller *isolatedMigrationController, port int) {
	t.Helper()
	if f == nil || controller == nil || f.detached || !isolatedScriptTempPath(f.fakeBin) || !isolatedScriptTempPath(f.store.Root) || f.store.KubeconfigPath() != os.Getenv("RTK_TEST_JOB_KUBECONFIG") || controller.listener.Addr().String() != os.Getenv("RTK_TEST_JOB_BRIDGE") || controller.capability != os.Getenv("RTK_TEST_JOB_CAPABILITY") {
		t.Fatal("isolated shell Job controller inputs differ")
	}
	shim := os.Getenv("RTK_CLOUD_KUBECTL")
	if shim != filepath.Join(controller.root, "kubectl") {
		t.Fatal("foreign Job controller wrapper")
	}
	raw, err := os.ReadFile(shim)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.fakeBin, "kubectl")
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("isolated shell kubectl placeholder unavailable")
	}
	if err := os.WriteFile(target, raw, 0700); err != nil {
		t.Fatal(err)
	}
	if f.env == nil {
		f.env = map[string]string{}
	}
	for _, name := range []string{"RTK_TEST_JOB_FALLBACK", "RTK_TEST_JOB_KUBECONFIG", "RTK_TEST_JOB_BRIDGE", "RTK_TEST_JOB_CAPABILITY", "RTK_UNIFIED_PYTHON", "RTK_UNIFIED_KUBECTL", "RTK_LATE_NATIVE_OBJECTS", "RTK_LATE_NATIVE_CALLS"} {
		value := os.Getenv(name)
		if value == "" {
			t.Fatal("isolated shell Job backend input unavailable", name)
		}
		if previous, exists := f.env[name]; exists && previous != value {
			t.Fatal("isolated shell backend input changed", name)
		}
		f.env[name] = value
	}
	if !slices.Contains(f.ports, port) {
		f.ports = append(f.ports, port)
	}
}

// This is a shell-wrapper wiring probe using a synthetic Go CLI, not the real
// fresh create. The executed migration process is real and Job failures flow
// back through the operating-system network sandbox.
func verifyIsolatedMigrationControllerThroughShell(t *testing.T, docker, network, owner string, selected map[string]string, accountJob deploymentplan.Object, accountSecret map[string]map[string]string, failedJob deploymentplan.Object) {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	environmentRoot := filepath.Join(workspace, "cloud_env", "dev")
	cfg, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, original, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"ISOLATED_OPERATOR": "synthetic"}})
	parent, _ := providerExecutionContractFork(t, original, plan, "isolated-migration-shell")
	actual, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(cfg) {
		t.Fatal("synthetic parent configuration changed", err)
	}
	namespace := "video-cloud-dev-account-manager"
	encoded := deploymentplan.Object{}
	for name, value := range accountSecret["account-manager-runtime"] {
		encoded[name] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	secret := deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": "account-manager-runtime", "namespace": namespace, "uid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, "data": encoded}
	objects := map[string]deploymentplan.Object{"Job/" + namespace + "/account-manager-migrate": accountJob, "Secret/" + namespace + "/account-manager-runtime": secret, "Job/video-cloud-dev-video-cloud/pki-grants": failedJob}
	backend, objectDir, _ := unifiedSessionNativeBackend(t, store, objects, nil)
	if err := store.write("kube/kubeconfig.yaml", backend.KubeconfigContent, false); err != nil {
		t.Fatal(err)
	}
	controller, port := installIsolatedMigrationController(t, store, objectDir, docker, network, owner, "video-cloud-dev", selected)
	bin := t.TempDir()
	const fakeGo = `#!/bin/sh
set -eu
[ "$1" = build ] && [ "$2" = -o ] || exit 81
cat > "$3" <<'INNER'
#!/bin/sh
exec "$RTK_CLOUD_KUBECTL" --kubeconfig "$RTK_TEST_JOB_KUBECONFIG" --request-timeout=90s --namespace "$RTK_SHELL_JOB_NAMESPACE" wait --for=condition=complete "Job/$RTK_SHELL_JOB_NAME" --timeout=60s
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
	f := isolatedScriptInvocation{workspace: workspace, environmentRoot: environmentRoot, fakeBin: bin, store: store, execution: parent, env: map[string]string{"RTK_SHELL_JOB_NAMESPACE": namespace, "RTK_SHELL_JOB_NAME": "account-manager-migrate"}}
	attachIsolatedMigrationControllerToScript(t, &f, controller, port)
	output, err := f.run(t, "preflight", "--operation", "create")
	if err != nil {
		t.Fatal("isolated shell did not propagate real migration outcome", err, output)
	}
	f.env["RTK_SHELL_JOB_NAMESPACE"] = "video-cloud-dev-video-cloud"
	f.env["RTK_SHELL_JOB_NAME"] = "pki-grants"
	if output, err = f.run(t, "create", "--confirm", parent.Journal.Stack); err == nil {
		t.Fatal("isolated shell concealed real Job failure", output)
	}
	if len(controller.runs) != 2 {
		t.Fatal("isolated shell did not execute original two Job owners")
	}
	failed, readErr := backend.Read(t.Context(), "Job/video-cloud-dev-video-cloud/pki-grants")
	if readErr != nil {
		t.Fatal(readErr)
	}
	conditions := certIssuerObjectList(deploymentplan.Map(failed["status"])["conditions"])
	if len(conditions) != 1 || deploymentplan.Map(conditions[0])["type"] != "Failed" {
		t.Fatal("real failing Job status was not retained")
	}
}

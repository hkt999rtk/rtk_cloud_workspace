package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the production CLI batch against the pinned provider. TLS/durable
// provider restart/TokenReview are tested by the native lifecycle fixture; this
// network-isolated dev server tests actual mount/ACL/role semantics, not mocks.
func TestCreateCustodyBootstrapAgainstRealOpenBao(t *testing.T) {
	if os.Getenv("RTK_CUSTODY_OPENBAO_INTEGRATION") != "1" {
		t.Skip("real isolated OpenBao fixture not selected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	run := func(input []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdin = bytes.NewReader(input)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("isolated OpenBao operation %s failed (private output suppressed)", args[0])
		}
		return raw
	}
	image := "quay.io/openbao/openbao@sha256:436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115"
	id := strings.TrimSpace(string(run(nil, "run", "-d", "--pull=never", "--platform=linux/amd64", "--network=none", "--user=0", "--label=rtk.test=custody-bootstrap", "--entrypoint=/bin/sh", image, "-ec", "printf 'disable_mlock = true\\n' >/tmp/custody.hcl; exec bao server -config=/tmp/custody.hcl -dev -dev-listen-address=127.0.0.1:8200 -dev-root-token-id=fixture-custody-token")))
	t.Cleanup(func() {
		if exec.Command("docker", "rm", "-f", id).Run() != nil {
			t.Error("isolated OpenBao cleanup failed")
		}
	})
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if exec.CommandContext(ctx, "docker", "exec", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "bao", "status").Run() == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("provider startup canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		t.Fatal("isolated provider did not start")
	}
	run(nil, "exec", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "BAO_TOKEN=fixture-custody-token", "bao", "auth", "enable", "kubernetes")
	// The checked production wrapper retains its selected CA path; the CLI
	// loads that CA even for this fixture's isolated HTTP endpoint.
	ca := testOpenBaoCAPEM(t)
	run([]byte(ca), "exec", "-i", id, "/bin/sh", "-ec", "mkdir -p /openbao/transport; cat >/openbao/transport/ca.crt; chmod 0600 /openbao/transport/ca.crt")
	cfg, store, backend, e, _ := createOpenBaoNativeFixture(t, "valid")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	if err := store.write("openbao/root-token", []byte("fixture-custody-token\n"), false); err != nil {
		t.Fatal(err)
	}
	caRelative := filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport", "ca.crt")
	if err := store.write(caRelative, []byte(ca), true); err != nil {
		t.Fatal(err)
	}
	p := environmentCreateBootstrapPlan{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"]}
	binding, err := createEnvironmentCustodyBinding(cfg, p, e.Journal.PlanSHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "kubectl")
	writeTestFile(t, tool, `#!/bin/sh
set -eu
for arg do script="$arg"; done
exec docker exec -i "$RTK_CUSTODY_DOCKER_ID" env BAO_ADDR=http://127.0.0.1:8200 BAO_CACERT=/openbao/transport/ca.crt /bin/sh -ec "$script"
`)
	if err := os.Chmod(tool, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CUSTODY_DOCKER_ID", id)
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: filepath.Join(store.Root, "openbao/root-token"), CACertPath: filepath.Join(store.Root, caRelative)}}
	var stages []string
	apply := func(_ context.Context, stage string, _ []deploymentplan.Definition) error {
		stages = append(stages, stage)
		return nil
	}
	// The deliberately absent migration recipe stops AFTER the real custody
	// initializer. This verifies the production foundation dependency boundary.
	runtime, _, err = continueEnvironmentCreateAfterOpenBao(ctx, cfg, &environmentCreatePlan{Bootstrap: p}, store, nil, e, apply, runtime, environmentCreateTransport{})
	if err == nil || runtime.Custody == nil || len(stages) != 1 || !strings.HasPrefix(stages[0], "custody-transport-trust-") {
		t.Fatal("custody did not initialize before SQL/managed identity stages", err)
	}
	if err = configureEnvironmentCreateCustody(ctx, cfg, runtime, binding); err != nil {
		t.Fatal("completed scope readback:", err)
	}
	// The provider itself enforces CAS, including for a root caller.
	if exec.CommandContext(ctx, "docker", "exec", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "BAO_TOKEN=fixture-custody-token", "bao", "kv", "put", "identities/no-cas", "value=fixture").Run() == nil {
		t.Fatal("real KV accepted an unconditional identity write")
	}
	owner := binding.Owners[0]
	prefix := cfg.Environment + "/" + binding.Generation + "/" + owner.ID
	role := environmentCustodyRoleName(prefix)
	token := bytes.TrimSpace(run(nil, "exec", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "BAO_TOKEN=fixture-custody-token", "bao", "token", "create", "-policy="+role, "-no-default-policy", "-ttl=1m", "-field=token"))
	if len(owner.Clients) == 0 {
		t.Fatal("isolated fixture has no declared identity")
	}
	const write = "IFS= read -r BAO_TOKEN; export BAO_TOKEN; bao kv put -cas=0 identities/"
	command := write + environmentCustodyRecordKey(prefix, owner.Clients[0].StatePath) + " value=fixture"
	run(append(bytes.Clone(token), '\n'), "exec", "-i", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "/bin/sh", "-ec", command)
	for _, key := range []string{environmentCustodyRecordKey(prefix, "/unbound/private.json"), "staging/foreign/identity"} {
		denied := exec.CommandContext(ctx, "docker", "exec", "-i", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "/bin/sh", "-ec", write+key+" value=fixture")
		denied.Stdin = bytes.NewReader(append(bytes.Clone(token), '\n'))
		output, err := denied.CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("403")) {
			t.Fatal("real scoped policy did not deny an unbound private record")
		}
	}
	reader := environmentCustodyReaderName(prefix)
	readerToken := bytes.TrimSpace(run(nil, "exec", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "BAO_TOKEN=fixture-custody-token", "bao", "token", "create", "-policy="+reader, "-no-default-policy", "-ttl=1m", "-field=token"))
	readerInput := append(bytes.Clone(readerToken), '\n')
	key := environmentCustodyRecordKey(prefix, owner.Clients[0].StatePath)
	read := "IFS= read -r BAO_TOKEN; export BAO_TOKEN; bao kv get -field=value identities/"
	if value := run(readerInput, "exec", "-i", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "/bin/sh", "-ec", read+key); strings.TrimSpace(string(value)) != "fixture" {
		t.Fatal("real inspection policy cannot read its declared credential")
	}
	publicKey := environmentCustodyRecordKey(prefix+"/public", "/public/initial.json")
	for _, command := range []string{write + publicKey + " value=fixture", strings.Replace(write, "-cas=0", "-cas=1", 1) + key + " value=changed",
		read + environmentCustodyRecordKey(prefix, "/unbound/private.json")} {
		denied := exec.CommandContext(ctx, "docker", "exec", "-i", id, "env", "BAO_ADDR=http://127.0.0.1:8200", "/bin/sh", "-ec", command)
		denied.Stdin = bytes.NewReader(readerInput)
		output, err := denied.CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("403")) {
			t.Fatal("real inspection policy allowed mutation or unbound credential access")
		}
	}
}

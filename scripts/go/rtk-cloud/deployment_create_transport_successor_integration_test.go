package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercises the actual OpenBao listener reload with a successor CA and the
// original serving private key. The isolated container has no network access.
func TestInitialTransportSuccessorRealOpenBaoTLSReload(t *testing.T) {
	if os.Getenv("RTK_CUSTODY_OPENBAO_INTEGRATION") != "1" {
		t.Skip("real isolated OpenBao fixture not selected")
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	now := time.Now().UTC()
	old, err := createEnvironmentTransport(store, strings.Repeat("a", 64), "openbao", map[string][]string{"openbao": {"localhost"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := renewInitialTransportMaterial(old, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if old.Identities["openbao"].Key != next.Identities["openbao"].Key {
		t.Fatal("serving key changed")
	}
	dir := t.TempDir()
	files := map[string]string{
		"server.hcl": "disable_mlock = true\nlistener \"tcp\" {\n address = \"127.0.0.1:8200\"\n tls_cert_file = \"/tmp/tls.crt\"\n tls_key_file = \"/tmp/tls.key\"\n}\nstorage \"inmem\" {}\napi_addr = \"https://localhost:8200\"\n",
		"tls.crt":    old.Identities["openbao"].Certificate,
		"tls.key":    old.Identities["openbao"].Key,
		"ca.crt":     old.CACert,
		"old-ca.crt": old.CACert,
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated OpenBao %s failed: %s", args[0], strings.TrimSpace(string(out)))
		}
		return out
	}
	image := "quay.io/openbao/openbao@sha256:436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115"
	id := strings.TrimSpace(string(run("create", "--platform=linux/amd64", "--network=none", "--user=0", "--label=rtk.test=transport-reload", "--entrypoint=bao", image, "server", "-config=/tmp/server.hcl")))
	t.Cleanup(func() {
		if err := exec.Command("docker", "rm", "-f", id).Run(); err != nil {
			t.Error("isolated OpenBao cleanup failed")
		}
	})
	for name := range files {
		run("cp", filepath.Join(dir, name), id+":/tmp/"+name)
	}
	run("start", id)
	status := func(ca string) bool {
		out, _ := exec.CommandContext(ctx, "docker", "exec", id, "env", "BAO_ADDR=https://localhost:8200", "BAO_CACERT=/tmp/"+ca, "bao", "status", "-format=json").Output()
		var value struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
		}
		return json.Unmarshal(out, &value) == nil && !value.Initialized && value.Sealed
	}
	wait := func(ca string) {
		t.Helper()
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if status(ca) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("isolated OpenBao did not accept %s", ca)
	}
	wait("ca.crt")
	for name, value := range map[string]string{"tls.crt": next.Identities["openbao"].Certificate, "ca.crt": next.CACert} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		run("cp", filepath.Join(dir, name), id+":/tmp/"+name)
	}
	run("kill", "--signal=HUP", id)
	wait("ca.crt")
	if status("old-ca.crt") {
		t.Fatal("old CA still authenticates the reloaded successor listener")
	}
}

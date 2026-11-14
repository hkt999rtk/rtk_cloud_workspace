package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test uses a real isolated OpenBao process; Kubernetes ownership
// remains an explicit fixture. The production initializer records its own
// parent journal and performs real TLS init/unseal, audit and auth readback.
func TestIsolatedRealOpenBaoFormalInitialization(t *testing.T) {
	if os.Getenv("RTK_TEST_REAL_OPENBAO_INIT") != "1" {
		t.Skip("explicit isolated OpenBao integration required")
	}
	image := os.Getenv("RTK_TEST_OPENBAO_IMAGE")
	if image == "" {
		t.Fatal("explicit locally available test OpenBao image required")
	}
	cfg, store, backend, execution, _ := createOpenBaoNativeFixture(t, "valid")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	fallback, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", fallback)
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/private/tmp", "rtk-real-openbao-init-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	serverName := "openbao." + cfg.Values["CLOUD_STACK_NAME"] + "-secrets.svc"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: serverName}, DNSNames: []string{serverName}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for name, data := range map[string][]byte{"ca.crt": ca, "tls.crt": ca, "tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encodedKey})} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	caPath := filepath.Join(store.Root, "deployment", "create", "material", execution.Journal.PlanSHA256, "openbao-transport", "ca.crt")
	if err := os.WriteFile(caPath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.write("kube/kubeconfig.yaml", backend.KubeconfigContent, false); err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	owner := "rtk-openbao-test-" + hex.EncodeToString(random[:])
	config := `ui = false
disable_mlock = true
listener "tcp" {
  address = "127.0.0.1:8200"
  tls_cert_file = "/openbao/transport/tls.crt"
  tls_key_file = "/openbao/transport/tls.key"
  tls_min_version = "tls12"
}
storage "file" { path = "/openbao/data" }
audit "file" "deployment-audit" {
  options {
    file_path = "/openbao/audit/audit.jsonl"
    log_raw = "false"
  }
}
`
	server := `mkdir -p /openbao/data /openbao/audit /var/run/secrets/kubernetes.io/serviceaccount
cp /openbao/transport/ca.crt /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
printf %s "$RTK_TEST_CONFIG" > /tmp/rtk-openbao-test.hcl
exec bao server -config=/tmp/rtk-openbao-test.hcl`
	cmd := exec.Command(docker, "run", "--detach", "--rm", "--name", owner, "--label", "rtk.test.owner="+owner, "--network", "none", "--tmpfs", "/openbao/file:rw,size=16m", "--tmpfs", "/openbao/logs:rw,size=16m", "--mount", "type=bind,src="+root+",dst=/openbao/transport,readonly", "--env", "RTK_TEST_CONFIG="+config, "--env", "BAO_CACERT=/openbao/transport/ca.crt", "--entrypoint", "/bin/sh", image, "-ec", server)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal("isolated OpenBao container startup failed")
	}
	containerID := strings.TrimSpace(string(output))
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "--force", "--volumes", containerID).Run() })
	rpc, port := installIsolatedOpenBaoRPC(t, store, cfg.Values["CLOUD_STACK_NAME"]+"-secrets", containerID, owner)
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		status, err := readEnvironmentCreateOpenBaoStatus(context.Background(), cfg, backend)
		if err == nil && !status.Initialized && status.Sealed {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated real OpenBao did not reach uninitialized sealed state over verified TLS")
	}
	verifyIsolatedOpenBaoRPCSandbox(t, rpc, port, store)
	if _, err := environmentCreateOpenBaoExec(context.Background(), cfg, backend, nil, "cat /etc/passwd"); err == nil {
		t.Fatal("OpenBao RPC accepted an unregistered shell command")
	}
	if _, err := initializeEnvironmentCreateOpenBao(context.Background(), cfg, store, backend, execution); err != nil {
		t.Fatal("formal real OpenBao initialization failed", err)
	}
	for _, key := range []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config"} {
		found := false
		for _, entry := range execution.Journal.Entries {
			if entry.Key == key {
				found = entry.State == "completed" && len(entry.Receipt) > 0
			}
		}
		if !found {
			t.Fatal("formal initializer did not record accepted native outcome", key)
		}
	}
	status, err := readEnvironmentCreateOpenBaoStatus(context.Background(), cfg, backend)
	if err != nil || !status.Initialized || status.Sealed {
		t.Fatal("actual real OpenBao was not unsealed after formal initialization", err)
	}
	verifyIsolatedOpenBaoRPCThroughShell(t, containerID, owner)
}

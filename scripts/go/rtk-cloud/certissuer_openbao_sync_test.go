package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCertIssuerOpenBaoSyncScopesTrustRepair(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
`)
	logPath := fakeKubectlForCertIssuerOpenBaoSync(t, base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t)))
	kubeconfig := filepath.Join(workspace, "kubeconfig")
	writeTestFile(t, kubeconfig, "test")
	t.Setenv("RTK_CLOUD_KUBECONFIG", "previous-kubeconfig")

	if err := runCertIssuerOpenBaoSync([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--kubeconfig", kubeconfig,
		"--confirm", "video-cloud-staging",
	}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("RTK_CLOUD_KUBECONFIG"); got != "previous-kubeconfig" {
		t.Fatalf("RTK_CLOUD_KUBECONFIG was not restored: %q", got)
	}
	if err := os.Unsetenv("RTK_CLOUD_KUBECONFIG"); err != nil {
		t.Fatal(err)
	}
	if err := runCertIssuerOpenBaoSync([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--kubeconfig", kubeconfig,
		"--confirm", "video-cloud-staging",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("RTK_CLOUD_KUBECONFIG"); ok {
		t.Fatal("RTK_CLOUD_KUBECONFIG was left set after command")
	}
	log := readTestFile(t, logPath)
	for _, want := range []string{
		"get secret openbao-tls -o json",
		"get secret certissuer-openbao-auth -o json",
		"get deployment certissuer -o json",
		`"rtk.realtek.com/openbao-ca-checksum"`,
		"rollout status deployment/certissuer --timeout 5m",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("scoped sync log missing %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{"account-manager", "factoryenroll", "video-cloud-api", "cloud-admin"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("scoped sync touched %q:\n%s", forbidden, log)
		}
	}
}

func TestRunCertIssuerOpenBaoSyncRejectsInvalidInputs(t *testing.T) {
	if err := runCertIssuerOpenBaoSync([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runCertIssuerOpenBaoSync(nil); err == nil {
		t.Fatal("missing env root accepted")
	}
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
`)
	if err := runCertIssuerOpenBaoSync([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--confirm", "wrong-stack",
	}); err == nil {
		t.Fatal("incorrect stack confirmation accepted")
	}
}

func TestReconcileCertIssuerOpenBaoCAPreservesAppRoleAndRollsDeployment(t *testing.T) {
	ca := testOpenBaoCAPEM(t)
	encodedCA := base64.StdEncoding.EncodeToString(ca)
	openBao := map[string]any{"data": map[string]any{"ca.crt": encodedCA}}
	authData := map[string]any{
		"ca.crt":    base64.StdEncoding.EncodeToString([]byte("stale")),
		"role_id":   "preserve-role",
		"secret_id": "preserve-secret",
	}
	auth := map[string]any{"data": authData}
	deployment := map[string]any{"spec": map[string]any{"template": map[string]any{}}}

	changed, err := reconcileCertIssuerOpenBaoCA(openBao, auth, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || authData["ca.crt"] != encodedCA {
		t.Fatal("OpenBao CA was not synchronized")
	}
	if authData["role_id"] != "preserve-role" || authData["secret_id"] != "preserve-secret" {
		t.Fatal("AppRole material changed during CA synchronization")
	}
	annotations := deployment["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["rtk.realtek.com/openbao-ca-checksum"] == "" {
		t.Fatal("deployment rollout checksum is missing")
	}

	changed, err = reconcileCertIssuerOpenBaoCA(openBao, auth, deployment)
	if err != nil || changed {
		t.Fatalf("unchanged CA reconciled again: changed=%v err=%v", changed, err)
	}
}

func TestReconcileCertIssuerOpenBaoCARejectsMissingAppRole(t *testing.T) {
	encodedCA := base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
	openBao := map[string]any{"data": map[string]any{"ca.crt": encodedCA}}
	auth := map[string]any{"data": map[string]any{"ca.crt": "stale", "role_id": "role"}}
	deployment := map[string]any{"spec": map[string]any{"template": map[string]any{}}}
	if _, err := reconcileCertIssuerOpenBaoCA(openBao, auth, deployment); err == nil {
		t.Fatal("missing AppRole secret accepted")
	}
}

func TestRequireOpenBaoUnsealed(t *testing.T) {
	if err := requireOpenBaoUnsealed(lkeOpenBaoStatus{Initialized: true}); err != nil {
		t.Fatalf("unsealed OpenBao rejected: %v", err)
	}
	for _, status := range []lkeOpenBaoStatus{
		{},
		{Initialized: true, Sealed: true},
	} {
		if err := requireOpenBaoUnsealed(status); err == nil {
			t.Fatalf("unsafe OpenBao status accepted: %+v", status)
		}
	}
}

func testOpenBaoCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-openbao-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-openbao-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func fakeKubectlForCertIssuerOpenBaoSync(t *testing.T, encodedCA string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
line='ARGS'
for arg in "$@"; do line="$line $arg"; done
printf '%s\n' "$line" >> "` + logPath + `"
if [[ "$*" == *"get --raw=/readyz"* ]]; then printf 'ok\n'; exit 0; fi
if [[ "$*" == *"get nodes -o name"* ]]; then printf 'node/test\n'; exit 0; fi
if [[ "$*" == *"exec openbao-0"*"bao status -format=json"* ]]; then
  printf '{"initialized":true,"sealed":false}\n'
  exit 0
fi
if [[ "$*" == *"get secret openbao-tls -o json"* ]]; then
  printf '{"data":{"ca.crt":"` + encodedCA + `"}}\n'
  exit 0
fi
if [[ "$*" == *"get secret certissuer-openbao-auth -o json"* ]]; then
  printf '{"data":{"ca.crt":"c3RhbGU=","role_id":"cm9sZQ==","secret_id":"c2VjcmV0"}}\n'
  exit 0
fi
if [[ "$*" == *"get deployment certissuer -o json"* ]]; then
  printf '{"spec":{"template":{"metadata":{"annotations":{"keep":"yes"}}}}}\n'
  exit 0
fi
if [[ "$*" == *"replace -f -"* ]]; then
  cat >> "` + logPath + `"
  printf '\n---\n' >> "` + logPath + `"
  exit 0
fi
if [[ "$*" == *"rollout status deployment/certissuer"* ]]; then exit 0; fi
exit 0
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_POLL", "1ms")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", "1")
	return logPath
}

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshRuntimeDeviceClientCABundleUsesLiveCertIssuerSecret(t *testing.T) {
	root, rootCert, rootKey := testSigningCA(t, "video-cloud-staging-root-ca", nil, nil, 1)
	device, _, _ := testSigningCA(t, "video-cloud-staging-device-ca", rootCert, rootKey, 2)
	app, _, _ := testSigningCA(t, "video-cloud-staging-app-ca", rootCert, rootKey, 3)
	workspace, envRoot := testRuntimeCAWorkspace(t)
	installRuntimeCAKubectlStub(t, map[string]string{
		"root-ca.crt":   base64.StdEncoding.EncodeToString([]byte(root)),
		"device-ca.crt": base64.StdEncoding.EncodeToString([]byte(device)),
		"app-ca.crt":    base64.StdEncoding.EncodeToString([]byte(app)),
	})

	path, err := refreshRuntimeDeviceClientCABundle(workspace, envRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(envRoot, "state", "secrets", "device-client-ca-bundle.pem")
	if path != wantPath {
		t.Fatalf("bundle path = %q, want %q", path, wantPath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), lkeClientCABundle(root, device, app); got != want {
		t.Fatal("runtime bundle does not contain the exact live root/device/app CA chain")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRefreshRuntimeDeviceClientCABundleRejectsMismatchedChainWithoutOverwrite(t *testing.T) {
	root, rootCert, rootKey := testSigningCA(t, "video-cloud-staging-root-ca", nil, nil, 1)
	device, _, _ := testSigningCA(t, "video-cloud-staging-device-ca", rootCert, rootKey, 2)
	otherRoot, otherRootCert, otherRootKey := testSigningCA(t, "foreign-root", nil, nil, 9)
	_ = otherRoot
	app, _, _ := testSigningCA(t, "video-cloud-staging-app-ca", otherRootCert, otherRootKey, 3)
	workspace, envRoot := testRuntimeCAWorkspace(t)
	path := filepath.Join(envRoot, "state", "secrets", "device-client-ca-bundle.pem")
	writeTestFile(t, path, "sentinel\n")
	installRuntimeCAKubectlStub(t, map[string]string{
		"root-ca.crt":   base64.StdEncoding.EncodeToString([]byte(root)),
		"device-ca.crt": base64.StdEncoding.EncodeToString([]byte(device)),
		"app-ca.crt":    base64.StdEncoding.EncodeToString([]byte(app)),
	})

	_, err := refreshRuntimeDeviceClientCABundle(workspace, envRoot)
	if err == nil || !strings.Contains(err.Error(), "app CA is not signed by the live root") {
		t.Fatalf("refresh error = %v, want mismatched app CA rejection", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "sentinel\n" {
		t.Fatal("invalid live chain overwrote the last local bundle")
	}
}

func testRuntimeCAWorkspace(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"), "apiVersion: v1\n")
	return workspace, envRoot
}

func installRuntimeCAKubectlStub(t *testing.T, data map[string]string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "kubectl")
	writeTestFile(t, stub, `#!/bin/sh
case "$*" in
  *"--raw=/readyz"*) exit 0 ;;
  *"get secret certissuer-runtime -o json"*) printf '%s' "$RTK_TEST_CERTISSUER_SECRET" ;;
  *) echo "unexpected kubectl arguments: $*" >&2; exit 2 ;;
esac
`)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", stub)
	t.Setenv("RTK_TEST_CERTISSUER_SECRET", string(raw))
}

func testSigningCA(t *testing.T, commonName string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, serial int64) (string, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	if parent == nil {
		parent = template
		parentKey = key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), cert, key
}

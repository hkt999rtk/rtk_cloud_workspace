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
	wantPath := filepath.Join(envRoot, "state", "pki", "device-client-ca-bundle.pem")
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

func TestRunRefreshRuntimeClientCAWritesLiveBundle(t *testing.T) {
	root, rootCert, rootKey := testSigningCA(t, "video-cloud-staging-root-ca", nil, nil, 11)
	device, _, _ := testSigningCA(t, "video-cloud-staging-device-ca", rootCert, rootKey, 12)
	app, _, _ := testSigningCA(t, "video-cloud-staging-app-ca", rootCert, rootKey, 13)
	workspace, envRoot := testRuntimeCAWorkspace(t)
	installRuntimeCAKubectlStub(t, map[string]string{
		"root-ca.crt":   base64.StdEncoding.EncodeToString([]byte(root)),
		"device-ca.crt": base64.StdEncoding.EncodeToString([]byte(device)),
		"app-ca.crt":    base64.StdEncoding.EncodeToString([]byte(app)),
	})

	if err := runRefreshRuntimeClientCA([]string{"--workspace", workspace, "--env-root", envRoot}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "pki", "device-client-ca-bundle.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestRunRefreshRuntimeClientCARejectsInvalidArguments(t *testing.T) {
	if err := runRefreshRuntimeClientCA([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag was accepted")
	}
	if err := runRefreshRuntimeClientCA([]string{"--workspace", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--env-root is required") {
		t.Fatalf("missing env-root error = %v", err)
	}
}

func TestValidateRuntimeClientCAStack(t *testing.T) {
	tests := []struct {
		name     string
		stack    string
		stackEnv map[string]string
		wantErr  bool
	}{
		{name: "shared staging", stack: "video-cloud-staging", stackEnv: map[string]string{}},
		{
			name:  "exact run scoped coverage stack",
			stack: "coverage-123-1",
			stackEnv: map[string]string{
				"CLOUD_ENV_NAME":               "runtime-coverage",
				"CLOUD_RUNTIME_COVERAGE_STACK": "coverage-123-1",
			},
		},
		{
			name:  "coverage metadata mismatch",
			stack: "coverage-123-1",
			stackEnv: map[string]string{
				"CLOUD_ENV_NAME":               "runtime-coverage",
				"CLOUD_RUNTIME_COVERAGE_STACK": "coverage-456-1",
			},
			wantErr: true,
		},
		{
			name:  "coverage name outside runtime coverage environment",
			stack: "coverage-123-1",
			stackEnv: map[string]string{
				"CLOUD_ENV_NAME":               "staging",
				"CLOUD_RUNTIME_COVERAGE_STACK": "coverage-123-1",
			},
			wantErr: true,
		},
		{name: "production", stack: "video-cloud-production", stackEnv: map[string]string{}, wantErr: true},
		{name: "arbitrary stack", stack: "developer-stack", stackEnv: map[string]string{}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRuntimeClientCAStack(test.stack, test.stackEnv)
			if test.wantErr && err == nil {
				t.Fatal("stack was accepted, want rejection")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("stack was rejected: %v", err)
			}
		})
	}
}

func TestRefreshRuntimeDeviceClientCABundleUsesRunScopedCoverageSecret(t *testing.T) {
	root, rootCert, rootKey := testSigningCA(t, "coverage-root-ca", nil, nil, 21)
	device, _, _ := testSigningCA(t, "coverage-device-ca", rootCert, rootKey, 22)
	app, _, _ := testSigningCA(t, "coverage-app-ca", rootCert, rootKey, 23)
	workspace, envRoot := testRuntimeCAWorkspace(t)
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), strings.Join([]string{
		"CLOUD_PROVIDER=lke",
		"CLOUD_ENV_NAME=runtime-coverage",
		"CLOUD_STACK_NAME=coverage-123-1",
		"CLOUD_RUNTIME_COVERAGE_STACK=coverage-123-1",
		"",
	}, "\n"))
	installRuntimeCAKubectlStub(t, map[string]string{
		"root-ca.crt":   base64.StdEncoding.EncodeToString([]byte(root)),
		"device-ca.crt": base64.StdEncoding.EncodeToString([]byte(device)),
		"app-ca.crt":    base64.StdEncoding.EncodeToString([]byte(app)),
	})

	if _, err := refreshRuntimeDeviceClientCABundle(workspace, envRoot); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshRuntimeDeviceClientCABundleRejectsMismatchedChainWithoutOverwrite(t *testing.T) {
	root, rootCert, rootKey := testSigningCA(t, "video-cloud-staging-root-ca", nil, nil, 1)
	device, _, _ := testSigningCA(t, "video-cloud-staging-device-ca", rootCert, rootKey, 2)
	otherRoot, otherRootCert, otherRootKey := testSigningCA(t, "foreign-root", nil, nil, 9)
	_ = otherRoot
	app, _, _ := testSigningCA(t, "video-cloud-staging-app-ca", otherRootCert, otherRootKey, 3)
	workspace, envRoot := testRuntimeCAWorkspace(t)
	path := filepath.Join(envRoot, "state", "pki", "device-client-ca-bundle.pem")
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

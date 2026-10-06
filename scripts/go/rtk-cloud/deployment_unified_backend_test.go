package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	"gopkg.in/yaml.v3"
)

func frozenKubernetesFixture(t *testing.T) map[string]any {
	makeIsolatedTestSecretStore(t, "dev")
	return map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "selected", "contexts": []any{map[string]any{"name": "selected", "context": map[string]any{"cluster": "selected", "user": "operator"}}}, "clusters": []any{map[string]any{"name": "selected", "cluster": map[string]any{"server": "https://selected.example:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}, map[string]any{"name": "unused", "cluster": map[string]any{"server": "http://other.example"}}}, "users": []any{map[string]any{"name": "operator", "user": map[string]any{"token": "private-frozen-token"}}, map[string]any{"name": "unused", "user": map[string]any{"exec": map[string]any{"command": "must-not-execute"}}}}}
}

func TestEnvironmentKubernetesIdentityUsesPrivateFrozenSelectedContext(t *testing.T) {
	c := frozenKubernetesFixture(t)
	raw, _ := yaml.Marshal(c)
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(backend.KubeconfigContent), "unused") || strings.Contains(string(backend.KubeconfigContent), "must-not-execute") {
		t.Fatal("unselected identity retained")
	}
	path, cleanup, err := backend.privateKubeconfig()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	store, _ := newSecretStore("", "dev")
	if !strings.HasPrefix(path, filepath.Join(store.Root, "deployment", "tmp")+string(filepath.Separator)) {
		t.Fatal("Kubernetes deployment identity escaped canonical environment temporary storage")
	}
	info, _ := os.Stat(path)
	directory, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || directory.Mode().Perm() != 0700 {
		t.Fatal("Kubernetes identity permissions")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != string(backend.KubeconfigContent) || !strings.Contains(string(actual), "private-frozen-token") {
		t.Fatal("API identity escaped snapshot")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("private API identity retained")
	}
}

func TestEnvironmentKubernetesIdentityRejectsUnfencedOrAmbiguousInputs(t *testing.T) {
	for _, mode := range []string{"http", "no-context", "no-ca", "external-ca", "proxy", "insecure", "external-token", "exec", "partial-client-key", "two-methods", "duplicate-context", "duplicate-cluster", "duplicate-user"} {
		t.Run(mode, func(t *testing.T) {
			c := frozenKubernetesFixture(t)
			cluster := c["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)
			user := c["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
			switch mode {
			case "http":
				cluster["server"] = "http://selected.example"
			case "no-context":
				c["current-context"] = "absent"
			case "no-ca":
				delete(cluster, "certificate-authority-data")
			case "external-ca":
				cluster["certificate-authority"] = "/private/other.pem"
			case "proxy":
				cluster["proxy-url"] = "https://other.example"
			case "insecure":
				cluster["insecure-skip-tls-verify"] = true
			case "external-token":
				user["tokenFile"] = "/private/other-token"
			case "exec":
				user["exec"] = map[string]any{"command": "must-not-execute"}
			case "partial-client-key":
				user["client-certificate-data"] = "partial"
			case "two-methods":
				user["client-certificate-data"] = "cert"
				user["client-key-data"] = "key"
			case "duplicate-context":
				c["contexts"] = append(c["contexts"].([]any), c["contexts"].([]any)[0])
			case "duplicate-cluster":
				c["clusters"] = append(c["clusters"].([]any), c["clusters"].([]any)[0])
			case "duplicate-user":
				c["users"] = append(c["users"].([]any), c["users"].([]any)[0])
			}
			raw, _ := yaml.Marshal(c)
			if _, err := newEnvironmentKubernetesBackend(string(raw), "dev"); err == nil {
				t.Fatal("unfenced identity accepted")
			}
		})
	}
	c := frozenKubernetesFixture(t)
	user := c["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
	delete(user, "token")
	cert, key := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
	user["client-certificate-data"] = cert
	user["client-key-data"] = key
	raw, _ := yaml.Marshal(c)
	if _, err := newEnvironmentKubernetesBackend(string(raw), "dev"); err != nil {
		t.Fatal("embedded client identity denied", err)
	}
}

func frozenClientIdentity(t *testing.T, expiry time.Time, usage x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "operator"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
}
func TestEnvironmentKubernetesIdentityRejectsExpiredMismatchedAndWrongPurposeCertificates(t *testing.T) {
	for _, mode := range []string{"malformed-ca", "unexpected-ca-content", "expired-client", "wrong-purpose", "wrong-key", "token-newline"} {
		t.Run(mode, func(t *testing.T) {
			c := frozenKubernetesFixture(t)
			cluster := c["clusters"].([]any)[0].(map[string]any)["cluster"].(map[string]any)
			user := c["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
			cert, key := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
			switch mode {
			case "malformed-ca":
				cluster["certificate-authority-data"] = "malformed"
			case "unexpected-ca-content":
				cluster["certificate-authority-data"] = base64.StdEncoding.EncodeToString(append(testOpenBaoCAPEM(t), []byte("secret trailing material")...))
			case "token-newline":
				user["token"] = "token\n"
			default:
				delete(user, "token")
				if mode == "expired-client" {
					cert, key = frozenClientIdentity(t, time.Now().Add(-time.Minute), x509.ExtKeyUsageClientAuth)
				}
				if mode == "wrong-purpose" {
					cert, key = frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)
				}
				if mode == "wrong-key" {
					_, key = frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
				}
				user["client-certificate-data"], user["client-key-data"] = cert, key
			}
			raw, _ := yaml.Marshal(c)
			if _, err := newEnvironmentKubernetesBackend(string(raw), "dev"); err == nil {
				t.Fatal("invalid API identity accepted")
			}
		})
	}
}

func TestEnvironmentAPICommandBoundsPrivateResponsesAndRemovesCredentials(t *testing.T) {
	directory := t.TempDir()
	receipt := filepath.Join(directory, "private-path.txt")
	stub := `#!/bin/sh
set -eu
printf '%s' "$2" > "$RTK_TEST_API_PATH_FILE"
test -r "$2"
printf 'PRIVATE_DIAGNOSTIC' >&2
head -c 33554433 /dev/zero
`
	if err := os.WriteFile(filepath.Join(directory, "kubectl"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_API_PATH_FILE", receipt)
	raw, _ := yaml.Marshal(frozenKubernetesFixture(t))
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	output, err := backend.command(context.Background(), nil, "get", "namespace", "-o", "json")
	if err == nil || len(output) != 0 || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC") || strings.Contains(err.Error(), "private-frozen-token") {
		t.Fatal("oversized/private subprocess output escaped", err)
	}
	path, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatal("private API identity survived a failed command")
	}
}

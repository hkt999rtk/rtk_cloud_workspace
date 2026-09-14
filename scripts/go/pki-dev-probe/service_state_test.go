package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serverChain(t *testing.T, names []string) (string, string) {
	t.Helper()
	now := time.Now().Add(-time.Minute)
	key := func() *ecdsa.PrivateKey {
		value, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	certificate := func(template, parent *x509.Certificate, public any, signer any) []byte {
		value, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	rootKey := key()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now, NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER := certificate(rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	intermediateKey := key()
	intermediateTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "intermediate"}, NotBefore: now, NotAfter: now.Add(12 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	intermediateDER := certificate(intermediateTemplate, root, &intermediateKey.PublicKey, rootKey)
	intermediate, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey := key()
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: now, NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER := certificate(leafTemplate, intermediate, &leafKey.PublicKey, intermediateKey)
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
	return chain, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func TestServiceStateExposesOnlyPublicMetadata(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	pair := server.TLS.Certificates[0]
	key, _ := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	private := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	raw, _ := json.Marshal(map[string]any{"subject": "service:account-manager", "current": map[string]string{
		"private_key_pem": private, "certificate_chain_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}))}})
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := serviceState(path, &out); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 9 || result["version"] != float64(0) || result["domain"] != "" || result["pending"] != false || len(result["fingerprint"].(string)) != 64 || strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), private) {
		t.Fatal("status exposed unexpected state")
	}
	os.Chmod(path, 0644)
	out.Reset()
	if err := serviceState(path, &out); err == nil || out.Len() != 0 {
		t.Fatal("accepted public state")
	}
	os.Chmod(path, 0600)
	os.WriteFile(path, []byte(`{"current":{"private_key_pem":"secret-invalid-key"}}`), 0600)
	if err := serviceState(path, &out); err == nil || strings.Contains(err.Error(), "secret-invalid-key") || out.Len() != 0 {
		t.Fatal("invalid key leaked")
	}
}

func TestClearPendingRequiresExactRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	pair := server.TLS.Certificates[0]
	key, _ := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	raw, _ := json.Marshal(map[string]any{"subject": "service:test", "current": map[string]string{
		"private_key_pem":       string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})),
		"certificate_chain_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}))},
		"pending": map[string]string{"request_id": "request-1", "private_key_pem": "pending-key"}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := clearPending(path, "other"); err == nil {
		t.Fatal("wrong request cleared pending state")
	}
	if err := clearPending(path, "request-1"); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "pending-key") || !strings.Contains(string(stored), `"current"`) {
		t.Fatal("pending cleanup changed retained identity")
	}
}

func TestMigrateServerStateAddsOnlyVerifiedPolicy(t *testing.T) {
	names := []string{"openbao.dev.svc", "openbao.dev.svc.cluster.local"}
	chain, key := serverChain(t, names)
	path := filepath.Join(t.TempDir(), "server.json")
	raw, err := json.Marshal(map[string]any{"subject": names[0], "current": map[string]string{"private_key_pem": key, "certificate_chain_pem": chain}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(chain[strings.LastIndex(chain, "-----BEGIN CERTIFICATE-----"):]))
	if block == nil {
		t.Fatal("root missing")
	}
	root := sha256.Sum256(block.Bytes)
	if err = migrateServerState(path, "openbao_tls", fmt.Sprintf("%x", root), names); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Version int    `json:"version"`
		Domain  string `json:"domain"`
		Current struct {
			Key   string `json:"private_key_pem"`
			Chain string `json:"certificate_chain_pem"`
		} `json:"current"`
	}
	if json.Unmarshal(updated, &state) != nil || state.Version != 1 || state.Domain != "openbao_tls" || state.Current.Key != key || state.Current.Chain != chain {
		t.Fatal("verified migration did not preserve identity")
	}
	before := string(updated)
	if err = migrateServerState(path, "openbao_tls", strings.Repeat("0", 64), names); err == nil {
		t.Fatal("migration accepted a changed identity policy")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != before {
		t.Fatal("migration accepted a changed identity policy")
	}
}

package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if len(result) != 6 || result["pending"] != false || len(result["fingerprint"].(string)) != 64 || strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), private) {
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

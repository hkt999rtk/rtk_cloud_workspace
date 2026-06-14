package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteDeviceKeyAndCSRUsesEd25519CNOnly(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "device.key.pem")
	csrPath := filepath.Join(dir, "device.csr.pem")

	key, err := writeDeviceKeyAndCSR(keyPath, csrPath, "load-device-0001", "camera", "ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.Public().(ed25519.PublicKey); !ok {
		t.Fatalf("device key public type = %T, want ed25519.PublicKey", key.Public())
	}
	raw, err := os.ReadFile(csrPath)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		t.Fatalf("unexpected CSR PEM block=%v rest=%q", block, rest)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != "load-device-0001" {
		t.Fatalf("CSR CN = %q", csr.Subject.CommonName)
	}
	if len(csr.DNSNames) != 0 || len(csr.URIs) != 0 || len(csr.IPAddresses) != 0 || len(csr.EmailAddresses) != 0 {
		t.Fatalf("load-device CSR must not contain unvalidated SANs: dns=%v uris=%v ips=%v emails=%v", csr.DNSNames, csr.URIs, csr.IPAddresses, csr.EmailAddresses)
	}
}

func TestFactoryEnrollDeviceUsesDistinctP256FallbackRequestID(t *testing.T) {
	dir := t.TempDir()
	csrPath := filepath.Join(dir, "device.csr.pem")
	if _, err := writeDeviceKeyAndCSR(filepath.Join(dir, "device.key.pem"), csrPath, "load-device-0001", "camera", "p256"); err != nil {
		t.Fatal(err)
	}

	var gotRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotRequestID, _ = body["request_id"].(string)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate_pem":       "test-cert",
			"certificate_chain_pem": "test-chain",
			"serial_number":         "test-serial",
		})
	}))
	defer server.Close()

	outcome, err := factoryEnrollDevice(loadDeviceInput{
		Index: 1, FactoryURL: server.URL, FactoryAuthKey: "test-key", RunID: "run-1",
		SerialPrefix: "LOAD", Timeout: time.Second,
	}, "load-device-0001", "Load Device 0001", csrPath, filepath.Join(dir, "device.cert.pem"), filepath.Join(dir, "device.chain.pem"), "p256")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.OK {
		t.Fatalf("outcome = %+v", outcome)
	}
	if gotRequestID != "run-1-load-device-0001-p256" || outcome.RequestID != gotRequestID {
		t.Fatalf("request_id = %q outcome=%q", gotRequestID, outcome.RequestID)
	}
}

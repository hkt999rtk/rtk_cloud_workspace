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
	"strings"
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

func TestFactoryEnrollURLForDeviceDistributesAcrossURLs(t *testing.T) {
	raw := " http://127.0.0.1:18443/ ,http://127.0.0.1:18444,http://127.0.0.1:18445/ "
	want := map[int]string{
		0: "http://127.0.0.1:18443",
		1: "http://127.0.0.1:18443",
		2: "http://127.0.0.1:18444",
		3: "http://127.0.0.1:18445",
		4: "http://127.0.0.1:18443",
		5: "http://127.0.0.1:18444",
	}
	for index, expected := range want {
		if got := factoryEnrollURLForDevice(raw, index); got != expected {
			t.Fatalf("factoryEnrollURLForDevice(index=%d) = %q, want %q", index, got, expected)
		}
	}
}

func TestWriteLoadDeviceReusesCompleteLocalFactoryArtifact(t *testing.T) {
	outDir := t.TempDir()
	_, caCert, err := writeGeneratedCA(filepath.Join(t.TempDir(), "ca"), 1)
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			http.Error(w, "factory enroll should not be called for a complete local device", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate_pem":       string(caCert),
			"certificate_chain_pem": string(caCert),
			"serial_number":         "test-serial",
		})
	}))
	defer server.Close()

	in := loadDeviceInput{
		Index:          1,
		Ordinal:        1,
		Type:           loadDeviceTypes[0],
		Prefix:         "load-device",
		OutDir:         outDir,
		FactoryURL:     server.URL,
		FactoryAuthKey: "test-key",
		FactoryID:      "factory",
		LineID:         "line",
		StationID:      "station",
		FixtureID:      "fixture",
		OperatorID:     "operator",
		BatchID:        "batch",
		SerialPrefix:   "LOAD",
		RunID:          "run-1",
		Timeout:        time.Second,
		ResultsPath:    filepath.Join(outDir, "factory-enroll-results.jsonl"),
	}
	first, ok, err := writeLoadDevice(in)
	if err != nil || !ok {
		t.Fatalf("first writeLoadDevice() device=%+v ok=%v err=%v", first, ok, err)
	}

	second, ok, err := writeLoadDevice(in)
	if err != nil || !ok {
		t.Fatalf("second writeLoadDevice() device=%+v ok=%v err=%v", second, ok, err)
	}
	if requests != 1 {
		t.Fatalf("factory enroll requests = %d, want 1", requests)
	}
	if second.DeviceID != first.DeviceID || second.CertificatePath != first.CertificatePath || second.KeyPath != first.KeyPath {
		t.Fatalf("reused device mismatch: first=%+v second=%+v", first, second)
	}
}

func TestWriteLoadDeviceRetriesTransientFactoryFailure(t *testing.T) {
	t.Setenv("CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRIES", "3")
	t.Setenv("CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRY_DELAY", "0s")
	outDir := t.TempDir()
	_, caCert, err := writeGeneratedCA(filepath.Join(t.TempDir(), "ca"), 1)
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests < 3 {
			http.Error(w, "temporary issuer failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate_pem":       string(caCert),
			"certificate_chain_pem": string(caCert),
			"serial_number":         "retry-serial",
		})
	}))
	defer server.Close()

	in := loadDeviceInput{
		Index: 1, Ordinal: 1, Type: loadDeviceTypes[0], Prefix: "retry-device", OutDir: outDir,
		FactoryURL: server.URL, FactoryAuthKey: "test-key", FactoryID: "factory", LineID: "line",
		StationID: "station", FixtureID: "fixture", OperatorID: "operator", BatchID: "batch",
		SerialPrefix: "LOAD", RunID: "run-retry", Timeout: time.Second,
		ResultsPath: filepath.Join(outDir, "factory-enroll-results.jsonl"),
	}
	device, ok, err := writeLoadDevice(in)
	if err != nil || !ok {
		t.Fatalf("writeLoadDevice() device=%+v ok=%v err=%v", device, ok, err)
	}
	if requests != 3 {
		t.Fatalf("factory enroll requests = %d, want 3", requests)
	}
	if device.KeyAlgorithm != "ed25519" {
		t.Fatalf("key algorithm = %q, want same-algorithm retry success", device.KeyAlgorithm)
	}
}

func TestGenerateLoadDevicesForceReusesCompleteLocalFactoryArtifact(t *testing.T) {
	envRoot := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "devices")
	_, caCert, err := writeGeneratedCA(filepath.Join(t.TempDir(), "ca"), 1)
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			http.Error(w, "factory enroll should not be called during force reuse", http.StatusInternalServerError)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate_pem":       string(caCert),
			"certificate_chain_pem": string(caCert),
			"request_id":            stringValue(body["request_id"]),
			"serial_number":         "test-serial",
		})
	}))
	defer server.Close()

	args := []string{
		"--workspace", t.TempDir(),
		"--env-root", envRoot,
		"--out-dir", outDir,
		"--count", "1",
		"--mix", "camera=1",
		"--prefix", "load-device",
		"--factory-url", server.URL,
		"--factory-auth-key", "test-key",
		"--run-id", "run-1",
		"--force",
		"--concurrency", "1",
	}
	if err := runGenerateLoadDevices(args); err != nil {
		t.Fatalf("first runGenerateLoadDevices() error = %v", err)
	}
	if err := runGenerateLoadDevices(args); err != nil {
		t.Fatalf("second runGenerateLoadDevices() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("factory enroll requests = %d, want 1", requests)
	}
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cred, err := store.ReadDeviceCredential("RTK", "load-device-0001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cred.CertPEM) == "" || strings.TrimSpace(cred.KeyPEM) == "" {
		t.Fatalf("SQLite credential missing cert/key after reuse: %+v", cred)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifests", "factory-enroll-results.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("legacy factory enroll result should be cleaned after SQLite import, stat err=%v", err)
	}
}

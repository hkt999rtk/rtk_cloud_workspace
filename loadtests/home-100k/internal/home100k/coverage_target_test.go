package home100k

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateFixtureCertificatesAcceptsDeviceAndAppIdentities(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	ca, caKey, caPEM := newFixtureCA(t)
	leafPEM, keyPEM := newFixtureIdentity(t, ca, caKey, "load-device")
	chainPEM := string(leafPEM) + string(caPEM)

	db := openHomeTestDataForUpdate(t, envRoot)
	defer db.Close()
	if _, err := db.Exec(`alter table users add column role text not null default 'member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update device_credentials set cert_pem = ?, key_pem = ?, chain_pem = ?`, string(leafPEM), string(keyPEM), chainPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update device_bindings set device_type = 'gateway', service_options_json = '["mqtt"]'`); err != nil {
		t.Fatal(err)
	}
	credentials, _ := json.Marshal(map[string]string{"private_key_pem": string(keyPEM)})
	certificate, _ := json.Marshal(map[string]string{"certificate_chain_pem": chainPEM})
	if _, err := db.Exec(`update users set app_credentials_json = ?, app_certificate_json = ?`, string(credentials), string(certificate)); err != nil {
		t.Fatal(err)
	}

	plan, err := NewPlan(PlanOptions{
		EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea",
		DeviceCount: 2, UserCount: 2, DevicesPerUser: 1, VMCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to load fixture CA")
	}
	if err := validateFixtureCertificates(envRoot, plan, roots); err != nil {
		t.Fatalf("validateFixtureCertificates() error = %v", err)
	}
	caPath := filepath.Join(envRoot, "device-client-ca.crt")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"preflight",
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "2",
		"--users", "2",
		"--devices-per-user", "1",
		"--vm-count", "1",
		"--ca-bundle", caPath,
	}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"status": "PASS"`) {
		t.Fatalf("preflight code = %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	if err := verifyFixtureCertificate("bad-device", "not-pem", string(keyPEM), chainPEM, roots); err == nil || !strings.Contains(err.Error(), "leaf certificate is invalid") {
		t.Fatalf("invalid leaf error = %v", err)
	}
	if got := firstStringMapValue(map[string]any{"secondary": "value"}, "primary", "secondary"); got != "value" {
		t.Fatalf("firstStringMapValue() = %q", got)
	}
	if firstCertificateFromPEM("not-pem") != nil {
		t.Fatal("invalid PEM unexpectedly parsed")
	}
}

func TestWriteCredentialBundleCommandProducesResumableShard(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	outDir := filepath.Join(t.TempDir(), "bundle")
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"write-credential-bundle",
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "2",
		"--users", "2",
		"--devices-per-user", "1",
		"--vm-count", "1",
		"--label", "lg01",
		"--out-dir", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	var bundle shardCredentialBundle
	if err := json.Unmarshal(stdout.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.DeviceCount != 2 || bundle.SHA256 == "" {
		t.Fatalf("bundle = %#v", bundle)
	}
	for _, path := range []string{bundle.CompressedPath, bundle.ManifestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing bundle artifact %s: %v", path, err)
		}
	}
}

func TestRunnerDaemonHandlersEnforceStartContract(t *testing.T) {
	state := &runnerDaemonState{
		runID: "run-001", outDir: t.TempDir(), done: make(chan struct{}),
		telemetry: VMStartTelemetry{Label: "lg01", RunID: "run-001", Status: "READY_WAIT"},
	}
	for _, path := range []string{"/ready", "/status"} {
		rec := httptestResponse(t, http.MethodGet, path, "", func(w http.ResponseWriter, r *http.Request) {
			if path == "/ready" {
				state.handleReady(w, r)
			} else {
				state.handleStatus(w, r)
			}
		})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "READY_WAIT") {
			t.Fatalf("%s response = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	tests := []struct {
		name, method, body string
		want               int
	}{
		{name: "method", method: http.MethodGet, body: `{}`, want: http.StatusMethodNotAllowed},
		{name: "invalid JSON", method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "wrong run", method: http.MethodPost, body: `{"run_id":"other"}`, want: http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptestResponse(t, tc.method, "/start", tc.body, state.handleStart)
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	state.started = true
	rec := httptestResponse(t, http.MethodPost, "/start", `{"run_id":"run-001"}`, state.handleStart)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "READY_WAIT") {
		t.Fatalf("duplicate start = %d %s", rec.Code, rec.Body.String())
	}
}

func TestExecuteRunnerDaemonRejectsMissingAndUnknownAssignments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := executeRunnerDaemon(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--role is required") {
		t.Fatalf("missing role code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := executeRunnerDaemon([]string{
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--role", "unknown",
		"--shard-index", "999",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "assignment not found") {
		t.Fatalf("unknown assignment code=%d stderr=%s", code, stderr.String())
	}
}

func TestCoordinateRemoteRunnerStartCompletesReadyStartAndStatusBarriers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Skipf("runner control port is in use: %v", err)
	}
	started := false
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			_ = json.NewEncoder(w).Encode(VMStartTelemetry{Label: "lg01", RunID: "run-001", Status: "READY_WAIT"})
		case "/start":
			started = true
			w.WriteHeader(http.StatusAccepted)
		case "/status":
			if !started {
				http.Error(w, "not started", http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(VMStartTelemetry{
				Label: "lg01", RunID: "run-001", Status: "completed",
				StageStartedAt: timestamp, StageCompletedAt: timestamp,
			})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	result, err := coordinateRemoteRunnerStart(
		[]LinodeVM{{Label: "lg01", PublicIPv4: "127.0.0.1"}},
		Plan{}, "run-001", workflowFlagValues{coordinatorDelayMS: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReadyBarrier != "1/1" || result.StartDelayMS != 1 || len(result.VMs) != 1 {
		t.Fatalf("coordination = %#v", result)
	}
}

func httptestResponse(t *testing.T, method, path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func openHomeTestDataForUpdate(t *testing.T, envRoot string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", homeTestDataDBPath(envRoot, "RTK"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newFixtureCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newFixtureIdentity(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

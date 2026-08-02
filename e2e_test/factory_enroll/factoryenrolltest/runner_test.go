package factoryenrolltest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignHMACMatchesCanonicalFactoryEnrollmentExample(t *testing.T) {
	body := []byte(`{"request_id":"factory-req-001","devid":"factory-device-001"}`)
	got := SignHMAC([]byte("factory-secret"), http.MethodPost, enrollPath, "2026-05-15T00:00:00Z", "factory-req-001", body)
	want := "v1=f071ab74238e71f4d8c16e1335ef2b20d4716572f99b76054e5bd7f7729d15d7"
	if got != want {
		t.Fatalf("SignHMAC() = %q, want %q", got, want)
	}
}

func TestGenerateDeviceIdentityUsesPublicKeyFingerprintAsDeviceID(t *testing.T) {
	key, deviceID, csrPEM, _, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatalf("GenerateDeviceIdentity() error = %v", err)
	}
	if !strings.HasPrefix(deviceID, "pk-") || len(deviceID) != 67 {
		t.Fatalf("device id = %q", deviceID)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		t.Fatal("missing CSR PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificateRequest() error = %v", err)
	}
	if csr.Subject.CommonName != deviceID {
		t.Fatalf("CSR CN = %q, want %q", csr.Subject.CommonName, deviceID)
	}
	if csr.PublicKey.(*ecdsa.PublicKey).X.Cmp(key.PublicKey.X) != 0 {
		t.Fatal("CSR public key does not match generated key")
	}
}

func TestRunnerEnrollsMultipleDevicesAndValidatesCertificates(t *testing.T) {
	signer := newTestSigner(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != enrollPath {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Video-Cloud-Signature"); !strings.HasPrefix(got, "v1=") {
			t.Fatalf("missing signature: %q", got)
		}
		var in EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		certPEM := signer.signCSR(t, in.DeviceID, in.CSRPem)
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			RequestID:           in.RequestID,
			IssuerRequestID:     in.RequestID,
			DeviceID:            in.DeviceID,
			SerialNumber:        in.SerialNumber,
			NotBefore:           time.Now().UTC(),
			NotAfter:            time.Now().Add(time.Hour).UTC(),
			CertificatePEM:      string(certPEM),
			CertificateChainPEM: string(signer.caPEM),
			IssuedAt:            time.Now().UTC(),
		})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		FactoryURL:  server.URL,
		AuthKey:     "factory-secret",
		Count:       5,
		RunID:       "test-run",
		Concurrency: 2,
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Summary.Successes != 5 || result.Summary.Failures != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	for _, device := range result.Devices {
		if !device.Success || !device.ClientAuthUsable || !device.ChainVerified || device.CA || device.IssuerRequestID == "" {
			t.Fatalf("device result = %+v", device)
		}
	}
}

func TestRunnerPrefersProductionJWTAndRedactsItFromEvidence(t *testing.T) {
	const productionJWT = "header.payload.signature-secret-marker"
	signer := newTestSigner(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+productionJWT {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Video-Cloud-Signature"); got != "" {
			t.Fatalf("production JWT request must not send legacy HMAC signature, got %q", got)
		}
		if got := r.Header.Get("X-Video-Cloud-Timestamp"); got != "" {
			t.Fatalf("production JWT request must not send legacy timestamp, got %q", got)
		}
		var in EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			RequestID:           in.RequestID,
			IssuerRequestID:     "issuer-production-001",
			DeviceID:            in.DeviceID,
			SerialNumber:        in.SerialNumber,
			CertificatePEM:      string(signer.signCSR(t, in.DeviceID, in.CSRPem)),
			CertificateChainPEM: string(signer.caPEM),
		})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		FactoryURL:    server.URL,
		AuthKey:       "legacy-key-must-not-be-used",
		ProductionJWT: productionJWT,
		Count:         1,
		RunID:         "production-jwt-run",
		Concurrency:   1,
		Timeout:       time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.AuthMode != "production_jwt" || result.Summary.Successes != 1 {
		t.Fatalf("result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), productionJWT) || strings.Contains(RenderMarkdown(result), productionJWT) {
		t.Fatal("production JWT leaked into redacted evidence")
	}
}

func TestConfigRequiresFactoryAuthentication(t *testing.T) {
	cfg := Config{FactoryURL: "https://factory.example.test", Count: 1, Concurrency: 1}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "production JWT or auth key") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestQualificationRunsCanonicalProductionWorkflowAndRedactsSecrets(t *testing.T) {
	const (
		adminPassword = "admin-password-secret-marker"
		accessToken   = "admin-access-token-secret-marker"
		productionJWT = "production-jwt-secret-marker"
	)
	signer := newTestSigner(t)
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": accessToken}})
		case "/v1/admin/brand-clouds":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"brand_cloud": map[string]string{"id": "brand-001"}})
		case "/v1/admin/brand-clouds/brand-001/device-item-profiles":
			if r.Header.Get("Authorization") != "Bearer "+accessToken {
				t.Fatalf("profile request authorization = %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profile": map[string]string{"id": "profile-001"}})
		case "/v1/admin/brand-clouds/brand-001/device-item-profiles/profile-001/production-runs":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"production_run": map[string]string{"id": "run-001"}, "factory_jwt": productionJWT})
		case enrollPath:
			if r.Header.Get("Authorization") != "Bearer "+productionJWT {
				t.Fatalf("factory authorization = %q", r.Header.Get("Authorization"))
			}
			var in EnrollRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(EnrollResponse{
				RequestID: in.RequestID, IssuerRequestID: "issuer-001", DeviceID: in.DeviceID, SerialNumber: in.SerialNumber,
				CertificatePEM: strings.TrimSpace(string(signer.signCSR(t, in.DeviceID, in.CSRPem))), CertificateChainPEM: strings.TrimSpace(string(signer.caPEM)),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	artifactDir := t.TempDir()
	tokenProbeCalled := false
	result, err := NewQualificationRunner(server.Client(), func(_ context.Context, baseURL, certFile, keyFile, _ string, _ time.Duration) (int, error) {
		tokenProbeCalled = true
		if baseURL != server.URL {
			t.Fatalf("device base URL = %q", baseURL)
		}
		for _, path := range []string{certFile, keyFile} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("mTLS material %s: %v", path, err)
			}
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			t.Fatalf("load generated device TLS identity: %v", err)
		}
		return http.StatusOK, nil
	}).Run(context.Background(), QualificationConfig{
		AccountManagerURL: server.URL, AdminEmail: "admin@example.test", AdminPassword: adminPassword,
		FactoryURL: server.URL, DeviceBaseURL: server.URL, RunID: "qualification-001", ArtifactDir: artifactDir, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tokenProbeCalled || result.TokenHTTPStatus != http.StatusOK || result.Steps["bootstrap_device_token"] != "PASS" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "factory-runner", "device-material")); !os.IsNotExist(err) {
		t.Fatalf("ephemeral device private material was not removed: %v", err)
	}
	if len(paths) != 5 {
		t.Fatalf("paths = %v", paths)
	}
	raw, err := os.ReadFile(filepath.Join(artifactDir, "factory-qualification-results.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{adminPassword, accessToken, productionJWT} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("qualification evidence leaked secret marker %q", secret)
		}
	}
}

func TestQualificationRejectsNonHTTPSEndpoints(t *testing.T) {
	cfg := QualificationConfig{
		AccountManagerURL: "http://account.example.test", FactoryURL: "https://factory.example.test",
		DeviceBaseURL: "https://device.example.test", AdminEmail: "admin@example.test", AdminPassword: "password",
		RunID: "run", ArtifactDir: t.TempDir(),
	}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "must be HTTPS") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestQualificationAllowsOnlyExplicitAccountAndFactoryLoopbackTunnels(t *testing.T) {
	cfg := QualificationConfig{
		AccountManagerURL: "http://127.0.0.1:18081", FactoryURL: "http://127.0.0.1:18443",
		DeviceBaseURL: "https://device.example.test", AdminEmail: "admin@example.test", AdminPassword: "password",
		RunID: "run", ArtifactDir: t.TempDir(), AllowLoopbackTunnel: true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	cfg.DeviceBaseURL = "http://127.0.0.1:18080"
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "device base URL must be HTTPS") {
		t.Fatalf("Normalize() device tunnel error = %v", err)
	}
}

func TestQualificationConfigRejectsIncompleteOrInvalidInputs(t *testing.T) {
	valid := QualificationConfig{
		AccountManagerURL: "https://account.example.test", FactoryURL: "https://factory.example.test",
		DeviceBaseURL: "https://device.example.test", AdminEmail: "admin@example.test", AdminPassword: "password",
		RunID: "run", ArtifactDir: t.TempDir(), Timeout: -time.Second,
	}
	tests := []struct {
		name string
		edit func(*QualificationConfig)
		want string
	}{
		{name: "invalid URL", edit: func(c *QualificationConfig) { c.FactoryURL = "://bad" }, want: "valid URL"},
		{name: "missing admin email", edit: func(c *QualificationConfig) { c.AdminEmail = "" }, want: "email and password"},
		{name: "missing admin password", edit: func(c *QualificationConfig) { c.AdminPassword = "" }, want: "email and password"},
		{name: "missing run ID", edit: func(c *QualificationConfig) { c.RunID = "" }, want: "run ID"},
		{name: "missing artifact directory", edit: func(c *QualificationConfig) { c.ArtifactDir = "" }, want: "artifact directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.edit(&cfg)
			if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Normalize() error = %v, want %q", err, tc.want)
			}
		})
	}
	if err := valid.Normalize(); err != nil {
		t.Fatal(err)
	}
	if valid.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %s", valid.Timeout)
	}
}

func TestQualificationAPIStepsRejectIncompleteResponses(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		body    string
		invoke  func(*QualificationRunner, QualificationConfig) error
		wantErr string
	}{
		{name: "login HTTP failure", path: "/v1/auth/login", status: http.StatusUnauthorized, body: `{"error":"denied"}`, invoke: func(r *QualificationRunner, c QualificationConfig) error {
			_, err := r.login(context.Background(), c)
			return err
		}, wantErr: "platform admin login"},
		{name: "login missing token", path: "/v1/auth/login", status: http.StatusOK, body: `{"tokens":{}}`, invoke: func(r *QualificationRunner, c QualificationConfig) error {
			_, err := r.login(context.Background(), c)
			return err
		}, wantErr: "no access token"},
		{name: "brand missing ID", path: "/v1/admin/brand-clouds", status: http.StatusCreated, body: `{"brand_cloud":{}}`, invoke: func(r *QualificationRunner, c QualificationConfig) error {
			_, err := r.createBrandCloud(context.Background(), c, "token")
			return err
		}, wantErr: "no ID"},
		{name: "profile missing ID", path: "/v1/admin/brand-clouds/brand/device-item-profiles", status: http.StatusCreated, body: `{"device_item_profile":{}}`, invoke: func(r *QualificationRunner, c QualificationConfig) error {
			_, err := r.createProfile(context.Background(), c, "token", "brand")
			return err
		}, wantErr: "no ID"},
		{name: "production missing JWT", path: "/v1/admin/brand-clouds/brand/device-item-profiles/profile/production-runs", status: http.StatusCreated, body: `{"production_run":{"id":"run"}}`, invoke: func(r *QualificationRunner, c QualificationConfig) error {
			_, _, err := r.createProductionRun(context.Background(), c, "token", "brand", "profile")
			return err
		}, wantErr: "incomplete production JWT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != tc.path {
					t.Fatalf("path = %q, want %q", req.URL.Path, tc.path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			cfg := QualificationConfig{AccountManagerURL: server.URL, RunID: "run", AdminEmail: "admin", AdminPassword: "password"}
			err := tc.invoke(NewQualificationRunner(server.Client(), func(context.Context, string, string, string, string, time.Duration) (int, error) { return 0, nil }), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestProbeDeviceTokenResponsesAndMaterialGuardrails(t *testing.T) {
	clientCert, clientKey := deviceTokenTestIdentity(t)
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantErr    string
	}{
		{name: "success", status: http.StatusOK, body: `{"access_token":"redacted"}`, wantStatus: http.StatusOK},
		{name: "HTTP failure", status: http.StatusNotFound, body: `{"reason":"projection missing"}`, wantStatus: http.StatusNotFound, wantErr: "HTTP 404"},
		{name: "invalid JSON", status: http.StatusOK, body: `{`, wantStatus: http.StatusOK, wantErr: "not JSON"},
		{name: "missing access token", status: http.StatusOK, body: `{}`, wantStatus: http.StatusOK, wantErr: "missing access token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/request_token" || req.Method != http.MethodPost {
					t.Fatalf("request = %s %s", req.Method, req.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAnyClientCert}
			server.StartTLS()
			defer server.Close()
			caFile := filepath.Join(t.TempDir(), "server-ca.pem")
			if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
				t.Fatal(err)
			}
			status, err := ProbeDeviceToken(context.Background(), server.URL, clientCert, clientKey, caFile, time.Second)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}

	t.Run("malformed identity diagnostics", func(t *testing.T) {
		dir := t.TempDir()
		certFile := filepath.Join(dir, "device-bundle.crt")
		keyFile := filepath.Join(dir, "device.key")
		_ = os.WriteFile(certFile, []byte("not a certificate"), 0o600)
		_ = os.WriteFile(keyFile, []byte("not a key"), 0o600)
		_, err := ProbeDeviceToken(context.Background(), "https://device.example.test", certFile, keyFile, "", time.Second)
		if err == nil || !strings.Contains(err.Error(), "bundle public keys unavailable") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid CA", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "invalid-ca.pem")
		_ = os.WriteFile(caFile, []byte("invalid"), 0o600)
		_, err := ProbeDeviceToken(context.Background(), "https://device.example.test", clientCert, clientKey, caFile, time.Second)
		if err == nil || !strings.Contains(err.Error(), "contains no certificates") {
			t.Fatalf("error = %v", err)
		}
	})
}

func deviceTokenTestIdentity(t *testing.T) (string, string) {
	t.Helper()
	_, deviceID, csrPEM, keyPEM, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	signer := newTestSigner(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "device-bundle.crt")
	keyFile := filepath.Join(dir, "device.key")
	if err := os.WriteFile(certFile, joinPEM(signer.signCSR(t, deviceID, string(csrPEM)), signer.caPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestReportRoundTripRendersStableEvidenceAndSortedErrors(t *testing.T) {
	started := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	result := &Result{
		Schema:    ResultSchema,
		RunID:     "run-report-001",
		StartedAt: started,
		EndedAt:   started.Add(3 * time.Second),
		Config: ResultConfig{
			FactoryURL: "https://factory.example.test", Count: 2, Concurrency: 2, BatchID: "batch-001",
		},
		Summary: Summary{
			Total: 2, Successes: 1, Failures: 1, SuccessRate: .5,
			P95LatencyMS: 20, P99LatencyMS: 25, DurationMillis: 3000,
		},
		Errors: map[string]int{"z_transport": 1, "a_certificate": 1},
		Devices: []DeviceResult{
			{Index: 1, DeviceID: "pk-good", Success: true, StatusCode: http.StatusOK, LatencyMillis: 12},
			{Index: 2, DeviceID: "pk-bad", Success: false, StatusCode: http.StatusBadRequest, LatencyMillis: 18, ErrorClass: "certificate", Error: "invalid | certificate\nrejected"},
		},
	}
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "result.json")
	reportPath := filepath.Join(dir, "report.md")
	if err := WriteJSON(jsonPath, result); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadJSON(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.RunID != result.RunID || roundTrip.Summary.Failures != 1 {
		t.Fatalf("round trip = %#v", roundTrip)
	}
	if err := WriteMarkdown(reportPath, roundTrip); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(report)
	for _, want := range []string{"Overall result: `FAIL`", "issuer request recorded; certificate CN/public key/clientAuth/chain validated", "invalid \\| certificate rejected", "`a_certificate`", "`z_transport`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "`a_certificate`") > strings.Index(text, "`z_transport`") {
		t.Fatal("error classes are not sorted")
	}
}

type testSigner struct {
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
	caPEM []byte
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{ca: cert, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (s *testSigner) signCSR(t *testing.T, deviceID, rawCSR string) []byte {
	t.Helper()
	block, _ := pem.Decode([]byte(rawCSR))
	if block == nil {
		t.Fatal("missing csr")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != deviceID {
		t.Fatalf("csr cn = %q, want %q", csr.Subject.CommonName, deviceID)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: csr.Subject, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, s.ca, csr.PublicKey, s.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

package accountvideosmoke

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

func TestPlanBlocksWhenRequiredInputsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		RunID:                 "test-run",
		ArtifactDir:           filepath.Join(dir, "artifacts"),
		AccountManagerBaseURL: "https://account-manager.example.test",
		VideoCloudBaseURL:     "https://video.example.test",
	}

	result := Plan(cfg)

	if result.Overall != StatusBlocked {
		t.Fatalf("expected blocked result, got %s", result.Overall)
	}
	if !result.HasStep("load_account_fixture", StatusBlocked) {
		t.Fatalf("expected missing account fixture to be blocked: %+v", result.Steps)
	}
	if !result.HasStep("load_device_certset", StatusBlocked) {
		t.Fatalf("expected missing device certset to be blocked: %+v", result.Steps)
	}
}

func TestRedactSensitiveMaterial(t *testing.T) {
	input := strings.Join([]string{
		"Authorization: Bearer abc.def.ghi",
		"claim_token=claim_secret_value",
		"PASSWORD=fake",
		"-----BEGIN PRIVATE KEY-----",
		"raw line",
		"-----END PRIVATE KEY-----",
		"-----BEGIN CERTIFICATE-----",
		"MIIBsecretcert",
		"-----END CERTIFICATE-----",
	}, "\n")

	got := Redact(input)
	for _, secret := range []string{"abc.def.ghi", "claim_secret_value", "fake", "MIIBsecretcert"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker in %q", got)
	}
}

func TestLoadDeviceCertsetSelectsFirstDeviceWithoutLeakingKey(t *testing.T) {
	dir := t.TempDir()
	deviceDir := filepath.Join(dir, "device-material", "device-001")
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device.key"), []byte("PRIVATE KEY SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device-chain.crt"), []byte("CERT CHAIN SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "factory-enroll-results.json"), []byte(`{
		"devices": [
			{"index": 1, "devid": "pk-test-device", "success": true}
		]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	certset, err := LoadDeviceCertset(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if certset.DeviceID != "pk-test-device" {
		t.Fatalf("unexpected device id %q", certset.DeviceID)
	}
	if strings.Contains(certset.Summary(), "PRIVATE KEY SECRET") || strings.Contains(certset.Summary(), "CERT CHAIN SECRET") {
		t.Fatalf("certset summary leaked key/cert material: %s", certset.Summary())
	}
}

func TestRunCompletesAccountProvisioningAndDeviceMTLSSmoke(t *testing.T) {
	fixtureDir, certsetDir, clientCert := writeSmokeFixtures(t)
	deviceServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			t.Fatal("device request did not present its mTLS certificate")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "secret-device-token"})
	}))
	deviceServer.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	deviceServer.StartTLS()
	defer deviceServer.Close()
	if err := os.WriteFile(filepath.Join(certsetDir, "device-ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: deviceServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	var paths []string
	accountServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "secret-login-token"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/org-001/devices/claim/resolve":
			if r.Header.Get("Authorization") != "Bearer secret-login-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"claim_id": "claim-001",
				"device":   map[string]string{"id": "account-device-001"},
				"provision_input": map[string]string{
					"video_cloud_devid": "pk-device-001",
					"activity_id":       "activity-001",
					"clip_public_key":   "clip-key",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/org-001/devices/account-device-001/provision":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation_id":"operation-001"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/orgs/org-001/devices/account-device-001/provisioning":
			_ = json.NewEncoder(w).Encode(map[string]any{"readiness": map[string]string{"state": "ready", "product_state": "activated"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer accountServer.Close()

	result, err := Run(context.Background(), Config{
		RunID:                    "run-smoke-001",
		AccountUsersDir:          fixtureDir,
		DeviceCertsetDir:         certsetDir,
		AccountManagerBaseURL:    accountServer.URL,
		VideoCloudBaseURL:        "https://video-control.example.test",
		VideoCloudDeviceBaseURL:  deviceServer.URL,
		ClaimToken:               "claim-secret",
		Timeout:                  2 * time.Second,
		ProvisioningPollInterval: time.Millisecond,
		ProvisioningPollAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Overall != StatusPass || !result.HasStep("device_mtls_token_smoke", StatusPass) {
		t.Fatalf("result = %#v", result)
	}
	if result.Config.ClaimToken != "" {
		t.Fatal("sanitized result retained the claim token")
	}
	if len(paths) != 4 {
		t.Fatalf("account requests = %v", paths)
	}
	if !strings.Contains(clientCert.Subject.CommonName, "pk-device-001") {
		t.Fatalf("unexpected client certificate CN %q", clientCert.Subject.CommonName)
	}

	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	if err := WriteArtifacts(result, artifactDir); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(artifactDir, "account-video-smoke-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "claim-secret") || !strings.Contains(string(report), "Overall: `PASS`") {
		t.Fatalf("unsafe or incomplete report:\n%s", report)
	}
}

func TestRunReportsHTTPFailureWithoutLeakingResponseSecret(t *testing.T) {
	fixtureDir, certsetDir, _ := writeSmokeFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"password":"do-not-leak"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	result, err := Run(context.Background(), Config{
		AccountUsersDir:       fixtureDir,
		DeviceCertsetDir:      certsetDir,
		AccountManagerBaseURL: server.URL,
		VideoCloudBaseURL:     "https://video.example.test",
		ClaimToken:            "claim-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Overall != StatusFail || !result.HasStep("account_login", StatusFail) {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(RenderMarkdown(result), "do-not-leak") {
		t.Fatal("failure report leaked response credentials")
	}
}

func writeSmokeFixtures(t *testing.T) (string, string, *x509.Certificate) {
	t.Helper()
	accountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(accountDir, "credentials.json"), []byte(`{"email":"smoke@example.test","password":"fixture-password","organization_id":"org-001"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	certsetDir := t.TempDir()
	deviceDir := filepath.Join(certsetDir, "device-material", "device-001")
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certsetDir, "factory-enroll-results.json"), []byte(`{"devices":[{"index":1,"devid":"pk-device-001","success":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, cert := newSmokeClientIdentity(t)
	if err := os.WriteFile(filepath.Join(deviceDir, "device.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device-chain.crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return accountDir, certsetDir, cert
}

func newSmokeClientIdentity(t *testing.T) ([]byte, []byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pk-device-001"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), cert
}

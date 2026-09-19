package accountvideosmoke

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestPlanBlocksProductModeWithImportedClaimToken(t *testing.T) {
	result := Plan(Config{AccountManagerBaseURL: "https://account.example.test", VideoCloudBaseURL: "https://video.example.test",
		ProductID: "product-1", ClaimToken: "secret-claim"})
	if result.Overall != StatusBlocked || !result.HasStep("validate_config", StatusBlocked) || strings.Contains(RenderMarkdown(result), "secret-claim") {
		t.Fatalf("unsafe Product-mode plan: %+v", result)
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
	provisionPolls := 0
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
			provisionPolls++
			if provisionPolls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"readiness": map[string]string{"state": "provisioning", "product_state": "cloud_activation_pending"}})
				return
			}
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
	if len(paths) != 5 {
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

func TestRegisteredProductSmokeVerifiesDeviceTokenGrant(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tokenOptions  []string
		tokenRevision int64
		readiness     string
		badSignature  bool
		unavailable   bool
		wantOverall   Status
		wantStep      string
	}{
		{"matching grant", []string{"test_capability", "mqtt"}, 3, "activated", false, false, StatusPass, "device_mtls_token_smoke"},
		{"expanded token", []string{"mqtt", "test_capability", "ungranted"}, 3, "activated", false, false, StatusFail, "device_mtls_token_smoke"},
		{"stale revision", []string{"mqtt", "test_capability"}, 2, "activated", false, false, StatusFail, "device_mtls_token_smoke"},
		{"invalid signature", []string{"mqtt", "test_capability"}, 3, "activated", true, false, StatusFail, "device_mtls_token_smoke"},
		{"activation pending", []string{"mqtt", "test_capability"}, 3, "cloud_activation_pending", false, false, StatusFail, "read_account_provisioning"},
		{"plugin unavailable", []string{"mqtt", "test_capability"}, 3, "activated", false, true, StatusBlocked, "load_registered_product_grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ACCOUNT_MANAGER_PLATFORM_ADMIN_TOKEN", "fixture-platform-admin")
			fixtureDir, certsetDir, _ := writeSmokeFixtures(t)
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
			if err != nil {
				t.Fatal(err)
			}
			var deviceCalls atomic.Int32
			deviceServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deviceCalls.Add(1)
				if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
					http.Error(w, "device mTLS required", http.StatusUnauthorized)
					return
				}
				if r.URL.Path == "/get/token.pubkey" {
					_, _ = w.Write(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
					return
				}
				if r.URL.Path != "/request_token" {
					http.NotFound(w, r)
					return
				}
				payload, _ := json.Marshal(map[string]any{
					"scope": "device", "subject_id": "pk-device-001", "brand_cloud_id": "org-001",
					"service_options": tc.tokenOptions, "product_service_revision": tc.tokenRevision,
					"entitlement_revision": 4, "exp": float64(time.Now().Add(time.Hour).Unix()) + 0.5,
				})
				signingInput := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
				signature := ed25519.Sign(privateKey, []byte(signingInput))
				if tc.badSignature {
					signature[0] ^= 0xff
				}
				jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
				_ = json.NewEncoder(w).Encode(map[string]string{"access_token": jwt})
			}))
			deviceServer.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
			deviceServer.StartTLS()
			defer deviceServer.Close()
			if err := os.WriteFile(filepath.Join(certsetDir, "device-ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: deviceServer.Certificate().Raw}), 0o600); err != nil {
				t.Fatal(err)
			}

			var invalidClaimRequest atomic.Bool
			accountServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
					_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "fixture-user-token"}})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/orgs/org-001/device-item-profiles/product-001":
					_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profile": map[string]any{
						"id": "product-001", "brand_cloud_id": "org-001", "service_options": []string{"mqtt", "test_capability"},
					}})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/platform/service-options" && r.URL.Query().Get("brand_cloud_id") == "org-001":
					_ = json.NewEncoder(w).Encode(map[string]any{"catalog_revision": 6, "product_writes_enabled": true, "options": []map[string]any{
						{"code": "mqtt", "selectable": true}, {"code": "test_capability", "selectable": !tc.unavailable},
					}})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/device-claim-tokens":
					var request struct {
						ProductID      string   `json:"device_item_profile_id"`
						ServiceOptions []string `json:"service_options"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ProductID != "product-001" ||
						!sameServiceOptionSet(request.ServiceOptions, []string{"mqtt", "test_capability"}) || r.Header.Get("Authorization") != "Bearer fixture-platform-admin" {
						invalidClaimRequest.Store(true)
						http.Error(w, "invalid claim request", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"claim_token": "fixture-claim-token", "device_claim_token": map[string]any{
						"service_options": []string{"mqtt", "test_capability"},
						"metadata":        map[string]any{"product_service_revision": 3, "service_grant_sha256": strings.Repeat("a", 64)},
					}})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/org-001/devices/claim/resolve":
					_ = json.NewEncoder(w).Encode(map[string]any{"device": map[string]string{"id": "account-device-001", "device_item_profile_id": "product-001"},
						"provision_input": map[string]string{"video_cloud_devid": "pk-device-001", "activity_id": "activity-001", "clip_public_key": "clip-key"}})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/org-001/devices/account-device-001/provision":
					w.WriteHeader(http.StatusAccepted)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/orgs/org-001/devices/account-device-001/provisioning":
					state := "ready"
					if tc.readiness != "activated" {
						state = "provisioning"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"readiness": map[string]string{"state": state, "product_state": tc.readiness}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer accountServer.Close()

			result, err := Run(context.Background(), Config{
				RunID: "registered-product-smoke", AccountUsersDir: fixtureDir, DeviceCertsetDir: certsetDir,
				AccountManagerBaseURL: accountServer.URL, VideoCloudBaseURL: "https://video.example.test",
				VideoCloudDeviceBaseURL: deviceServer.URL, ProductID: "product-001",
				Timeout: 2 * time.Second, ProvisioningPollAttempts: 2, ProvisioningPollInterval: time.Millisecond,
			})
			if err != nil || result.Overall != tc.wantOverall || !result.HasStep(tc.wantStep, tc.wantOverall) || invalidClaimRequest.Load() {
				t.Fatalf("registered smoke result=%+v, error=%v, bad claim=%v", result, err, invalidClaimRequest.Load())
			}
			if (tc.readiness != "activated" || tc.unavailable) && deviceCalls.Load() != 0 {
				t.Fatalf("unavailable Product or pending activation requested %d device tokens", deviceCalls.Load())
			}
		})
	}
}

func TestRegisteredProductGrantRejectsUnavailablePlatformFacts(t *testing.T) {
	for _, tc := range []struct {
		name, productID              string
		productStatus, catalogStatus int
		options                      []string
		writes                       bool
		revision                     int64
	}{
		{"Product request failed", "product-1", http.StatusServiceUnavailable, http.StatusOK, []string{"mqtt"}, true, 2},
		{"Product lacks MQTT", "product-1", http.StatusOK, http.StatusOK, []string{"iot_shadow"}, true, 2},
		{"catalog request failed", "product-1", http.StatusOK, http.StatusServiceUnavailable, []string{"mqtt"}, true, 2},
		{"Product writes disabled", "product-1", http.StatusOK, http.StatusOK, []string{"mqtt"}, false, 2},
		{"catalog revision missing", "product-1", http.StatusOK, http.StatusOK, []string{"mqtt"}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/orgs/org-1/device-item-profiles/product-1" {
					w.WriteHeader(tc.productStatus)
					_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profile": map[string]any{
						"id": tc.productID, "brand_cloud_id": "org-1", "service_options": tc.options,
					}})
					return
				}
				w.WriteHeader(tc.catalogStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"catalog_revision": tc.revision, "product_writes_enabled": tc.writes,
					"options": []map[string]any{{"code": "mqtt", "selectable": true}}})
			}))
			defer server.Close()
			_, _, _, err := loadRegisteredProductGrant(context.Background(), apiClient{baseURL: server.URL, http: server.Client()}, "bearer", "org-1", "product-1")
			if err == nil {
				t.Fatal("unavailable Product or catalog facts were accepted")
			}
		})
	}
}

func TestClaimTokenRejectsMissingOrWidenedProductGrant(t *testing.T) {
	grant := &registeredProductGrant{ProductID: "product-1", ServiceOptions: []string{"mqtt"}, CatalogRevision: 2}
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		grant     *registeredProductGrant
		wantError bool
	}{
		{"request failed", http.StatusServiceUnavailable, `{"error":"unavailable"}`, grant, true},
		{"raw token missing", http.StatusOK, `{"claim_token":""}`, grant, true},
		{"Product grant missing", http.StatusOK, `{"claim_token":"raw","device_claim_token":{"service_options":["mqtt"],"metadata":{"product_service_revision":0}}}`, grant, true},
		{"legacy claim", http.StatusOK, `{"claim_token":"legacy"}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			token, _, _, _, err := createClaimToken(context.Background(), apiClient{baseURL: server.URL, http: server.Client()}, "bearer", "org-1", "device-1", tc.grant)
			if (err != nil) != tc.wantError || (!tc.wantError && token != "legacy") {
				t.Fatalf("claim token = %q, error = %v", token, err)
			}
		})
	}
}

func TestDeviceTokenPublicKeyAndJWTRejectMalformedTrustData(t *testing.T) {
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyDER, err := x509.MarshalPKIXPublicKey(&ecdsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP error", http.StatusServiceUnavailable, ""},
		{"not PEM", http.StatusOK, "not PEM"},
		{"invalid DER", http.StatusOK, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("invalid")}))},
		{"wrong key type", http.StatusOK, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: wrongKeyDER}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if _, err := fetchDeviceTokenPublicKey(context.Background(), server.Client(), server.URL); err == nil {
				t.Fatal("untrusted device-token public key was accepted")
			}
		})
	}
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	if _, err := fetchDeviceTokenPublicKey(context.Background(), closed.Client(), closed.URL); err == nil {
		t.Fatal("unreachable public-key host was accepted")
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(header, payload string) string {
		input := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(input)))
	}
	validHeader := `{"alg":"EdDSA","typ":"JWT"}`
	invalidPayload := base64.RawURLEncoding.EncodeToString([]byte(validHeader)) + ".!"
	invalidPayload += "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(invalidPayload)))
	for _, raw := range []string{
		"not-a-jwt", "!.e30.c2ln", sign(`{"alg":"HS256","typ":"JWT"}`, `{}`), invalidPayload, sign(validHeader, "not JSON"),
	} {
		if _, err := inspectDeviceTokenClaims(raw, publicKey); err == nil {
			t.Fatalf("malformed device JWT was accepted: %q", raw)
		}
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

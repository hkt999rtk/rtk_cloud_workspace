package main

import (
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testMQTTToken(scope string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"scope":          scope,
		"brand_cloud_id": "test-brand-cloud",
		"mqtt_client_id": "mqtt-" + scope + "-test",
		"exp":            time.Now().Add(time.Hour).Unix(),
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".test"
}

func TestVideoStatePathUsesConfiguredStackName(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "env"))
	mkdir(t, filepath.Join(root, "state"))
	stackEnv := filepath.Join(root, "env", "stack.env")
	write(t, stackEnv, "CLOUD_STACK_NAME=video-cloud-stg-0529\n")
	want := filepath.Join(root, "state", "video-cloud-stg-0529.state.json")
	write(t, want, `{"stack":"video-cloud-stg-0529"}`)
	write(t, filepath.Join(root, "state", "video-cloud-staging.state.json"), `{"stack":"legacy"}`)

	if got := videoStatePath(root, stackEnv); got != want {
		t.Fatalf("videoStatePath = %q, want %q", got, want)
	}
}

func TestShadowDocumentsDeltaClearedUsesDesiredAndReportedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		doc  map[string]any
		want bool
	}{
		{
			name: "matching snapshots with extra reported state",
			doc: map[string]any{"current": map[string]any{"state": map[string]any{
				"desired":  map[string]any{"power": true},
				"reported": map[string]any{"power": true, "online": true},
			}}},
			want: true,
		},
		{
			name: "missing desired value",
			doc: map[string]any{"current": map[string]any{"state": map[string]any{
				"desired":  map[string]any{"power": true},
				"reported": map[string]any{"online": true},
			}}},
			want: false,
		},
		{
			name: "nested mismatch",
			doc: map[string]any{"current": map[string]any{"state": map[string]any{
				"desired":  map[string]any{"settings": map[string]any{"mode": "cool"}},
				"reported": map[string]any{"settings": map[string]any{"mode": "heat"}},
			}}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shadowDocumentsDeltaCleared(tc.doc); got != tc.want {
				t.Fatalf("shadowDocumentsDeltaCleared() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestLatestHomeMQTTBindArtifactSkipsIncompleteLatestArtifact(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "rtk-device-bind-older.json")
	newer := filepath.Join(root, "rtk-device-bind-newer.json")
	write(t, older, completeHomeDiverseMQTTBindJSON())
	write(t, newer, `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "camera", "service_options": ["mqtt", "video_streaming"]}
  ]
}`)
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	got := latestHomeMQTTBindArtifact(filepath.Join(root, "rtk-device-bind-*.json"), "rtk")
	if got != older {
		t.Fatalf("latestHomeMQTTBindArtifact = %q, want %q", got, older)
	}
}

func TestLatestHomeMQTTBindArtifactPrefersFilenameTimestampOverMTime(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "rtk-device-bind-20260615T010000Z.json")
	newer := filepath.Join(root, "rtk-device-bind-20260615T020000Z.json")
	complete := completeHomeDiverseMQTTBindJSON()
	write(t, older, complete)
	write(t, newer, complete)
	oldTime := time.Now()
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	got := latestHomeMQTTBindArtifact(filepath.Join(root, "rtk-device-bind-*.json"), "rtk")
	if got != newer {
		t.Fatalf("latestHomeMQTTBindArtifact = %q, want %q", got, newer)
	}
}

func completeHomeDiverseMQTTBindJSON() string {
	return `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "light", "service_options": ["mqtt"]},
    {"device_type": "switch", "service_options": ["mqtt"]},
    {"device_type": "smart_plug", "service_options": ["mqtt"]},
    {"device_type": "air_conditioner", "service_options": ["mqtt"]},
    {"device_type": "environment_sensor", "service_options": ["mqtt"]},
    {"device_type": "security_sensor", "service_options": ["mqtt"]},
    {"device_type": "smart_meter", "service_options": ["mqtt"]},
    {"device_type": "camera_status", "service_options": ["mqtt"]},
    {"device_type": "door_lock", "service_options": ["mqtt"]},
    {"device_type": "appliance", "service_options": ["mqtt"]},
    {"device_type": "gateway", "service_options": ["mqtt"]}
  ]
}`
}

func TestVideoCloudMTLSBaseURLUsesDeviceClientDomainFromTopology(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "topology"))
	write(t, filepath.Join(root, "topology", "video-cloud-staging.yaml"), `stack: video-cloud-stg-0529
deploy:
  verify_gateway_url: https://video-cloud-stg-0529.realtekconnect.com
  device_client_domain: "device.video-cloud-stg-0529.realtekconnect.com"
`)

	got := videoCloudMTLSBaseURL(root, map[string]string{"VIDEO_CLOUD_DOMAIN": "video-cloud-stg-0529.realtekconnect.com"}, "https://video-cloud-stg-0529.realtekconnect.com")
	want := "https://device.video-cloud-stg-0529.realtekconnect.com"
	if got != want {
		t.Fatalf("videoCloudMTLSBaseURL = %q, want %q", got, want)
	}
}

func TestVideoCloudMTLSBaseURLUsesConfiguredDeviceDomain(t *testing.T) {
	got := videoCloudMTLSBaseURL(t.TempDir(), map[string]string{
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video-cloud-staging.realtekconnect.com",
	}, "https://video-cloud-staging.realtekconnect.com")
	if got != "https://device.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("videoCloudMTLSBaseURL = %q, want configured device domain", got)
	}
}

func TestVideoCloudMTLSBaseURLDefaultsToDeviceSubdomain(t *testing.T) {
	got := videoCloudMTLSBaseURL(t.TempDir(), map[string]string{
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}, "https://video-cloud-staging.realtekconnect.com")
	if got != "https://device.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("videoCloudMTLSBaseURL = %q, want default device subdomain", got)
	}
}

func TestResolveVideoCloudEndpointsKeepsTokenBootstrapOnDeviceMTLSURL(t *testing.T) {
	t.Setenv("VIDEO_CLOUD_BASE_URL", "https://video-cloud-staging.realtekconnect.com")

	endpoints := resolveVideoCloudEndpoints(t.TempDir(), map[string]string{
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	})

	if endpoints.PublicBaseURL != "https://video-cloud-staging.realtekconnect.com" {
		t.Fatalf("PublicBaseURL = %q, want public API URL", endpoints.PublicBaseURL)
	}
	if endpoints.TokenBootstrapBaseURL != "https://device.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("TokenBootstrapBaseURL = %q, want device mTLS URL", endpoints.TokenBootstrapBaseURL)
	}
}

func TestMQTTEndpointUsesLKEPortForwardOverrides(t *testing.T) {
	t.Setenv("RTK_CLOUD_MQTT_TEST_MQTT_HOST", "127.0.0.1")
	t.Setenv("RTK_CLOUD_MQTT_TEST_MQTT_PORT", "39123")

	host, port := mqttEndpoint("/missing/state.json", map[string]string{"MQTT_HOST": "mqtt.example.test", "MQTT_TLS_PORT": "8883"})

	if host != "127.0.0.1" || port != 39123 {
		t.Fatalf("mqttEndpoint = %s:%d, want 127.0.0.1:39123", host, port)
	}
}

func TestRequestDeviceTokenSendsTrustedCertHeadersForHTTPPortForward(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Verify"); got != "SUCCESS" {
			t.Fatalf("X-Client-Verify = %q, want SUCCESS", got)
		}
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=device-1/O=VideoCloud" {
			t.Fatalf("X-Client-S-DN = %q, want device subject", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["access_token_only"]; ok {
			t.Fatalf("access_token_only = %#v, want omitted for refreshable token pair", body["access_token_only"])
		}
		writeJSON(t, w, map[string]string{"access_token": "device-token", "refresh_token": "device-refresh"})
	}))
	defer server.Close()

	token, err := requestDeviceToken(server.URL, cert, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if token != "device-token" {
		t.Fatalf("token = %q, want device-token", token)
	}
}

func TestRequestAppTokenSendsTrustedCertHeadersForHTTPPortForward(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Verify"); got != "SUCCESS" {
			t.Fatalf("X-Client-Verify = %q, want SUCCESS", got)
		}
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=app-user:user-1/O=VideoCloud" {
			t.Fatalf("X-Client-S-DN = %q, want app subject", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["access_token_only"]; ok {
			t.Fatalf("access_token_only = %#v, want omitted for refreshable token pair", body["access_token_only"])
		}
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token", "refresh_token": "app-refresh"})
	}))
	defer server.Close()

	token, err := requestAppToken(server.URL, cert, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "app-token" || token.Scope != "app" {
		t.Fatalf("token = %#v, want app-token", token)
	}
	if token.RefreshToken != "app-refresh" {
		t.Fatalf("refresh token = %q, want app-refresh", token.RefreshToken)
	}
}

func TestManagedTokenRefreshesAtHalfLifetimeBeforeUse(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Unix(1_800_000_000, 0)
	now := issuedAt
	requests := 0
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/request_token":
			requests++
			if got := r.Header.Get("X-Client-Verify"); got != "SUCCESS" {
				t.Fatalf("X-Client-Verify = %q, want SUCCESS", got)
			}
			writeJSON(t, w, map[string]string{
				"scope":         "app",
				"access_token":  testJWT(t, issuedAt, issuedAt.Add(100*time.Second)),
				"refresh_token": "refresh-1",
			})
		case "/refresh_token":
			refreshes++
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["scope"] != "app" || body["devid"] != "device-1" || body["refresh_token"] != "refresh-1" {
				t.Fatalf("refresh body = %#v", body)
			}
			writeJSON(t, w, map[string]string{
				"scope":         "app",
				"access_token":  testJWT(t, now, now.Add(100*time.Second)),
				"refresh_token": "refresh-2",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	manager := newAppTokenManager(server.URL, cert, "device-1")
	manager.now = func() time.Time { return now }
	first, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = issuedAt.Add(60 * time.Second)
	second, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("manager returned cached token after half lifetime instead of refreshed token")
	}
	if requests != 1 || refreshes != 1 {
		t.Fatalf("requests=%d refreshes=%d, want 1/1", requests, refreshes)
	}
	if manager.bundle.RefreshToken != "refresh-2" {
		t.Fatalf("refresh token = %q, want refresh-2", manager.bundle.RefreshToken)
	}
}

func TestManagedTokenRequestsWithClientCertificateWhenExpired(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Unix(1_800_000_000, 0)
	now := issuedAt.Add(2 * time.Minute)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh_token" {
			t.Fatal("expired access token should use client certificate request_token, not refresh_token")
		}
		if r.URL.Path != "/request_token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		requests++
		if got := r.Header.Get("X-Client-Verify"); got != "SUCCESS" {
			t.Fatalf("X-Client-Verify = %q, want SUCCESS", got)
		}
		writeJSON(t, w, map[string]string{
			"scope":         "device",
			"access_token":  testJWT(t, now, now.Add(100*time.Second)),
			"refresh_token": "refresh-new",
		})
	}))
	defer server.Close()

	manager := newDeviceTokenManager(server.URL, cert, "device-1")
	manager.now = func() time.Time { return now }
	manager.bundle = tokenBundle{
		Scope:        "device",
		AccessToken:  testJWT(t, issuedAt, issuedAt.Add(10*time.Second)),
		RefreshToken: "refresh-expired",
		issuedAt:     issuedAt,
	}
	token, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || requests != 1 {
		t.Fatalf("token=%q requests=%d, want new token and one request_token call", token, requests)
	}
}

func TestDeviceManagedTokenRenewsWithClientCertificateAtHalfLifetime(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Unix(1_800_000_000, 0)
	now := issuedAt
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh_token" {
			t.Fatal("device token renewal should use client certificate request_token, not refresh_token")
		}
		if r.URL.Path != "/request_token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		requests++
		writeJSON(t, w, map[string]string{
			"scope":        "device",
			"access_token": testJWT(t, now, now.Add(100*time.Second)),
		})
	}))
	defer server.Close()

	manager := newDeviceTokenManager(server.URL, cert, "device-1")
	manager.now = func() time.Time { return now }
	first, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = issuedAt.Add(60 * time.Second)
	second, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("manager returned cached token after half lifetime instead of requesting a new device token")
	}
	if requests != 2 {
		t.Fatalf("request_token calls = %d, want 2", requests)
	}
}

func TestDeviceRequestTokenRetryRecoversTransientTimeout(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if atomic.AddInt32(&requests, 1) == 1 {
			time.Sleep(80 * time.Millisecond)
			return
		}
		writeJSON(t, w, map[string]string{
			"scope":        "device",
			"access_token": testJWT(t, time.Now(), time.Now().Add(time.Minute)),
		})
	}))
	defer server.Close()

	var totals mqttIOTotals
	token, err := requestDeviceTokenWithRetry(server.URL, cert, "device-1", time.Now().Add(time.Second), tokenRequestOptions{Timeout: 20 * time.Millisecond, Retries: 1}, func(update func(*mqttIOTotals)) {
		update(&totals)
	})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || requests != 2 {
		t.Fatalf("token=%q requests=%d, want token and two attempts", token, requests)
	}
	if totals.DeviceTokenAttempts != 2 || totals.DeviceTokenSuccesses != 1 || totals.DeviceTokenFailures != 0 ||
		totals.DeviceTokenFirstSuccess != 0 || totals.DeviceTokenFirstFailures != 1 ||
		totals.DeviceTokenRetryAttempts != 1 || totals.DeviceTokenRetrySuccesses != 1 || totals.DeviceTokenRetryExhausted != 0 {
		t.Fatalf("token totals = %#v, want retry success evidence", totals)
	}
}

func TestDeviceRequestTokenRetryExhaustionRecordsEvidence(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, "try later", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var totals mqttIOTotals
	_, err = requestDeviceTokenWithRetry(server.URL, cert, "device-1", time.Now().Add(time.Second), tokenRequestOptions{Timeout: 20 * time.Millisecond, Retries: 1}, func(update func(*mqttIOTotals)) {
		update(&totals)
	})
	if err == nil {
		t.Fatal("expected request_token failure")
	}
	if totals.DeviceTokenAttempts != 2 || totals.DeviceTokenSuccesses != 0 || totals.DeviceTokenFailures != 1 ||
		totals.DeviceTokenFirstSuccess != 0 || totals.DeviceTokenFirstFailures != 1 ||
		totals.DeviceTokenRetryAttempts != 1 || totals.DeviceTokenRetrySuccesses != 0 || totals.DeviceTokenRetryExhausted != 1 {
		t.Fatalf("token totals = %#v, want retry exhaustion evidence", totals)
	}
}

func TestRedactedErrorPreservesRequestTokenHTTPStatus(t *testing.T) {
	got := redactedErrorString("request_token failed with HTTP 400")
	if got != "request_token failed with HTTP 400" {
		t.Fatalf("redactedErrorString = %q", got)
	}
	if got := redactedErrorString("request_token returned secret material"); got != "redacted sensitive error" {
		t.Fatalf("redactedErrorString secret = %q", got)
	}
}

func TestRedactedErrorPreservesMissingAccessTokenDiagnostics(t *testing.T) {
	for _, raw := range []string{
		"request_token response missing access_token",
		"app request_token response missing access_token",
	} {
		if got := redactedErrorString(raw); got != raw {
			t.Fatalf("redactedErrorString(%q) = %q", raw, got)
		}
	}
}

func TestRedactedErrorHandlesNilError(t *testing.T) {
	if got := redactedError(nil); got != "" {
		t.Fatalf("redactedError(nil) = %q, want empty string", got)
	}
}

func TestUserArtifactPreservesAppCredentials(t *testing.T) {
	var artifact userArtifact
	if err := json.Unmarshal([]byte(`{
  "brandname": "RTK",
  "tenant_slug": "rtk-1234",
  "users": [{
    "email": "rtk+001@users.local",
	    "password": "secret",
	    "app_credentials": {
	      "private_key_pem": "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----",
	      "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----"
	    }
  }]
}`), &artifact); err != nil {
		t.Fatal(err)
	}
	got := artifact.Users[0].AppCredentials.PrivateKeyPEM
	if got == "" || !hasLocalAppCredentials(artifact.Users[0].AppCredentials) {
		t.Fatalf("app credentials were not preserved: %#v", artifact.Users[0].AppCredentials)
	}
	if artifact.TenantSlug != "rtk-1234" {
		t.Fatalf("tenant_slug = %q", artifact.TenantSlug)
	}
}

func TestRunAppCertificateBootstrapUsesArtifactKeyForIssuedCertificate(t *testing.T) {
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/brand-clouds/rtk-1234/auth/login" {
			t.Fatalf("login path = %q, want brand-cloud login route", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode login: %v", err)
		}
		if body["app_csr_pem"] != "" {
			t.Fatal("issued app certificate path must not submit a new CSR")
		}
		writeJSON(t, w, map[string]any{
			"user":   map[string]string{"id": "user-1"},
			"tokens": map[string]string{"access_token": "account-access", "refresh_token": "account-refresh"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       certPEM,
				"certificate_chain_pem": certPEM,
			},
		})
	}))
	defer account.Close()

	status := runAppCertificateBootstrap(account.URL, "https://video.example.invalid", "rtk-1234", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
		AppCredentials: appCertificateKeys{
			PrivateKeyPEM: keyPEM,
			CSRPem:        csrPEM,
		},
		AppCertificate: appCertificateSummary{
			CertificatePEM:      certPEM,
			CertificateChainPEM: certPEM,
		},
	}, "rtk-0041")

	if status.Status != "PASS" || status.TokenScope != "account_manager_login" || status.AccessToken != "account-access" {
		t.Fatalf("status = %#v, want PASS account_manager_login", status)
	}
}

func TestSustainedEventsTelemetryCanBeDisabled(t *testing.T) {
	events := sustainedEvents([]sustainedDeviceSession{
		{Record: certRecord{DeviceID: "device-1", DeviceType: "light"}},
	}, loadOptions{
		TelemetryInterval:          "off",
		CommandRatePerDevicePerDay: "0",
	}, 1234, time.Minute)
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no telemetry when interval is off", events)
	}
}

func TestSustainedEventsUseExecutableCommandWindow(t *testing.T) {
	sessions := make([]sustainedDeviceSession, 450)
	for idx := range sessions {
		sessions[idx] = sustainedDeviceSession{Record: certRecord{DeviceID: fmt.Sprintf("device-%04d", idx), DeviceType: "light"}}
	}
	events := sustainedEventsWithCommandWindow(sessions, loadOptions{
		TelemetryInterval:          "off",
		CommandRatePerDevicePerDay: "86400000",
	}, 1234, 75*time.Second, 60*time.Second)
	if len(events) == 0 {
		t.Fatal("expected command events")
	}
	for _, event := range events {
		if event.Kind != "command" {
			continue
		}
		if event.Offset >= 60*time.Second {
			t.Fatalf("command event offset %s falls outside executable command window", event.Offset)
		}
	}
}

func TestSustainedCommandRuntimeLogStreamsAreUniquePerCommand(t *testing.T) {
	first := newRuntimeLogRecorderForCommand("rtk-0041", "run-1", "cmd-1", time.Now)
	second := newRuntimeLogRecorderForCommand("rtk-0041", "run-1", "cmd-2", time.Now)
	if first.streamID == second.streamID {
		t.Fatalf("stream IDs should differ per command: %s", first.streamID)
	}
	if !strings.HasPrefix(first.streamID, "mqtt-e2e-run-1-") || !strings.HasPrefix(second.streamID, "mqtt-e2e-run-1-") {
		t.Fatalf("stream IDs must keep run prefix for server evidence SQL: %q %q", first.streamID, second.streamID)
	}
}

func TestPrepareAppCertificateBootstrapForAssignmentsUsesAccountLoginToken(t *testing.T) {
	t.Setenv("HOME100K_REFRESH_APP_CERT", "1")
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	loginCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if csr, _ := request["app_csr_pem"].(string); strings.TrimSpace(csr) != "" {
			t.Fatal("issued certificate login unexpectedly sent a replacement CSR")
		}
		writeJSON(t, w, map[string]any{
			"user":   map[string]string{"id": "user-1"},
			"tokens": map[string]string{"access_token": "account-access", "refresh_token": "account-refresh"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       certPEM,
				"certificate_chain_pem": certPEM,
			},
		})
	}))
	defer account.Close()

	user := userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
		AppCredentials: appCertificateKeys{
			PrivateKeyPEM: keyPEM,
			CSRPem:        csrPEM,
		},
		AppCertificate: appCertificateSummary{
			CertificatePEM:      certPEM,
			CertificateChainPEM: certPEM,
		},
	}
	material := prepareAppCertificateBootstrapForAssignments(account.URL, "https://video.example.invalid", "rtk-1234", map[string]userCredential{
		user.Email: user,
	}, []assignment{
		{AssignedEmail: user.Email, DeviceID: "device-1"},
		{AssignedEmail: user.Email, DeviceID: "device-2"},
	}, 10)

	if material.Status.Status != "PASS" || material.Status.DeviceID != "device-1" || material.Status.AccessToken != "account-access" {
		t.Fatalf("status = %#v, want PASS from account login", material.Status)
	}
	if loginCalls != 1 {
		t.Fatalf("loginCalls = %d, want 1 per assigned user", loginCalls)
	}
	if got := material.TokensByEmail[user.Email].RefreshToken; got != "account-refresh" {
		t.Fatalf("refresh token = %q, want account-refresh", got)
	}
	if material.ManagersByEmail[user.Email] == nil {
		t.Fatal("manager missing for assigned user")
	}
	issuedUser, ok := material.UsersByEmail[user.Email]
	if !ok {
		t.Fatal("issued user material missing")
	}
	if strings.TrimSpace(issuedUser.AppCredentials.PrivateKeyPEM) == "" || strings.TrimSpace(issuedUser.AppCertificate.CertificatePEM) == "" {
		t.Fatalf("issued user key pair is incomplete: key=%t cert=%t", strings.TrimSpace(issuedUser.AppCredentials.PrivateKeyPEM) != "", strings.TrimSpace(issuedUser.AppCertificate.CertificatePEM) != "")
	}
	if _, err := loadAppX509KeyPairForUser(issuedUser); err != nil {
		t.Fatalf("issued app certificate no longer matches its persisted private key: %v", err)
	}
}

func TestPrepareAppCertificateBootstrapForAssignmentsUsesCachedAccountToken(t *testing.T) {
	loginCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		http.Error(w, "unexpected login", http.StatusInternalServerError)
	}))
	defer account.Close()

	user := userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
		Tokens: tokenBundle{
			AccessToken:  "cached-access",
			RefreshToken: "cached-refresh",
		},
	}
	material := prepareAppCertificateBootstrapForAssignments(account.URL, "https://video.example.invalid", "rtk-1234", map[string]userCredential{
		user.Email: user,
	}, []assignment{
		{AssignedEmail: user.Email, DeviceID: "device-1"},
		{AssignedEmail: user.Email, DeviceID: "device-2"},
	}, 10)

	if material.Status.Status != "PASS" || material.Status.DeviceID != "device-1" {
		t.Fatalf("status = %#v, want PASS from cached token", material.Status)
	}
	if loginCalls != 0 {
		t.Fatalf("loginCalls = %d, want cached token path to avoid login", loginCalls)
	}
	if got := material.TokensByEmail[user.Email].RefreshToken; got != "cached-refresh" {
		t.Fatalf("refresh token = %q, want cached-refresh", got)
	}
	if material.ManagersByEmail[user.Email] == nil {
		t.Fatal("manager missing for cached token user")
	}
}

func TestGenerateAppCSRUsesEd25519(t *testing.T) {
	csrPEM, keyPEM, err := generateAppCSR("app-user:user-1")
	if err != nil {
		t.Fatal(err)
	}
	csrBlock, _ := pem.Decode([]byte(csrPEM))
	if csrBlock == nil {
		t.Fatal("missing CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		t.Fatalf("CSR public key = %T, want ed25519.PublicKey", csr.PublicKey)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("key PEM block = %#v", keyBlock)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.(ed25519.PrivateKey); !ok {
		t.Fatalf("private key = %T, want ed25519.PrivateKey", key)
	}
}

func TestRequestAppTokenUsesTrustedHeadersForHTTPBaseURL(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Client-Verify") != "SUCCESS" {
			t.Fatalf("X-Client-Verify = %q", r.Header.Get("X-Client-Verify"))
		}
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=app-user:user-1/O=VideoCloud" {
			t.Fatalf("X-Client-S-DN = %q", got)
		}
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token"})
	}))
	defer server.Close()

	token, err := requestAppToken(server.URL, cert, "rtk-0041")
	if err != nil {
		t.Fatalf("requestAppToken() error = %v", err)
	}
	if token.Scope != "app" || token.AccessToken != "app-token" {
		t.Fatalf("token = %#v", token)
	}
}

func TestRequestDeviceTokenUsesTrustedHeadersForHTTPBaseURL(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Client-Verify") != "SUCCESS" {
			t.Fatalf("X-Client-Verify = %q", r.Header.Get("X-Client-Verify"))
		}
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=device-1/O=VideoCloud" {
			t.Fatalf("X-Client-S-DN = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["scope"] != "device" || body["devid"] != "device-1" || body["service"] != "mqtt" {
			t.Fatalf("body = %#v", body)
		}
		if _, ok := body["access_token_only"]; ok {
			t.Fatalf("access_token_only = %#v, want omitted for refreshable token pair", body["access_token_only"])
		}
		writeJSON(t, w, map[string]string{"access_token": "device-token", "refresh_token": "device-refresh"})
	}))
	defer server.Close()

	token, err := requestDeviceToken(server.URL, cert, "device-1")
	if err != nil {
		t.Fatalf("requestDeviceToken() error = %v", err)
	}
	if token != "device-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestRunAppCertificateBootstrapUsesLoginTokenWithoutArtifactKey(t *testing.T) {
	certPEM, _, _ := testAppMaterial(t, "app-user:user-1")
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"user":   map[string]string{"id": "user-1"},
			"tokens": map[string]string{"access_token": "account-access", "refresh_token": "account-refresh"},
			"app_certificate": map[string]string{
				"status":          "issued",
				"subject":         "app-user:user-1",
				"certificate_pem": certPEM,
			},
		})
	}))
	defer account.Close()

	status := runAppCertificateBootstrap(account.URL, "https://video.example.invalid", "rtk-1234", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
	}, "rtk-0041")

	if status.Status != "PASS" || status.AccessToken != "account-access" {
		t.Fatalf("status = %#v, want PASS from account login", status)
	}
}

func TestRunAppCertificateBootstrapCSRRequiredStillGeneratesCSR(t *testing.T) {
	loginCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/brand-clouds/rtk-1234/auth/login" {
			t.Fatalf("login path = %q, want brand-cloud login route", r.URL.Path)
		}
		loginCalls++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode login: %v", err)
		}
		if body["app_csr_pem"] == "" {
			writeJSON(t, w, map[string]any{
				"user":            map[string]string{"id": "user-1"},
				"app_certificate": map[string]string{"status": "csr_required"},
			})
			return
		}
		certPEM := issueCertificateForCSR(t, body["app_csr_pem"])
		writeJSON(t, w, map[string]any{
			"user":   map[string]string{"id": "user-1"},
			"tokens": map[string]string{"access_token": "account-access", "refresh_token": "account-refresh"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       certPEM,
				"certificate_chain_pem": certPEM,
			},
		})
	}))
	defer account.Close()

	status := runAppCertificateBootstrap(account.URL, "https://video.example.invalid", "rtk-1234", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
	}, "rtk-0041")

	if status.Status != "PASS" {
		t.Fatalf("status = %#v, want PASS", status)
	}
	if loginCalls != 2 {
		t.Fatalf("loginCalls = %d, want 2", loginCalls)
	}
	if status.AccessToken != "account-access" {
		t.Fatalf("access token = %q, want account-access", status.AccessToken)
	}
}

func TestRunAppCertificateBootstrapBlocksMissingTenantSlug(t *testing.T) {
	status := runAppCertificateBootstrap("https://account.example.invalid", "https://video.example.invalid", "", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
	}, "rtk-0041")

	if status.Status != "BLOCKED" || status.Reason != "users artifact missing tenant_slug" {
		t.Fatalf("status = %#v, want BLOCKED missing tenant_slug", status)
	}
}

func TestRequestAppTokenParsesAccessToken(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	video, _ := newAppTokenServer(t, "app-user:user-1")
	defer video.Close()

	token, err := requestAppToken(video.URL, cert, "rtk-0041")
	if err != nil {
		t.Fatal(err)
	}
	if token.Scope != "app" || token.AccessToken != "app-token-rtk-0041" {
		t.Fatalf("token = %#v, want parsed app access token", token)
	}
}

func TestLoadAppX509KeyPairSkipsNonPEMCertificateStatus(t *testing.T) {
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	cert, err := loadAppX509KeyPairForUser(userCredential{
		AppCredentials: appCertificateKeys{PrivateKeyPEM: keyPEM, CSRPem: csrPEM},
		AppCertificate: appCertificateSummary{
			CertificateChainPEM: "issued",
			CertificatePEM:      certPEM,
		},
	})
	if err != nil {
		t.Fatalf("loadAppX509KeyPairForUser() error = %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("certificate chain is empty")
	}
}

func TestLoadAppX509KeyPairNormalizesEscapedPEM(t *testing.T) {
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	escapedCertPEM := strings.ReplaceAll(certPEM, "\n", `\n`)
	cert, err := loadAppX509KeyPairForUser(userCredential{
		AppCredentials: appCertificateKeys{PrivateKeyPEM: keyPEM, CSRPem: csrPEM},
		AppCertificate: appCertificateSummary{
			CertificatePEM: escapedCertPEM,
		},
	})
	if err != nil {
		t.Fatalf("loadAppX509KeyPairForUser() error = %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("certificate chain is empty")
	}
}

func TestLoadAppX509KeyPairFallsBackFromInvalidChainToLeaf(t *testing.T) {
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	cert, err := loadAppX509KeyPairForUser(userCredential{
		AppCredentials: appCertificateKeys{PrivateKeyPEM: keyPEM, CSRPem: csrPEM},
		AppCertificate: appCertificateSummary{
			CertificateChainPEM: "-----BEGIN CERTIFICATE-----\nnot-valid\n-----END CERTIFICATE-----",
			CertificatePEM:      certPEM,
		},
	})
	if err != nil {
		t.Fatalf("loadAppX509KeyPairForUser() error = %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("certificate chain is empty")
	}
}

func TestMQTTTopicsEquivalentNormalizesPhysicalTenantPrefix(t *testing.T) {
	if !mqttTopicsEquivalent("_bc/brand-cloud-1/$vc/devices/device-1/shadow/update/accepted", "$vc/devices/device-1/shadow/update/accepted") {
		t.Fatal("physical tenant shadow topic did not match logical topic")
	}
	if !mqttTopicsEquivalent("_bc/brand-cloud-1/devices/device-1/logs", "devices/device-1/logs") {
		t.Fatal("physical tenant transport topic did not match logical topic")
	}
	if mqttTopicsEquivalent("_bc/brand-cloud-2/$vc/devices/device-2/shadow/update/accepted", "$vc/devices/device-1/shadow/update/accepted") {
		t.Fatal("different logical topic matched")
	}
}

func TestBoundedShadowClientTokenHonorsProtocolLimit(t *testing.T) {
	longRunID := strings.Repeat("qualification-run-", 8)
	token := boundedShadowClientToken("cmd", longRunID, "device-0001")
	if len(token) > 64 {
		t.Fatalf("client token length = %d, want <= 64: %q", len(token), token)
	}
	if token != boundedShadowClientToken("cmd", longRunID, "device-0001") {
		t.Fatal("bounded client token is not deterministic")
	}
	if token == boundedShadowClientToken("cmd", longRunID, "device-0002") {
		t.Fatal("different device identities produced the same client token")
	}
	if got := boundedShadowClientToken("cmd", "short", "device"); got != "cmd-short-device" {
		t.Fatalf("short token changed unexpectedly: %q", got)
	}
}

func TestAccountLoginTokenManagerKeepsValidAccessTokenPastHalfLife(t *testing.T) {
	baseTime := time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)
	oldToken := testJWT(t, baseTime, baseTime.Add(2*time.Minute))
	refreshCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		t.Fatalf("valid access token should not call %s", r.URL.Path)
	}))
	defer account.Close()

	manager := newAccountLoginTokenManager(account.URL, "rtk-1234", userCredential{}, tokenBundle{
		AccessToken:  oldToken,
		RefreshToken: "old-refresh",
		issuedAt:     baseTime,
	})
	manager.now = func() time.Time { return baseTime.Add(70 * time.Second) }

	got, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != oldToken {
		t.Fatalf("token = %q, want cached access token", got)
	}
	if refreshCalls != 0 {
		t.Fatalf("refreshCalls = %d, want 0", refreshCalls)
	}
}

func TestAccountLoginTokenManagerRefreshesExpiredAccessToken(t *testing.T) {
	baseTime := time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)
	oldToken := testJWT(t, baseTime, baseTime.Add(time.Minute))
	newToken := testJWT(t, baseTime.Add(2*time.Minute), baseTime.Add(12*time.Minute))
	refreshCalls := 0
	loginCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-1234/auth/refresh":
			refreshCalls++
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["refresh_token"] != "old-refresh" {
				t.Fatalf("refresh_token = %q, want old-refresh", body["refresh_token"])
			}
			writeJSON(t, w, map[string]any{
				"tokens": map[string]string{"access_token": newToken, "refresh_token": "new-refresh"},
			})
		case "/v1/brand-clouds/rtk-1234/auth/login":
			loginCalls++
			t.Fatal("expired access token with refresh token should refresh before login")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer account.Close()

	manager := newAccountLoginTokenManager(account.URL, "rtk-1234", userCredential{}, tokenBundle{
		AccessToken:  oldToken,
		RefreshToken: "old-refresh",
		issuedAt:     baseTime,
	})
	manager.now = func() time.Time { return baseTime.Add(70 * time.Second) }

	got, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != newToken {
		t.Fatalf("token = %q, want refreshed token", got)
	}
	if refreshCalls != 1 || loginCalls != 0 {
		t.Fatalf("refreshCalls=%d loginCalls=%d, want 1/0", refreshCalls, loginCalls)
	}
	if manager.bundle.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token = %q, want new-refresh", manager.bundle.RefreshToken)
	}
}

func TestAccountLoginTokenManagerFallsBackToLoginWhenRefreshFails(t *testing.T) {
	baseTime := time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)
	oldToken := testJWT(t, baseTime, baseTime.Add(time.Minute))
	loginToken := testJWT(t, baseTime.Add(2*time.Minute), baseTime.Add(12*time.Minute))
	refreshCalls := 0
	loginCalls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-1234/auth/refresh":
			refreshCalls++
			http.Error(w, "expired refresh token", http.StatusUnauthorized)
		case "/v1/brand-clouds/rtk-1234/auth/login":
			loginCalls++
			writeJSON(t, w, map[string]any{
				"user":   map[string]string{"id": "user-1"},
				"tokens": map[string]string{"access_token": loginToken, "refresh_token": "login-refresh"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer account.Close()

	manager := newAccountLoginTokenManager(account.URL, "rtk-1234", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
	}, tokenBundle{
		AccessToken:  oldToken,
		RefreshToken: "old-refresh",
		issuedAt:     baseTime,
	})
	manager.now = func() time.Time { return baseTime.Add(70 * time.Second) }

	got, err := manager.Token(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != loginToken {
		t.Fatalf("token = %q, want login token", got)
	}
	if refreshCalls != 1 || loginCalls != 1 {
		t.Fatalf("refreshCalls=%d loginCalls=%d, want 1/1", refreshCalls, loginCalls)
	}
	if manager.bundle.RefreshToken != "login-refresh" {
		t.Fatalf("refresh token = %q, want login-refresh", manager.bundle.RefreshToken)
	}
}

func TestActorSeparatedTelemetryRequiresAppObserverReceive(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: testMQTTToken("device"),
		AppToken:    testMQTTToken("app"),
		Dial:        broker.Dial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}

	result := runActorSeparatedProbe(probe)

	if result.MQTTStatus != "PASS" {
		t.Fatalf("result = %#v, want PASS", result)
	}
	if result.TelemetryPublishActor != "device_client" || result.TelemetrySubscribeActor != "app_observer" {
		t.Fatalf("telemetry actors = %q/%q", result.TelemetryPublishActor, result.TelemetrySubscribeActor)
	}
	if broker.PublishCount("app-observer", "devices/rtk-0041/up/messages") != 0 {
		t.Fatal("app observer must not be the telemetry publisher")
	}
	if broker.PublishCount("device", "devices/rtk-0041/up/messages") == 0 {
		t.Fatal("device did not publish telemetry")
	}
}

func TestActorSeparatedCommandRequiresDeviceReceiveAndAppAck(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: testMQTTToken("device"),
		AppToken:    testMQTTToken("app"),
		Dial:        broker.Dial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}

	result := runActorSeparatedProbe(probe)

	if result.MQTTStatus != "PASS" || result.CommandStatus != "PASS" {
		t.Fatalf("result = %#v, want command PASS", result)
	}
	if result.CommandPublishActor != "app_controller" || result.CommandSubscribeActor != "device_client" {
		t.Fatalf("command actors = %q/%q", result.CommandPublishActor, result.CommandSubscribeActor)
	}
	if broker.PublishCount("app-controller", "$vc/devices/rtk-0041/shadow/update") == 0 {
		t.Fatal("app controller did not publish shadow desired")
	}
	if broker.PublishCount("device", "$vc/devices/rtk-0041/shadow/update") == 0 {
		t.Fatal("device did not publish shadow reported")
	}
}

func TestShadowStateWithLoadTestMarkerForcesFreshDeltaAndReportedClear(t *testing.T) {
	base := map[string]any{"power": true}

	got := shadowStateWithLoadTestMarker(base, "run-1", "cmd-1")

	if got["power"] != true || got["_loadtest_run_id"] != "run-1" || got["_loadtest_command_id"] != "cmd-1" {
		t.Fatalf("shadowStateWithLoadTestMarker = %#v, want base state plus marker", got)
	}
	if _, ok := base["_loadtest_run_id"]; ok {
		t.Fatalf("shadowStateWithLoadTestMarker mutated base state: %#v", base)
	}
}

func TestActorSeparatedProbePublishesRuntimeLogsForDeviceAndAppActors(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: testMQTTToken("device"),
		AppToken:    testMQTTToken("app"),
		Dial:        broker.Dial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}

	result := runActorSeparatedProbe(probe)

	if result.MQTTStatus != "PASS" {
		t.Fatalf("result = %#v, want PASS", result)
	}
	if result.RuntimeLogStreamID == "" {
		t.Fatalf("runtime log stream id missing: %#v", result)
	}
	if len(result.RuntimeLogExpectations) < 6 {
		t.Fatalf("runtime log expectations = %#v, want publish/receive entries", result.RuntimeLogExpectations)
	}
	for _, actor := range []string{"device", "app-controller", "app-observer"} {
		if broker.PublishCount(actor, "devices/rtk-0041/logs") == 0 {
			t.Fatalf("%s did not publish runtime logs", actor)
		}
	}
}

func TestSustainedShadowCommandPublishesRuntimeLogsForServerCorrelation(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		RunID:       "run-sustained-logs",
		DeviceToken: testMQTTToken("device"),
		Dial:        broker.TLSDial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}, "device", testMQTTToken("device"))
	if err != nil {
		t.Fatal(err)
	}
	defer deviceConn.Close()
	if err := mqttSubscribe(deviceConn, 1, "$vc/devices/rtk-0041/shadow/update/delta"); err != nil {
		t.Fatal(err)
	}
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}

	var totals mqttIOTotals
	reader := startSustainedDeviceReader(deviceConn)
	defer reader.Close()
	err = runSustainedShadowCommand(sustainedDeviceSession{
		Record:          certRecord{DeviceID: "rtk-0041", DeviceType: "light"},
		Conn:            deviceConn,
		Reader:          reader,
		MQTTTarget:      mqttEndpointTarget{Host: host, Port: port},
		AppLoginManager: newAccountLoginTokenManager("", "", userCredential{}, tokenBundle{AccessToken: testMQTTToken("app")}),
	}, "RTK", "run-sustained-logs", "", &totals)
	if err != nil {
		t.Fatal(err)
	}
	if totals.AppDesiredWrites != 1 || totals.DeltaReceived != 1 || totals.ReportedEvents != 1 || totals.AppReceivedAcks != 1 {
		t.Fatalf("totals = %+v, want desired/delta/reported/ack", totals)
	}

	var payloads [][]byte
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		payloads = append(
			broker.PublishPayloads("app-controller", "devices/rtk-0041/logs"),
			broker.PublishPayloads("device", "devices/rtk-0041/logs")...,
		)
		if len(payloads) >= 4 {
			break
		}
	}
	got := map[string]int{}
	for _, payload := range payloads {
		var row struct {
			Source  string `json:"source"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &row); err != nil {
			t.Fatal(err)
		}
		got[row.Source+"\x00"+row.Message]++
	}
	for _, want := range []struct {
		source  string
		message string
	}{
		{"app_controller", "mqtt_e2e shadow_desired app_controller publish"},
		{"device_client", "mqtt_e2e shadow_delta device_client receive"},
		{"device_client", "mqtt_e2e shadow_reported device_client publish"},
		{"app_observer", "mqtt_e2e shadow_reported app_observer receive"},
	} {
		if got[want.source+"\x00"+want.message] != 1 {
			t.Fatalf("runtime logs = %#v, missing %s %s", got, want.source, want.message)
		}
	}
}

func TestSustainedShadowCommandCanDisableRuntimeLogs(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		RunID:       "run-no-runtime-logs",
		DeviceToken: testMQTTToken("device"),
		Dial:        broker.TLSDial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}, "device", testMQTTToken("device"))
	if err != nil {
		t.Fatal(err)
	}
	defer deviceConn.Close()
	if err := mqttSubscribe(deviceConn, 1, "$vc/devices/rtk-0041/shadow/update/delta"); err != nil {
		t.Fatal(err)
	}
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}

	var totals mqttIOTotals
	reader := startSustainedDeviceReader(deviceConn)
	defer reader.Close()
	err = runSustainedShadowCommandWithContext(sustainedDeviceSession{
		Record:          certRecord{DeviceID: "rtk-0041", DeviceType: "light"},
		Conn:            deviceConn,
		Reader:          reader,
		MQTTTarget:      mqttEndpointTarget{Host: host, Port: port},
		AppLoginManager: newAccountLoginTokenManager("", "", userCredential{}, tokenBundle{AccessToken: testMQTTToken("app")}),
	}, "RTK", "run-no-runtime-logs", "", &totals, sustainedCommandContext{DisableRuntimeLogs: true})
	if err != nil {
		t.Fatal(err)
	}
	if totals.AppDesiredWrites != 1 || totals.DeltaReceived != 1 || totals.ReportedEvents != 1 || totals.AppReceivedAcks != 1 {
		t.Fatalf("totals = %+v, want desired/delta/reported/ack", totals)
	}
	if len(totals.CommandEvents) != 1 {
		t.Fatalf("command events = %#v, want one event", totals.CommandEvents)
	}
	if totals.CommandEvents[0].RuntimeLogStreamID != "" || len(totals.CommandEvents[0].ExpectedLogs) != 0 {
		t.Fatalf("runtime log metadata should be empty when disabled: %#v", totals.CommandEvents[0])
	}
	if got := broker.PublishCount("app-controller", "devices/rtk-0041/logs") + broker.PublishCount("device", "devices/rtk-0041/logs"); got != 0 {
		t.Fatalf("runtime log publishes = %d, want 0", got)
	}
}

func TestSustainedShadowCommandRequestsAppMQTTTokenWithAppCertificate(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		RunID:       "run-app-cert-token",
		DeviceToken: testMQTTToken("device"),
		Dial:        broker.TLSDial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}, "device", testMQTTToken("device"))
	if err != nil {
		t.Fatal(err)
	}
	defer deviceConn.Close()
	if err := mqttSubscribe(deviceConn, 1, "$vc/devices/rtk-0041/shadow/update/delta"); err != nil {
		t.Fatal(err)
	}
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	tokenRequests := 0
	video := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests++
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=app-user:user-1/O=VideoCloud" {
			t.Fatalf("X-Client-S-DN = %q, want app certificate subject", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["scope"] != "app" || body["service"] != "mqtt" || body["devid"] != "rtk-0041" {
			t.Fatalf("request_token body = %#v", body)
		}
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": testMQTTToken("app")})
	}))
	defer video.Close()

	var totals mqttIOTotals
	reader := startSustainedDeviceReader(deviceConn)
	defer reader.Close()
	err = runSustainedShadowCommandWithContext(sustainedDeviceSession{
		Record:     certRecord{DeviceID: "rtk-0041", DeviceType: "light"},
		Conn:       deviceConn,
		Reader:     reader,
		MQTTTarget: mqttEndpointTarget{Host: host, Port: port},
		AppLoginManager: newAccountLoginTokenManager("", "", userCredential{
			Email: "rtk+001@users.local",
			AppCredentials: appCertificateKeys{
				PrivateKeyPEM: keyPEM,
				CSRPem:        csrPEM,
			},
			AppCertificate: appCertificateSummary{
				CertificatePEM:      certPEM,
				CertificateChainPEM: certPEM,
			},
		}, tokenBundle{AccessToken: testMQTTToken("account")}),
	}, "RTK", "run-app-cert-token", video.URL, &totals, sustainedCommandContext{DisableRuntimeLogs: true})
	if err != nil {
		t.Fatal(err)
	}
	if tokenRequests != 1 {
		t.Fatalf("tokenRequests = %d, want 1", tokenRequests)
	}
	if totals.AppTokenSuccesses != 1 || totals.AppDesiredWrites != 1 || totals.AppReceivedAcks != 1 {
		t.Fatalf("totals = %+v, want app token and command success", totals)
	}
}

func TestSustainedShadowCommandFailsBeforeDeltaWhenAcceptedIsMissing(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	broker.SuppressShadowAccepted = true
	defer broker.Close()
	deviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		RunID:       "run-missing-accepted",
		DeviceToken: testMQTTToken("device"),
		Dial:        broker.TLSDial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}, "device", testMQTTToken("device"))
	if err != nil {
		t.Fatal(err)
	}
	defer deviceConn.Close()
	if err := mqttSubscribe(deviceConn, 1, "$vc/devices/rtk-0041/shadow/update/delta"); err != nil {
		t.Fatal(err)
	}
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}

	var totals mqttIOTotals
	reader := startSustainedDeviceReader(deviceConn)
	defer reader.Close()
	err = runSustainedShadowCommandUntil(sustainedDeviceSession{
		Record:          certRecord{DeviceID: "rtk-0041", DeviceType: "light"},
		Conn:            deviceConn,
		Reader:          reader,
		MQTTTarget:      mqttEndpointTarget{Host: host, Port: port},
		AppLoginManager: newAccountLoginTokenManager("", "", userCredential{}, tokenBundle{AccessToken: testMQTTToken("app")}),
	}, "RTK", "run-missing-accepted", "", time.Now().Add(250*time.Millisecond), &totals)
	if err == nil {
		t.Fatal("runSustainedShadowCommandUntil succeeded without shadow accepted")
	}
	if totals.FailureReasons["app_shadow_accepted_wait_failed"] != 1 {
		t.Fatalf("failure reasons = %#v, want app_shadow_accepted_wait_failed", totals.FailureReasons)
	}
	if totals.FailureReasons["device_delta_wait_failed"] != 0 {
		t.Fatalf("failure reasons = %#v, should not report device_delta_wait_failed before accepted", totals.FailureReasons)
	}
}

func TestShadowPolicyProbeCapturesVersionDuplicateAndAuthorizationEvidence(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	broker.RejectForbiddenTopics = true
	defer broker.Close()
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _ := testAppMaterial(t, "policy-device")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"scope": "device", "access_token": testMQTTToken("device")})
	}))
	defer api.Close()
	appConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID: "policy-device", Brandname: "RTK", RunID: "policy-run", AppToken: testMQTTToken("app"),
		Dial: broker.TLSDial, Timeout: time.Second, Now: fixedProbeTime,
	}, "app-controller", testMQTTToken("app"))
	if err != nil {
		t.Fatal(err)
	}
	defer appConn.Close()
	rejectedTopic := "$vc/devices/policy-device/shadow/update/rejected"
	if err := mqttSubscribe(appConn, 1, rejectedTopic); err != nil {
		t.Fatal(err)
	}
	var totals mqttIOTotals
	err = runShadowPolicyProbe(sustainedDeviceSession{
		Record: certRecord{
			DeviceID: "policy-device", DeviceType: "light",
			CertPEM: certPEM, KeyPEM: keyPEM,
		},
		MQTTTarget: mqttEndpointTarget{Host: host, Port: port},
	}, "RTK", "policy-run", api.URL, appConn, rejectedTopic, map[string]any{"power": true}, "command-1",
		map[string]any{"version": float64(1)}, time.Now().Add(5*time.Second), time.Second, &totals)
	if err != nil {
		t.Fatal(err)
	}
	if totals.VersionConflicts != 1 || totals.RejectedUpdates != 2 || totals.DuplicateSuppressions != 1 || totals.UnauthorizedRejections != 1 || totals.AWSNamespaceRejections != 1 || totals.AuthViolations != 0 {
		t.Fatalf("policy totals = %+v", totals)
	}
	broker.mu.Lock()
	broker.AllowAWSNamespace = true
	broker.mu.Unlock()
	var unsafeTotals mqttIOTotals
	err = runShadowPolicyProbe(sustainedDeviceSession{
		Record: certRecord{
			DeviceID: "policy-device", DeviceType: "light",
			CertPEM: certPEM, KeyPEM: keyPEM,
		},
		MQTTTarget: mqttEndpointTarget{Host: host, Port: port},
	}, "RTK", "policy-run-unsafe", api.URL, appConn, rejectedTopic, map[string]any{"power": true}, "command-2",
		map[string]any{"version": float64(1)}, time.Now().Add(5*time.Second), time.Second, &unsafeTotals)
	if err == nil || !strings.Contains(err.Error(), "forbidden AWS shadow namespace") {
		t.Fatalf("AWS namespace acceptance error = %v", err)
	}
	if unsafeTotals.UnauthorizedRejections != 1 || unsafeTotals.AWSNamespaceRejections != 0 || unsafeTotals.AuthViolations != 1 {
		t.Fatalf("unsafe policy totals = %+v", unsafeTotals)
	}
}

func TestSustainedStageResultsJSONIncludesSuccessfulCommandEvents(t *testing.T) {
	rows := sustainedStageResultsJSON([]sustainedStageResult{{
		Name:              "25pct",
		ConnectedTarget:   2250,
		ActiveConnections: 2250,
		Status:            "PASS",
		Totals: mqttIOTotals{
			AppDesiredWrites: 1,
			CommandEvents: []sustainedCommandEvent{{
				Stage:              "25pct",
				DeviceID:           "rtk-0041",
				CommandID:          "cmd-0041",
				RuntimeLogStreamID: "mqtt-e2e-run-rtk-0041-abcd",
				EventIndex:         7,
				SessionSlot:        3,
				MQTTTarget:         "127.0.0.1:8883",
				ExpectedLogs: []logExpect{
					{Seq: 1, Source: "app_controller", Message: "mqtt_e2e shadow_desired app_controller publish"},
					{Seq: 2, Source: "device_client", Message: "mqtt_e2e shadow_delta device_client receive"},
					{Seq: 3, Source: "device_client", Message: "mqtt_e2e shadow_reported device_client publish"},
					{Seq: 4, Source: "app_observer", Message: "mqtt_e2e shadow_reported app_observer receive"},
				},
				OccurredAt: "2026-06-17T04:00:00Z",
			}},
		},
		CommandsAttempted: 1,
		CommandsPassed:    1,
	}}, appBootstrapStatus{})

	events, ok := rows[0]["command_events"].([]sustainedCommandEvent)
	if !ok || len(events) != 1 {
		t.Fatalf("command events = %#v, want one successful command event", rows[0]["command_events"])
	}
	if events[0].RuntimeLogStreamID != "mqtt-e2e-run-rtk-0041-abcd" || len(events[0].ExpectedLogs) != 4 {
		t.Fatalf("unexpected command event: %#v", events[0])
	}
}

func TestSustainedActorsUseLongMQTTKeepAlive(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	certPEM, keyPEM, _ := testAppMaterial(t, "rtk-0041")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"access_token": testMQTTToken("device")})
	}))
	defer tokenServer.Close()
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	var totals mqttIOTotals
	conn, err := connectSustainedDevice(certRecord{
		DeviceID:   "rtk-0041",
		DeviceType: "light",
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
	}, "RTK", "run-sustained-keepalive", tokenServer.URL, mqttEndpointTarget{Host: host, Port: port}, time.Time{}, tokenRequestOptions{Timeout: time.Second}, func(update func(*mqttIOTotals)) {
		update(&totals)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if totals.DeviceTokenAttempts != 1 || totals.DeviceTokenSuccesses != 1 ||
		totals.DeviceMQTTDialAttempts != 1 || totals.DeviceMQTTDialSuccesses != 1 ||
		totals.DeviceMQTTConnackAttempts != 1 || totals.DeviceMQTTConnackSuccesses != 1 {
		t.Fatalf("unexpected sustained device phase counters: %#v", totals)
	}

	clientID := "mqtt-device-test-device"
	if got := broker.KeepAlive(clientID); got != sustainedMQTTKeepAliveSeconds {
		t.Fatalf("sustained device keepalive = %d, want %d", got, sustainedMQTTKeepAliveSeconds)
	}
}

func TestWaitForSDKReconnectSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconnect.signal")
	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = os.WriteFile(path, []byte("ready\n"), 0o600)
	}()
	if err := waitForSDKReconnectSignal(path, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("waitForSDKReconnectSignal() error = %v", err)
	}
	if err := waitForSDKReconnectSignal(filepath.Join(t.TempDir(), "missing"), time.Now().Add(20*time.Millisecond)); err == nil {
		t.Fatal("waitForSDKReconnectSignal() unexpectedly accepted a missing signal")
	}
}

func TestSustainedDeviceReaderSendsMQTTKeepAlivePing(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	reader := startSustainedDeviceReaderWithKeepAlive(&lockedReadWriteCloser{ReadWriteCloser: client}, 20*time.Millisecond)
	defer reader.Close()

	_ = server.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	packetType, body, err := mqttReadPacket(server)
	if err != nil {
		t.Fatalf("read keepalive packet: %v", err)
	}
	if packetType != 0xc0 || len(body) != 0 {
		t.Fatalf("keepalive packet type=%#x body_len=%d, want PINGREQ", packetType, len(body))
	}
}

func TestRuntimeLogRecorderQoS1WaitsForPubAck(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	recorder := newRuntimeLogRecorderForCommand("rtk-0041", "run-qos1", "cmd-1", fixedProbeTime)
	done := make(chan error, 1)
	go func() {
		_, err := recorder.RecordWithExpectationQoS1(client, "shadow_reported", "app_observer", "receive", "$vc/devices/rtk-0041/shadow/update/documents", map[string]any{"command_id": "cmd-1"})
		done <- err
	}()

	packetType, body, err := mqttReadPacket(server)
	if err != nil {
		t.Fatalf("read runtime log publish: %v", err)
	}
	if packetType != 0x32 {
		t.Fatalf("runtime log packet type = %#x, want QoS1 PUBLISH", packetType)
	}
	packetID := mqttPublishPacketIDForTest(packetType&0x0f, body)
	if packetID == 0 {
		t.Fatal("runtime log QoS1 publish missing packet id")
	}
	select {
	case err := <-done:
		t.Fatalf("RecordWithExpectationQoS1 returned before PUBACK: %v", err)
	default:
	}
	if err := mqttWritePacket(server, 0x40, []byte{byte(packetID >> 8), byte(packetID)}); err != nil {
		t.Fatalf("write puback: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RecordWithExpectationQoS1 returned error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("RecordWithExpectationQoS1 did not return after PUBACK")
	}
}

func TestActorSeparatedProbeFailsWhenAppMQTTAuthRejected(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	broker.RejectUsername = "test-brand-cloud"
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: testMQTTToken("device"),
		AppToken:    testMQTTToken("app"),
		Dial:        broker.Dial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}

	result := runActorSeparatedProbe(probe)

	if result.MQTTStatus != "FAIL" {
		t.Fatalf("result = %#v, want FAIL", result)
	}
	if !strings.Contains(result.Error, "app MQTT actor unauthorized") {
		t.Fatalf("error = %q, want app MQTT actor unauthorized", result.Error)
	}
}

func TestRenderReportDoesNotDescribeLoopback(t *testing.T) {
	report := renderReport(map[string]any{
		"status":           "PASS",
		"overall":          "pass",
		"generated_at":     "2026-06-04T00:00:00Z",
		"env":              map[string]string{"root": "/tmp/env"},
		"brandname":        "RTK",
		"profile":          "smoke",
		"duration_seconds": 120,
		"seed":             1,
	})
	if strings.Contains(strings.ToLower(report), "loopback") {
		t.Fatalf("report must not mention loopback:\n%s", report)
	}
}

func TestRenderReportShowsMQTTE2ETraceChain(t *testing.T) {
	report := renderReport(map[string]any{
		"status":           "PASS",
		"overall":          "pass",
		"generated_at":     "2026-06-04T00:00:00Z",
		"env":              map[string]string{"root": "/tmp/env"},
		"brandname":        "RTK",
		"profile":          "smoke",
		"duration_seconds": 120,
		"seed":             1,
		"mqtt": map[string]any{
			"probe_model":          "actor_separated_iot",
			"client_identity_mode": "account_login_token_and_device_token",
			"telemetry_receiver":   "app_observer",
			"command_receiver":     "device_client",
		},
		"devices": []deviceResult{{
			DeviceID:   "rtk-0041",
			DeviceType: "light",
			TraceChain: []traceStep{
				{Step: 1, Timestamp: "2026-06-04T08:00:00Z", Phase: "app_login", Actor: "app_actor", Action: "account_manager_login", Status: "PASS"},
				{Step: 2, Timestamp: "2026-06-04T08:00:01Z", Phase: "telemetry", Actor: "app_observer", Action: "subscribe", Topic: "devices/rtk-0041/up/messages", Status: "PASS"},
				{Step: 3, Timestamp: "2026-06-04T08:00:02Z", Phase: "telemetry", Actor: "device_client", Action: "publish", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report message_id=msg-1 device_id=rtk-0041"},
			},
		}},
	})
	for _, want := range []string{"## MQTT E2E Trace Chain", "Timestamp", "app_actor", "device_client", "app_observer", "devices/rtk-0041/up/messages", "message_type=status_report"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(strings.ToLower(report), "access_token") || strings.Contains(report, "BEGIN ") {
		t.Fatalf("report leaked sensitive material:\n%s", report)
	}
}

func TestRenderConsoleShowsRuntimeMQTTTraceData(t *testing.T) {
	base := map[string]any{
		"status":           "PASS",
		"overall":          "pass",
		"env":              map[string]string{"root": "/tmp/env"},
		"brandname":        "RTK",
		"profile":          "smoke",
		"duration_seconds": 120,
		"results_file":     "/tmp/results.json",
		"report_file":      "/tmp/TEST_REPORT.md",
		"devices": []deviceResult{{
			DeviceID:   "rtk-0041",
			DeviceType: "light",
			TraceChain: []traceStep{
				{Step: 8, Timestamp: "2026-06-04T08:00:02Z", Phase: "telemetry", Actor: "device_client", Action: "publish", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report message_id=msg-1 device_id=rtk-0041"},
				{Step: 9, Timestamp: "2026-06-04T08:00:03Z", Phase: "telemetry", Actor: "app_observer", Action: "receive", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report message_id=msg-1 device_id=rtk-0041"},
				{Step: 10, Timestamp: "2026-06-04T08:00:04Z", Phase: "mqtt_connect", Actor: "app_controller", Action: "mqtt_connect", Status: "PASS"},
			},
		}},
	}
	console := renderConsole(base)
	for _, want := range []string{"Runtime MQTT Trace", "2026-06-04T08:00:02Z", "device_client publish", "app_observer receive", "message_type=status_report", "message_id=msg-1"} {
		if !strings.Contains(console, want) {
			t.Fatalf("console missing %q:\n%s", want, console)
		}
	}
	if strings.Contains(console, "app_controller mqtt_connect") {
		t.Fatalf("summary console should not include connect step:\n%s", console)
	}

	base["trace_detail"] = "full"
	full := renderConsole(base)
	if !strings.Contains(full, "app_controller mqtt_connect") {
		t.Fatalf("full console should include connect step:\n%s", full)
	}

	base["trace_detail"] = "none"
	none := renderConsole(base)
	if strings.Contains(none, "Runtime MQTT Trace") {
		t.Fatalf("none console should hide runtime trace:\n%s", none)
	}
}

func TestAggregateMQTTIOTotalsFromTraceChain(t *testing.T) {
	rows := []deviceResult{{
		DeviceID:   "rtk-0041",
		DeviceType: "light",
		TraceChain: []traceStep{
			{Phase: "mqtt_connect", Actor: "device_client", Action: "mqtt_connect", Status: "PASS"},
			{Phase: "command", Actor: "device_client", Action: "subscribe", Topic: "devices/rtk-0041/down/commands", Status: "PASS"},
			{Phase: "telemetry", Actor: "device_client", Action: "publish", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report"},
			{Phase: "telemetry", Actor: "app_observer", Action: "receive", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report"},
			{Phase: "command", Actor: "app_controller", Action: "publish", Topic: "devices/rtk-0041/down/commands", Status: "PASS", Data: "message_type=command"},
			{Phase: "command", Actor: "device_client", Action: "receive", Topic: "devices/rtk-0041/down/commands", Status: "PASS", Data: "message_type=command"},
			{Phase: "command_ack", Actor: "device_client", Action: "publish", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=command_result"},
			{Phase: "command_ack", Actor: "app_observer", Action: "receive", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=command_result"},
		},
	}}

	totals := aggregateMQTTIOTotals(rows, appBootstrapStatus{Status: "PASS"}, 2, 1)
	if totals.ConnectAttempts != 1 || totals.ConnectSuccesses != 1 || totals.SubscribeSuccesses != 1 {
		t.Fatalf("unexpected connection totals: %#v", totals)
	}
	if totals.PublishSuccesses != 2 || totals.MessagesReceived != 1 || totals.ReportedEvents != 1 {
		t.Fatalf("unexpected MQTT IO totals: %#v", totals)
	}
	if totals.HTTPRequests != 1 || totals.HTTPSuccesses != 1 || totals.HTTPFailures != 0 ||
		totals.AppLoginAttempts != 1 || totals.AppDesiredWrites != 1 || totals.AppReceivedAcks != 1 {
		t.Fatalf("unexpected app totals: %#v", totals)
	}

	result := map[string]any{}
	attachMQTTIOTotals(result, totals)
	if result["connect_attempts"] != int64(1) {
		t.Fatalf("top-level counters not attached: %#v", result)
	}
	if result["device_mqtt_totals"] == nil || result["app_user_totals"] == nil {
		t.Fatalf("structured totals not attached: %#v", result)
	}
}

func TestAttachMQTTIOTotalsIncludesFailureReasons(t *testing.T) {
	totals := mqttIOTotals{
		HTTPFailures: 2,
		FailureReasons: map[string]int64{
			"app_desired_publish_failed": 2,
		},
	}
	result := map[string]any{}
	attachMQTTIOTotals(result, totals)
	reasons, ok := result["failure_reasons"].(map[string]int64)
	if !ok {
		t.Fatalf("failure_reasons missing or wrong type: %#v", result["failure_reasons"])
	}
	if reasons["app_desired_publish_failed"] != 2 {
		t.Fatalf("failure_reasons = %#v", reasons)
	}
}

func TestAttachMQTTIOTotalsIncludesBoundedFailureEvents(t *testing.T) {
	totals := mqttIOTotals{}
	for idx := 0; idx < maxFailureEvents+5; idx++ {
		recordFailureEvent(&totals, sustainedFailureEvent{
			Stage:       "75pct",
			Reason:      "device_delta_wait_failed",
			Detail:      "network EOF",
			Phase:       "device_delta_wait",
			DeviceID:    fmt.Sprintf("rtk-%04d", idx),
			CommandID:   fmt.Sprintf("cmd-%04d", idx),
			EventIndex:  idx,
			SessionSlot: idx,
		})
	}
	result := map[string]any{}
	attachMQTTIOTotals(result, totals)
	events, ok := result["failure_events"].([]sustainedFailureEvent)
	if !ok {
		t.Fatalf("failure_events missing or wrong type: %#v", result["failure_events"])
	}
	if len(events) != maxFailureEvents {
		t.Fatalf("failure_events len = %d, want %d", len(events), maxFailureEvents)
	}
	if events[0].DeviceID != "rtk-0000" || events[len(events)-1].DeviceID != fmt.Sprintf("rtk-%04d", maxFailureEvents-1) {
		t.Fatalf("failure_events should keep the first bounded samples: %#v", events)
	}
}

func TestAttachMQTTIOTotalsIncludesConnectionPhaseCounters(t *testing.T) {
	totals := mqttIOTotals{
		ConnectAttempts:            3,
		ConnectFailures:            1,
		DeviceTokenAttempts:        3,
		DeviceTokenSuccesses:       2,
		DeviceTokenFailures:        1,
		DeviceTokenFirstSuccess:    1,
		DeviceTokenFirstFailures:   2,
		DeviceTokenRetryAttempts:   2,
		DeviceTokenRetrySuccesses:  1,
		DeviceTokenRetryExhausted:  1,
		DeviceMQTTDialAttempts:     2,
		DeviceMQTTDialSuccesses:    2,
		DeviceMQTTDialFailures:     0,
		DeviceMQTTConnackAttempts:  2,
		DeviceMQTTConnackSuccesses: 1,
		DeviceMQTTConnackFailures:  1,
		DeviceSubscribeAttempts:    1,
		SubscribeSuccesses:         1,
		AppTokenAttempts:           2,
		AppTokenSuccesses:          1,
		AppTokenFailures:           1,
		AppMQTTDialAttempts:        1,
		AppMQTTDialSuccesses:       1,
		AppMQTTConnackAttempts:     1,
		AppMQTTConnackSuccesses:    1,
	}
	result := map[string]any{}
	attachMQTTIOTotals(result, totals)
	deviceTotals, ok := result["device_mqtt_totals"].(map[string]any)
	if !ok {
		t.Fatalf("device_mqtt_totals missing: %#v", result)
	}
	appTotals, ok := result["app_user_totals"].(map[string]any)
	if !ok {
		t.Fatalf("app_user_totals missing: %#v", result)
	}
	if deviceTotals["token_attempts"] != int64(3) ||
		deviceTotals["token_first_attempt_success"] != int64(1) ||
		deviceTotals["token_first_attempt_fail"] != int64(2) ||
		deviceTotals["token_retry_attempts"] != int64(2) ||
		deviceTotals["token_retry_success"] != int64(1) ||
		deviceTotals["token_retry_exhausted"] != int64(1) ||
		deviceTotals["mqtt_connack_fail"] != int64(1) ||
		deviceTotals["subscribe_attempts"] != int64(1) {
		t.Fatalf("device phase counters missing: %#v", deviceTotals)
	}
	if appTotals["token_attempts"] != int64(2) || appTotals["mqtt_dial_success"] != int64(1) || appTotals["mqtt_connack_success"] != int64(1) {
		t.Fatalf("app phase counters missing: %#v", appTotals)
	}
}

func TestNormalizeSustainedFailureDetailsPreservesConnectionPhase(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want string
	}{
		{
			name: "device token deadline",
			err:  "device request_token: Post \"https://device.example/request_token\": context deadline exceeded",
			want: "device request_token context deadline exceeded",
		},
		{
			name: "mqtt tls dial timeout",
			err:  "mqtt dial: mqtt tls dial: dial tcp 203.0.113.10:8883: i/o timeout",
			want: "mqtt tls dial timeout",
		},
		{
			name: "app mqtt tls dial includes safe endpoint and timeout",
			err:  "mqtt dial: mqtt tls dial host=172.238.59.219 port=8883 timeout=4.5s: dial tcp 172.238.59.219:8883: i/o timeout",
			want: "mqtt dial: mqtt tls dial host=172.238.59.219 port=8883 timeout=4.5s: dial tcp 172.238.59.219:8883: i/o timeout",
		},
		{
			name: "app token includes safe base URL and timeout",
			err:  `app request_token base_url=https://video-cloud-staging.realtekconnect.com timeout=4.5s: Post "https://video-cloud-staging.realtekconnect.com/request_token": context deadline exceeded`,
			want: "app request_token base_url=https://video-cloud-staging.realtekconnect.com timeout=4.5s: context deadline exceeded",
		},
		{
			name: "mqtt connack timeout",
			err:  "mqtt connack read: read tcp 192.0.2.10:40000->203.0.113.10:8883: i/o timeout",
			want: "mqtt connack read failed",
		},
		{
			name: "mqtt connect write",
			err:  "mqtt connect write: write tcp 192.0.2.10:40000->203.0.113.10:8883: broken pipe",
			want: "mqtt connect write failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeFailureDetail(tt.err); got != tt.want {
				t.Fatalf("normalizeFailureDetail(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestSustainedDeviceReaderDispatchesMatchingDeltaAfterUnrelatedPublish(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	reader := startSustainedDeviceReader(client)
	defer reader.Close()
	topic := "$vc/devices/rtk-0041/shadow/update/delta"
	go func() {
		_ = mqttWritePacket(server, 0x30, append(mqttString(topic), []byte(`{"clientToken":"other"}`)...))
		_ = mqttWritePacket(server, 0x30, append(mqttString(topic), []byte(`{"clientToken":"cmd-1"}`)...))
	}()

	doc, err := reader.WaitForPublish(topic, 500*time.Millisecond, func(doc map[string]any) bool {
		return doc["clientToken"] == "cmd-1"
	})
	if err != nil {
		t.Fatalf("WaitForPublish() error = %v", err)
	}
	if doc["clientToken"] != "cmd-1" {
		t.Fatalf("clientToken = %#v, want cmd-1", doc["clientToken"])
	}
}

func TestConnectFailureReasonPreservesRequestTokenPhaseAtWindowExpiry(t *testing.T) {
	deadline := time.Now().Add(-time.Second)

	reason := connectFailureReason(errors.New(`device request_token: Post "https://device.example/request_token": context deadline exceeded`), deadline)

	if reason != "device_request_token_failed" {
		t.Fatalf("reason = %q, want device_request_token_failed", reason)
	}
}

func TestConnectFailureReasonStillReportsWindowExpiryForMQTTAtDeadline(t *testing.T) {
	deadline := time.Now().Add(-time.Second)

	reason := connectFailureReason(errors.New(`mqtt connect write: i/o timeout`), deadline)

	if reason != "connect_window_expired" {
		t.Fatalf("reason = %q, want connect_window_expired", reason)
	}
}

func TestParseSustainedStagesRequiresMonotonicTargets(t *testing.T) {
	stages, err := parseSustainedStages(loadOptions{
		StageNames:            "window-a,window-b,window-c,window-d",
		StageConnectedDevices: "2500,5000,7500,10000",
		StageDurationsSeconds: "75,75,75,75",
	})
	if err != nil {
		t.Fatalf("parseSustainedStages() error = %v", err)
	}
	if len(stages) != 4 || stages[0].ConnectedTarget != 2500 || stages[3].ConnectedTarget != 10000 {
		t.Fatalf("unexpected stages: %#v", stages)
	}
	if _, err := parseSustainedStages(loadOptions{
		StageNames:            "window-b,window-a",
		StageConnectedDevices: "5000,2500",
		StageDurationsSeconds: "75,75",
	}); err == nil {
		t.Fatal("expected decreasing stage target to fail")
	}
}

func TestSustainedStageResultsJSONIncludesStageDiagnostics(t *testing.T) {
	rows := sustainedStageResultsJSON([]sustainedStageResult{{
		Name:              "target",
		ConnectedTarget:   2500,
		ActiveConnections: 1000,
		Status:            "FAIL",
		Diagnostics: sustainedStageDiagnostics{
			ConnectedTarget:  2500,
			ConnectedBefore:  0,
			ConnectedAfter:   1000,
			NewAssignments:   2500,
			ConnectAttempts:  1200,
			ConnectSuccesses: 1000,
			ConnectFailures:  200,
			SkipReason:       "device_connect_target_missed",
		},
	}}, appBootstrapStatus{})
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1", len(rows))
	}
	diag, ok := rows[0]["stage_diagnostics"].(sustainedStageDiagnostics)
	if !ok {
		t.Fatalf("stage_diagnostics type = %T, want sustainedStageDiagnostics", rows[0]["stage_diagnostics"])
	}
	if diag.SkipReason != "device_connect_target_missed" || diag.ConnectAttempts != 1200 {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}
}

func TestParseSustainedStagesIncludesMinimumCommands(t *testing.T) {
	stages, err := parseSustainedStages(loadOptions{
		StageNames:            "25pct,50pct",
		StageConnectedDevices: "450,900",
		StageDurationsSeconds: "75,75",
		StageMinCommands:      "23,45",
	})
	if err != nil {
		t.Fatalf("parseSustainedStages: %v", err)
	}
	if stages[0].MinCommands != 23 || stages[1].MinCommands != 45 {
		t.Fatalf("min commands = %d/%d, want 23/45", stages[0].MinCommands, stages[1].MinCommands)
	}
}

func TestUserCommandScheduleHonorsMinimumCommands(t *testing.T) {
	offsets := userCommandScheduleWithMin(900, 1, 50*time.Second, 1234, 68)
	if len(offsets) < 68 {
		t.Fatalf("offset count = %d, want at least 68", len(offsets))
	}
	for idx, offset := range offsets {
		if offset <= 0 || offset >= 50*time.Second {
			t.Fatalf("offset[%d] = %s outside command window", idx, offset)
		}
	}
}

func TestSustainedStageResultsJSONIncludesFailureEvents(t *testing.T) {
	rows := sustainedStageResultsJSON([]sustainedStageResult{{
		Name:              "75pct",
		ConnectedTarget:   6750,
		ActiveConnections: 6750,
		Status:            "FAIL",
		Totals: mqttIOTotals{
			FailureEvents: []sustainedFailureEvent{{
				Stage:       "75pct",
				Reason:      "device_delta_wait_failed",
				Detail:      "network EOF",
				Phase:       "device_delta_wait",
				DeviceID:    "rtk-0041",
				CommandID:   "cmd-0041",
				EventIndex:  61,
				SessionSlot: 61,
			}},
		},
	}}, appBootstrapStatus{})
	events, ok := rows[0]["failure_events"].([]sustainedFailureEvent)
	if !ok {
		t.Fatalf("failure_events missing or wrong type: %#v", rows[0]["failure_events"])
	}
	if len(events) != 1 || events[0].DeviceID != "rtk-0041" || events[0].EventIndex != 61 {
		t.Fatalf("unexpected failure_events: %#v", events)
	}
}

func TestStagedSustainedLoadRunsPartialShadowActionWhenTargetMissed(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceCertPEM, deviceKeyPEM, _ := testAppMaterial(t, "rtk-0041")
	appCertPEM, appKeyPEM, appCSRPEM := testAppMaterial(t, "app-user:user-1")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["scope"] == "app" {
			if got := r.Header.Get("X-Client-S-DN"); got != "/CN=app-user:user-1/O=VideoCloud" {
				t.Fatalf("X-Client-S-DN = %q, want app certificate subject", got)
			}
			writeJSON(t, w, map[string]string{"access_token": testMQTTToken("app")})
			return
		}
		writeJSON(t, w, map[string]string{"access_token": testMQTTToken("device")})
	}))
	defer tokenServer.Close()
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}

	overall, stages := runStagedSustainedHome100KLoad(
		[]assignment{{AssignedEmail: "user@example.test", DeviceID: "rtk-0041"}, {AssignedEmail: "user@example.test", DeviceID: "rtk-missing-cert"}},
		[]certRecord{{DeviceID: "rtk-0041", DeviceType: "light", CertPEM: deviceCertPEM, KeyPEM: deviceKeyPEM}},
		"RTK",
		"run-partial-shadow",
		tokenServer.URL,
		[]mqttEndpointTarget{{Host: host, Port: port}},
		map[string]*accountLoginTokenManager{"user@example.test": newAccountLoginTokenManager("", "", userCredential{
			AppCredentials: appCertificateKeys{PrivateKeyPEM: appKeyPEM, CSRPem: appCSRPEM},
			AppCertificate: appCertificateSummary{
				CertificatePEM:      appCertPEM,
				CertificateChainPEM: appCertPEM,
			},
		}, tokenBundle{AccessToken: testMQTTToken("app")})},
		20260616,
		loadOptions{Concurrency: 1, CommandRatePerDevicePerDay: "86400000"},
		[]sustainedStage{{Name: "partial", ConnectedTarget: 2, DurationSeconds: 1}},
	)
	if len(stages) != 1 {
		t.Fatalf("stages len = %d, want 1", len(stages))
	}
	stage := stages[0]
	if overall.Status != "FAIL" || stage.Status != "FAIL" {
		t.Fatalf("status overall=%s stage=%s, want FAIL due target miss", overall.Status, stage.Status)
	}
	if !stage.Diagnostics.TargetMissed || stage.Diagnostics.SkipReason != "device_connect_target_missed" {
		t.Fatalf("target miss diagnostics not preserved: %+v", stage.Diagnostics)
	}
	if stage.CommandsAttempted == 0 || stage.Totals.AppDesiredWrites == 0 || stage.Totals.ReportedEvents == 0 {
		t.Fatalf("partial shadow action did not run: commands=%d totals=%+v notes=%v", stage.CommandsAttempted, stage.Totals, stage.Notes)
	}
	if stage.ActiveConnections != 1 {
		t.Fatalf("active connections = %d, want 1", stage.ActiveConnections)
	}
}

func TestLoadLeafFirstX509KeyPairUsesInlineSQLiteBundleMaterial(t *testing.T) {
	certPEM, keyPEM, chainPEM := testAppMaterial(t, "device-1")
	cert, err := loadLeafFirstX509KeyPairForRecord(certRecord{
		DeviceID: "device-1",
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
		ChainPEM: chainPEM,
	})
	if err != nil {
		t.Fatalf("load inline cert material: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("expected certificate chain")
	}
}

func TestLoadHome100KCredentialBundleReadsGzippedSQLiteDevices(t *testing.T) {
	envRoot := t.TempDir()
	credentialsDir := filepath.Join(envRoot, "loadtests", "home-100k", "credentials")
	mkdir(t, credentialsDir)
	sqlitePath := filepath.Join(t.TempDir(), "home-100k-mixed-000.sqlite")
	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table metadata(key text primary key, value text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into metadata(key, value) values('brandname', 'RTK'), ('brand_cloud_id', 'brand-cloud-1'), ('tenant_slug', 'tenant-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table devices(device_id text primary key, device_type text not null, cert_pem text, key_pem text, chain_pem text, bundle_pem text, metadata_json text, factory_enroll_request_json text, factory_enroll_response_redacted_json text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table users(email text primary key, password text, tokens_json text, app_credentials_json text, app_certificate_json text, body_json text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table device_bindings(device_id text primary key, assignment_index integer not null, assigned_email text not null, device_type text not null, service_options_json text not null, body_json text not null)`); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, chainPEM := testAppMaterial(t, "device-1")
	if _, err := db.Exec(`insert into devices(device_id, device_type, cert_pem, key_pem, chain_pem) values(?, ?, ?, ?, ?)`, "device-1", "light", certPEM, keyPEM, chainPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into users(email, password, tokens_json, app_credentials_json, app_certificate_json, body_json) values(?, ?, ?, '{}', '{}', ?)`, "user-1@example.test", "pw", `{"access_token":"cached-access","refresh_token":"cached-refresh"}`, `{"email":"user-1@example.test","password":"pw"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into device_bindings(device_id, assignment_index, assigned_email, device_type, service_options_json, body_json) values(?, 0, ?, ?, ?, ?)`, "device-1", "user-1@example.test", "light", `["mqtt"]`, `{"assigned_email":"user-1@example.test","device_id":"device-1","device_type":"light","service_options":["mqtt"]}`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	gzipTestFile(t, sqlitePath, filepath.Join(credentialsDir, "home-100k-mixed-000.sqlite.gz"))

	bundle, err := loadHome100KCredentialBundle(envRoot)
	if err != nil {
		t.Fatalf("load credential bundle: %v", err)
	}
	device, ok := bundle.Devices["device-1"]
	if !ok {
		t.Fatalf("bundle missing device-1: %#v", bundle.Devices)
	}
	if device.CertPEM != certPEM || device.KeyPEM != keyPEM || device.ChainPEM != chainPEM {
		t.Fatalf("bundle device PEM mismatch: %#v", device)
	}
	if !bundle.HasShardTestData() {
		t.Fatalf("bundle should contain shard-scoped users and bindings: %#v", bundle)
	}
	if len(bundle.Users.Users) != 1 || bundle.Users.Users[0].Email != "user-1@example.test" {
		t.Fatalf("bundle users = %#v", bundle.Users.Users)
	}
	if bundle.Users.TenantSlug != "tenant-1" || bundle.Bind.TenantSlug != "tenant-1" {
		t.Fatalf("bundle tenant metadata users=%q bind=%q, want tenant-1", bundle.Users.TenantSlug, bundle.Bind.TenantSlug)
	}
	if got := bundle.Users.Users[0].Tokens.RefreshToken; got != "cached-refresh" {
		t.Fatalf("bundle user refresh token = %q, want cached-refresh", got)
	}
	explicit, err := loadHome100KCredentialBundleAt(envRoot, sqlitePath)
	if err != nil {
		t.Fatalf("load explicit SQLite credential bundle: %v", err)
	}
	if explicit.Source != sqlitePath || len(explicit.Bind.Assignments) != 1 || explicit.Bind.Assignments[0].DeviceID != "device-1" {
		t.Fatalf("explicit bundle = %#v", explicit)
	}
	if len(bundle.Bind.Assignments) != 1 || bundle.Bind.Assignments[0].DeviceID != "device-1" {
		t.Fatalf("bundle bindings = %#v", bundle.Bind.Assignments)
	}
}

func TestActorSeparatedProbeRecordsTraceChain(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: testMQTTToken("device"),
		AppToken:    testMQTTToken("app"),
		Dial:        broker.Dial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}

	result := runActorSeparatedProbe(probe)

	if result.MQTTStatus != "PASS" {
		t.Fatalf("result = %#v, want PASS", result)
	}
	if len(result.TraceChain) < 8 {
		t.Fatalf("trace chain has %d steps, want at least 8: %#v", len(result.TraceChain), result.TraceChain)
	}
	if result.TraceChain[0].Actor != "app_observer" || result.TraceChain[0].Action != "mqtt_connect" {
		t.Fatalf("first trace step = %#v, want app observer connect", result.TraceChain[0])
	}
	foundCommandAck := false
	foundDesiredState := false
	foundReportedState := false
	for _, step := range result.TraceChain {
		if step.Phase == "shadow_reported" && step.Actor == "app_observer" && step.Action == "receive" && step.Status == "PASS" {
			foundCommandAck = true
		}
		if step.Phase == "shadow_desired" && step.Actor == "app_controller" && step.Action == "publish" && strings.Contains(step.Data, "desired.power=true") {
			foundDesiredState = true
		}
		if step.Phase == "shadow_reported" && step.Actor == "device_client" && step.Action == "publish" && strings.Contains(step.Data, "reported.power=true") {
			foundReportedState = true
		}
		if strings.Contains(strings.ToLower(step.Detail), "token") || strings.Contains(step.Detail, "BEGIN ") {
			t.Fatalf("trace detail leaked sensitive material: %#v", step)
		}
	}
	if !foundCommandAck {
		t.Fatalf("trace chain missing app observer command ack receive: %#v", result.TraceChain)
	}
	if !foundDesiredState || !foundReportedState {
		t.Fatalf("trace chain missing light desired/reported state: %#v", result.TraceChain)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gzipTestFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatal(err)
	}
}

func newAppTokenServer(t *testing.T, wantSubject string) (*httptest.Server, *bool) {
	t.Helper()
	sawClientCert := false
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			t.Fatal("missing app client certificate")
		}
		if got := r.TLS.PeerCertificates[0].Subject.CommonName; got != wantSubject {
			t.Fatalf("client certificate subject = %q, want %q", got, wantSubject)
		}
		sawClientCert = true
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode app token request: %v", err)
		}
		deviceID, _ := body["devid"].(string)
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token-" + deviceID})
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	return server, &sawClientCert
}

func fixedProbeTime() time.Time {
	return time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC)
}

func testJWT(t *testing.T, issuedAt time.Time, expiresAt time.Time) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]int64{"iat": issuedAt.Unix(), "exp": expiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

type fakeMQTTBroker struct {
	t                      *testing.T
	listener               net.Listener
	mu                     sync.Mutex
	subscribers            map[string][]net.Conn
	clientNames            map[net.Conn]string
	keepAlives             map[string]uint16
	publishCounts          map[string]int
	publishPayloads        map[string][][]byte
	shadowStates           map[string]map[string]any
	RejectUsername         string
	RejectForbiddenTopics  bool
	AllowAWSNamespace      bool
	SuppressShadowAccepted bool
}

func newFakeMQTTBroker(t *testing.T) *fakeMQTTBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return newFakeMQTTBrokerWithListener(t, ln)
}

func newFakeTLSMQTTBroker(t *testing.T) *fakeMQTTBroker {
	t.Helper()
	certPEM, keyPEM, _ := testAppMaterial(t, "mqtt.test")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	return newFakeMQTTBrokerWithListener(t, ln)
}

func newFakeMQTTBrokerWithListener(t *testing.T, ln net.Listener) *fakeMQTTBroker {
	t.Helper()
	broker := &fakeMQTTBroker{
		t:               t,
		listener:        ln,
		subscribers:     map[string][]net.Conn{},
		clientNames:     map[net.Conn]string{},
		keepAlives:      map[string]uint16{},
		publishCounts:   map[string]int{},
		publishPayloads: map[string][][]byte{},
		shadowStates:    map[string]map[string]any{},
	}
	go broker.serve()
	return broker
}

func (b *fakeMQTTBroker) Close() {
	_ = b.listener.Close()
}

func (b *fakeMQTTBroker) Dial() (io.ReadWriteCloser, error) {
	return net.Dial("tcp", b.listener.Addr().String())
}

func (b *fakeMQTTBroker) TLSDial() (io.ReadWriteCloser, error) {
	return tls.Dial("tcp", b.listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
}

func (b *fakeMQTTBroker) PublishCount(actor, topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishCounts[actor+"\x00"+topic]
}

func (b *fakeMQTTBroker) PublishPayloads(actor, topic string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := actor + "\x00" + topic
	payloads := make([][]byte, 0, len(b.publishPayloads[key]))
	for _, payload := range b.publishPayloads[key] {
		payloads = append(payloads, append([]byte(nil), payload...))
	}
	return payloads
}

func (b *fakeMQTTBroker) KeepAlive(clientID string) uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keepAlives[clientID]
}

func TestConnectSustainedDevicesUntilReturnsWhenDeadlineAlreadyExpired(t *testing.T) {
	assignments := make([]assignment, 100)
	for idx := range assignments {
		assignments[idx] = assignment{DeviceID: fmt.Sprintf("device-%03d", idx)}
	}
	done := make(chan []sustainedDeviceSession, 1)
	go func() {
		var totals mqttIOTotals
		done <- connectSustainedDevicesUntil(assignments, nil, "RTK", "run-deadline", "http://127.0.0.1:1", []mqttEndpointTarget{{Host: "127.0.0.1", Port: 1}}, 32, time.Now().Add(-time.Millisecond), &totals)
	}()
	select {
	case sessions := <-done:
		if len(sessions) != 0 {
			t.Fatalf("sessions = %d, want 0", len(sessions))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("connectSustainedDevicesUntil deadlocked after expired deadline")
	}
}

func TestConnectSustainedDevicesUntilReturnsWhenDeadlineExpiresDuringDispatch(t *testing.T) {
	assignments := make([]assignment, 10000)
	for idx := range assignments {
		assignments[idx] = assignment{DeviceID: fmt.Sprintf("device-%05d", idx)}
	}
	done := make(chan []sustainedDeviceSession, 1)
	go func() {
		var totals mqttIOTotals
		done <- connectSustainedDevicesUntil(assignments, nil, "RTK", "run-deadline", "http://127.0.0.1:1", []mqttEndpointTarget{{Host: "127.0.0.1", Port: 1}}, 1, time.Now().Add(10*time.Millisecond), &totals)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("connectSustainedDevicesUntil deadlocked when deadline expired during dispatch")
	}
}

func TestStagedConnectDeadlineReservesActionWindow(t *testing.T) {
	start := time.Unix(1000, 0)
	deadline := start.Add(75 * time.Second)
	got := stagedConnectDeadline(start, deadline)
	if got.Sub(start) != 37500*time.Millisecond {
		t.Fatalf("connect deadline offset = %s, want 37.5s", got.Sub(start))
	}

	longDeadline := start.Add(10 * time.Minute)
	got = stagedConnectDeadline(start, longDeadline)
	if longDeadline.Sub(got) != 90*time.Second {
		t.Fatalf("long stage action reserve = %s, want 90s", longDeadline.Sub(got))
	}
}

func TestConnectDispatchDelaySpreadsAcrossRampWindow(t *testing.T) {
	window := 10 * time.Minute

	if got := connectDispatchDelay(0, 20000, window); got != 0 {
		t.Fatalf("first dispatch delay = %s, want 0", got)
	}
	if got := connectDispatchDelay(10000, 20000, window); got != 5*time.Minute {
		t.Fatalf("mid dispatch delay = %s, want 5m", got)
	}
	if got := connectDispatchDelay(19999, 20000, window); got <= 0 || got >= window {
		t.Fatalf("last dispatch delay = %s, want positive delay before %s", got, window)
	}
}

func TestConnectDispatchWindowReservesTokenRetryBudget(t *testing.T) {
	window := 10 * time.Minute
	got := connectDispatchWindow(window, tokenRequestOptions{Timeout: 15 * time.Second, Retries: 2})
	want := window - 45*time.Second - 300*time.Millisecond
	if got != want {
		t.Fatalf("dispatch window = %s, want %s", got, want)
	}

	if got := connectDispatchWindow(10*time.Second, tokenRequestOptions{Timeout: 15 * time.Second, Retries: 2}); got != 5*time.Second {
		t.Fatalf("short dispatch window = %s, want 5s", got)
	}
}

func TestParsePositiveDurationRejectsEmptyAndInvalidValues(t *testing.T) {
	if got := parsePositiveDuration("10m"); got != 10*time.Minute {
		t.Fatalf("duration = %s, want 10m", got)
	}
	for _, raw := range []string{"", "0", "off", "-1s", "bogus"} {
		if got := parsePositiveDuration(raw); got != 0 {
			t.Fatalf("duration %q = %s, want 0", raw, got)
		}
	}
}

func TestDesiredWriteRemainingBudgetScalesForShortDebugStages(t *testing.T) {
	if got := desiredWriteRemainingBudget(75*time.Second, 0); got != 15*time.Second {
		t.Fatalf("75s stage budget = %s, want 15s", got)
	}
	if got := desiredWriteRemainingBudget(1*time.Second, 0); got != 250*time.Millisecond {
		t.Fatalf("1s stage budget = %s, want 250ms", got)
	}
	if got := desiredWriteRemainingBudget(150*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("150s stage budget = %s, want configured 30s command timeout", got)
	}
	if got := desiredWriteRemainingBudget(10*time.Second, 30*time.Second); got != 5*time.Second {
		t.Fatalf("10s stage budget = %s, want half-stage cap 5s", got)
	}
}

func TestTimeoutUntilDeadlineBoundsCommandPhases(t *testing.T) {
	got, err := timeoutUntilDeadline(time.Time{}, 10*time.Second, "phase")
	if err != nil || got != 10*time.Second {
		t.Fatalf("zero deadline timeout=%s err=%v, want 10s nil", got, err)
	}
	got, err = timeoutUntilDeadline(time.Now().Add(200*time.Millisecond), 10*time.Second, "phase")
	if err != nil {
		t.Fatalf("near deadline returned error: %v", err)
	}
	if got <= 0 || got > 250*time.Millisecond {
		t.Fatalf("near deadline timeout = %s, want bounded remaining duration", got)
	}
	if _, err := timeoutUntilDeadline(time.Now().Add(-time.Millisecond), 10*time.Second, "app_mqtt_connect"); err == nil || !strings.Contains(err.Error(), "app_mqtt_connect") {
		t.Fatalf("expired deadline err = %v, want phase-specific error", err)
	}
}

func TestShadowCommandTimeoutUsesConfiguredDuration(t *testing.T) {
	if got := shadowCommandTimeout(loadOptions{ShadowCommandTimeout: "30s"}); got != 30*time.Second {
		t.Fatalf("shadowCommandTimeout = %s, want 30s", got)
	}
	if got := shadowCommandTimeout(loadOptions{ShadowCommandTimeout: "bogus"}); got != 10*time.Second {
		t.Fatalf("invalid shadowCommandTimeout = %s, want 10s fallback", got)
	}
}

func TestSustainedCommandConcurrencyIsIndependentFromMQTTConcurrency(t *testing.T) {
	opts := loadOptions{Concurrency: 1000, CommandConcurrency: 100}
	if got := sustainedCommandConcurrency(opts, 20000); got != 100 {
		t.Fatalf("sustainedCommandConcurrency = %d, want 100", got)
	}
	if got := sustainedCommandConcurrency(loadOptions{Concurrency: 1000, CommandConcurrency: 100}, 40); got != 40 {
		t.Fatalf("sustainedCommandConcurrency capped by sessions = %d, want 40", got)
	}
	if got := sustainedCommandConcurrency(loadOptions{Concurrency: 7}, 100); got != 7 {
		t.Fatalf("default sustainedCommandConcurrency = %d, want MQTT concurrency fallback", got)
	}
}

func (b *fakeMQTTBroker) serve() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		go b.handle(conn)
	}
}

func (b *fakeMQTTBroker) handle(conn net.Conn) {
	defer conn.Close()
	for {
		packetType, body, err := mqttReadPacket(conn)
		if err != nil {
			return
		}
		switch packetType >> 4 {
		case 1:
			clientID, username, keepAlive, ok := decodeMQTTConnectForTest(body)
			if !ok {
				return
			}
			if username == b.RejectUsername {
				_ = mqttWritePacket(conn, 0x20, []byte{0, 5})
				return
			}
			b.mu.Lock()
			b.clientNames[conn] = actorNameForClientID(clientID)
			b.keepAlives[clientID] = keepAlive
			b.mu.Unlock()
			_ = mqttWritePacket(conn, 0x20, []byte{0, 0})
		case 8:
			packetID, topic, ok := decodeMQTTSubscribeForTest(body)
			if !ok {
				return
			}
			b.mu.Lock()
			rejectTopic := b.RejectForbiddenTopics && (strings.Contains(topic, "-forbidden/") || (strings.HasPrefix(topic, "$aws/") && !b.AllowAWSNamespace))
			b.mu.Unlock()
			if rejectTopic {
				_ = mqttWritePacket(conn, 0x90, []byte{byte(packetID >> 8), byte(packetID), 0x80})
				continue
			}
			b.mu.Lock()
			b.subscribers[topic] = append(b.subscribers[topic], conn)
			b.mu.Unlock()
			_ = mqttWritePacket(conn, 0x90, []byte{byte(packetID >> 8), byte(packetID), 0})
		case 3:
			packetID := mqttPublishPacketIDForTest(packetType&0x0f, body)
			topic, payload, err := mqttDecodePublish(packetType&0x0f, body)
			if err != nil {
				return
			}
			b.mu.Lock()
			actor := b.clientNames[conn]
			key := actor + "\x00" + topic
			b.publishCounts[key]++
			b.publishPayloads[key] = append(b.publishPayloads[key], append([]byte(nil), payload...))
			targets := append([]net.Conn(nil), b.subscribers[topic]...)
			b.mu.Unlock()
			for _, target := range targets {
				_ = mqttPublish(target, topic, payload)
			}
			b.publishShadowResponses(topic, payload)
			if packetID > 0 {
				_ = mqttWritePacket(conn, 0x40, []byte{byte(packetID >> 8), byte(packetID)})
			}
		case 12:
			_ = mqttWritePacket(conn, 0xd0, nil)
		default:
			return
		}
	}
}

func mqttPublishPacketIDForTest(flags byte, body []byte) uint16 {
	qos := (flags >> 1) & 0x03
	if qos == 0 || len(body) < 2 {
		return 0
	}
	topicLen := int(binary.BigEndian.Uint16(body[:2]))
	pos := 2 + topicLen
	if len(body) < pos+2 {
		return 0
	}
	return binary.BigEndian.Uint16(body[pos : pos+2])
}

func (b *fakeMQTTBroker) publishShadowResponses(topic string, payload []byte) {
	if !strings.HasPrefix(topic, "$vc/devices/") {
		return
	}
	if strings.HasSuffix(topic, "/shadow/get") {
		var req struct {
			Token string `json:"clientToken"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return
		}
		deviceID := strings.TrimSuffix(strings.TrimPrefix(topic, "$vc/devices/"), "/shadow/get")
		b.mu.Lock()
		state := b.shadowStates[deviceID]
		if state == nil {
			state = map[string]any{"desired": map[string]any{}, "reported": map[string]any{}, "delta": map[string]any{}}
		}
		b.mu.Unlock()
		b.publishToSubscribers(topic+"/accepted", map[string]any{"clientToken": req.Token, "version": 1, "state": state})
		return
	}
	if !strings.HasSuffix(topic, "/shadow/update") {
		return
	}
	var req struct {
		State   map[string]map[string]any `json:"state"`
		Token   string                    `json:"clientToken"`
		Version *int64                    `json:"version"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	acceptedTopic := topic + "/accepted"
	rejectedTopic := topic + "/rejected"
	documentsTopic := topic + "/documents"
	deltaTopic := topic + "/delta"
	deviceID := strings.TrimSuffix(strings.TrimPrefix(topic, "$vc/devices/"), "/shadow/update")
	if req.Version != nil && *req.Version == 0 {
		b.publishToSubscribers(rejectedTopic, map[string]any{"clientToken": req.Token, "code": float64(http.StatusConflict)})
		return
	}
	accepted := map[string]any{"clientToken": req.Token, "version": 1, "state": req.State}
	if !b.SuppressShadowAccepted {
		b.publishToSubscribers(acceptedTopic, accepted)
	}
	if desired := req.State["desired"]; len(desired) > 0 {
		b.mu.Lock()
		state := b.shadowStates[deviceID]
		if state == nil {
			state = map[string]any{"reported": map[string]any{}}
			b.shadowStates[deviceID] = state
		}
		state["desired"] = desired
		state["delta"] = desired
		b.mu.Unlock()
		b.publishToSubscribers(deltaTopic, map[string]any{"clientToken": req.Token, "version": 1, "state": map[string]any{"delta": desired}})
		b.publishToSubscribers(documentsTopic, map[string]any{
			"clientToken": req.Token,
			"version":     1,
			"current": map[string]any{"state": map[string]any{
				"desired":  desired,
				"reported": map[string]any{},
				"delta":    desired,
			}},
		})
		return
	}
	if reported := req.State["reported"]; len(reported) > 0 {
		b.mu.Lock()
		state := b.shadowStates[deviceID]
		if state == nil {
			state = map[string]any{}
			b.shadowStates[deviceID] = state
		}
		state["reported"] = reported
		state["desired"] = reported
		state["delta"] = map[string]any{}
		b.mu.Unlock()
		b.publishToSubscribers(documentsTopic, map[string]any{
			"clientToken": req.Token,
			"version":     2,
			"current": map[string]any{"state": map[string]any{
				"desired":  reported,
				"reported": reported,
				"delta":    map[string]any{},
			}},
		})
	}
}

func (b *fakeMQTTBroker) publishToSubscribers(topic string, doc map[string]any) {
	payload, err := json.Marshal(doc)
	if err != nil {
		return
	}
	b.mu.Lock()
	targets := append([]net.Conn(nil), b.subscribers[topic]...)
	b.mu.Unlock()
	for _, target := range targets {
		_ = mqttPublish(target, topic, payload)
	}
}

func decodeMQTTConnectForTest(body []byte) (clientID, username string, keepAlive uint16, ok bool) {
	pos := 0
	if _, next, ok := readMQTTStringForTest(body, pos); !ok {
		return "", "", 0, false
	} else {
		pos = next
	}
	if len(body) < pos+4 {
		return "", "", 0, false
	}
	flags := body[pos+1]
	keepAlive = uint16(body[pos+2])<<8 | uint16(body[pos+3])
	pos += 4
	clientID, pos, ok = readMQTTStringForTest(body, pos)
	if !ok {
		return "", "", 0, false
	}
	if flags&0x80 != 0 {
		username, _, ok = readMQTTStringForTest(body, pos)
		if !ok {
			return "", "", 0, false
		}
	}
	return clientID, username, keepAlive, true
}

func decodeMQTTSubscribeForTest(body []byte) (uint16, string, bool) {
	if len(body) < 5 {
		return 0, "", false
	}
	packetID := uint16(body[0])<<8 | uint16(body[1])
	topic, _, ok := readMQTTStringForTest(body, 2)
	return packetID, topic, ok
}

func readMQTTStringForTest(body []byte, pos int) (string, int, bool) {
	if len(body) < pos+2 {
		return "", 0, false
	}
	size := int(body[pos])<<8 | int(body[pos+1])
	start := pos + 2
	end := start + size
	if len(body) < end {
		return "", 0, false
	}
	return string(body[start:end]), end, true
}

func actorNameForClientID(clientID string) string {
	switch {
	case strings.Contains(clientID, "app-observer"):
		return "app-observer"
	case strings.Contains(clientID, "app-controller"):
		return "app-controller"
	case strings.Contains(clientID, "device"):
		return "device"
	default:
		return clientID
	}
}

func testAppMaterial(t *testing.T, subject string) (certPEM, keyPEM, csrPEM string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, key)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: subject},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
}

func issueCertificateForCSR(t *testing.T, csrPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		t.Fatal("missing CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      csr.Subject,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, csr.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
}

func TestParseMQTTEndpointTargetsAcceptsCommaSeparatedLoadBalancers(t *testing.T) {
	targets := parseMQTTEndpointTargets(" 172.238.1.10:8883, mqtt-a.example.test:8883, bad-value,172.238.1.10:8883 ")

	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2: %#v", len(targets), targets)
	}
	if targets[0] != (mqttEndpointTarget{Host: "172.238.1.10", Port: 8883}) {
		t.Fatalf("target[0] = %#v", targets[0])
	}
	if targets[1] != (mqttEndpointTarget{Host: "mqtt-a.example.test", Port: 8883}) {
		t.Fatalf("target[1] = %#v", targets[1])
	}
}

func TestSDKSimulatorDeltaAcceptsCanonicalNestedAndLegacyShapes(t *testing.T) {
	canonical := sdkSimulatorDelta(map[string]any{
		"state": map[string]any{"enabled": true},
	})
	if canonical["enabled"] != true {
		t.Fatalf("canonical delta = %#v", canonical)
	}
	nested := sdkSimulatorDelta(map[string]any{
		"state": map[string]any{"delta": map[string]any{"power": true}},
	})
	if nested["power"] != true {
		t.Fatalf("nested delta = %#v", nested)
	}
	legacy := sdkSimulatorDelta(map[string]any{
		"delta": map[string]any{"temperature": float64(25)},
	})
	if legacy["temperature"] != float64(25) {
		t.Fatalf("legacy delta = %#v", legacy)
	}
	if got := sdkSimulatorDelta(map[string]any{"state": "invalid"}); got != nil {
		t.Fatalf("invalid delta = %#v, want nil", got)
	}
}

func TestProbeInvalidDeviceCredentialRequiresAuthorizationDenial(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "device-1")
	assertProbeRejected := func(t *testing.T, status int, response string) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["devid"] != "device-1-invalid-binding" {
				t.Fatalf("devid = %#v", body["devid"])
			}
			http.Error(w, response, status)
		}))
		defer server.Close()
		records := map[string]certRecord{
			"device-1": {DeviceID: "device-1", CertPEM: certPEM, KeyPEM: keyPEM},
		}
		if err := probeInvalidDeviceCredential([]assignment{{DeviceID: "device-1"}}, records, server.URL, tokenRequestOptions{Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("authorization denial", func(t *testing.T) {
		assertProbeRejected(t, http.StatusForbidden, `{"code":"DEVICE_CERTIFICATE_BINDING_MISMATCH"}`)
	})
	t.Run("certificate identity denial", func(t *testing.T) {
		assertProbeRejected(t, http.StatusBadRequest, `{"status":"fail","reason":"certificate device id mismatch"}`)
	})

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	records := map[string]certRecord{
		"device-1": {DeviceID: "device-1", CertPEM: certPEM, KeyPEM: keyPEM},
	}
	if err := probeInvalidDeviceCredential([]assignment{{DeviceID: "device-1"}}, records, unavailable.URL, tokenRequestOptions{Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "expected authorization or certificate-identity denial") {
		t.Fatalf("unavailable probe err = %v, want non-auth failure", err)
	}
}

func TestSDKDeviceSimulatorMatchesHome100KShadowDeviceContract(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceCertPEM, deviceKeyPEM, _ := testAppMaterial(t, "device-parity")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]string{"access_token": testMQTTToken("device")})
	}))
	defer tokenServer.Close()
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	topic := "$vc/devices/device-parity/shadow/update"
	readyFile := filepath.Join(t.TempDir(), "ready.json")
	resultCh := make(chan sustainedLoadResult, 1)
	go func() {
		resultCh <- runSDKDeviceSimulator(
			[]assignment{{DeviceID: "device-parity", DeviceType: "light"}},
			[]certRecord{{DeviceID: "device-parity", DeviceType: "light", CertPEM: deviceCertPEM, KeyPEM: deviceKeyPEM}},
			"RTK", "run-sdk-parity", tokenServer.URL,
			[]mqttEndpointTarget{{Host: host, Port: port}}, 1,
			loadOptions{Concurrency: 1, ReadyFile: readyFile},
		)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sdk simulator did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	appConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID: "device-parity", Brandname: "RTK", RunID: "run-sdk-parity",
		AppToken: testMQTTToken("app"), Dial: broker.TLSDial, Timeout: time.Second, Now: fixedProbeTime,
	}, "app-controller", testMQTTToken("app"))
	if err != nil {
		t.Fatal(err)
	}
	desiredPayload, err := json.Marshal(map[string]any{
		"state": map[string]any{"desired": map[string]any{"power": true}}, "clientToken": "sdk-parity",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mqttPublish(appConn, topic, desiredPayload); err != nil {
		t.Fatal(err)
	}
	_ = appConn.Close()
	sdkResult := <-resultCh
	if sdkResult.Status != "PASS" || sdkResult.CommandsPassed != 1 || sdkResult.Totals.DeltaReceived != 1 || sdkResult.Totals.ReportedEvents != 1 {
		t.Fatalf("sdk device result = %+v", sdkResult)
	}

	homeDeviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID: "device-parity", DeviceType: "light", Brandname: "RTK", RunID: "run-home-parity",
		DeviceToken: testMQTTToken("device"), Dial: broker.TLSDial, Timeout: time.Second, Now: fixedProbeTime,
	}, "device", testMQTTToken("device"))
	if err != nil {
		t.Fatal(err)
	}
	defer homeDeviceConn.Close()
	if err := mqttSubscribe(homeDeviceConn, 10, topic+"/delta"); err != nil {
		t.Fatal(err)
	}
	reader := startSustainedDeviceReader(homeDeviceConn)
	defer reader.Close()
	var homeTotals mqttIOTotals
	if err := runSustainedShadowCommandWithContext(sustainedDeviceSession{
		Record: certRecord{DeviceID: "device-parity", DeviceType: "light"}, Conn: homeDeviceConn, Reader: reader,
		MQTTTarget:      mqttEndpointTarget{Host: host, Port: port},
		AppLoginManager: newAccountLoginTokenManager("", "", userCredential{}, tokenBundle{AccessToken: testMQTTToken("app")}),
	}, "RTK", "run-home-parity", "", &homeTotals, sustainedCommandContext{DisableRuntimeLogs: true}); err != nil {
		t.Fatal(err)
	}
	if homeTotals.DeltaReceived != 1 || homeTotals.ReportedEvents != 1 || homeTotals.AppReceivedAcks != 1 {
		t.Fatalf("home-1k device totals = %+v", homeTotals)
	}

	reportedPayloads := broker.PublishPayloads("device", topic)
	if len(reportedPayloads) < 2 {
		t.Fatalf("reported payloads = %d, want SDK and home-1k payloads", len(reportedPayloads))
	}
	for idx, payload := range reportedPayloads[len(reportedPayloads)-2:] {
		var doc struct {
			State map[string]map[string]any `json:"state"`
			Token string                    `json:"clientToken"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.State["reported"]) == 0 || !strings.HasPrefix(doc.Token, "reported-") {
			t.Fatalf("reported payload[%d] = %#v, want shared reported-state contract", idx, doc)
		}
	}
}

func TestSDKDeviceSimulatorResyncsShadowAfterOfflineReconnect(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	deviceCertPEM, deviceKeyPEM, _ := testAppMaterial(t, "device-reconnect")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]string{"access_token": testMQTTToken("device")})
	}))
	defer tokenServer.Close()
	host, rawPort, err := net.SplitHostPort(broker.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	readyFile := filepath.Join(tmp, "ready.json")
	reconnectSignal := filepath.Join(tmp, "reconnect.signal")
	resultCh := make(chan sustainedLoadResult, 1)
	go func() {
		resultCh <- runSDKDeviceSimulator(
			[]assignment{{DeviceID: "device-reconnect", DeviceType: "light"}},
			[]certRecord{{DeviceID: "device-reconnect", DeviceType: "light", CertPEM: deviceCertPEM, KeyPEM: deviceKeyPEM}},
			"RTK", "run-sdk-reconnect", tokenServer.URL,
			[]mqttEndpointTarget{{Host: host, Port: port}}, 3,
			loadOptions{Concurrency: 1, ReadyFile: readyFile, SDKReconnectSignalFile: reconnectSignal},
		)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sdk reconnect simulator did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	disconnectRequest := filepath.Join(tmp, "offline-disconnect.request")
	if err := os.WriteFile(disconnectRequest, []byte("disconnect\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offlineReady := filepath.Join(tmp, "offline-ready")
	deadline = time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(offlineReady); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sdk reconnect simulator did not confirm offline state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	appConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID: "device-reconnect", Brandname: "RTK", RunID: "run-sdk-reconnect",
		AppToken: testMQTTToken("app"), Dial: broker.TLSDial, Timeout: time.Second, Now: fixedProbeTime,
	}, "app-controller", testMQTTToken("app"))
	if err != nil {
		t.Fatal(err)
	}
	desiredPayload, err := json.Marshal(map[string]any{
		"state": map[string]any{"desired": map[string]any{"power": true}}, "clientToken": "offline-desired",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mqttPublish(appConn, "$vc/devices/device-reconnect/shadow/update", desiredPayload); err != nil {
		t.Fatal(err)
	}
	_ = appConn.Close()
	if err := os.WriteFile(reconnectSignal, []byte("reconnect\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultCh:
		if result.Status != "PASS" || result.CommandsPassed != 1 || result.Totals.DeltaReceived != 1 || result.Totals.ReportedEvents != 1 {
			t.Fatalf("sdk reconnect result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sdk reconnect simulator did not finish")
	}
}

func TestWriteSDKDeviceReadyFileIsDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "virtual-device", "ready.json")
	sessions := []sustainedDeviceSession{
		{Record: certRecord{DeviceID: "device-b"}},
		{Record: certRecord{DeviceID: "device-a"}},
	}
	totals := mqttIOTotals{DeviceTokenSuccesses: 2, DeviceMQTTConnackSuccesses: 2, SubscribeSuccesses: 2}
	if err := writeSDKDeviceReadyFile(path, "run-1", sessions, totals); err != nil {
		t.Fatal(err)
	}
	var ready struct {
		SchemaVersion int      `json:"schema_version"`
		RunID         string   `json:"run_id"`
		Status        string   `json:"status"`
		DeviceIDs     []string `json:"device_ids"`
		Evidence      struct {
			DeviceTokenSuccesses int64 `json:"device_token_successes"`
			MQTTConnackSuccesses int64 `json:"mqtt_connack_successes"`
			SubscribeSuccesses   int64 `json:"subscribe_successes"`
		} `json:"evidence"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.SchemaVersion != 1 || ready.RunID != "run-1" || ready.Status != "READY" {
		t.Fatalf("ready identity = %#v", ready)
	}
	if strings.Join(ready.DeviceIDs, ",") != "device-a,device-b" {
		t.Fatalf("device ids = %#v", ready.DeviceIDs)
	}
	if ready.Evidence.DeviceTokenSuccesses != 2 || ready.Evidence.MQTTConnackSuccesses != 2 || ready.Evidence.SubscribeSuccesses != 2 {
		t.Fatalf("ready evidence = %#v", ready.Evidence)
	}
}

func TestReadDeviceInfoWithAppTokenRequiresMatchingSubject(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/devices/device-1/info" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer app-token" {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"status":"ok","devid":"device-1","info":{"model":"C1"}}`)
	}))
	defer server.Close()

	if err := readDeviceInfoWithAppToken(server.URL, "device-1", "app-token", cert); err != nil {
		t.Fatal(err)
	}
}

func TestReadDeviceInfoWithAppTokenRejectsMismatch(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","devid":"other-device"}`)
	}))
	defer server.Close()

	if err := readDeviceInfoWithAppToken(server.URL, "device-1", "app-token", cert); err == nil {
		t.Fatal("mismatched device info unexpectedly passed")
	}
}

func TestReadDeviceInfoWithAppTokenPresentsAppClientCertificate(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.TLS == nil || len(req.TLS.PeerCertificates) != 1 {
			t.Fatal("missing app client certificate")
		}
		if got := req.TLS.PeerCertificates[0].Subject.CommonName; got != "app-user:user-1" {
			t.Fatalf("client certificate subject = %q", got)
		}
		_, _ = io.WriteString(w, `{"status":"ok","devid":"device-1"}`)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()

	if err := readDeviceInfoWithAppToken(server.URL, "device-1", "app-token", cert); err != nil {
		t.Fatal(err)
	}
}

func TestRunSelectedDeviceProbesStopsWhenDeviceInfoFails(t *testing.T) {
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/request_token":
			writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token"})
		case "/api/devices/device-1/info":
			http.Error(w, "unavailable", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
		}
	}))
	defer server.Close()

	results := runSelectedDeviceProbes(
		[]assignment{{AssignedEmail: "user@example.test", DeviceID: "device-1", DeviceType: "camera"}},
		nil,
		"RTK",
		"run-1",
		server.URL,
		"",
		0,
		map[string]userCredential{
			"user@example.test": {
				Email: "user@example.test",
				AppCredentials: appCertificateKeys{
					PrivateKeyPEM: keyPEM,
				},
				AppCertificate: appCertificateSummary{
					CertificatePEM: certPEM,
				},
			},
		},
		1,
	)
	result := results["device-1"]
	if result.MQTTStatus != "FAIL" || result.Error != "read device info returned HTTP 500" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPrependRuntimeBootstrapTraceRenumbersEvidence(t *testing.T) {
	observed := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	steps := prependRuntimeBootstrapTrace([]traceStep{{Step: 1, Phase: "mqtt_connect", Status: "PASS"}}, observed)
	if len(steps) != 3 || steps[0].Phase != "app_token" || steps[1].Phase != "device_info" || steps[2].Phase != "mqtt_connect" {
		t.Fatalf("steps = %#v", steps)
	}
	for index, step := range steps {
		if step.Step != index+1 {
			t.Fatalf("step[%d].Step = %d", index, step.Step)
		}
	}
}

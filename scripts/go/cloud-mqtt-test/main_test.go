package main

import (
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
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
	"testing"
	"time"
)

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

func TestLatestHomeMQTTBindArtifactSkipsIncompleteLatestArtifact(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "rtk-device-bind-older.json")
	newer := filepath.Join(root, "rtk-device-bind-newer.json")
	write(t, older, `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "light", "service_options": ["mqtt"]},
    {"device_type": "air_conditioner", "service_options": ["mqtt"]},
    {"device_type": "smart_meter", "service_options": ["mqtt"]}
  ]
}`)
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
	complete := `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "light", "service_options": ["mqtt"]},
    {"device_type": "air_conditioner", "service_options": ["mqtt"]},
    {"device_type": "smart_meter", "service_options": ["mqtt"]}
  ]
}`
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
		writeJSON(t, w, map[string]string{"access_token": "device-token"})
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
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token"})
	}))
	defer server.Close()

	token, err := requestAppToken(server.URL, cert, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "app-token" || token.Scope != "app" {
		t.Fatalf("token = %#v, want app-token", token)
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
			"user": map[string]string{"id": "user-1"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       "issued",
				"certificate_chain_pem": "issued",
			},
		})
	}))
	defer account.Close()
	video, sawClientCert := newAppTokenServer(t, "app-user:user-1")
	defer video.Close()

	status := runAppCertificateBootstrap(account.URL, video.URL, "rtk-1234", userCredential{
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

	if status.Status != "PASS" || status.TokenScope != "app" {
		t.Fatalf("status = %#v, want PASS app", status)
	}
	if !*sawClientCert {
		t.Fatal("video token server did not receive an app client certificate")
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

func TestPrepareAppCertificateBootstrapForAssignmentsFallsBackToNextCandidate(t *testing.T) {
	certPEM, keyPEM, csrPEM := testAppMaterial(t, "app-user:user-1")
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"user": map[string]string{"id": "user-1"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       "issued",
				"certificate_chain_pem": "issued",
			},
		})
	}))
	defer account.Close()
	videoCalls := 0
	video := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		videoCalls++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["devid"] == "device-1" {
			http.Error(w, "try another candidate", http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token-" + body["devid"]})
	}))
	defer video.Close()

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
	material := prepareAppCertificateBootstrapForAssignments(account.URL, video.URL, "rtk-1234", map[string]userCredential{
		user.Email: user,
	}, []assignment{
		{AssignedEmail: user.Email, DeviceID: "device-1"},
		{AssignedEmail: user.Email, DeviceID: "device-2"},
	}, 10)

	if material.Status.Status != "PASS" || material.Status.DeviceID != "device-2" || material.Status.AccessToken != "app-token-device-2" {
		t.Fatalf("status = %#v, want PASS on second candidate", material.Status)
	}
	if videoCalls != 2 {
		t.Fatalf("videoCalls = %d, want 2", videoCalls)
	}
	if len(material.Status.Attempts) != 2 {
		t.Fatalf("attempts = %#v, want 2 entries", material.Status.Attempts)
	}
	if material.Status.Attempts[0].Status != "FAIL" || material.Status.Attempts[1].Status != "PASS" {
		t.Fatalf("attempts = %#v, want FAIL then PASS", material.Status.Attempts)
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
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["scope"] != "device" || body["devid"] != "device-1" || body["service"] != "mqtt" {
			t.Fatalf("body = %#v", body)
		}
		writeJSON(t, w, map[string]string{"access_token": "device-token"})
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

func TestRunAppCertificateBootstrapBlocksIssuedCertificateWithoutArtifactKey(t *testing.T) {
	certPEM, _, _ := testAppMaterial(t, "app-user:user-1")
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"user": map[string]string{"id": "user-1"},
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

	if status.Status != "BLOCKED" {
		t.Fatalf("status = %#v, want BLOCKED", status)
	}
	if status.Reason != "users artifact lacks local app credentials for issued app certificate" {
		t.Fatalf("reason = %q", status.Reason)
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
			"user": map[string]string{"id": "user-1"},
			"app_certificate": map[string]string{
				"status":                "issued",
				"subject":               "app-user:user-1",
				"certificate_pem":       certPEM,
				"certificate_chain_pem": certPEM,
			},
		})
	}))
	defer account.Close()
	video, sawClientCert := newAppTokenServer(t, "app-user:user-1")
	defer video.Close()

	status := runAppCertificateBootstrap(account.URL, video.URL, "rtk-1234", userCredential{
		Email:    "rtk+001@users.local",
		Password: "secret",
	}, "rtk-0041")

	if status.Status != "PASS" {
		t.Fatalf("status = %#v, want PASS", status)
	}
	if loginCalls != 2 {
		t.Fatalf("loginCalls = %d, want 2", loginCalls)
	}
	if !*sawClientCert {
		t.Fatal("video token server did not receive generated app client certificate")
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

func TestActorSeparatedTelemetryRequiresAppObserverReceive(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: "device-token",
		AppToken:    "app-token",
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
		DeviceToken: "device-token",
		AppToken:    "app-token",
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
		DeviceToken: "device-token",
		AppToken:    "app-token",
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
	certPEM, keyPEM, _ := testAppMaterial(t, "app-user:user-1")
	appCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token"})
	}))
	defer tokenServer.Close()

	deviceConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		RunID:       "run-sustained-logs",
		DeviceToken: "device-token",
		Dial:        broker.TLSDial,
		Timeout:     time.Second,
		Now:         fixedProbeTime,
	}, "device", "rtk-0041", "device-token")
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
	err = runSustainedShadowCommand(sustainedDeviceSession{
		Record: certRecord{DeviceID: "rtk-0041", DeviceType: "light"},
		Conn:   deviceConn,
	}, "RTK", "run-sustained-logs", tokenServer.URL, host, port, appCert, &totals)
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

func TestSustainedActorsUseLongMQTTKeepAlive(t *testing.T) {
	broker := newFakeTLSMQTTBroker(t)
	defer broker.Close()
	certPEM, keyPEM, _ := testAppMaterial(t, "rtk-0041")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"access_token": "device-token"})
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
	conn, err := connectSustainedDevice(certRecord{
		DeviceID:   "rtk-0041",
		DeviceType: "light",
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
	}, "RTK", "run-sustained-keepalive", tokenServer.URL, host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	clientID := fmt.Sprintf("rtk-e2e-run-sustained-keepalive-rtk-0041-device-%d", os.Getpid())
	if got := broker.KeepAlive(clientID); got != sustainedMQTTKeepAliveSeconds {
		t.Fatalf("sustained device keepalive = %d, want %d", got, sustainedMQTTKeepAliveSeconds)
	}
}

func TestActorSeparatedProbeFailsWhenAppMQTTAuthRejected(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	broker.RejectUsername = "app-user:rtk-0041"
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: "device-token",
		AppToken:    "app-token",
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
			"client_identity_mode": "app_token_and_device_token",
			"telemetry_receiver":   "app_observer",
			"command_receiver":     "device_client",
		},
		"devices": []deviceResult{{
			DeviceID:   "rtk-0041",
			DeviceType: "light",
			TraceChain: []traceStep{
				{Step: 1, Timestamp: "2026-06-04T08:00:00Z", Phase: "app_token", Actor: "app_actor", Action: "request_token", Status: "PASS"},
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

func TestParseSustainedStagesRequiresMonotonicTargets(t *testing.T) {
	stages, err := parseSustainedStages(loadOptions{
		StageNames:            "25k,50k,75k,100k",
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
		StageNames:            "50k,25k",
		StageConnectedDevices: "5000,2500",
		StageDurationsSeconds: "75,75",
	}); err == nil {
		t.Fatal("expected decreasing stage target to fail")
	}
}

func TestSustainedStageResultsJSONIncludesStageDiagnostics(t *testing.T) {
	rows := sustainedStageResultsJSON([]sustainedStageResult{{
		Name:              "25k",
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
	if _, err := db.Exec(`create table devices(device_id text primary key, device_type text not null, cert_pem text, key_pem text, chain_pem text, bundle_pem text, metadata_json text, factory_enroll_request_json text, factory_enroll_response_redacted_json text)`); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, chainPEM := testAppMaterial(t, "device-1")
	if _, err := db.Exec(`insert into devices(device_id, device_type, cert_pem, key_pem, chain_pem) values(?, ?, ?, ?, ?)`, "device-1", "light", certPEM, keyPEM, chainPEM); err != nil {
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
}

func TestActorSeparatedProbeRecordsTraceChain(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	probe := mqttActorProbe{
		DeviceID:    "rtk-0041",
		DeviceType:  "light",
		Brandname:   "RTK",
		DeviceToken: "device-token",
		AppToken:    "app-token",
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
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode app token request: %v", err)
		}
		writeJSON(t, w, map[string]string{"scope": "app", "access_token": "app-token-" + body["devid"]})
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	return server, &sawClientCert
}

func fixedProbeTime() time.Time {
	return time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC)
}

type fakeMQTTBroker struct {
	t               *testing.T
	listener        net.Listener
	mu              sync.Mutex
	subscribers     map[string][]net.Conn
	clientNames     map[net.Conn]string
	keepAlives      map[string]uint16
	publishCounts   map[string]int
	publishPayloads map[string][][]byte
	RejectUsername  string
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
		done <- connectSustainedDevicesUntil(assignments, nil, "RTK", "run-deadline", "http://127.0.0.1:1", "127.0.0.1", 1, 32, time.Now().Add(-time.Millisecond), &totals)
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
		done <- connectSustainedDevicesUntil(assignments, nil, "RTK", "run-deadline", "http://127.0.0.1:1", "127.0.0.1", 1, 1, time.Now().Add(10*time.Millisecond), &totals)
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
			b.subscribers[topic] = append(b.subscribers[topic], conn)
			b.mu.Unlock()
			_ = mqttWritePacket(conn, 0x90, []byte{byte(packetID >> 8), byte(packetID), 0})
		case 3:
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
		default:
			return
		}
	}
}

func (b *fakeMQTTBroker) publishShadowResponses(topic string, payload []byte) {
	if !strings.HasPrefix(topic, "$vc/devices/") || !strings.HasSuffix(topic, "/shadow/update") {
		return
	}
	var req struct {
		State map[string]map[string]any `json:"state"`
		Token string                    `json:"clientToken"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	acceptedTopic := topic + "/accepted"
	documentsTopic := topic + "/documents"
	deltaTopic := topic + "/delta"
	accepted := map[string]any{"clientToken": req.Token, "version": 1, "state": req.State}
	b.publishToSubscribers(acceptedTopic, accepted)
	if desired := req.State["desired"]; len(desired) > 0 {
		b.publishToSubscribers(deltaTopic, map[string]any{"clientToken": req.Token, "version": 1, "state": desired})
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

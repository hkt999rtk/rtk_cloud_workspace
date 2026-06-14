package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestRedactedErrorPreservesRequestTokenHTTPStatus(t *testing.T) {
	got := redactedErrorString("request_token failed with HTTP 400")
	if got != "request_token failed with HTTP 400" {
		t.Fatalf("redactedErrorString = %q", got)
	}
	if got := redactedErrorString("request_token returned secret material"); got != "redacted sensitive error" {
		t.Fatalf("redactedErrorString secret = %q", got)
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
      "private_key_pem": "-----BEGIN RSA PRIVATE KEY-----\nkey\n-----END RSA PRIVATE KEY-----",
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
		AppCredentials: appCertificateKeys{
			PrivateKeyPEM: keyPEM,
			CSRPem:        csrPEM,
		},
	}, "rtk-0041")

	if status.Status != "PASS" || status.TokenScope != "app" {
		t.Fatalf("status = %#v, want PASS app", status)
	}
	if !*sawClientCert {
		t.Fatal("video token server did not receive an app client certificate")
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
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=app-user:user-1" {
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
		if got := r.Header.Get("X-Client-S-DN"); got != "/CN=device-1" {
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
	if broker.PublishCount("app-controller", "devices/rtk-0041/down/commands") == 0 {
		t.Fatal("app controller did not publish command")
	}
	if broker.PublishCount("device", "devices/rtk-0041/up/messages") < 2 {
		t.Fatal("device did not publish telemetry and command ack on up topic")
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
		if step.Phase == "command_ack" && step.Actor == "app_observer" && step.Action == "receive" && step.Status == "PASS" {
			foundCommandAck = true
		}
		if step.Phase == "command" && step.Actor == "app_controller" && step.Action == "publish" && strings.Contains(step.Data, "desired.power=true") {
			foundDesiredState = true
		}
		if step.Phase == "command_ack" && step.Actor == "app_observer" && step.Action == "receive" && strings.Contains(step.Data, "reported.power=true") {
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
	t              *testing.T
	listener       net.Listener
	mu             sync.Mutex
	subscribers    map[string][]net.Conn
	clientNames    map[net.Conn]string
	publishCounts  map[string]int
	RejectUsername string
}

func newFakeMQTTBroker(t *testing.T) *fakeMQTTBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	broker := &fakeMQTTBroker{
		t:             t,
		listener:      ln,
		subscribers:   map[string][]net.Conn{},
		clientNames:   map[net.Conn]string{},
		publishCounts: map[string]int{},
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

func (b *fakeMQTTBroker) PublishCount(actor, topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishCounts[actor+"\x00"+topic]
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
			clientID, username, ok := decodeMQTTConnectForTest(body)
			if !ok {
				return
			}
			if username == b.RejectUsername {
				_ = mqttWritePacket(conn, 0x20, []byte{0, 5})
				return
			}
			b.mu.Lock()
			b.clientNames[conn] = actorNameForClientID(clientID)
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
			b.publishCounts[actor+"\x00"+topic]++
			targets := append([]net.Conn(nil), b.subscribers[topic]...)
			b.mu.Unlock()
			for _, target := range targets {
				_ = mqttPublish(target, topic, payload)
			}
		default:
			return
		}
	}
}

func decodeMQTTConnectForTest(body []byte) (clientID, username string, ok bool) {
	pos := 0
	if _, next, ok := readMQTTStringForTest(body, pos); !ok {
		return "", "", false
	} else {
		pos = next
	}
	if len(body) < pos+4 {
		return "", "", false
	}
	flags := body[pos+1]
	pos += 4
	clientID, pos, ok = readMQTTStringForTest(body, pos)
	if !ok {
		return "", "", false
	}
	if flags&0x80 != 0 {
		username, _, ok = readMQTTStringForTest(body, pos)
		if !ok {
			return "", "", false
		}
	}
	return clientID, username, true
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
	key, err := rsa.GenerateKey(rand.Reader, 2048)
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
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
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
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
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

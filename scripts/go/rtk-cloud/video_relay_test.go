package main

import (
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

func TestVideoRelaySelectsOnlyVideoCapableCameraDevices(t *testing.T) {
	bind := videoRelayBindArtifact{
		Brandname: "RTK",
		Assignments: []bindAssignment{
			{AssignedEmail: "u1@example.test", DeviceID: "cam-1", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming"}},
			{AssignedEmail: "u2@example.test", DeviceID: "light-1", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
			{AssignedEmail: "u3@example.test", DeviceID: "cam-storage", DeviceType: "camera", ServiceOptions: []string{"video_storage"}},
			{AssignedEmail: "u4@example.test", DeviceID: "cam-2", DeviceType: "camera", ServiceOptions: []string{"video_streaming", "video_storage"}},
		},
	}
	users := map[string]videoRelayUser{
		"u1@example.test": {Email: "u1@example.test", AppCredentials: videoRelayAppCredentials{PrivateKeyPEM: "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----", CSRPem: "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----"}, AppCertificate: videoRelayAppCertificate{CertificateChainPEM: "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----"}},
		"u4@example.test": {Email: "u4@example.test", AppCredentials: videoRelayAppCredentials{PrivateKeyPEM: "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----", CSRPem: "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----"}, AppCertificate: videoRelayAppCertificate{CertificateChainPEM: "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----"}},
	}
	manifest := map[string]videoRelayDeviceManifest{
		"cam-1": {DeviceID: "cam-1", CertificatePath: "devices/camera/cam-1/device.cert.pem", KeyPath: "devices/camera/cam-1/device.key.pem"},
		"cam-2": {DeviceID: "cam-2", CertificatePath: "devices/camera/cam-2/device.cert.pem", KeyPath: "devices/camera/cam-2/device.key.pem"},
	}

	selected, blockers := selectVideoRelayDevices(bind, users, manifest, 3)
	if len(blockers) != 0 {
		t.Fatalf("blockers = %v, want none", blockers)
	}
	if got := deviceIDs(selected); strings.Join(got, ",") != "cam-1,cam-2" {
		t.Fatalf("selected device ids = %v, want cam-1,cam-2", got)
	}
}

func TestVideoRelaySelectionBlocksWithoutVideoDevices(t *testing.T) {
	selected, blockers := selectVideoRelayDevices(videoRelayBindArtifact{
		Assignments: []bindAssignment{{AssignedEmail: "u1@example.test", DeviceID: "light-1", DeviceType: "light", ServiceOptions: []string{"mqtt"}}},
	}, nil, nil, 3)
	if len(selected) != 0 {
		t.Fatalf("selected = %v, want empty", selected)
	}
	if !containsString(blockers, "no bound camera devices with video_streaming service option") {
		t.Fatalf("blockers = %v, want no video devices blocker", blockers)
	}
}

func TestVideoRelayBuildsRunnerArgsWithoutLeakingTokens(t *testing.T) {
	cfg := videoRelayRunnerConfig{
		Workspace:          "/workspace",
		APIURL:             "https://video.example.test",
		OutDir:             "/tmp/out",
		Profile:            "smoke",
		DurationSeconds:    120,
		DeviceIDs:          []string{"cam-1"},
		DeviceTokenMapFile: "/tmp/device-tokens.json",
		AppTokenMapFile:    "/tmp/app-tokens.json",
	}
	args, display, err := buildVideoRelayRunnerArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--webrtc-media-set") || !strings.Contains(joined, "h264") {
		t.Fatalf("runner args missing WebRTC H.264 media flags: %v", args)
	}
	if !strings.Contains(joined, "--webrtc-media-duration") || !strings.Contains(joined, "20s") {
		t.Fatalf("runner args missing 20s WebRTC media duration: %v", args)
	}
	if !strings.Contains(joined, "--device-route-set") || !strings.Contains(joined, "off") {
		t.Fatalf("runner args should disable legacy device HTTP route coverage: %v", args)
	}
	if strings.Contains(joined, "--device-token-map-json") || strings.Contains(joined, "--app-token-map-json") {
		t.Fatalf("runner args must not expose token JSON flags: %v", args)
	}
	if !strings.Contains(joined, "--device-token-map-file") || !strings.Contains(joined, "--app-token-map-file") {
		t.Fatalf("runner args missing token map file flags: %v", args)
	}
	if strings.Contains(display, "device-secret-token") || strings.Contains(display, "app-secret-token") {
		t.Fatalf("display args leaked token: %s", display)
	}
}

func TestVideoRelayWritesTokenMapFilesWithoutEmbeddingSecretsInArgs(t *testing.T) {
	dir := t.TempDir()
	files, cleanup, err := writeVideoRelayTokenMapFiles(map[string]string{"cam-1": "device-secret-token"}, map[string]string{"cam-1": "app-secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Dir(files.Device) == dir || filepath.Dir(files.App) == dir {
		t.Fatalf("token files should be process temp files, got %v", files)
	}
	for _, path := range []string{files.Device, files.App} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", path, info.Mode().Perm())
		}
	}
}

func TestVideoRelayRenderPassReportIncludesRTPEvidenceAndSanitizesSecrets(t *testing.T) {
	result := videoRelayResult{
		Status:     "PASS",
		Overall:    "pass",
		Brandname:  "RTK",
		ProbeModel: "webrtc_rtp_relay",
		WebRTC: videoRelayWebRTCResult{
			SignalingTraceStatus:   "PASS",
			RelayEvidenceStatus:    "not_required",
			SelectedCandidateTypes: "host",
		},
		Devices: []videoRelayDeviceResult{{
			DeviceID: "cam-1", WebSocketOwnerStatus: "PASS", WebRTCCreateStatus: "PASS", WebRTCAnswerStatus: "PASS",
			ICEConnectedStatus: "PASS", RTPReceiveStatus: "PASS", CloseStatus: "PASS", ICEServerCount: 3,
			ICEConnectedLatencyMS: 12, RTPPacketsReceived: 8, RTPBytesReceived: 40,
		}},
		SignalingTrace: []videoRelayTraceEvent{
			{DeviceID: "cam-1", Event: "websocket_owner_online", Status: "PASS"},
			{DeviceID: "cam-1", Event: "request_webrtc_response", Status: "PASS"},
			{DeviceID: "cam-1", Event: "webrtc_offer_received", Status: "PASS"},
			{DeviceID: "cam-1", Event: "answer_submitted", Status: "PASS"},
			{DeviceID: "cam-1", Event: "ice_connected", Status: "PASS"},
			{DeviceID: "cam-1", Event: "h264_bitstream_compared", Status: "PASS"},
			{DeviceID: "cam-1", Event: "request_webrtc_close", Status: "PASS"},
		},
		Error: "Bearer abc private_key_pem -----BEGIN PRIVATE KEY----- turn credential secret",
	}
	report := renderVideoRelayReport(result)
	if !strings.Contains(report, "webrtc_rtp_relay") || !strings.Contains(report, "RTP packets") || !strings.Contains(report, "Signaling Trace") || !strings.Contains(report, "Relay Evidence") {
		t.Fatalf("report missing relay evidence:\n%s", report)
	}
	for _, forbidden := range []string{"abc", "PRIVATE KEY", "turn credential secret", "Bearer", "loopback"} {
		if strings.Contains(report, forbidden) {
			t.Fatalf("report leaked %q:\n%s", forbidden, report)
		}
	}
}

func TestVideoRelaySummaryUsesOperationEvidenceICEServerCount(t *testing.T) {
	raw := []byte(`{"operations":[
		{"name":"webrtc_media_answer","device_id":"cam-1","success":true,"evidence":"webrtc_response mode=webrtc session_id_present=true ice_servers=2 offer_present=true answer_present=false candidate_types=relay"},
		{"name":"webrtc_media_answer","device_id":"cam-1","success":true,"evidence":"{\"status\":\"ok\"}"},
		{"name":"webrtc_media_ice_connected","device_id":"cam-1","success":true,"latency_ms":25,"evidence":"ice_connected_ms=25"},
		{"name":"webrtc_media_receive","device_id":"cam-1","success":true,"evidence":"codec=h264 packets=10 bytes=20 nal_types=idr,sps receiver_bitstream_match=true"},
		{"name":"webrtc_media_close","device_id":"cam-1","success":true,"evidence":"{\"status\":\"ok\"}"}
	]}`)
	path := filepath.Join(t.TempDir(), "load-results.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	devices := summarizeVideoRelayLoadResults(path, []videoRelaySelectedDevice{{DeviceID: "cam-1", AssignedEmail: "u1@example.test"}})
	if len(devices) != 1 {
		t.Fatalf("devices len = %d, want 1", len(devices))
	}
	if devices[0].ICEServerCount != 2 {
		t.Fatalf("ICE server count = %d, want operation evidence count 2", devices[0].ICEServerCount)
	}
}

func TestVideoRelaySignalingTraceSanitizesSensitiveEvidence(t *testing.T) {
	summary := videoRelayLoadSummary{
		RunID:     "run-1",
		StartedAt: time.Date(2026, 6, 5, 1, 2, 3, 0, time.UTC),
		Operations: []videoRelayLoadOperation{{
			Actor:    "viewer",
			Name:     "webrtc_media_answer",
			DeviceID: "cam-1",
			Success:  true,
			Evidence: `{"status":"ok","session_id":"session-1","offer":{"type":"offer","sdp":"v=0 secret-sdp"},"ice_servers":[{"username":"turn-user","credential":"turn-secret"}],"access_token":"token-secret"}`,
		}, {
			Actor:    "device",
			Name:     "webrtc_media_offer_receive",
			DeviceID: "cam-1",
			Success:  true,
			Evidence: "session_id_present=true offer_present=true candidate_types=relay Bearer abc.def -----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----",
		}},
	}
	trace := buildVideoRelaySignalingTrace(summary)
	if len(trace) != 2 {
		t.Fatalf("trace len = %d, want 2", len(trace))
	}
	path := filepath.Join(t.TempDir(), "SIGNALING_TRACE.jsonl")
	if err := writeVideoRelaySignalingTraceJSONL(path, trace); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{"secret-sdp", "turn-user", "turn-secret", "token-secret", "abc.def", "PRIVATE KEY"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("trace leaked %q:\n%s", forbidden, body)
		}
	}
	for _, want := range []string{"request_webrtc_response", "webrtc_offer_received", "candidate_types=relay"} {
		if !strings.Contains(body, want) {
			t.Fatalf("trace missing %q:\n%s", want, body)
		}
	}
}

func TestVideoRelayCoturnEvidencePolicy(t *testing.T) {
	status, events := evaluateVideoRelayCoturnEvidence(t.TempDir(), t.TempDir(), time.Time{}, "", "host")
	if status.Status != "not_required" || status.Required || len(events) != 0 {
		t.Fatalf("direct relay status = %+v events=%v, want not_required", status, events)
	}
	status, _ = evaluateVideoRelayCoturnEvidence(t.TempDir(), t.TempDir(), time.Time{}, "relay", "")
	if status.Status != "FAIL" || !status.Required {
		t.Fatalf("relay-required status = %+v, want FAIL required", status)
	}
	events = parseVideoRelayCoturnEvents("Jun 05 turnserver: session 001: new allocation; relay addr 203.0.113.99\n")
	if len(events) != 1 || events[0].Kind != "allocation" {
		t.Fatalf("coturn events = %#v, want allocation event", events)
	}
}

func TestVideoRelayReadsCurrentUsersArtifactShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	raw := map[string]any{
		"brandname": "RTK",
		"users": []map[string]any{{
			"email": "u1@example.test",
			"app_credentials": map[string]any{
				"private_key_pem": "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----",
				"csr_pem":         "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----",
			},
			"app_certificate": map[string]any{
				"certificate_chain_pem": "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----",
				"subject":               "app-user:1",
			},
		}},
	}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := readVideoRelayUsersArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Brandname != "RTK" || len(artifact.Users) != 1 || artifact.Users[0].AppCredentials.PrivateKeyPEM == "" {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestVideoRelayTokenRequestsUseExpectedScopes(t *testing.T) {
	seen := []map[string]string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/request_token" {
			t.Fatalf("path = %s, want /request_token", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, body)
		_, _ = w.Write([]byte(`{"scope":"` + body["scope"] + `","access_token":"token-` + body["scope"] + `"}`))
	}))
	defer server.Close()

	cert := testTLSCertificate(t)
	deviceToken, err := requestVideoRelayDeviceToken(server.URL, cert)
	if err != nil {
		t.Fatal(err)
	}
	appToken, err := requestVideoRelayAppToken(server.URL, cert, "cam-1")
	if err != nil {
		t.Fatal(err)
	}
	if deviceToken != "token-device" || appToken.AccessToken != "token-app" {
		t.Fatalf("tokens = %q/%q", deviceToken, appToken.AccessToken)
	}
	if len(seen) != 2 || seen[0]["scope"] != "device" || seen[1]["scope"] != "app" || seen[1]["devid"] != "cam-1" {
		t.Fatalf("seen token requests = %#v", seen)
	}
}

func TestVideoRelayDeviceCertificatePrefersChainPEM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leaf.pem"), []byte("bad cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := testTLSCertificate(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "chain.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRelayDeviceCertificate(dir, videoRelaySelectedDevice{CertPath: "leaf.pem", ChainPath: "chain.pem", KeyPath: "key.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Certificate) == 0 {
		t.Fatal("loaded certificate chain is empty")
	}
}

func TestVideoRelayMTLSBaseURLUsesTopologyDeviceClientDomain(t *testing.T) {
	envRoot := t.TempDir()
	topologyDir := filepath.Join(envRoot, "topology")
	if err := os.MkdirAll(topologyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(topologyDir, "video-cloud.yaml"), []byte(`
deploy:
  domain: video.example.test
  device_client_domain: device.video.example.test
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := videoCloudMTLSBaseURLForRelay(envRoot, map[string]string{"VIDEO_CLOUD_DOMAIN": "video.example.test"}, "https://video.example.test")
	if got != "https://device.video.example.test" {
		t.Fatalf("mTLS base URL = %q, want device-client topology domain", got)
	}
}

func deviceIDs(devices []videoRelaySelectedDevice) []string {
	out := make([]string, 0, len(devices))
	for _, device := range devices {
		out = append(out, device.DeviceID)
	}
	return out
}

func testTLSCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

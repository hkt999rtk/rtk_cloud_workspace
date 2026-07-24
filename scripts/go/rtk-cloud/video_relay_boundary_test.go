package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVideoRelayTraceCompletenessAndRedactionBoundaries(t *testing.T) {
	started := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	summary := videoRelayLoadSummary{
		RunID: "run-1", StartedAt: started,
		Operations: []videoRelayLoadOperation{
			{Name: "device_websocket_owner", DeviceID: "device-1", Actor: "device", Success: true},
			{Name: "request_webrtc_create", DeviceID: "device-1", Actor: "viewer", Success: true, Evidence: `{"session_id":"session-1","access_token":"secret"}`},
			{Name: "webrtc_media_offer_receive", DeviceID: "device-1", Actor: "device", Success: true},
			{Name: "webrtc_media_answer", DeviceID: "device-1", Actor: "device", Success: true},
			{Name: "webrtc_media_ice_connected", DeviceID: "device-1", Success: true, Evidence: "candidate_types=host,relay"},
			{Name: "webrtc_media_receive", DeviceID: "device-1", Success: true},
			{Name: "webrtc_media_close", DeviceID: "device-1", Success: true},
			{Name: "ignored_operation", DeviceID: "device-1", Success: true},
		},
	}
	trace := buildVideoRelaySignalingTrace(summary)
	if len(trace) != 7 || videoRelaySignalingTraceStatus(trace, []string{"device-1"}) != "PASS" {
		t.Fatalf("trace = %#v", trace)
	}
	if strings.Contains(trace[1].Evidence, "secret") || trace[1].SessionID != "session-1" {
		t.Fatalf("redacted trace event = %#v", trace[1])
	}
	if got := videoRelayCandidateTypes(trace); !reflect.DeepEqual(got, []string{"host", "relay"}) {
		t.Fatalf("candidate types = %#v", got)
	}
	trace[0].Status = "FAIL"
	if videoRelaySignalingTraceStatus(trace, []string{"device-1"}) != "FAIL" {
		t.Fatal("incomplete trace unexpectedly passed")
	}
	if sanitized := sanitizeVideoRelayTraceEvidence(`{"offer":{"sdp":"secret"},"nested":[{"token":"secret","safe":"value"}]}`); strings.Contains(sanitized, "secret") || !strings.Contains(sanitized, "safe") {
		t.Fatalf("sanitized evidence = %q", sanitized)
	}
}

func TestVideoRelaySummaryFilesPolicyAndFinalReports(t *testing.T) {
	root := t.TempDir()
	summaryPath := filepath.Join(root, "summary.json")
	summaryBody := map[string]any{
		"run_id": "run-file", "started_at": "2026-07-24T01:02:03Z",
		"operations": []map[string]any{{"name": "request_webrtc_create", "device_id": "device-1", "success": true}},
	}
	raw, err := json.Marshal(summaryBody)
	if err != nil || os.WriteFile(summaryPath, raw, 0o600) != nil {
		t.Fatal("failed to write summary fixture")
	}
	if got := readVideoRelayLoadSummary(summaryPath); got.RunID != "run-file" || len(got.Operations) != 1 {
		t.Fatalf("load summary = %#v", got)
	}
	if got := readVideoRelayLoadSummary(filepath.Join(root, "missing")); got.RunID != "" {
		t.Fatalf("missing summary = %#v", got)
	}

	envPath := filepath.Join(root, "services", "video-cloud", "video-cloud.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("VIDEO_CLOUD_WEBRTC_ICE_POLICY=relay\nSAFE=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if videoRelayICEPolicy(root) != "relay" || videoRelayEnvValues(envPath)["SAFE"] != "value" {
		t.Fatal("video relay environment was not loaded")
	}
	if len(videoRelayEnvValues(filepath.Join(root, "missing"))) != 0 {
		t.Fatal("missing relay env unexpectedly returned values")
	}

	result := videoRelayResult{
		Schema: "v1", Brandname: "RTK", Profile: "qualification-1k", ProbeModel: videoRelayProbeModel,
		TraceDetail: "verbose",
		WebRTC: videoRelayWebRTCResult{
			SignalingTraceStatus: "PASS", RelayEvidenceStatus: "PASS",
			SelectedCandidateTypes: "relay", ICEPolicy: "relay", RelayEvidenceRequired: true,
		},
		Devices: []videoRelayDeviceResult{{
			DeviceID: "device-1", WebSocketOwnerStatus: "PASS", WebRTCCreateStatus: "PASS",
			WebRTCAnswerStatus: "PASS", ICEConnectedStatus: "PASS", RTPReceiveStatus: "PASS",
			CloseStatus: "PASS", RTPCodec: "H264", RTPPacketsReceived: 10, RTPBytesReceived: 100,
		}},
		SignalingTrace: []videoRelayTraceEvent{{
			Timestamp: "2026-07-24T01:02:03Z", DeviceID: "device-1", Event: "ice_connected", Status: "PASS",
		}},
	}
	blockedDir := filepath.Join(root, "blocked")
	failedDir := filepath.Join(root, "failed")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	final, err := writeVideoRelayBlocked(blockedDir, result, "credential token=secret")
	if err != nil || final.Status != "BLOCKED" || strings.Contains(final.Error, "secret") {
		t.Fatalf("blocked result = %#v, error = %v", final, err)
	}
	final, err = writeVideoRelayFailed(failedDir, result, "relay failed")
	if err != nil || final.Status != "FAIL" {
		t.Fatalf("failed result = %#v, error = %v", final, err)
	}
	for _, path := range []string{final.Artifacts["results"], final.Artifacts["report"]} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing relay artifact %s: %v", path, err)
		}
	}
}

func TestVideoRelayArtifactReadersAndDeviceSelection(t *testing.T) {
	root := t.TempDir()
	idsPath := filepath.Join(root, "device-ids.txt")
	if err := os.WriteFile(idsPath, []byte("# qualification devices\n device-1\n\ndevice-2\ndevice-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids, err := readVideoLoadtestDeviceIDsFile(idsPath)
	if err != nil || !reflect.DeepEqual(ids, []string{"device-1", "device-2"}) {
		t.Fatalf("device IDs = %#v, error = %v", ids, err)
	}
	emptyPath := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(emptyPath, []byte("# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVideoLoadtestDeviceIDsFile(emptyPath); err == nil {
		t.Fatal("empty device ID file unexpectedly passed")
	}

	usersArtifact := videoRelayUsersArtifact{Brandname: "RTK", Users: []videoRelayUser{{
		Email: "user@example.test",
		AppCredentials: videoRelayAppCredentials{
			PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----",
			CSRPem:        "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----",
		},
		AppCertificate: videoRelayAppCertificate{
			CertificateChainPEM: "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----",
		},
	}}}
	bindArtifact := videoRelayBindArtifact{Brandname: "RTK", Assignments: []bindAssignment{
		{AssignedEmail: "user@example.test", DeviceID: "device-1", DeviceType: "camera", ServiceOptions: []string{"video_streaming"}},
		{AssignedEmail: "missing@example.test", DeviceID: "device-2", DeviceType: "camera", ServiceOptions: []string{"video_streaming"}},
		{AssignedEmail: "user@example.test", DeviceID: "light-1", DeviceType: "light"},
	}}
	manifestRows := []videoRelayDeviceManifest{{
		DeviceID: "device-1", DeviceType: "camera", CertificatePath: "/cert.pem",
		CertificateChainPath: "/chain.pem", KeyPath: "/key.pem",
	}}
	writeFixture := func(name string, value any) string {
		t.Helper()
		path := filepath.Join(root, name)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	usersPath := writeFixture("users.json", usersArtifact)
	bindPath := writeFixture("bind.json", bindArtifact)
	manifestPath := writeFixture("manifest.json", manifestRows)
	gotUsers, err := readVideoRelayUsersArtifact(usersPath)
	if err != nil || len(gotUsers.Users) != 1 {
		t.Fatalf("users = %#v, error = %v", gotUsers, err)
	}
	gotBind, err := readVideoRelayBindArtifact(bindPath)
	if err != nil || len(gotBind.Assignments) != 3 {
		t.Fatalf("bind = %#v, error = %v", gotBind, err)
	}
	gotManifest, err := readVideoRelayManifest(manifestPath)
	if err != nil || gotManifest["device-1"].KeyPath != "/key.pem" {
		t.Fatalf("manifest = %#v, error = %v", gotManifest, err)
	}
	usersByEmail := map[string]videoRelayUser{"user@example.test": gotUsers.Users[0]}
	selected, blockers := selectVideoRelayDevices(gotBind, usersByEmail, gotManifest, 1)
	if len(selected) != 1 || selected[0].DeviceID != "device-1" {
		t.Fatalf("selected = %#v, blockers = %#v", selected, blockers)
	}
	selected, blockers = selectVideoRelayDevices(videoRelayBindArtifact{}, usersByEmail, gotManifest, 0)
	if len(selected) != 0 || len(blockers) != 1 || !strings.Contains(blockers[0], "no bound camera") {
		t.Fatalf("empty selected = %#v, blockers = %#v", selected, blockers)
	}
}

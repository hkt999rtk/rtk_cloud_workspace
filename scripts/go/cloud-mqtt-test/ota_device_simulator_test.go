package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOTAOptionsValidate(t *testing.T) {
	opts := defaultOTAOptions()
	if err := opts.validate(); err == nil || !strings.Contains(err.Error(), "--ota-campaign-id") {
		t.Fatalf("validate missing required fields error = %v", err)
	}
	opts.CampaignID = "campaign-1"
	opts.TargetVersion = "2.0.0"
	opts.CurrentVersion = "1.0.0"
	opts.HardwareRevision = "rev-a"
	if err := opts.validate(); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
	opts.DownloadFailurePercent = 60
	opts.VerifyFailurePercent = 41
	if err := opts.validate(); err == nil || !strings.Contains(err.Error(), "sum") {
		t.Fatalf("validate failure total error = %v", err)
	}
	opts = defaultOTAOptions()
	opts.CampaignID, opts.TargetVersion, opts.CurrentVersion, opts.HardwareRevision = "c", "2", "1", "r"
	opts.DownloadConcurrency = opts.HTTPConcurrency + 1
	if err := opts.validate(); err == nil || !strings.Contains(err.Error(), "download-concurrency") {
		t.Fatalf("validate concurrency error = %v", err)
	}
}

func TestSelectOTAFaultIsDeterministicAndExclusive(t *testing.T) {
	cases := []struct {
		name string
		set  func(*otaOptions)
		want string
		term string
	}{
		{"download", func(o *otaOptions) { o.DownloadFailurePercent = 100 }, "download", "failed"},
		{"verify", func(o *otaOptions) { o.VerifyFailurePercent = 100 }, "verify", "failed"},
		{"install", func(o *otaOptions) { o.InstallFailurePercent = 100 }, "install", "failed"},
		{"reboot", func(o *otaOptions) { o.RebootFailurePercent = 100 }, "reboot", "failed"},
		{"timeout", func(o *otaOptions) { o.TimeoutPercent = 100 }, "timeout", "timed_out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := otaOptions{}
			tc.set(&opts)
			firstFault, firstTerminal := selectOTAFault(opts, 42, "run-1", "device-1")
			secondFault, secondTerminal := selectOTAFault(opts, 42, "run-1", "device-1")
			if firstFault != tc.want || firstTerminal != tc.term || firstFault != secondFault || firstTerminal != secondTerminal {
				t.Fatalf("fault = %q/%q then %q/%q, want %q/%q", firstFault, firstTerminal, secondFault, secondTerminal, tc.want, tc.term)
			}
		})
	}
	if fault, terminal := selectOTAFault(otaOptions{}, 42, "run-1", "device-1"); fault != "" || terminal != "succeeded" {
		t.Fatalf("no-injection fault = %q terminal = %q", fault, terminal)
	}
}

func TestValidateOTAAssignment(t *testing.T) {
	payload := []byte("firmware")
	digest := sha256.Sum256(payload)
	config := testOTARuntimeConfig()
	valid := otaAssignment{
		DeploymentID: "deployment-1", CampaignID: config.CampaignID, ReleaseID: "release-1", TargetVersion: config.TargetVersion,
		Manifest: otaManifest{ReleaseID: "release-1", Version: config.TargetVersion, ArtifactSize: int64(len(payload)), ArtifactSHA256: hex.EncodeToString(digest[:]), HardwareRevisions: []string{"rev-a"}, AntiRollback: 2, SigningAlgorithm: "ed25519", SigningKeyID: "key-1", Signature: strings.Repeat("0", 128)},
	}
	if err := validateOTAAssignment(config, valid); err != nil {
		t.Fatalf("valid assignment: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*otaAssignment)
	}{
		{"campaign", func(a *otaAssignment) { a.CampaignID = "wrong" }},
		{"target", func(a *otaAssignment) { a.TargetVersion = "wrong" }},
		{"hardware", func(a *otaAssignment) { a.Manifest.HardwareRevisions = []string{"rev-b"} }},
		{"anti rollback", func(a *otaAssignment) { a.Manifest.AntiRollback = 0 }},
		{"signature", func(a *otaAssignment) { a.Manifest.Signature = "" }},
		{"expired manifest", func(a *otaAssignment) { a.Manifest.ExpiresAt = time.Now().Add(-time.Minute) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			got.Manifest.HardwareRevisions = append([]string(nil), valid.Manifest.HardwareRevisions...)
			tc.mutate(&got)
			if err := validateOTAAssignment(config, got); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestOTADeviceRunnerCompletesArtifactAndLifecycle(t *testing.T) {
	payload := []byte("complete firmware artifact")
	runner, server, events := newTestOTADeviceRunner(t, payload, payload, nil)
	defer server.Close()
	runner.reconnectHook = func(context.Context, *sustainedDeviceSession) error { return nil }
	result := runner.runDevice(context.Background(), testOTASession())
	if !result.TerminalMatched || result.ActualTerminal != "succeeded" || !result.ArtifactHashVerified || !result.ArtifactSizeVerified || !result.MQTTRebootDisconnected || !result.MQTTReconnectSucceeded {
		t.Fatalf("result = %+v", result)
	}
	wantStatuses := []string{"downloading", "downloaded", "installing", "rebooting", "verifying", "succeeded"}
	wantSequences := []int64{2, 3, 4, 5, 6, 7}
	if len(*events) != len(wantStatuses) {
		t.Fatalf("events = %#v", *events)
	}
	for i, event := range *events {
		if event.Status != wantStatuses[i] || event.Sequence != wantSequences[i] {
			t.Fatalf("event[%d] = %+v, want status=%s sequence=%d", i, event, wantStatuses[i], wantSequences[i])
		}
	}
}

func TestOTADeviceRunnerClassifiesInjectedDownloadFailureAsExpected(t *testing.T) {
	payload := []byte("firmware")
	runner, server, _ := newTestOTADeviceRunner(t, payload, payload, nil)
	defer server.Close()
	runner.config.DownloadFailurePercent = 100
	result := runner.runDevice(context.Background(), testOTASession())
	if !result.TerminalMatched || result.ExpectedTerminal != "failed" || result.ActualTerminal != "failed" || result.InjectedFailure != "download" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOTADeviceRunnerRejectsNaturalHashMismatch(t *testing.T) {
	expected := []byte("expected")
	downloaded := []byte("tampered")
	runner, server, _ := newTestOTADeviceRunner(t, expected, downloaded, nil)
	defer server.Close()
	result := runner.runDevice(context.Background(), testOTASession())
	if result.TerminalMatched || result.ErrorCategory != "artifact_verification_failed" || result.ArtifactHashVerified {
		t.Fatalf("result = %+v", result)
	}
}

func TestOTADeviceRunnerTreatsMQTTReconnectFailureAsUnexpected(t *testing.T) {
	payload := []byte("firmware")
	runner, server, _ := newTestOTADeviceRunner(t, payload, payload, nil)
	defer server.Close()
	runner.reconnectHook = func(context.Context, *sustainedDeviceSession) error { return errors.New("broker unavailable") }
	result := runner.runDevice(context.Background(), testOTASession())
	if result.TerminalMatched || result.ActualTerminal != "failed" || result.ErrorCategory != "mqtt_reconnect_failed" || result.MQTTReconnectSucceeded {
		t.Fatalf("result = %+v", result)
	}
}

func TestOTADownloadGetsFreshArtifactAuthorizationOnRetry(t *testing.T) {
	payload := []byte("firmware")
	digest := sha256.Sum256(payload)
	assignment := testOTAAssignment(len(payload), hex.EncodeToString(digest[:]))
	var mu sync.Mutex
	tokenCalls := 0
	downloadCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/artifact-token"):
			mu.Lock()
			tokenCalls++
			token := tokenCalls
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment_id": assignment.DeploymentID, "release_id": assignment.ReleaseID, "url": server.URL + "/artifact?token=" + strconv.Itoa(token), "range_supported": true, "manifest": assignment.Manifest})
		case req.URL.Path == "/artifact":
			mu.Lock()
			downloadCalls++
			attempt := downloadCalls
			mu.Unlock()
			if attempt == 1 {
				http.Error(w, "expired", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	n, gotDigest, err := runner.downloadArtifact(context.Background(), testOTATokenManager(), assignment, &result)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || gotDigest != hex.EncodeToString(digest[:]) || tokenCalls != 2 || downloadCalls != 2 {
		t.Fatalf("bytes=%d digest=%s token_calls=%d download_calls=%d", n, gotDigest, tokenCalls, downloadCalls)
	}
}

func TestOTAWaitForAssignmentPollsDeferredAndNoUpdate(t *testing.T) {
	payload := []byte("firmware")
	decisions := []string{"deferred", "no_update", "assigned"}
	runner, server, _ := newTestOTADeviceRunner(t, payload, payload, &decisions)
	defer server.Close()
	runner.config.PollEvery = time.Millisecond
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	assignment, err := runner.waitForAssignment(context.Background(), testOTASession(), &result)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.CampaignID != runner.config.CampaignID || result.CheckAttempts != 3 || result.LastCheckDecision != "assigned" {
		t.Fatalf("assignment=%+v result=%+v", assignment, result)
	}
}

func TestOTAWaitForAssignmentHonorsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"decision": "deferred", "reason_code": "rollout_rate_limit"})
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	runner.config.PollEvery = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	_, err := runner.waitForAssignment(ctx, testOTASession(), &result)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestOTAEventRetryReusesExactPayload(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	event := otaEventRequest{Sequence: 2, Status: "downloading", ObservedVersion: "1.0.0", DeviceTimestamp: time.Now().UTC()}
	if err := runner.reportEvent(context.Background(), testOTATokenManager(), "deployment-1", event, &result); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatalf("retry bodies differ: %q vs %q", bodies[0], bodies[1])
	}
}

func TestWriteOTADeviceResultsSortsAndProtectsEvidence(t *testing.T) {
	outDir := t.TempDir()
	path := filepath.Join(outDir, "ota-devices.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []otaDeviceResult{{DeviceID: "device-b", Error: "safe"}, {DeviceID: "device-a", Error: "safe"}}
	if err := writeOTADeviceResults(outDir, rows); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"device_id":"device-a"`) || !strings.Contains(lines[1], `"device_id":"device-b"`) {
		t.Fatalf("rows not sorted: %s", data)
	}
}

func newTestOTADeviceRunner(t *testing.T, manifestPayload, servedPayload []byte, decisions *[]string) (otaDeviceRunner, *httptest.Server, *[]otaEventRequest) {
	t.Helper()
	digest := sha256.Sum256(manifestPayload)
	events := &[]otaEventRequest{}
	checkCount := 0
	var mu sync.Mutex
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/v1/device/ota/check":
			decision := "assigned"
			if decisions != nil {
				mu.Lock()
				if checkCount < len(*decisions) {
					decision = (*decisions)[checkCount]
				}
				checkCount++
				mu.Unlock()
			}
			response := map[string]any{"decision": decision}
			if decision != "assigned" {
				response["reason_code"] = "test-" + decision
			} else {
				response["assignment"] = testOTAAssignment(len(manifestPayload), hex.EncodeToString(digest[:]))
			}
			_ = json.NewEncoder(w).Encode(response)
		case strings.HasSuffix(req.URL.Path, "/artifact-token"):
			assignment := testOTAAssignment(len(manifestPayload), hex.EncodeToString(digest[:]))
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment_id": assignment.DeploymentID, "release_id": assignment.ReleaseID, "url": server.URL + "/artifact", "range_supported": true, "manifest": assignment.Manifest})
		case req.URL.Path == "/artifact":
			_, _ = w.Write(servedPayload)
		case strings.HasSuffix(req.URL.Path, "/events"):
			var event otaEventRequest
			if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			*events = append(*events, event)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(event)
		default:
			http.NotFound(w, req)
		}
	}))
	runner := testOTARunner(server.URL)
	return runner, server, events
}

func testOTARunner(baseURL string) otaDeviceRunner {
	config := testOTARuntimeConfig()
	return otaDeviceRunner{
		config:         config,
		seed:           7,
		runID:          "run-test",
		brandname:      "RTK",
		apiBaseURL:     baseURL,
		httpClient:     &http.Client{Timeout: time.Second},
		artifactClient: &http.Client{Timeout: time.Second},
		httpSem:        make(chan struct{}, 4),
		downloadSem:    make(chan struct{}, 2),
	}
}

func testOTARuntimeConfig() otaRuntimeConfig {
	opts := defaultOTAOptions()
	opts.CampaignID = "campaign-1"
	opts.TargetVersion = "2.0.0"
	opts.CurrentVersion = "1.0.0"
	opts.HardwareRevision = "rev-a"
	opts.AntiRollbackCounter = 1
	opts.StageJitterPercent = 0
	config := opts.runtimeConfig()
	config.Install, config.Reboot, config.Verify = 0, 0, 0
	return config
}

func testOTAAssignment(size int, digest string) otaAssignment {
	return otaAssignment{
		DeploymentID:  "deployment-1",
		CampaignID:    "campaign-1",
		ReleaseID:     "release-1",
		TargetVersion: "2.0.0",
		Manifest: otaManifest{
			ReleaseID: "release-1", SKUID: "sku-1", Version: "2.0.0", BuildNumber: "2", ArtifactSize: int64(size), ArtifactSHA256: digest,
			HardwareRevisions: []string{"rev-a"}, AntiRollback: 2, SigningAlgorithm: "ed25519", SigningKeyID: "key-1", Signature: strings.Repeat("0", 128),
		},
	}
}

func testOTATokenManager() *tokenManager {
	return &tokenManager{now: time.Now, timeout: time.Second, bundle: tokenBundle{AccessToken: "opaque-test-token", issuedAt: time.Now()}}
}

func testOTASession() sustainedDeviceSession {
	return sustainedDeviceSession{
		Assignment:         assignment{Brandname: "RTK", DeviceID: "device-1", DeviceType: "camera"},
		Record:             certRecord{Brandname: "RTK", DeviceID: "device-1", DeviceType: "camera"},
		DeviceTokenManager: testOTATokenManager(),
		MQTTTarget:         mqttEndpointTarget{Host: "127.0.0.1", Port: 8883},
	}
}

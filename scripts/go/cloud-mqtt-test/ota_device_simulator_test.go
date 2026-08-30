package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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

func TestRegisterOTAFlags(t *testing.T) {
	opts := defaultOTAOptions()
	registerOTAFlags(&opts)
	for _, name := range []string{
		"ota-campaign-id", "ota-target-version", "ota-current-version", "ota-hardware-revision", "ota-anti-rollback-counter",
		"ota-poll-min-interval", "ota-poll-max-interval", "ota-upgrade-timeout", "ota-http-concurrency", "ota-download-concurrency", "ota-install-delay",
		"ota-reboot-delay", "ota-verify-delay", "ota-stage-jitter-percent", "ota-download-failure-percent", "ota-verify-failure-percent",
		"ota-install-failure-percent", "ota-reboot-failure-percent", "ota-timeout-percent",
	} {
		if flag.Lookup(name) == nil {
			t.Fatalf("flag %q was not registered", name)
		}
	}
}

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
	valid := defaultOTAOptions()
	valid.CampaignID, valid.TargetVersion, valid.CurrentVersion, valid.HardwareRevision = "c", "2", "1", "r"
	tests := []struct {
		name   string
		mutate func(*otaOptions)
		want   string
	}{
		{"negative anti rollback", func(o *otaOptions) { o.AntiRollbackCounter = -1 }, "anti-rollback"},
		{"invalid poll minimum", func(o *otaOptions) { o.PollMinInterval = "invalid" }, "poll-min-interval"},
		{"invalid poll maximum", func(o *otaOptions) { o.PollMaxInterval = "invalid" }, "poll-max-interval"},
		{"reversed poll range", func(o *otaOptions) { o.PollMinInterval, o.PollMaxInterval = "60s", "10s" }, "greater than"},
		{"zero timeout", func(o *otaOptions) { o.UpgradeTimeout = "0s" }, "upgrade-timeout"},
		{"negative install", func(o *otaOptions) { o.InstallDelay = "-1s" }, "install-delay"},
		{"invalid reboot", func(o *otaOptions) { o.RebootDelay = "invalid" }, "reboot-delay"},
		{"negative verify", func(o *otaOptions) { o.VerifyDelay = "-1s" }, "verify-delay"},
		{"zero http concurrency", func(o *otaOptions) { o.HTTPConcurrency = 0 }, "http-concurrency"},
		{"zero download concurrency", func(o *otaOptions) { o.DownloadConcurrency = 0 }, "download-concurrency"},
		{"negative percentage", func(o *otaOptions) { o.StageJitterPercent = -1 }, "jitter-percent"},
		{"oversized percentage", func(o *otaOptions) { o.TimeoutPercent = 101 }, "timeout-percent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			tc.mutate(&got)
			if err := got.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error = %v, want %q", err, tc.want)
			}
		})
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
		{"missing identity", func(a *otaAssignment) { a.DeploymentID = "" }},
		{"campaign", func(a *otaAssignment) { a.CampaignID = "wrong" }},
		{"target", func(a *otaAssignment) { a.TargetVersion = "wrong" }},
		{"manifest identity", func(a *otaAssignment) { a.Manifest.ReleaseID = "wrong" }},
		{"artifact size", func(a *otaAssignment) { a.Manifest.ArtifactSize = 0 }},
		{"artifact digest", func(a *otaAssignment) { a.Manifest.ArtifactSHA256 = "invalid" }},
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
	wildcard := valid
	wildcard.Manifest.HardwareRevisions = []string{"*"}
	if err := validateOTAAssignment(config, wildcard); err != nil {
		t.Fatalf("wildcard hardware assignment: %v", err)
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

func TestOTADeviceRunnerClassifiesInjectedTerminalFailures(t *testing.T) {
	tests := []struct {
		name     string
		terminal string
		set      func(*otaRuntimeConfig)
	}{
		{"verify", "failed", func(c *otaRuntimeConfig) { c.VerifyFailurePercent = 100 }},
		{"install", "failed", func(c *otaRuntimeConfig) { c.InstallFailurePercent = 100 }},
		{"reboot", "failed", func(c *otaRuntimeConfig) { c.RebootFailurePercent = 100 }},
		{"timeout", "timed_out", func(c *otaRuntimeConfig) { c.TimeoutPercent = 100 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("firmware")
			runner, server, _ := newTestOTADeviceRunner(t, payload, payload, nil)
			defer server.Close()
			tc.set(&runner.config)
			runner.reconnectHook = func(context.Context, *sustainedDeviceSession) error { return nil }
			result := runner.runDevice(context.Background(), testOTASession())
			if !result.TerminalMatched || result.ExpectedTerminal != tc.terminal || result.ActualTerminal != tc.terminal || result.InjectedFailure != tc.name {
				t.Fatalf("result = %+v", result)
			}
		})
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

func TestOTADeviceRunnerRejectsAssignmentMismatch(t *testing.T) {
	payload := []byte("firmware")
	digest := sha256.Sum256(payload)
	assignment := testOTAAssignment(len(payload), hex.EncodeToString(digest[:]))
	assignment.CampaignID = "wrong-campaign"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"decision": "assigned", "assignment": assignment})
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	result := runner.runDevice(context.Background(), testOTASession())
	if result.ErrorCategory != "assignment_mismatch" || result.TerminalMatched {
		t.Fatalf("result = %+v", result)
	}
}

func TestOTADeviceRunnerReportsEventFailure(t *testing.T) {
	payload := []byte("firmware")
	digest := sha256.Sum256(payload)
	assignment := testOTAAssignment(len(payload), hex.EncodeToString(digest[:]))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/device/ota/check" {
			_ = json.NewEncoder(w).Encode(map[string]any{"decision": "assigned", "assignment": assignment})
			return
		}
		http.Error(w, "event rejected", http.StatusBadRequest)
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	result := runner.runDevice(context.Background(), testOTASession())
	if result.ErrorCategory != "downloading_event_failed" || result.TerminalMatched {
		t.Fatalf("result = %+v", result)
	}
}

func TestOTADeviceRunnerClassifiesStageFailures(t *testing.T) {
	tests := []struct {
		name             string
		wantCategory     string
		checkDecision    string
		artifactStatus   int
		failEventStatus  string
		cancelAfterEvent string
		configure        func(*otaDeviceRunner)
	}{
		{name: "assignment", wantCategory: "assignment_failed", checkDecision: "paused"},
		{name: "artifact download", wantCategory: "artifact_download_failed", artifactStatus: http.StatusBadRequest},
		{name: "downloaded event", wantCategory: "downloaded_event_failed", failEventStatus: "downloaded"},
		{name: "install delay", wantCategory: "install_delay_failed", cancelAfterEvent: "downloaded", configure: func(r *otaDeviceRunner) { r.config.Install = time.Second }},
		{name: "installing event", wantCategory: "installing_event_failed", failEventStatus: "installing"},
		{name: "reboot delay", wantCategory: "reboot_delay_failed", cancelAfterEvent: "installing", configure: func(r *otaDeviceRunner) { r.config.Reboot = time.Second }},
		{name: "rebooting event", wantCategory: "rebooting_event_failed", failEventStatus: "rebooting"},
		{name: "verifying event", wantCategory: "verifying_event_failed", failEventStatus: "verifying"},
		{name: "verify delay", wantCategory: "verify_delay_failed", cancelAfterEvent: "verifying", configure: func(r *otaDeviceRunner) { r.config.Verify = time.Second }},
		{name: "succeeded event", wantCategory: "succeeded_event_failed", failEventStatus: "succeeded"},
		{name: "injected terminal event", wantCategory: "injected_terminal_event_failed", failEventStatus: "failed", configure: func(r *otaDeviceRunner) { r.config.DownloadFailurePercent = 100 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner, server := newControlledOTARunner(t, cancel, tc.checkDecision, tc.artifactStatus, tc.failEventStatus, tc.cancelAfterEvent)
			defer server.Close()
			runner.reconnectHook = func(context.Context, *sustainedDeviceSession) error { return nil }
			if tc.configure != nil {
				tc.configure(&runner)
			}
			result := runner.runDevice(ctx, testOTASession())
			if result.ErrorCategory != tc.wantCategory || result.TerminalMatched {
				t.Fatalf("result = %+v, want category %q", result, tc.wantCategory)
			}
		})
	}
}

func TestOTADeviceRunnerClassifiesCanceledDownloadSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner, server := newControlledOTARunner(t, cancel, "", 0, "", "downloading")
	defer server.Close()
	for range cap(runner.downloadSem) {
		runner.downloadSem <- struct{}{}
	}
	result := runner.runDevice(ctx, testOTASession())
	if result.ErrorCategory != "download_slot_failed" || result.TerminalMatched {
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
	runner.config.PollMin, runner.config.PollMax = time.Millisecond, 2*time.Millisecond
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
	runner.config.PollMin, runner.config.PollMax = 50*time.Millisecond, 60*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	_, err := runner.waitForAssignment(ctx, testOTASession(), &result)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestOTAWaitForAssignmentRejectsMalformedDecisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response map[string]any
		want     string
	}{
		{"assigned without payload", map[string]any{"decision": "assigned"}, "without assignment"},
		{"unsupported", map[string]any{"decision": "paused"}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(tc.response) }))
			defer server.Close()
			runner := testOTARunner(server.URL)
			result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
			_, err := runner.waitForAssignment(context.Background(), testOTASession(), &result)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNormallyDistributedOTAPollDurationIsBoundedDeterministicAndCentered(t *testing.T) {
	const samples = 10_000
	minimum, maximum := 10*time.Second, 60*time.Second
	var total time.Duration
	for index := 0; index < samples; index++ {
		deviceID := "device-" + strconv.Itoa(index)
		got := normallyDistributedOTAPollDuration(minimum, maximum, 7, "run", deviceID, 1)
		if got < minimum || got > maximum {
			t.Fatalf("poll duration %s is outside [%s, %s]", got, minimum, maximum)
		}
		if repeat := normallyDistributedOTAPollDuration(minimum, maximum, 7, "run", deviceID, 1); repeat != got {
			t.Fatalf("poll duration is not deterministic: first=%s repeat=%s", got, repeat)
		}
		total += got
	}
	mean := total / samples
	if mean < 34*time.Second || mean > 36*time.Second {
		t.Fatalf("poll duration mean = %s, want approximately 35s", mean)
	}
}

func TestOTAArtifactAuthorizationRejectsInvalidResponses(t *testing.T) {
	assignment := testOTAAssignment(8, strings.Repeat("0", 64))
	for _, tc := range []struct {
		name   string
		mutate func(*otaArtifactAuthorization)
		want   string
	}{
		{"identity", func(a *otaArtifactAuthorization) { a.DeploymentID = "wrong" }, "identity"},
		{"manifest", func(a *otaArtifactAuthorization) { a.Manifest.Version = "wrong" }, "manifest differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := otaArtifactAuthorization{DeploymentID: assignment.DeploymentID, ReleaseID: assignment.ReleaseID, URL: "https://artifact.example.test", Manifest: assignment.Manifest}
			tc.mutate(&response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
			defer server.Close()
			runner := testOTARunner(server.URL)
			result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
			_, err := runner.artifactAuthorization(context.Background(), testOTATokenManager(), assignment, &result)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOTAConcurrencyAndTimeoutHelpers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := make(chan struct{}, 1)
	full <- struct{}{}
	if err := acquireOTA(ctx, full); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire canceled error = %v", err)
	}
	releaseOTA(full)
	if len(full) != 0 {
		t.Fatal("release did not free semaphore")
	}
	invalidateOTAToken(nil)
	manager := testOTATokenManager()
	invalidateOTAToken(manager)
	if manager.bundle.AccessToken != "" {
		t.Fatal("token was not invalidated")
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer deadlineCancel()
	if got := minOTATimeout(deadlineCtx, time.Second); got <= 0 || got >= time.Second {
		t.Fatalf("deadline timeout = %s", got)
	}
	if got := minOTATimeout(context.Background(), time.Second); got != time.Second {
		t.Fatalf("fallback timeout = %s", got)
	}
	if err := sleepOTAStage(ctx, time.Second, 20, 1, "run", "device", "install"); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep canceled error = %v", err)
	}
	if got := minInt(2, 1); got != 1 {
		t.Fatalf("minInt = %d", got)
	}
	foundClampedJitter := false
	for seed := 0; seed < 100 && !foundClampedJitter; seed++ {
		foundClampedJitter = jitteredOTADuration(time.Second, 1000, seed, "run", "device", "stage") == 0
	}
	if !foundClampedJitter {
		t.Fatal("expected an oversized negative jitter factor to clamp to zero")
	}
}

func TestCloseOTADeviceMQTTClosesReaderAndConnection(t *testing.T) {
	conn := &trackingReadWriteCloser{}
	reader := &sustainedDeviceReader{done: make(chan struct{})}
	session := &sustainedDeviceSession{Conn: conn, Reader: reader}
	closeOTADeviceMQTT(session)
	if session.Conn != nil || session.Reader != nil || !conn.closed {
		t.Fatalf("session was not closed: %+v conn=%+v", session, conn)
	}
	select {
	case <-reader.done:
	default:
		t.Fatal("reader was not closed")
	}
	closeOTADeviceMQTT(nil)
}

func TestOTAReconnectRejectsMissingManagerTokenAndBroker(t *testing.T) {
	runner := testOTARunner("https://api.example.test")
	if err := runner.reconnectMQTT(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "token manager") {
		t.Fatalf("missing manager error = %v", err)
	}
	badTokenSession := testOTASession()
	badTokenSession.DeviceTokenManager = &tokenManager{apiBaseURL: "http://127.0.0.1:1", deviceID: "device-1", now: time.Now, timeout: time.Millisecond}
	if err := runner.reconnectMQTT(context.Background(), &badTokenSession); err == nil || !strings.Contains(err.Error(), "renew device token") {
		t.Fatalf("token renewal error = %v", err)
	}
	brokerSession := testOTASession()
	brokerSession.MQTTTarget = mqttEndpointTarget{Host: "127.0.0.1", Port: 1}
	if err := runner.reconnectMQTT(context.Background(), &brokerSession); err == nil {
		t.Fatal("expected broker connection failure")
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

func TestOTAHTTPHelpersRejectNonRetryableAndMalformedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/bad-request":
			http.Error(w, "invalid request", http.StatusBadRequest)
		case "/malformed":
			_, _ = w.Write([]byte("not-json"))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	runner := testOTARunner(server.URL)
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	if err := runner.doDeviceJSON(context.Background(), testOTATokenManager(), http.MethodPost, "/bad-request", map[string]string{"value": "x"}, nil, "bad", &result); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("non-retryable error = %v", err)
	}
	var decoded map[string]any
	if err := runner.doDeviceJSON(context.Background(), testOTATokenManager(), http.MethodGet, "/malformed", nil, &decoded, "malformed", &result); err == nil || !strings.Contains(err.Error(), "decode malformed") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestOTAHTTPHelpersExhaustTransportAndTokenRetries(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		runner := testOTARunner("https://api.example.test")
		runner.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("simulated network failure")
		})}
		result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
		err := runner.doDeviceJSON(context.Background(), testOTATokenManager(), http.MethodGet, "/unavailable", nil, nil, "transport", &result)
		if err == nil || !strings.Contains(err.Error(), "simulated network failure") || result.HTTPRetries != 2 {
			t.Fatalf("error=%v result=%+v", err, result)
		}
	})

	t.Run("token", func(t *testing.T) {
		runner := testOTARunner("https://api.example.test")
		manager := &tokenManager{apiBaseURL: "http://127.0.0.1:1", deviceID: "device-1", now: time.Now, timeout: time.Millisecond}
		result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
		err := runner.doDeviceJSON(context.Background(), manager, http.MethodGet, "/unavailable", nil, nil, "token", &result)
		if err == nil || result.HTTPRetries != 2 {
			t.Fatalf("error=%v result=%+v", err, result)
		}
	})
}

func TestOTADownloadExhaustsTransportRetries(t *testing.T) {
	payload := []byte("controlled firmware")
	digest := sha256.Sum256(payload)
	assignment := testOTAAssignment(len(payload), hex.EncodeToString(digest[:]))
	runner, server := newControlledOTARunner(t, func() {}, "", 0, "", "")
	defer server.Close()
	runner.artifactClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated artifact network failure")
	})}
	result := newOTADeviceResult("device-1", "camera", runner.config, "", "succeeded")
	_, _, err := runner.downloadArtifact(context.Background(), testOTATokenManager(), assignment, &result)
	if err == nil || !strings.Contains(err.Error(), "simulated artifact network failure") || result.HTTPRetries != 2 {
		t.Fatalf("error=%v result=%+v", err, result)
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

func TestWriteOTADeviceResultsRejectsFileAsOutputDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOTADeviceResults(path, nil); err == nil {
		t.Fatal("expected output directory error")
	}
}

func TestSummarizeOTAAggregatesDeviceEvidence(t *testing.T) {
	config := testOTARuntimeConfig()
	peaks := &otaRuntimePeaks{goroutines: 12, heap: 4096}
	devices := []otaDeviceResult{
		{
			CampaignID: config.CampaignID, DeploymentID: "deployment-1", ExpectedTerminal: "succeeded", ActualTerminal: "succeeded", TerminalMatched: true,
			ArtifactBytes: 128, ArtifactHashVerified: true, ArtifactSizeVerified: true, MQTTRebootDisconnected: true, MQTTReconnectSucceeded: true,
			HTTPStatusCounts: map[string]int{"ota_check:200": 2}, HTTPLatencyMS: map[string][]float64{"ota_check": {10, 20, 30}},
		},
		{
			ExpectedTerminal: "failed", ActualTerminal: "failed", TerminalMatched: false, ArtifactBytes: 64,
			HTTPStatusCounts: map[string]int{"ota_check:200": 1}, HTTPLatencyMS: map[string][]float64{"ota_check": {40}},
		},
	}
	summary := summarizeOTA(config, devices, 2, time.Now().Add(-time.Second), peaks)
	if summary.DevicesSelected != 2 || summary.MQTTReady != 2 || summary.AssignmentsReceived != 1 || summary.TerminalMatched != 1 || summary.UnexpectedFailures != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.ArtifactBytes != 192 || summary.ArtifactHashVerified != 1 || summary.MQTTRebootDisconnects != 1 || summary.MQTTReconnectSuccesses != 1 {
		t.Fatalf("summary evidence = %+v", summary)
	}
	if summary.HTTPStatusCounts["ota_check:200"] != 3 || summary.HTTPLatencyMS["ota_check"]["p50"] == 0 || summary.PeakGoroutines != 12 || summary.PeakHeapAllocBytes != 4096 || summary.ArtifactThroughputBPS <= 0 {
		t.Fatalf("summary runtime metrics = %+v", summary)
	}
}

func TestRunOTADeviceSimulatorWithNoAssignments(t *testing.T) {
	opts := loadOptions{Concurrency: 1, RampUp: "0s", OTA: defaultOTAOptions()}
	opts.OTA.CampaignID, opts.OTA.TargetVersion, opts.OTA.CurrentVersion, opts.OTA.HardwareRevision = "campaign-1", "2.0.0", "1.0.0", "rev-a"
	result := runOTADeviceSimulator(nil, nil, "RTK", "empty-run", "https://api.example.test", "https://token.example.test", nil, 7, opts)
	if result.Status != "PASS" || result.Summary.DevicesSelected != 0 || result.Summary.CampaignID != "campaign-1" {
		t.Fatalf("empty simulation = %+v", result)
	}
	client := newOTAHTTPClient(0)
	if client.Timeout != 30*time.Minute {
		t.Fatalf("default OTA HTTP timeout = %s", client.Timeout)
	}
}

func TestRunOTADeviceSimulatorFailsClosedWhenMQTTBarrierIsIncomplete(t *testing.T) {
	opts := loadOptions{Concurrency: 1, RampUp: "0s", OTA: defaultOTAOptions()}
	opts.OTA.CampaignID, opts.OTA.TargetVersion, opts.OTA.CurrentVersion, opts.OTA.HardwareRevision = "campaign-1", "2.0.0", "1.0.0", "rev-a"
	assignments := []assignment{{Brandname: "RTK", DeviceID: "device-without-certificate", DeviceType: "camera"}}
	result := runOTADeviceSimulator(assignments, nil, "RTK", "barrier-run", "https://api.example.test", "https://token.example.test", nil, 7, opts)
	if result.Status != "FAIL" || len(result.Devices) != 1 || result.Devices[0].ErrorCategory != "initial_mqtt_not_ready" || result.Summary.MQTTReady != 0 {
		t.Fatalf("barrier simulation = %+v", result)
	}
}

func TestLoadOptionsValidateOTAModel(t *testing.T) {
	opts := loadOptions{LoadModel: "ota-device-simulator", OTA: defaultOTAOptions()}
	if err := opts.validateLoadModel(); err == nil || !strings.Contains(err.Error(), "campaign-id") {
		t.Fatalf("missing OTA config error = %v", err)
	}
	opts.OTA.CampaignID, opts.OTA.TargetVersion, opts.OTA.CurrentVersion, opts.OTA.HardwareRevision = "campaign-1", "2.0.0", "1.0.0", "rev-a"
	if err := opts.validateLoadModel(); err != nil {
		t.Fatalf("valid OTA load model: %v", err)
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

func newControlledOTARunner(t *testing.T, cancel context.CancelFunc, checkDecision string, artifactStatus int, failEventStatus, cancelAfterEvent string) (otaDeviceRunner, *httptest.Server) {
	t.Helper()
	payload := []byte("controlled firmware")
	digest := sha256.Sum256(payload)
	assignment := testOTAAssignment(len(payload), hex.EncodeToString(digest[:]))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/v1/device/ota/check":
			if checkDecision != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"decision": checkDecision})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"decision": "assigned", "assignment": assignment})
		case strings.HasSuffix(req.URL.Path, "/artifact-token"):
			_ = json.NewEncoder(w).Encode(otaArtifactAuthorization{DeploymentID: assignment.DeploymentID, ReleaseID: assignment.ReleaseID, URL: server.URL + "/artifact", Manifest: assignment.Manifest})
		case req.URL.Path == "/artifact":
			if artifactStatus != 0 {
				http.Error(w, "artifact rejected", artifactStatus)
				return
			}
			_, _ = w.Write(payload)
		case strings.HasSuffix(req.URL.Path, "/events"):
			var event otaEventRequest
			if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if event.Status == failEventStatus {
				http.Error(w, "event rejected", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			if event.Status == cancelAfterEvent {
				time.AfterFunc(time.Millisecond, cancel)
			}
		default:
			http.NotFound(w, req)
		}
	}))
	return testOTARunner(server.URL), server
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
	config.PollMin, config.PollMax = time.Nanosecond, 2*time.Nanosecond
	return config
}

func testOTAAssignment(size int, digest string) otaAssignment {
	return otaAssignment{
		DeploymentID:  "deployment-1",
		CampaignID:    "campaign-1",
		ReleaseID:     "release-1",
		TargetVersion: "2.0.0",
		Manifest: otaManifest{
			ReleaseID: "release-1", ProductID: "product-1", Version: "2.0.0", BuildNumber: "2", ArtifactSize: int64(size), ArtifactSHA256: digest,
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadWriteCloser struct {
	closed bool
}

func (*trackingReadWriteCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (*trackingReadWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *trackingReadWriteCloser) Close() error {
	c.closed = true
	return nil
}

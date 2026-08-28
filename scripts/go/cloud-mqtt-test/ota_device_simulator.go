package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const otaResultSchemaVersion = 1

type otaOptions struct {
	CampaignID             string  `json:"campaign_id,omitempty"`
	TargetVersion          string  `json:"target_version,omitempty"`
	CurrentVersion         string  `json:"current_version,omitempty"`
	HardwareRevision       string  `json:"hardware_revision,omitempty"`
	AntiRollbackCounter    int     `json:"anti_rollback_counter"`
	PollInterval           string  `json:"poll_interval"`
	UpgradeTimeout         string  `json:"upgrade_timeout"`
	HTTPConcurrency        int     `json:"http_concurrency"`
	DownloadConcurrency    int     `json:"download_concurrency"`
	InstallDelay           string  `json:"install_delay"`
	RebootDelay            string  `json:"reboot_delay"`
	VerifyDelay            string  `json:"verify_delay"`
	StageJitterPercent     float64 `json:"stage_jitter_percent"`
	DownloadFailurePercent float64 `json:"download_failure_percent"`
	VerifyFailurePercent   float64 `json:"verify_failure_percent"`
	InstallFailurePercent  float64 `json:"install_failure_percent"`
	RebootFailurePercent   float64 `json:"reboot_failure_percent"`
	TimeoutPercent         float64 `json:"timeout_percent"`
}

func defaultOTAOptions() otaOptions {
	return otaOptions{
		PollInterval:        "5s",
		UpgradeTimeout:      "30m",
		HTTPConcurrency:     250,
		DownloadConcurrency: 64,
		InstallDelay:        "2s",
		RebootDelay:         "2s",
		VerifyDelay:         "1s",
		StageJitterPercent:  20,
	}
}

func registerOTAFlags(opts *otaOptions) {
	flag.StringVar(&opts.CampaignID, "ota-campaign-id", opts.CampaignID, "required OTA campaign id")
	flag.StringVar(&opts.TargetVersion, "ota-target-version", opts.TargetVersion, "required OTA target version")
	flag.StringVar(&opts.CurrentVersion, "ota-current-version", opts.CurrentVersion, "required initial device firmware version")
	flag.StringVar(&opts.HardwareRevision, "ota-hardware-revision", opts.HardwareRevision, "required device hardware revision")
	flag.IntVar(&opts.AntiRollbackCounter, "ota-anti-rollback-counter", opts.AntiRollbackCounter, "device anti-rollback counter")
	flag.StringVar(&opts.PollInterval, "ota-poll-interval", opts.PollInterval, "OTA assignment poll interval")
	flag.StringVar(&opts.UpgradeTimeout, "ota-upgrade-timeout", opts.UpgradeTimeout, "per-device OTA deadline after MQTT ready")
	flag.IntVar(&opts.HTTPConcurrency, "ota-http-concurrency", opts.HTTPConcurrency, "maximum concurrent OTA HTTP requests")
	flag.IntVar(&opts.DownloadConcurrency, "ota-download-concurrency", opts.DownloadConcurrency, "maximum concurrent artifact streams")
	flag.StringVar(&opts.InstallDelay, "ota-install-delay", opts.InstallDelay, "simulated installation delay")
	flag.StringVar(&opts.RebootDelay, "ota-reboot-delay", opts.RebootDelay, "simulated reboot delay")
	flag.StringVar(&opts.VerifyDelay, "ota-verify-delay", opts.VerifyDelay, "simulated post-reboot verification delay")
	flag.Float64Var(&opts.StageJitterPercent, "ota-stage-jitter-percent", opts.StageJitterPercent, "deterministic OTA timing jitter percentage")
	flag.Float64Var(&opts.DownloadFailurePercent, "ota-download-failure-percent", opts.DownloadFailurePercent, "deterministic download failure percentage")
	flag.Float64Var(&opts.VerifyFailurePercent, "ota-verify-failure-percent", opts.VerifyFailurePercent, "deterministic artifact verification failure percentage")
	flag.Float64Var(&opts.InstallFailurePercent, "ota-install-failure-percent", opts.InstallFailurePercent, "deterministic installation failure percentage")
	flag.Float64Var(&opts.RebootFailurePercent, "ota-reboot-failure-percent", opts.RebootFailurePercent, "deterministic reboot failure percentage")
	flag.Float64Var(&opts.TimeoutPercent, "ota-timeout-percent", opts.TimeoutPercent, "deterministic OTA timeout percentage")
}

func (opts otaOptions) validate() error {
	required := []struct {
		name  string
		value string
	}{
		{"--ota-campaign-id", opts.CampaignID},
		{"--ota-target-version", opts.TargetVersion},
		{"--ota-current-version", opts.CurrentVersion},
		{"--ota-hardware-revision", opts.HardwareRevision},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required for ota-device-simulator", item.name)
		}
	}
	if opts.AntiRollbackCounter < 0 {
		return errors.New("--ota-anti-rollback-counter must be non-negative")
	}
	for _, item := range []struct {
		name      string
		value     string
		allowZero bool
	}{
		{"--ota-poll-interval", opts.PollInterval, false},
		{"--ota-upgrade-timeout", opts.UpgradeTimeout, false},
		{"--ota-install-delay", opts.InstallDelay, true},
		{"--ota-reboot-delay", opts.RebootDelay, true},
		{"--ota-verify-delay", opts.VerifyDelay, true},
	} {
		d, err := time.ParseDuration(strings.TrimSpace(item.value))
		if err != nil || d < 0 || (!item.allowZero && d == 0) {
			return fmt.Errorf("%s must be a valid %sduration", item.name, map[bool]string{true: "non-negative ", false: "positive "}[item.allowZero])
		}
	}
	if opts.HTTPConcurrency <= 0 {
		return errors.New("--ota-http-concurrency must be positive")
	}
	if opts.DownloadConcurrency <= 0 || opts.DownloadConcurrency > opts.HTTPConcurrency {
		return errors.New("--ota-download-concurrency must be positive and no greater than --ota-http-concurrency")
	}
	percentages := []struct {
		name  string
		value float64
	}{
		{"--ota-stage-jitter-percent", opts.StageJitterPercent},
		{"--ota-download-failure-percent", opts.DownloadFailurePercent},
		{"--ota-verify-failure-percent", opts.VerifyFailurePercent},
		{"--ota-install-failure-percent", opts.InstallFailurePercent},
		{"--ota-reboot-failure-percent", opts.RebootFailurePercent},
		{"--ota-timeout-percent", opts.TimeoutPercent},
	}
	for _, item := range percentages {
		if item.value < 0 || item.value > 100 {
			return fmt.Errorf("%s must be between 0 and 100", item.name)
		}
	}
	total := opts.DownloadFailurePercent + opts.VerifyFailurePercent + opts.InstallFailurePercent + opts.RebootFailurePercent + opts.TimeoutPercent
	if total > 100.0000001 {
		return errors.New("OTA failure percentages must sum to no more than 100")
	}
	return nil
}

type otaRuntimeConfig struct {
	otaOptions
	PollEvery time.Duration
	Timeout   time.Duration
	Install   time.Duration
	Reboot    time.Duration
	Verify    time.Duration
}

func (opts otaOptions) runtimeConfig() otaRuntimeConfig {
	poll, _ := time.ParseDuration(opts.PollInterval)
	timeout, _ := time.ParseDuration(opts.UpgradeTimeout)
	install, _ := time.ParseDuration(opts.InstallDelay)
	reboot, _ := time.ParseDuration(opts.RebootDelay)
	verify, _ := time.ParseDuration(opts.VerifyDelay)
	return otaRuntimeConfig{otaOptions: opts, PollEvery: poll, Timeout: timeout, Install: install, Reboot: reboot, Verify: verify}
}

type otaManifest struct {
	ReleaseID         string    `json:"release_id"`
	SKUID             string    `json:"sku_id"`
	Version           string    `json:"version"`
	BuildNumber       string    `json:"build_number"`
	ArtifactSize      int64     `json:"artifact_size"`
	ArtifactSHA256    string    `json:"artifact_sha256"`
	HardwareRevisions []string  `json:"hardware_revisions"`
	MinimumVersion    string    `json:"minimum_current_version,omitempty"`
	AntiRollback      int       `json:"anti_rollback_counter"`
	SigningAlgorithm  string    `json:"signing_algorithm"`
	SigningKeyID      string    `json:"signing_key_id"`
	Signature         string    `json:"signature"`
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
}

type otaAssignment struct {
	DeploymentID  string      `json:"deployment_id"`
	CampaignID    string      `json:"campaign_id"`
	ReleaseID     string      `json:"release_id"`
	TargetVersion string      `json:"target_version"`
	Manifest      otaManifest `json:"manifest"`
}

type otaCheckResponse struct {
	Decision   string         `json:"decision"`
	ReasonCode string         `json:"reason_code"`
	NotBefore  time.Time      `json:"not_before"`
	Assignment *otaAssignment `json:"assignment"`
}

type otaArtifactAuthorization struct {
	DeploymentID   string      `json:"deployment_id"`
	ReleaseID      string      `json:"release_id"`
	URL            string      `json:"url"`
	ExpiresAt      time.Time   `json:"expires_at"`
	RangeSupported bool        `json:"range_supported"`
	Manifest       otaManifest `json:"manifest"`
}

type otaEventRequest struct {
	Sequence           int64     `json:"sequence"`
	Status             string    `json:"status"`
	ObservedVersion    string    `json:"observed_version"`
	ProgressPercentage float64   `json:"progress_percentage,omitempty"`
	ErrorCode          string    `json:"error_code,omitempty"`
	ErrorReason        string    `json:"error_reason,omitempty"`
	DeviceTimestamp    time.Time `json:"device_timestamp"`
}

type otaDeviceResult struct {
	SchemaVersion          int                  `json:"schema_version"`
	DeviceID               string               `json:"device_id"`
	DeviceType             string               `json:"device_type"`
	CampaignID             string               `json:"campaign_id,omitempty"`
	DeploymentID           string               `json:"deployment_id,omitempty"`
	ReleaseID              string               `json:"release_id,omitempty"`
	CurrentVersion         string               `json:"current_version"`
	TargetVersion          string               `json:"target_version"`
	ExpectedTerminal       string               `json:"expected_terminal"`
	ActualTerminal         string               `json:"actual_terminal,omitempty"`
	InjectedFailure        string               `json:"injected_failure,omitempty"`
	TerminalMatched        bool                 `json:"terminal_matched"`
	LastSequence           int64                `json:"last_sequence,omitempty"`
	LastCheckDecision      string               `json:"last_check_decision,omitempty"`
	LastCheckReason        string               `json:"last_check_reason,omitempty"`
	CheckAttempts          int                  `json:"check_attempts"`
	HTTPRetries            int                  `json:"http_retries,omitempty"`
	HTTPStatusCounts       map[string]int       `json:"http_status_counts,omitempty"`
	HTTPLatencyMS          map[string][]float64 `json:"http_latency_ms,omitempty"`
	StageTimestamps        map[string]string    `json:"stage_timestamps,omitempty"`
	ArtifactBytes          int64                `json:"artifact_bytes,omitempty"`
	ArtifactSHA256         string               `json:"artifact_sha256,omitempty"`
	ArtifactSizeVerified   bool                 `json:"artifact_size_verified"`
	ArtifactHashVerified   bool                 `json:"artifact_hash_verified"`
	MQTTInitialConnected   bool                 `json:"mqtt_initial_connected"`
	MQTTRebootDisconnected bool                 `json:"mqtt_reboot_disconnected"`
	MQTTReconnectSucceeded bool                 `json:"mqtt_reconnect_succeeded"`
	ErrorCategory          string               `json:"error_category,omitempty"`
	Error                  string               `json:"error,omitempty"`
}

type otaSummary struct {
	SchemaVersion          int                           `json:"schema_version"`
	CampaignID             string                        `json:"campaign_id"`
	TargetVersion          string                        `json:"target_version"`
	DevicesSelected        int                           `json:"devices_selected"`
	MQTTReady              int                           `json:"mqtt_ready"`
	AssignmentsReceived    int                           `json:"assignments_received"`
	TerminalExpected       int                           `json:"terminal_expected"`
	TerminalMatched        int                           `json:"terminal_matched"`
	ByExpectedTerminal     map[string]int                `json:"by_expected_terminal"`
	ByActualTerminal       map[string]int                `json:"by_actual_terminal"`
	ArtifactBytes          int64                         `json:"artifact_bytes"`
	ArtifactHashVerified   int                           `json:"artifact_hash_verified"`
	MQTTRebootDisconnects  int                           `json:"mqtt_reboot_disconnects"`
	MQTTReconnectSuccesses int                           `json:"mqtt_reconnect_successes"`
	UnexpectedFailures     int                           `json:"unexpected_failures"`
	HTTPStatusCounts       map[string]int                `json:"http_status_counts,omitempty"`
	HTTPLatencyMS          map[string]map[string]float64 `json:"http_latency_ms,omitempty"`
	ArtifactThroughputBPS  float64                       `json:"artifact_throughput_bytes_per_second"`
	PeakGoroutines         int                           `json:"peak_goroutines"`
	PeakHeapAllocBytes     uint64                        `json:"peak_heap_alloc_bytes"`
	ElapsedSeconds         float64                       `json:"elapsed_seconds"`
	DeviceResultsFile      string                        `json:"device_results_file"`
}

type otaSimulationResult struct {
	Status  string
	Totals  mqttIOTotals
	Summary otaSummary
	Devices []otaDeviceResult
	Notes   []string
}

type otaRuntimePeaks struct {
	mu         sync.Mutex
	goroutines int
	heap       uint64
}

func startOTARuntimeSampler(ctx context.Context) *otaRuntimePeaks {
	peaks := &otaRuntimePeaks{}
	sample := func() {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		peaks.mu.Lock()
		if n := runtime.NumGoroutine(); n > peaks.goroutines {
			peaks.goroutines = n
		}
		if stats.HeapAlloc > peaks.heap {
			peaks.heap = stats.HeapAlloc
		}
		peaks.mu.Unlock()
	}
	sample()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				sample()
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return peaks
}

func (p *otaRuntimePeaks) values() (int, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.goroutines, p.heap
}

type otaDeviceRunner struct {
	config         otaRuntimeConfig
	seed           int
	runID          string
	brandname      string
	apiBaseURL     string
	httpClient     *http.Client
	artifactClient *http.Client
	httpSem        chan struct{}
	downloadSem    chan struct{}
	tokenOptions   tokenRequestOptions
	reconnectHook  func(context.Context, *sustainedDeviceSession) error
}

func runOTADeviceSimulator(assignments []assignment, certs []certRecord, brandname, runID, apiBaseURL, tokenBaseURL string, mqttTargets []mqttEndpointTarget, seed int, opts loadOptions) otaSimulationResult {
	started := time.Now()
	result := otaSimulationResult{Status: "FAIL"}
	config := opts.OTA.runtimeConfig()
	ctx, cancel := context.WithCancel(context.Background())
	peaks := startOTARuntimeSampler(ctx)
	defer func() {
		cancel()
		// Give the final sampler call a scheduling opportunity without adding a
		// material delay to the load test.
		runtime.Gosched()
	}()

	certByID := map[string]certRecord{}
	for _, cert := range certs {
		certByID[brandDeviceKey(cert.Brandname, cert.DeviceID)] = cert
		certByID[cert.DeviceID] = cert
	}
	ramp, _ := time.ParseDuration(strings.TrimSpace(opts.RampUp))
	connectBudget := ramp + 5*time.Minute
	if connectBudget < time.Minute {
		connectBudget = time.Minute
	}
	connectDeadline := time.Now().Add(connectBudget)
	sessions := connectSustainedDevicesPacedUntilWithOptions(assignments, certByID, brandname, runID, tokenBaseURL, mqttTargets, opts.Concurrency, connectDeadline, ramp, opts.deviceTokenOptions(), &result.Totals)
	result.Totals.ActiveConnections = int64(len(sessions))
	result.Totals.ActiveSubscriptions = int64(len(sessions))

	if len(sessions) != len(assignments) {
		connected := map[string]bool{}
		for _, session := range sessions {
			connected[session.Record.DeviceID] = true
			closeOTADeviceMQTT(&session)
		}
		for _, item := range assignments {
			fault, terminal := selectOTAFault(config.otaOptions, seed, runID, item.DeviceID)
			detail := newOTADeviceResult(item.DeviceID, item.DeviceType, config, fault, terminal)
			detail.MQTTInitialConnected = connected[item.DeviceID]
			if !detail.MQTTInitialConnected {
				detail.ErrorCategory = "initial_mqtt_not_ready"
				detail.Error = "device did not pass the initial MQTT ready barrier"
			}
			result.Devices = append(result.Devices, detail)
		}
		result.Notes = append(result.Notes, fmt.Sprintf("OTA MQTT ready barrier connected %d/%d devices", len(sessions), len(assignments)))
		result.Summary = summarizeOTA(config, result.Devices, len(sessions), started, peaks)
		return result
	}

	runner := otaDeviceRunner{
		config:         config,
		seed:           seed,
		runID:          runID,
		brandname:      brandname,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		httpClient:     newOTAHTTPClient(config.Timeout),
		artifactClient: newOTAHTTPClient(config.Timeout),
		httpSem:        make(chan struct{}, config.HTTPConcurrency),
		downloadSem:    make(chan struct{}, config.DownloadConcurrency),
		tokenOptions:   opts.deviceTokenOptions(),
	}

	results := make(chan otaDeviceResult, len(sessions))
	var wg sync.WaitGroup
	for idx := range sessions {
		session := sessions[idx]
		wg.Add(1)
		go func() {
			defer wg.Done()
			deviceCtx, deviceCancel := context.WithTimeout(ctx, config.Timeout)
			defer deviceCancel()
			results <- runner.runDevice(deviceCtx, session)
		}()
	}
	wg.Wait()
	result.Totals.ActiveConnections = 0
	result.Totals.ActiveSubscriptions = 0
	close(results)
	for detail := range results {
		result.Devices = append(result.Devices, detail)
	}
	sort.Slice(result.Devices, func(i, j int) bool { return result.Devices[i].DeviceID < result.Devices[j].DeviceID })
	result.Summary = summarizeOTA(config, result.Devices, len(sessions), started, peaks)
	result.Summary.DeviceResultsFile = "ota-devices.jsonl"
	if result.Summary.TerminalMatched == len(assignments) && result.Summary.AssignmentsReceived == len(assignments) && result.Summary.UnexpectedFailures == 0 {
		result.Status = "PASS"
	}
	return result
}

func newOTAHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 512
	transport.MaxIdleConnsPerHost = 512
	transport.MaxConnsPerHost = 0
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func newOTADeviceResult(deviceID, deviceType string, config otaRuntimeConfig, fault, expectedTerminal string) otaDeviceResult {
	return otaDeviceResult{
		SchemaVersion:    otaResultSchemaVersion,
		DeviceID:         deviceID,
		DeviceType:       deviceType,
		CurrentVersion:   config.CurrentVersion,
		TargetVersion:    config.TargetVersion,
		ExpectedTerminal: expectedTerminal,
		InjectedFailure:  fault,
		HTTPStatusCounts: map[string]int{},
		HTTPLatencyMS:    map[string][]float64{},
		StageTimestamps:  map[string]string{},
	}
}

func (r *otaDeviceRunner) runDevice(ctx context.Context, session sustainedDeviceSession) otaDeviceResult {
	fault, expectedTerminal := selectOTAFault(r.config.otaOptions, r.seed, r.runID, session.Record.DeviceID)
	result := newOTADeviceResult(session.Record.DeviceID, session.Record.DeviceType, r.config, fault, expectedTerminal)
	result.MQTTInitialConnected = session.Conn != nil && session.Reader != nil
	defer closeOTADeviceMQTT(&session)

	assignment, err := r.waitForAssignment(ctx, session, &result)
	if err != nil {
		return unexpectedOTAResult(result, "assignment_failed", err)
	}
	result.CampaignID = assignment.CampaignID
	result.DeploymentID = assignment.DeploymentID
	result.ReleaseID = assignment.ReleaseID
	if err := validateOTAAssignment(r.config, assignment); err != nil {
		return unexpectedOTAResult(result, "assignment_mismatch", err)
	}

	sequence := int64(2)
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "downloading", ObservedVersion: r.config.CurrentVersion, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "downloading_event_failed", err)
	}
	result.LastSequence = sequence
	result.StageTimestamps["downloading"] = nowISO()

	if fault == "download" {
		return r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "SIMULATED_DOWNLOAD_FAILURE", "simulated artifact download failure", "failed")
	}

	if err := acquireOTA(ctx, r.downloadSem); err != nil {
		return unexpectedOTAResult(result, "download_slot_failed", err)
	}
	result.ArtifactBytes, result.ArtifactSHA256, err = r.downloadArtifact(ctx, session.DeviceTokenManager, assignment, &result)
	releaseOTA(r.downloadSem)
	if err != nil {
		return unexpectedOTAResult(result, "artifact_download_failed", err)
	}
	result.ArtifactSizeVerified = result.ArtifactBytes == assignment.Manifest.ArtifactSize
	result.ArtifactHashVerified = strings.EqualFold(result.ArtifactSHA256, assignment.Manifest.ArtifactSHA256)
	if !result.ArtifactSizeVerified || !result.ArtifactHashVerified {
		return unexpectedOTAResult(result, "artifact_verification_failed", fmt.Errorf("artifact verification mismatch: bytes=%d expected=%d sha256=%s expected_sha256=%s", result.ArtifactBytes, assignment.Manifest.ArtifactSize, result.ArtifactSHA256, assignment.Manifest.ArtifactSHA256))
	}
	if fault == "verify" {
		return r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "SIMULATED_VERIFY_FAILURE", "simulated artifact verification failure", "failed")
	}

	sequence++
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "downloaded", ObservedVersion: r.config.CurrentVersion, ProgressPercentage: 100, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "downloaded_event_failed", err)
	}
	result.LastSequence = sequence
	result.StageTimestamps["downloaded"] = nowISO()

	if err := sleepOTAStage(ctx, r.config.Install, r.config.StageJitterPercent, r.seed, r.runID, result.DeviceID, "install"); err != nil {
		return unexpectedOTAResult(result, "install_delay_failed", err)
	}
	sequence++
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "installing", ObservedVersion: r.config.CurrentVersion, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "installing_event_failed", err)
	}
	result.LastSequence = sequence
	result.StageTimestamps["installing"] = nowISO()
	if fault == "install" {
		return r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "SIMULATED_INSTALL_FAILURE", "simulated firmware installation failure", "failed")
	}

	if err := sleepOTAStage(ctx, r.config.Reboot, r.config.StageJitterPercent, r.seed, r.runID, result.DeviceID, "reboot"); err != nil {
		return unexpectedOTAResult(result, "reboot_delay_failed", err)
	}
	sequence++
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "rebooting", ObservedVersion: r.config.CurrentVersion, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "rebooting_event_failed", err)
	}
	result.LastSequence = sequence
	result.StageTimestamps["rebooting"] = nowISO()
	closeOTADeviceMQTT(&session)
	result.MQTTRebootDisconnected = true

	if fault == "reboot" {
		return r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "SIMULATED_REBOOT_FAILURE", "simulated device reboot failure", "failed")
	}
	if fault == "timeout" {
		return r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "SIMULATED_OTA_TIMEOUT", "simulated device upgrade timeout", "timed_out")
	}

	if err := r.reconnectDeviceMQTT(ctx, &session); err != nil {
		failed := r.finishInjectedFailure(ctx, session.DeviceTokenManager, assignment.DeploymentID, result, sequence+1, "MQTT_RECONNECT_FAILED", redactedError(err), "failed")
		failed.ExpectedTerminal = expectedTerminal
		failed.TerminalMatched = false
		failed.ErrorCategory = "mqtt_reconnect_failed"
		failed.Error = redactedError(err)
		return failed
	}
	result.MQTTReconnectSucceeded = true
	result.StageTimestamps["mqtt_reconnected"] = nowISO()

	sequence++
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "verifying", ObservedVersion: r.config.TargetVersion, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "verifying_event_failed", err)
	}
	result.LastSequence = sequence
	result.StageTimestamps["verifying"] = nowISO()
	if err := sleepOTAStage(ctx, r.config.Verify, r.config.StageJitterPercent, r.seed, r.runID, result.DeviceID, "verify"); err != nil {
		return unexpectedOTAResult(result, "verify_delay_failed", err)
	}

	sequence++
	if err := r.reportEvent(ctx, session.DeviceTokenManager, assignment.DeploymentID, otaEventRequest{
		Sequence: sequence, Status: "succeeded", ObservedVersion: r.config.TargetVersion, DeviceTimestamp: time.Now().UTC(),
	}, &result); err != nil {
		return unexpectedOTAResult(result, "succeeded_event_failed", err)
	}
	result.LastSequence = sequence
	result.ActualTerminal = "succeeded"
	result.TerminalMatched = result.ExpectedTerminal == result.ActualTerminal
	result.StageTimestamps["succeeded"] = nowISO()
	return result
}

func (r *otaDeviceRunner) reconnectDeviceMQTT(ctx context.Context, session *sustainedDeviceSession) error {
	if r.reconnectHook != nil {
		return r.reconnectHook(ctx, session)
	}
	return r.reconnectMQTT(ctx, session)
}

func (r *otaDeviceRunner) waitForAssignment(ctx context.Context, session sustainedDeviceSession, result *otaDeviceResult) (otaAssignment, error) {
	for {
		var response otaCheckResponse
		body := map[string]any{
			"current_version":       r.config.CurrentVersion,
			"hardware_revision":     r.config.HardwareRevision,
			"anti_rollback_counter": r.config.AntiRollbackCounter,
			"capabilities":          []string{"full_image"},
		}
		result.CheckAttempts++
		err := r.doDeviceJSON(ctx, session.DeviceTokenManager, http.MethodPost, "/v1/device/ota/check", body, &response, "check", result)
		if err != nil {
			return otaAssignment{}, err
		}
		result.LastCheckDecision = response.Decision
		result.LastCheckReason = response.ReasonCode
		if response.Decision == "assigned" {
			if response.Assignment == nil {
				return otaAssignment{}, errors.New("OTA check returned assigned without assignment")
			}
			return *response.Assignment, nil
		}
		if response.Decision != "deferred" && response.Decision != "no_update" {
			return otaAssignment{}, fmt.Errorf("unsupported OTA check decision %q", response.Decision)
		}
		wait := jitteredOTADuration(r.config.PollEvery, r.config.StageJitterPercent, r.seed, r.runID, result.DeviceID, "poll-"+strconv.Itoa(result.CheckAttempts))
		if !response.NotBefore.IsZero() {
			if until := time.Until(response.NotBefore); until > wait {
				wait = until
			}
		}
		if err := sleepContext(ctx, wait); err != nil {
			return otaAssignment{}, fmt.Errorf("OTA assignment deadline after decision=%s reason=%s: %w", response.Decision, response.ReasonCode, err)
		}
	}
}

func validateOTAAssignment(config otaRuntimeConfig, assignment otaAssignment) error {
	if assignment.DeploymentID == "" || assignment.ReleaseID == "" {
		return errors.New("OTA assignment missing deployment or release id")
	}
	if assignment.CampaignID != config.CampaignID {
		return fmt.Errorf("campaign id %q, want %q", assignment.CampaignID, config.CampaignID)
	}
	if assignment.TargetVersion != config.TargetVersion {
		return fmt.Errorf("target version %q, want %q", assignment.TargetVersion, config.TargetVersion)
	}
	m := assignment.Manifest
	if m.ReleaseID != assignment.ReleaseID || m.Version != assignment.TargetVersion {
		return errors.New("manifest identity does not match assignment")
	}
	if m.ArtifactSize <= 0 {
		return errors.New("manifest artifact size must be positive")
	}
	digest, err := hex.DecodeString(m.ArtifactSHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("manifest artifact sha256 is invalid")
	}
	if !otaHardwareAllowed(m.HardwareRevisions, config.HardwareRevision) {
		return fmt.Errorf("hardware revision %q is not allowed by manifest", config.HardwareRevision)
	}
	if m.AntiRollback < config.AntiRollbackCounter {
		return fmt.Errorf("manifest anti-rollback counter %d is below device counter %d", m.AntiRollback, config.AntiRollbackCounter)
	}
	if strings.TrimSpace(m.SigningAlgorithm) == "" || strings.TrimSpace(m.SigningKeyID) == "" || strings.TrimSpace(m.Signature) == "" {
		return errors.New("manifest signature metadata is incomplete")
	}
	if !m.ExpiresAt.IsZero() && !time.Now().UTC().Before(m.ExpiresAt) {
		return errors.New("manifest signature metadata is expired")
	}
	return nil
}

func otaHardwareAllowed(allowed []string, current string) bool {
	for _, value := range allowed {
		if value == "*" || value == current {
			return true
		}
	}
	return false
}

func (r *otaDeviceRunner) artifactAuthorization(ctx context.Context, manager *tokenManager, assignment otaAssignment, result *otaDeviceResult) (otaArtifactAuthorization, error) {
	var response otaArtifactAuthorization
	path := "/v1/device/ota/deployments/" + assignment.DeploymentID + "/artifact-token"
	if err := r.doDeviceJSON(ctx, manager, http.MethodPost, path, nil, &response, "artifact_token", result); err != nil {
		return response, err
	}
	if response.DeploymentID != assignment.DeploymentID || response.ReleaseID != assignment.ReleaseID || strings.TrimSpace(response.URL) == "" {
		return response, errors.New("artifact authorization identity is invalid")
	}
	if !sameOTAManifest(response.Manifest, assignment.Manifest) {
		return response, errors.New("artifact authorization manifest differs from assignment")
	}
	return response, nil
}

func sameOTAManifest(left, right otaManifest) bool {
	return left.ReleaseID == right.ReleaseID &&
		left.SKUID == right.SKUID &&
		left.Version == right.Version &&
		left.BuildNumber == right.BuildNumber &&
		left.ArtifactSize == right.ArtifactSize &&
		strings.EqualFold(left.ArtifactSHA256, right.ArtifactSHA256) &&
		strings.Join(left.HardwareRevisions, "\x00") == strings.Join(right.HardwareRevisions, "\x00") &&
		left.MinimumVersion == right.MinimumVersion &&
		left.AntiRollback == right.AntiRollback &&
		left.SigningAlgorithm == right.SigningAlgorithm &&
		left.SigningKeyID == right.SigningKeyID &&
		left.Signature == right.Signature &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func (r *otaDeviceRunner) downloadArtifact(ctx context.Context, manager *tokenManager, assignment otaAssignment, result *otaDeviceResult) (int64, string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			result.HTTPRetries++
			if err := sleepContext(ctx, otaRetryDelay(attempt, r.seed, r.runID, result.DeviceID)); err != nil {
				return 0, "", err
			}
		}
		auth, err := r.artifactAuthorization(ctx, manager, assignment, result)
		if err != nil {
			return 0, "", err
		}
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, auth.URL, nil)
		if err != nil {
			return 0, "", err
		}
		resp, err := r.artifactClient.Do(req)
		result.HTTPLatencyMS["artifact_download"] = append(result.HTTPLatencyMS["artifact_download"], float64(time.Since(started).Microseconds())/1000)
		if err != nil {
			lastErr = err
			continue
		}
		result.HTTPStatusCounts["artifact_download:"+strconv.Itoa(resp.StatusCode)]++
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("artifact download HTTP %d: %s", resp.StatusCode, safeHTTPErrorDetail(payload))
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return 0, "", lastErr
			}
			continue
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, resp.Body)
		closeErr := resp.Body.Close()
		if copyErr != nil {
			lastErr = copyErr
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			continue
		}
		return n, hex.EncodeToString(h.Sum(nil)), nil
	}
	if lastErr == nil {
		lastErr = errors.New("artifact download retries exhausted")
	}
	return 0, "", lastErr
}

func (r *otaDeviceRunner) reportEvent(ctx context.Context, manager *tokenManager, deploymentID string, event otaEventRequest, result *otaDeviceResult) error {
	path := "/v1/device/ota/deployments/" + deploymentID + "/events"
	return r.doDeviceJSON(ctx, manager, http.MethodPost, path, event, nil, "event_"+event.Status, result)
}

func (r *otaDeviceRunner) doDeviceJSON(ctx context.Context, manager *tokenManager, method, path string, body any, dest any, operation string, result *otaDeviceResult) error {
	payload := []byte(nil)
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			result.HTTPRetries++
			if err := sleepContext(ctx, otaRetryDelay(attempt, r.seed, r.runID, result.DeviceID)); err != nil {
				return err
			}
		}
		if err := acquireOTA(ctx, r.httpSem); err != nil {
			return err
		}
		token, tokenErr := manager.Token(minOTATimeout(ctx, 10*time.Second))
		if tokenErr != nil {
			releaseOTA(r.httpSem)
			lastErr = tokenErr
			continue
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, r.apiBaseURL+path, bytes.NewReader(payload))
		if reqErr != nil {
			releaseOTA(r.httpSem)
			return reqErr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		started := time.Now()
		resp, requestErr := r.httpClient.Do(req)
		releaseOTA(r.httpSem)
		result.HTTPLatencyMS[operation] = append(result.HTTPLatencyMS[operation], float64(time.Since(started).Microseconds())/1000)
		if requestErr != nil {
			lastErr = requestErr
			continue
		}
		responsePayload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		result.HTTPStatusCounts[operation+":"+strconv.Itoa(resp.StatusCode)]++
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if dest != nil && len(responsePayload) > 0 {
				if err := json.Unmarshal(responsePayload, dest); err != nil {
					return fmt.Errorf("decode %s response: %w", operation, err)
				}
			}
			return nil
		}
		lastErr = fmt.Errorf("%s HTTP %d: %s", operation, resp.StatusCode, safeHTTPErrorDetail(responsePayload))
		if resp.StatusCode == http.StatusUnauthorized {
			invalidateOTAToken(manager)
		}
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return lastErr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s retries exhausted", operation)
	}
	return lastErr
}

func invalidateOTAToken(manager *tokenManager) {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	manager.bundle = tokenBundle{}
	manager.mu.Unlock()
}

func minOTATimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < fallback {
			return remaining
		}
	}
	return fallback
}

func (r *otaDeviceRunner) finishInjectedFailure(ctx context.Context, manager *tokenManager, deploymentID string, result otaDeviceResult, sequence int64, code, reason, status string) otaDeviceResult {
	err := r.reportEvent(ctx, manager, deploymentID, otaEventRequest{
		Sequence: sequence, Status: status, ObservedVersion: r.config.CurrentVersion, ErrorCode: code, ErrorReason: reason, DeviceTimestamp: time.Now().UTC(),
	}, &result)
	if err != nil {
		return unexpectedOTAResult(result, "injected_terminal_event_failed", err)
	}
	result.LastSequence = sequence
	result.ActualTerminal = status
	result.TerminalMatched = result.ExpectedTerminal == status
	result.ErrorCategory = strings.ToLower(code)
	result.Error = reason
	result.StageTimestamps[status] = nowISO()
	return result
}

func unexpectedOTAResult(result otaDeviceResult, category string, err error) otaDeviceResult {
	result.TerminalMatched = false
	result.ErrorCategory = category
	result.Error = redactedError(err)
	return result
}

func (r *otaDeviceRunner) reconnectMQTT(ctx context.Context, session *sustainedDeviceSession) error {
	if session == nil || session.DeviceTokenManager == nil {
		return errors.New("device token manager is unavailable for MQTT reconnect")
	}
	token, err := session.DeviceTokenManager.Token(minOTATimeout(ctx, 10*time.Second))
	if err != nil {
		return fmt.Errorf("renew device token: %w", err)
	}
	target := session.MQTTTarget
	conn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:    session.Record.DeviceID,
		DeviceType:  session.Record.DeviceType,
		Brandname:   firstNonEmpty(session.Assignment.Brandname, session.Record.Brandname, r.brandname),
		RunID:       r.runID,
		DeviceToken: token,
		Dial: func() (io.ReadWriteCloser, error) {
			return tls.DialWithDialer(&net.Dialer{Timeout: minOTATimeout(ctx, 10*time.Second)}, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)), &tls.Config{InsecureSkipVerify: true})
		},
		Timeout:          minOTATimeout(ctx, 10*time.Second),
		KeepAliveSeconds: sustainedMQTTKeepAliveSeconds,
		Now:              time.Now,
	}, "device", token)
	if err != nil {
		return err
	}
	deltaTopic := "$vc/devices/" + session.Record.DeviceID + "/shadow/update/delta"
	if err := mqttSubscribe(conn, 1, deltaTopic); err != nil {
		_ = conn.Close()
		return err
	}
	clearConnDeadline(conn)
	locked := &lockedReadWriteCloser{ReadWriteCloser: conn}
	session.Conn = locked
	session.Reader = startSustainedDeviceReader(locked)
	return nil
}

func closeOTADeviceMQTT(session *sustainedDeviceSession) {
	if session == nil {
		return
	}
	if session.Reader != nil {
		session.Reader.Close()
		session.Reader = nil
	}
	if session.Conn != nil {
		_ = session.Conn.Close()
		session.Conn = nil
	}
}

func acquireOTA(ctx context.Context, semaphore chan struct{}) error {
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseOTA(semaphore chan struct{}) {
	select {
	case <-semaphore:
	default:
	}
}

func selectOTAFault(opts otaOptions, seed int, runID, deviceID string) (string, string) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", seed, runID, deviceID)))
	value := float64(binary.BigEndian.Uint64(digest[:8])%1_000_000) / 10_000
	cumulative := 0.0
	for _, item := range []struct {
		name     string
		percent  float64
		terminal string
	}{
		{"download", opts.DownloadFailurePercent, "failed"},
		{"verify", opts.VerifyFailurePercent, "failed"},
		{"install", opts.InstallFailurePercent, "failed"},
		{"reboot", opts.RebootFailurePercent, "failed"},
		{"timeout", opts.TimeoutPercent, "timed_out"},
	} {
		cumulative += item.percent
		if value < cumulative {
			return item.name, item.terminal
		}
	}
	return "", "succeeded"
}

func jitteredOTADuration(base time.Duration, percent float64, seed int, runID, deviceID, stage string) time.Duration {
	if base <= 0 || percent <= 0 {
		return base
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", seed, runID, deviceID, stage)))
	unit := float64(binary.BigEndian.Uint64(digest[:8])) / float64(^uint64(0))
	factor := 1 + ((unit*2 - 1) * percent / 100)
	if factor < 0 {
		factor = 0
	}
	return time.Duration(float64(base) * factor)
}

func sleepOTAStage(ctx context.Context, base time.Duration, percent float64, seed int, runID, deviceID, stage string) error {
	return sleepContext(ctx, jitteredOTADuration(base, percent, seed, runID, deviceID, stage))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func otaRetryDelay(attempt, seed int, runID, deviceID string) time.Duration {
	base := time.Duration(1<<minInt(attempt-1, 4)) * 100 * time.Millisecond
	return jitteredOTADuration(base, 20, seed, runID, deviceID, "retry-"+strconv.Itoa(attempt))
}

func summarizeOTA(config otaRuntimeConfig, devices []otaDeviceResult, mqttReady int, started time.Time, peaks *otaRuntimePeaks) otaSummary {
	elapsed := time.Since(started)
	summary := otaSummary{
		SchemaVersion:      otaResultSchemaVersion,
		CampaignID:         config.CampaignID,
		TargetVersion:      config.TargetVersion,
		DevicesSelected:    len(devices),
		MQTTReady:          mqttReady,
		TerminalExpected:   len(devices),
		ByExpectedTerminal: map[string]int{},
		ByActualTerminal:   map[string]int{},
		HTTPStatusCounts:   map[string]int{},
		HTTPLatencyMS:      map[string]map[string]float64{},
		ElapsedSeconds:     elapsed.Seconds(),
		DeviceResultsFile:  "ota-devices.jsonl",
	}
	latencies := map[string][]float64{}
	for _, device := range devices {
		summary.ByExpectedTerminal[device.ExpectedTerminal]++
		if device.ActualTerminal != "" {
			summary.ByActualTerminal[device.ActualTerminal]++
		}
		if device.CampaignID == config.CampaignID && device.DeploymentID != "" {
			summary.AssignmentsReceived++
		}
		if device.TerminalMatched {
			summary.TerminalMatched++
		} else {
			summary.UnexpectedFailures++
		}
		summary.ArtifactBytes += device.ArtifactBytes
		if device.ArtifactHashVerified && device.ArtifactSizeVerified {
			summary.ArtifactHashVerified++
		}
		if device.MQTTRebootDisconnected {
			summary.MQTTRebootDisconnects++
		}
		if device.MQTTReconnectSucceeded {
			summary.MQTTReconnectSuccesses++
		}
		for key, count := range device.HTTPStatusCounts {
			summary.HTTPStatusCounts[key] += count
		}
		for operation, values := range device.HTTPLatencyMS {
			latencies[operation] = append(latencies[operation], values...)
		}
	}
	for operation, values := range latencies {
		summary.HTTPLatencyMS[operation] = map[string]float64{
			"p50": percentile(values, 50),
			"p95": percentile(values, 95),
			"p99": percentile(values, 99),
		}
	}
	if elapsed > 0 {
		summary.ArtifactThroughputBPS = float64(summary.ArtifactBytes) / elapsed.Seconds()
	}
	if peaks != nil {
		summary.PeakGoroutines, summary.PeakHeapAllocBytes = peaks.values()
	}
	return summary
}

func writeOTADeviceResults(outDir string, devices []otaDeviceResult) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	rows := append([]otaDeviceResult(nil), devices...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].DeviceID < rows[j].DeviceID })
	path := filepath.Join(outDir, "ota-devices.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	encoder := json.NewEncoder(file)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package cloudvalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func Run(ctx context.Context, cfg Config) (RunReport, error) {
	started := time.Now().UTC()
	scenarios, scenarioErr := LoadScenarios(cfg.ScenarioFiles)
	report := RunReport{
		SchemaVersion:    1,
		RunID:            cfg.RunID,
		Environment:      cfg.Environment,
		Platform:         cfg.Platform,
		Mode:             cfg.Mode,
		StartedAt:        started,
		SDKCommit:        valueOrUnknown(cfg.SDKCommit),
		ContractsCommit:  valueOrUnknown(os.Getenv("CLOUD_VALIDATION_CONTRACTS_COMMIT")),
		ServerVersion:    valueOrUnknown(cfg.ServerVersion),
		BrandCloudSlug:   cfg.BrandCloudSlug,
		ArtifactChecksum: cfg.ArtifactChecksum,
		Host:             hostMetadata(),
		Toolchains:       toolchainMetadata(),
		Scenarios:        scenarios,
	}
	if scenarioErr != nil {
		report.Steps = append(report.Steps, failedStep("load_scenarios", "invalid_scenario", scenarioErr.Error()))
		return finish(cfg, report)
	}
	report.Steps = append(report.Steps, passedStep("load_scenarios", fmt.Sprintf("loaded %d scenarios", len(scenarios))))

	preflight := preflight(cfg)
	report.Steps = append(report.Steps, preflight...)
	if hasBlockingStep(preflight) || cfg.PlanOnly {
		return finish(cfg, report)
	}
	readiness := runCommandStep(ctx, cfg, "cloud_readiness", cfg.ReadinessCommand, "environment_unavailable")
	report.Steps = append(report.Steps, readiness)
	if readiness.Status != StatusPass {
		return finish(cfg, report)
	}
	if report.ServerVersion == "unknown" {
		report.ServerVersion = readServerVersion(filepath.Join(cfg.OutDir, "server-version.json"))
	}

	if cfg.SetupCommand != "" {
		report.Steps = append(report.Steps, runCommandStep(ctx, cfg, "fixture_setup", cfg.SetupCommand, "fixture_setup_failed"))
		if hasBlockingStep(report.Steps) {
			return finishWithCleanup(ctx, cfg, report)
		}
		if err := validateRuntimeBundle(cfg.RuntimeBundle, cfg); err != nil {
			report.Steps = append(report.Steps, failedStep("runtime_bundle", "invalid_runtime_bundle", err.Error()))
			return finishWithCleanup(ctx, cfg, report)
		}
		report.Steps = append(report.Steps, passedStep("runtime_bundle", "identity and permissions validated"))
	}

	_ = os.Remove(cfg.ReadyFile)
	virtual, err := startManagedCommand(ctx, cfg, "virtual-device", cfg.VirtualCommand)
	if err != nil {
		report.Steps = append(report.Steps, failedStep("virtual_device_start", "virtual_device_failed", err.Error()))
		return finishWithCleanup(ctx, cfg, report)
	}
	if err := waitForReady(ctx, cfg.ReadyFile, cfg.ReadyTimeout, virtual); err != nil {
		report.Steps = append(report.Steps, failedStep("virtual_device_ready", "virtual_device_failed", err.Error()))
		return finishAfterVirtual(ctx, cfg, report, virtual)
	}
	if err := validateReadyFile(cfg.ReadyFile, cfg.RunID); err != nil {
		report.Steps = append(report.Steps, failedStep("virtual_device_ready", "invalid_ready_artifact", err.Error()))
		return finishAfterVirtual(ctx, cfg, report, virtual)
	}
	report.Steps = append(report.Steps, passedStep("virtual_device_ready", cfg.ReadyFile))

	platformStep := runCommandStep(ctx, cfg, "platform_test", cfg.PlatformCommand, "platform_test_failed")
	report.Steps = append(report.Steps, platformStep)
	if platformStep.Status == StatusPass {
		result, err := readPlatformResult(cfg.PlatformResult, cfg)
		if err != nil {
			report.Steps = append(report.Steps, failedStep("platform_result", "invalid_platform_result", err.Error()))
		} else if err := validateScenarioCoverage(result, scenarios); err != nil {
			report.Steps = append(report.Steps, failedStep("platform_result", "incomplete_scenario_coverage", err.Error()))
		} else if err := validateExpectedScenarioResults(result, scenarios); err != nil {
			report.Steps = append(report.Steps, failedStep("platform_result", "unexpected_scenario_result", err.Error()))
		} else {
			report.PlatformResult = &result
			report.Steps = append(report.Steps, passedStep("platform_result", cfg.PlatformResult))
		}
	}

	if cfg.EvidenceCommand != "" {
		step := runCommandStep(ctx, cfg, "cloud_evidence", cfg.EvidenceCommand, "cloud_evidence_missing")
		report.Steps = append(report.Steps, step)
		if step.Status == StatusPass {
			if err := validateCloudEvidence(cfg.CloudEvidenceFile, cfg.RunID, cfg.Platform, scenarios, report.PlatformResult); err != nil {
				report.Steps = append(report.Steps, failedStep("cloud_evidence_contract", "cloud_evidence_invalid", err.Error()))
			} else {
				report.Steps = append(report.Steps, passedStep("cloud_evidence_contract", cfg.CloudEvidenceFile))
			}
		}
	} else if requiredCloudEvidence(scenarios) && !fileExists(cfg.CloudEvidenceFile) {
		report.Steps = append(report.Steps, failedStep("cloud_evidence", "cloud_evidence_missing", "required Cloud evidence file is missing"))
	}
	return finishAfterVirtual(ctx, cfg, report, virtual)
}

func validateExpectedScenarioResults(result PlatformResult, scenarios []Scenario) error {
	byID := make(map[string]ScenarioResult, len(result.Results))
	for _, item := range result.Results {
		byID[item.ScenarioID] = item
	}
	for _, scenario := range scenarios {
		item := byID[scenario.ID]
		if scenario.ExpectedSDKResult == "capability_not_implemented" {
			if (item.Status != StatusSkip && item.Status != StatusBlocked) || item.ReasonCode != "capability_not_implemented" {
				return fmt.Errorf("scenario %s must report SKIP/BLOCKED capability_not_implemented", scenario.ID)
			}
			continue
		}
		if item.Status != StatusPass {
			return fmt.Errorf("scenario %s expected %s but reported %s (%s)", scenario.ID, scenario.ExpectedSDKResult, item.Status, item.ReasonCode)
		}
	}
	return nil
}

func validateScenarioCoverage(result PlatformResult, scenarios []Scenario) error {
	seen := make(map[string]struct{}, len(result.Results))
	for _, item := range result.Results {
		if _, exists := seen[item.ScenarioID]; exists {
			return fmt.Errorf("duplicate platform scenario result %s", item.ScenarioID)
		}
		seen[item.ScenarioID] = struct{}{}
	}
	for _, scenario := range scenarios {
		if _, exists := seen[scenario.ID]; !exists {
			return fmt.Errorf("platform result is missing scenario %s", scenario.ID)
		}
	}
	return nil
}

func validateRuntimeBundle(path string, cfg Config) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("runtime credential bundle is missing: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("runtime credential bundle permissions must be 0600 or stricter")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var bundle struct {
		SchemaVersion    int    `json:"schema_version"`
		RunID            string `json:"run_id"`
		Platform         string `json:"platform"`
		SDKCommit        string `json:"sdk_commit"`
		ServerVersion    string `json:"server_version"`
		BrandCloudSlug   string `json:"brand_cloud_slug"`
		BrandCloudActive bool   `json:"brand_cloud_active"`
		Brandname        string `json:"brandname"`
		EnvironmentRoot  string `json:"environment_root"`
		TestDataDB       string `json:"test_data_db"`
		App              struct {
			BaseURL  string `json:"base_url"`
			DeviceID string `json:"device_id"`
		} `json:"app"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return err
	}
	if bundle.SchemaVersion != 1 || bundle.RunID != cfg.RunID || bundle.Platform != cfg.Platform || strings.TrimSpace(bundle.SDKCommit) == "" || strings.TrimSpace(bundle.ServerVersion) == "" {
		return fmt.Errorf("runtime bundle identity does not match run")
	}
	if bundle.BrandCloudSlug != cfg.BrandCloudSlug || !bundle.BrandCloudActive || strings.TrimSpace(bundle.Brandname) == "" || strings.TrimSpace(bundle.EnvironmentRoot) == "" || strings.TrimSpace(bundle.TestDataDB) == "" || strings.TrimSpace(bundle.App.DeviceID) == "" || strings.TrimRight(bundle.App.BaseURL, "/") != strings.TrimRight(cfg.DeviceURL, "/") {
		return fmt.Errorf("runtime bundle cloud or test_data_db does not match configuration")
	}
	return nil
}

func validateReadyFile(path, runID string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var ready struct {
		SchemaVersion int      `json:"schema_version"`
		RunID         string   `json:"run_id"`
		Status        string   `json:"status"`
		DeviceIDs     []string `json:"device_ids"`
	}
	if err := json.Unmarshal(raw, &ready); err != nil {
		return err
	}
	if ready.SchemaVersion != 1 || ready.RunID != runID || ready.Status != "READY" || len(ready.DeviceIDs) == 0 {
		return fmt.Errorf("virtual-device ready artifact identity or device list is invalid")
	}
	return nil
}

func validateCloudEvidence(path, runID, platform string, scenarios []Scenario, platformResult *PlatformResult) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if Redact(string(raw)) != string(raw) {
		return fmt.Errorf("cloud evidence contains secret-bearing fields")
	}
	var evidence struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		Platform      string `json:"platform"`
		Events        []struct {
			ScenarioID    string `json:"scenario_id"`
			CorrelationID string `json:"correlation_id"`
			Type          string `json:"type"`
			ObservedAt    string `json:"observed_at"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return err
	}
	if evidence.SchemaVersion != 1 || evidence.RunID != runID || evidence.Platform != platform {
		return fmt.Errorf("cloud evidence identity is invalid")
	}
	if platformResult == nil {
		return fmt.Errorf("validated platform result is required before Cloud evidence")
	}
	resultByScenario := make(map[string]ScenarioResult, len(platformResult.Results))
	for _, result := range platformResult.Results {
		resultByScenario[result.ScenarioID] = result
	}
	available := make(map[string]map[string]struct{})
	for _, event := range evidence.Events {
		if event.ScenarioID == "" || event.CorrelationID == "" || event.Type == "" || event.ObservedAt == "" {
			return fmt.Errorf("cloud evidence event is missing scenario_id, correlation_id, type, or observed_at")
		}
		if _, err := time.Parse(time.RFC3339, event.ObservedAt); err != nil {
			return fmt.Errorf("cloud evidence observed_at is invalid")
		}
		result, exists := resultByScenario[event.ScenarioID]
		if !exists || event.CorrelationID != result.CorrelationID {
			return fmt.Errorf("cloud evidence correlation does not match platform result")
		}
		if available[event.ScenarioID] == nil {
			available[event.ScenarioID] = map[string]struct{}{}
		}
		available[event.ScenarioID][event.Type] = struct{}{}
	}
	for _, scenario := range scenarios {
		result, exists := resultByScenario[scenario.ID]
		if !exists || result.Status != StatusPass {
			continue
		}
		for _, expected := range scenario.ExpectedCloudEvidence {
			if _, ok := available[scenario.ID][expected]; !ok {
				return fmt.Errorf("cloud evidence missing %s for scenario %s", expected, scenario.ID)
			}
		}
	}
	return nil
}

func preflight(cfg Config) []StepResult {
	var steps []StepResult
	require := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			steps = append(steps, blockedStep("preflight_"+name, "not_configured", name+" is not configured"))
		}
	}
	require("account_manager_url", cfg.AccountManagerURL)
	require("video_cloud_url", cfg.VideoCloudURL)
	require("device_url", cfg.DeviceURL)
	require("mqtt_addr", cfg.MQTTAddr)
	require("brand_cloud_slug", cfg.BrandCloudSlug)
	if cfg.Platform != "ios" && cfg.Platform != "android" {
		steps = append(steps, failedStep("preflight_platform", "invalid_platform", "platform must be ios or android"))
	}
	if cfg.Mode != "source" && cfg.Mode != "package" {
		steps = append(steps, failedStep("preflight_mode", "invalid_mode", "mode must be source or package"))
	}
	if cfg.CABundle == "" || !fileExists(cfg.CABundle) {
		steps = append(steps, blockedStep("preflight_ca_bundle", "not_configured", "current CA bundle is missing"))
	}
	if cfg.Mode == "package" {
		if !fileExists(cfg.ArtifactPath) || cfg.ArtifactChecksum == "" {
			steps = append(steps, blockedStep("preflight_package", "not_configured", "package mode requires artifact path and checksum"))
		} else if actual, err := fileSHA256(cfg.ArtifactPath); err != nil || !strings.EqualFold(actual, cfg.ArtifactChecksum) {
			steps = append(steps, failedStep("preflight_package", "artifact_checksum_mismatch", "release artifact checksum does not match"))
		}
	}
	if cfg.PlanOnly {
		if runtime.GOOS != "darwin" && cfg.Platform == "ios" {
			steps = append(steps, blockedStep("preflight_host", "environment_unavailable", "iOS validation requires macOS"))
		}
	} else {
		for name, value := range map[string]string{
			"runtime_bundle":    cfg.RuntimeBundle,
			"readiness_command": cfg.ReadinessCommand,
			"setup_command":     cfg.SetupCommand,
			"virtual_command":   cfg.VirtualCommand,
			"platform_command":  cfg.PlatformCommand,
			"cleanup_command":   cfg.CleanupCommand,
		} {
			require(name, value)
		}
		if cfg.RuntimeBundle != "" {
			if info, err := os.Stat(cfg.RuntimeBundle); err == nil && info.Mode().Perm()&0o077 != 0 {
				steps = append(steps, failedStep("preflight_runtime_bundle", "insecure_credentials", "runtime credential bundle permissions must be 0600 or stricter"))
			} else if err != nil && cfg.SetupCommand == "" {
				steps = append(steps, blockedStep("preflight_runtime_bundle", "not_configured", "runtime credential bundle is missing and no fixture setup command is configured"))
			}
		}
		if filepath.Base(cfg.SetupCommand) == "setup-fixture.sh" && !fileExists(cfg.RuntimeBundle) && strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_FIXTURE_PROVIDER_COMMAND")) == "" {
			for name, value := range map[string]string{
				"env_root":                os.Getenv("CLOUD_VALIDATION_ENV_ROOT"),
				"platform_admin_token":    os.Getenv("CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN"),
				"video_cloud_admin_token": os.Getenv("CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN"),
			} {
				if strings.TrimSpace(value) == "" {
					steps = append(steps, blockedStep("preflight_fixture_"+name, "not_configured", name+" is required by the built-in fixture provider"))
				}
			}
			for _, platform := range []string{"IOS", "ANDROID"} {
				cloudKey := "CLOUD_VALIDATION_" + platform + "_CLOUD_SLUG"
				if strings.TrimSpace(os.Getenv(cloudKey)) == "" {
					steps = append(steps, blockedStep("preflight_fixture_"+strings.ToLower(platform)+"_cloud", "not_configured", cloudKey+" is required by the built-in fixture provider"))
				}
			}
		}
		if filepath.Base(cfg.EvidenceCommand) == "collect-cloud-evidence.sh" && strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_EVIDENCE_PROVIDER_COMMAND")) == "" {
			if strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_CLOUD_LOGGER_URL")) == "" || strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN")) == "" {
				steps = append(steps, blockedStep("preflight_evidence_provider", "not_configured", "Cloud logger URL and token are required by the built-in evidence provider"))
			}
		}
		if filepath.Base(cfg.CleanupCommand) == "cleanup-fixture.sh" && strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_FIXTURE_PROVIDER_COMMAND")) != "" && strings.TrimSpace(os.Getenv("CLOUD_VALIDATION_FIXTURE_CLEANUP_COMMAND")) == "" {
			steps = append(steps, blockedStep("preflight_cleanup_provider", "not_configured", "fixture cleanup provider is not configured"))
		}
	}
	if len(steps) == 0 {
		steps = append(steps, passedStep("preflight", "configuration and local prerequisites passed"))
	}
	return steps
}

type managedCommand struct {
	cmd  *exec.Cmd
	done chan error
}

func startManagedCommand(ctx context.Context, cfg Config, name, command string) (*managedCommand, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("%s command is not configured", name)
	}
	logFile, err := os.OpenFile(filepath.Join(cfg.OutDir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = commandEnv(cfg)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		done <- err
	}()
	return &managedCommand{cmd: cmd, done: done}, nil
}

func (m *managedCommand) stop() error {
	if m == nil || m.cmd == nil || m.cmd.Process == nil {
		return fmt.Errorf("virtual device process is unavailable")
	}
	if m.cmd.ProcessState != nil && m.cmd.ProcessState.Exited() {
		return fmt.Errorf("virtual device exited before teardown")
	}
	if err := syscall.Kill(-m.cmd.Process.Pid, syscall.SIGINT); err != nil {
		return fmt.Errorf("stop virtual device: %w", err)
	}
	select {
	case <-m.done:
		return nil
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-m.cmd.Process.Pid, syscall.SIGKILL)
		return fmt.Errorf("virtual device did not stop within 5s")
	}
}

func waitForReady(ctx context.Context, path string, timeout time.Duration, proc *managedCommand) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if pathExists(path) {
			return nil
		}
		select {
		case err := <-proc.done:
			return fmt.Errorf("virtual device exited before ready: %v", err)
		case <-timer.C:
			return fmt.Errorf("virtual device ready timeout after %s", timeout)
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runCommandStep(ctx context.Context, cfg Config, name, command, reasonCode string) StepResult {
	if strings.TrimSpace(command) == "" {
		return blockedStep(name, "not_configured", name+" command is not configured")
	}
	logPath := filepath.Join(cfg.OutDir, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return failedStep(name, reasonCode, err.Error())
	}
	defer logFile.Close()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = commandEnv(cfg)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			code := "environment_unavailable"
			if name == "fixture_setup" || name == "cloud_evidence" {
				code = "not_configured"
			}
			return blockedStep(name, code, fmt.Sprintf("%s was blocked; see %s", name, logPath))
		}
		return failedStep(name, reasonCode, fmt.Sprintf("%s failed: %v; see %s", name, err, logPath))
	}
	return passedStep(name, logPath)
}

func commandEnv(cfg Config) []string {
	return append(os.Environ(),
		"CLOUD_VALIDATION_ENVIRONMENT="+cfg.Environment,
		"CLOUD_VALIDATION_RUN_ID="+cfg.RunID,
		"CLOUD_VALIDATION_PLATFORM="+cfg.Platform,
		"CLOUD_VALIDATION_MODE="+cfg.Mode,
		"CLOUD_VALIDATION_OUT_DIR="+cfg.OutDir,
		"CLOUD_VALIDATION_ACCOUNT_MANAGER_URL="+cfg.AccountManagerURL,
		"CLOUD_VALIDATION_VIDEO_CLOUD_URL="+cfg.VideoCloudURL,
		"CLOUD_VALIDATION_DEVICE_URL="+cfg.DeviceURL,
		"CLOUD_VALIDATION_MQTT_ADDR="+cfg.MQTTAddr,
		"CLOUD_VALIDATION_BRAND_CLOUD_SLUG="+cfg.BrandCloudSlug,
		"CLOUD_VALIDATION_CA_BUNDLE="+cfg.CABundle,
		"CLOUD_VALIDATION_RUNTIME_BUNDLE="+cfg.RuntimeBundle,
		"CLOUD_VALIDATION_SDK_COMMIT="+cfg.SDKCommit,
		"CLOUD_VALIDATION_SERVER_VERSION="+cfg.ServerVersion,
		"CLOUD_VALIDATION_ARTIFACT="+cfg.ArtifactPath,
		"CLOUD_VALIDATION_ARTIFACT_SHA256="+cfg.ArtifactChecksum,
		"CLOUD_VALIDATION_READY_FILE="+cfg.ReadyFile,
		"CLOUD_VALIDATION_PLATFORM_RESULT="+cfg.PlatformResult,
		"CLOUD_VALIDATION_CLOUD_EVIDENCE="+cfg.CloudEvidenceFile,
		"CLOUD_VALIDATION_RESOURCE_MANIFEST="+cfg.ResourceManifest,
	)
}

func readPlatformResult(path string, cfg Config) (PlatformResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PlatformResult{}, err
	}
	if Redact(string(raw)) != string(raw) {
		return PlatformResult{}, fmt.Errorf("platform result contains secret-bearing fields")
	}
	var result PlatformResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.SchemaVersion != 1 || result.RunID != cfg.RunID || result.Platform != cfg.Platform {
		return result, fmt.Errorf("platform result identity does not match run")
	}
	if strings.TrimSpace(result.SDKCommit) == "" || strings.TrimSpace(result.ServerVersion) == "" || !validStatus(result.Status) || len(result.Results) == 0 {
		return result, fmt.Errorf("platform result status or results are invalid")
	}
	for _, item := range result.Results {
		if !scenarioIDPattern.MatchString(item.ScenarioID) || item.CorrelationID == "" || item.DurationMS < 0 || !validStatus(item.Status) {
			return result, fmt.Errorf("invalid scenario result")
		}
		expectedCorrelation := cfg.RunID + "-" + cfg.Platform + "-" + item.ScenarioID
		if item.CorrelationID != expectedCorrelation {
			return result, fmt.Errorf("scenario %s correlation does not match run and platform", item.ScenarioID)
		}
		if (item.Status == StatusSkip || item.Status == StatusBlocked) && (item.ReasonCode == "" || item.Reason == "") {
			return result, fmt.Errorf("SKIP/BLOCKED scenario %s requires reason_code and reason", item.ScenarioID)
		}
		if item.Status == StatusFail && (item.ReasonCode == "" || item.Reason == "") {
			return result, fmt.Errorf("FAIL scenario %s requires reason_code and reason", item.ScenarioID)
		}
	}
	if expected := aggregateScenarioStatus(result.Results); result.Status != expected {
		return result, fmt.Errorf("platform status %s does not match scenario aggregate %s", result.Status, expected)
	}
	return result, nil
}

func aggregateScenarioStatus(results []ScenarioResult) Status {
	status := StatusPass
	allSkipped := len(results) > 0
	for _, result := range results {
		switch result.Status {
		case StatusFail:
			return StatusFail
		case StatusBlocked:
			status = StatusBlocked
			allSkipped = false
		case StatusPass:
			allSkipped = false
		}
	}
	if allSkipped {
		return StatusSkip
	}
	return status
}

func finishWithCleanup(ctx context.Context, cfg Config, report RunReport) (RunReport, error) {
	_ = ctx
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cleanup := runCommandStep(cleanupCtx, cfg, "cleanup", cfg.CleanupCommand, "cleanup_failed")
	if cleanup.Status != StatusPass {
		cleanup.Status = StatusFail
		cleanup.ReasonCode = "cleanup_failed"
	}
	report.Steps = append(report.Steps, cleanup)
	return finish(cfg, report)
}

func finishAfterVirtual(ctx context.Context, cfg Config, report RunReport, virtual *managedCommand) (RunReport, error) {
	if err := virtual.stop(); err != nil {
		report.Steps = append(report.Steps, failedStep("virtual_device_stop", "virtual_device_failed", err.Error()))
	} else {
		report.Steps = append(report.Steps, passedStep("virtual_device_stop", "virtual device stopped before fixture cleanup"))
	}
	return finishWithCleanup(ctx, cfg, report)
}

func finish(cfg Config, report RunReport) (RunReport, error) {
	report.CompletedAt = time.Now().UTC()
	report.Status = overallStatus(report.Steps, report.PlatformResult)
	enrichReport(cfg, &report)
	if err := WriteReports(cfg.OutDir, report); err != nil {
		return report, fmt.Errorf("write validation reports: %w", err)
	}
	return report, nil
}

func enrichReport(cfg Config, report *RunReport) {
	report.ResourceManifest = cfg.ResourceManifest
	if raw, err := os.ReadFile(cfg.ResourceManifest); err == nil {
		var manifest struct {
			Resources []json.RawMessage `json:"resources"`
		}
		if json.Unmarshal(raw, &manifest) == nil {
			report.ResourceCount = len(manifest.Resources)
		}
	}
	report.StatusCounts = map[Status]int{
		StatusPass: 0, StatusFail: 0, StatusSkip: 0, StatusBlocked: 0,
	}
	for _, step := range report.Steps {
		report.StatusCounts[step.Status]++
	}
	if report.PlatformResult != nil {
		for _, result := range report.PlatformResult.Results {
			report.StatusCounts[result.Status]++
		}
	}
	report.Artifacts = map[string]string{}
	for name, path := range map[string]string{
		"platform_result":       cfg.PlatformResult,
		"cloud_evidence":        cfg.CloudEvidenceFile,
		"resource_manifest":     cfg.ResourceManifest,
		"virtual_device_ready":  cfg.ReadyFile,
		"virtual_device_log":    filepath.Join(cfg.OutDir, "virtual-device.log"),
		"virtual_device_result": filepath.Join(cfg.OutDir, "virtual-device", "results.json"),
		"virtual_device_report": filepath.Join(cfg.OutDir, "virtual-device", "TEST_REPORT.md"),
		"platform_test_log":     filepath.Join(cfg.OutDir, "platform_test.log"),
		"ios_launch_log":        filepath.Join(cfg.OutDir, "ios-simulator-launch.log"),
		"ios_system_log":        filepath.Join(cfg.OutDir, "ios", "simulator-system.log"),
		"ios_xcresult":          filepath.Join(cfg.OutDir, "ios", "CloudValidationUITests.xcresult"),
		"android_logcat":        filepath.Join(cfg.OutDir, "android", "logcat.log"),
		"android_crash_log":     filepath.Join(cfg.OutDir, "android", "crash.log"),
		"android_last_anr":      filepath.Join(cfg.OutDir, "android", "last-anr.txt"),
	} {
		if fileExists(path) {
			report.Artifacts[name] = path
		}
	}
}

func hostMetadata() map[string]string {
	hostname, _ := os.Hostname()
	return map[string]string{
		"hostname": valueOrUnknown(hostname),
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"go":       runtime.Version(),
	}
}

func toolchainMetadata() map[string]string {
	return map[string]string{
		"xcode":       commandVersion("xcodebuild", "-version"),
		"swift":       commandVersion("swift", "--version"),
		"android_sdk": valueOrUnknown(firstNonEmpty(os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT"))),
		"sdkmanager":  commandVersion("sdkmanager", "--version"),
		"java":        commandVersion("java", "-version"),
		"gradle":      commandVersion("gradle", "--version"),
	}
}

func commandVersion(name string, args ...string) string {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	line := strings.TrimSpace(string(output))
	for _, candidate := range strings.Split(line, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.Trim(candidate, "-") != "" {
			return candidate
		}
	}
	return "unknown"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func readServerVersion(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		return "unknown"
	}
	for _, key := range []string{"version", "commit", "git_commit", "build"} {
		if value, ok := document[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}

func requiredCloudEvidence(scenarios []Scenario) bool {
	for _, scenario := range scenarios {
		if len(scenario.ExpectedCloudEvidence) > 0 {
			return true
		}
	}
	return false
}

func hasBlockingStep(steps []StepResult) bool {
	for _, step := range steps {
		if step.Status == StatusFail || step.Status == StatusBlocked {
			return true
		}
	}
	return false
}

func validStatus(status Status) bool {
	return status == StatusPass || status == StatusFail || status == StatusSkip || status == StatusBlocked
}

func passedStep(name, evidence string) StepResult {
	return StepResult{Name: name, Status: StatusPass, Reason: "completed", Evidence: Redact(evidence)}
}

func failedStep(name, code, reason string) StepResult {
	return StepResult{Name: name, Status: StatusFail, ReasonCode: code, Reason: Redact(reason)}
}

func blockedStep(name, code, reason string) StepResult {
	return StepResult{Name: name, Status: StatusBlocked, ReasonCode: code, Reason: Redact(reason)}
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

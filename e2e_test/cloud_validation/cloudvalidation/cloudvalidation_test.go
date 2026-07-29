package cloudvalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadScenarios(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	raw := []byte("schema_version: 1\nscenarios:\n  - id: token\n    required_capabilities: [token]\n    device_profile: online\n    app_action: request_token\n    expected_sdk_result: success\n    expected_cloud_evidence: []\n    timeout: 10s\n    cleanup: local_only\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScenarios([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "token" {
		t.Fatalf("unexpected scenarios: %#v", got)
	}
}

func TestRepositoryScenarioManifestsRemainValid(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "scenarios", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no repository scenario manifests found")
	}
	if _, err := LoadScenarios(paths); err != nil {
		t.Fatalf("repository scenario manifests are invalid: %v", err)
	}
}

func TestPlanOnlyReportsBlockedInputs(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(scenario, []byte("schema_version: 1\nscenarios:\n  - id: token\n    required_capabilities: [token]\n    device_profile: online\n    app_action: request_token\n    expected_sdk_result: success\n    expected_cloud_evidence: []\n    timeout: 10s\n    cleanup: local_only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Config{
		Environment:   "staging",
		Platform:      "android",
		Mode:          "source",
		RunID:         "plan-test",
		OutDir:        dir,
		ScenarioFiles: []string{scenario},
		PlanOnly:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusBlocked {
		t.Fatalf("status=%s, want BLOCKED; steps=%#v", report.Status, report.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "SUMMARY.md")); err != nil {
		t.Fatalf("summary missing: %v", err)
	}
}

func TestReadPlatformResultRequiresReasonsForSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	result := PlatformResult{
		SchemaVersion: 1,
		RunID:         "run-1",
		Platform:      "ios",
		SDKCommit:     "abc",
		ServerVersion: "server",
		Status:        StatusPass,
		Results: []ScenarioResult{{
			ScenarioID:    "shadow",
			Status:        StatusSkip,
			DurationMS:    1,
			CorrelationID: "run-1-ios-shadow",
		}},
	}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readPlatformResult(path, Config{RunID: "run-1", Platform: "ios"})
	if err == nil || !strings.Contains(err.Error(), "requires reason_code") {
		t.Fatalf("error=%v, want missing reason error", err)
	}
}

func TestReadPlatformResultRejectsAggregateStatusMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	result := PlatformResult{
		SchemaVersion: 1,
		RunID:         "run-1",
		Platform:      "android",
		SDKCommit:     "sdk",
		ServerVersion: "server",
		Status:        StatusPass,
		Results: []ScenarioResult{{
			ScenarioID: "token", Status: StatusFail, ReasonCode: "sdk_error", Reason: "failed", DurationMS: 1, CorrelationID: "run-1-android-token",
		}},
	}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlatformResult(path, Config{RunID: "run-1", Platform: "android"}); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate mismatch error = %v", err)
	}
}

func TestOverallStatusPreservesAllSkippedPlatform(t *testing.T) {
	if got := overallStatus([]StepResult{passedStep("preflight", "ok")}, &PlatformResult{Status: StatusSkip}); got != StatusSkip {
		t.Fatalf("overall status=%s, want SKIP", got)
	}
}

func TestValidateScenarioCoverageRejectsMissingAndDuplicateResults(t *testing.T) {
	scenarios := []Scenario{{ID: "token"}, {ID: "websocket"}}
	missing := PlatformResult{Results: []ScenarioResult{{ScenarioID: "token"}}}
	if err := validateScenarioCoverage(missing, scenarios); err == nil || !strings.Contains(err.Error(), "websocket") {
		t.Fatalf("missing coverage error = %v", err)
	}
	duplicate := PlatformResult{Results: []ScenarioResult{{ScenarioID: "token"}, {ScenarioID: "token"}}}
	if err := validateScenarioCoverage(duplicate, scenarios[:1]); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate coverage error = %v", err)
	}
}

func TestValidateExpectedScenarioResultsRejectsSkippedRequiredNegativeTest(t *testing.T) {
	result := PlatformResult{Results: []ScenarioResult{{
		ScenarioID: "cross_cloud", Status: StatusSkip, ReasonCode: "not_configured",
	}}}
	scenarios := []Scenario{{ID: "cross_cloud", ExpectedSDKResult: "forbidden"}}
	if err := validateExpectedScenarioResults(result, scenarios); err == nil || !strings.Contains(err.Error(), "expected forbidden") {
		t.Fatalf("expected result validation error = %v", err)
	}

	result.Results[0] = ScenarioResult{ScenarioID: "shadow", Status: StatusSkip, ReasonCode: "capability_not_implemented"}
	scenarios[0] = Scenario{ID: "shadow", ExpectedSDKResult: "capability_not_implemented"}
	if err := validateExpectedScenarioResults(result, scenarios); err != nil {
		t.Fatalf("declared missing capability should be accepted: %v", err)
	}
}

func TestCloudEvidenceDoesNotRequireEventsForSkippedCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud-evidence.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"run_id":"run-1","platform":"ios","events":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := &PlatformResult{Results: []ScenarioResult{{
		ScenarioID: "shadow", Status: StatusSkip, CorrelationID: "run-1-ios-shadow",
	}}}
	scenarios := []Scenario{{ID: "shadow", ExpectedCloudEvidence: []string{"delta_cleared"}}}
	if err := validateCloudEvidence(path, "run-1", "ios", scenarios, result); err != nil {
		t.Fatalf("skipped scenario should not require evidence: %v", err)
	}
}

func TestWaitForReadyRejectsExitedProcess(t *testing.T) {
	proc := &managedCommand{done: make(chan error, 1)}
	proc.done <- nil
	err := waitForReady(context.Background(), filepath.Join(t.TempDir(), "ready.json"), time.Second, proc)
	if err == nil || !strings.Contains(err.Error(), "exited before ready") {
		t.Fatalf("error=%v", err)
	}
}

func TestRedact(t *testing.T) {
	got := Redact(`{"access_token":"secret","password":"pw"}`)
	if strings.Contains(got, "secret") || strings.Contains(got, `"pw"`) {
		t.Fatalf("secret leaked: %s", got)
	}
}

func TestPreflightAllowsFixtureSetupToCreateRuntimeBundle(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("test ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps := preflight(Config{
		Environment:       "staging",
		Platform:          "android",
		Mode:              "source",
		AccountManagerURL: "https://account.example",
		VideoCloudURL:     "https://video.example",
		DeviceURL:         "https://device.example",
		MQTTAddr:          "mqtt.example:8883",
		BrandCloudSlug:    "sdk-e2e-android",
		CABundle:          ca,
		RuntimeBundle:     filepath.Join(dir, "runtime.json"),
		ReadinessCommand:  "check-readiness",
		SetupCommand:      "create-runtime-bundle",
		VirtualCommand:    "virtual-device",
		PlatformCommand:   "platform-test",
		CleanupCommand:    "cleanup",
	})
	if hasBlockingStep(steps) {
		t.Fatalf("fixture-generated runtime bundle should pass preflight: %#v", steps)
	}
}

func TestRunExecutesFixtureDevicePlatformEvidenceAndCleanup(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	runtimeBundle := filepath.Join(dir, "runtime.json")
	manifest := filepath.Join(dir, "resource-manifest.json")
	ready := filepath.Join(dir, "virtual", "ready.json")
	platformResult := filepath.Join(dir, "ios", "platform-result.json")
	evidence := filepath.Join(dir, "cloud-evidence.json")
	cleanupMarker := filepath.Join(dir, "cleanup.done")
	scenario := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(ca, []byte("test ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scenario, []byte("schema_version: 1\nscenarios:\n  - id: token\n    required_capabilities: [token]\n    device_profile: online\n    app_action: request_token\n    expected_sdk_result: success\n    expected_cloud_evidence: [token_issued]\n    timeout: 10s\n    cleanup: run_owned_resources\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setup := writeTestScript(t, dir, "setup.sh", fmt.Sprintf(`
mkdir -p %q
printf '%s' > %q
chmod 600 %q
printf '{"resources":[{"type":"device","id":"device-1"}]}' > %q
`, filepath.Dir(manifest), `{"schema_version":1,"run_id":"run-1","platform":"ios","sdk_commit":"sdk","server_version":"server","brand_cloud_slug":"sdk-e2e-ios","brand_cloud_active":true,"brandname":"sdk-ios","environment_root":"/tmp/environment","test_data_db":"/tmp/test-data.db","app":{"base_url":"https://device.example","device_id":"device-1"}}`, runtimeBundle, runtimeBundle, manifest))
	virtual := writeTestScript(t, dir, "virtual.sh", fmt.Sprintf(`
mkdir -p %q
printf '{"schema_version":1,"run_id":"run-1","status":"READY","device_ids":["device-1"]}' > %q
while :; do sleep 1; done
`, filepath.Dir(ready), ready))
	platform := writeTestScript(t, dir, "platform.sh", fmt.Sprintf(`
mkdir -p %q
printf '%s' > %q
`, filepath.Dir(platformResult), `{"schema_version":1,"run_id":"run-1","platform":"ios","sdk_commit":"sdk","server_version":"server","status":"PASS","results":[{"scenario_id":"token","status":"PASS","duration_ms":1,"correlation_id":"run-1-ios-token","evidence":["token issued"]}]}`, platformResult))
	evidenceScript := writeTestScript(t, dir, "evidence.sh", fmt.Sprintf(`
printf '{"schema_version":1,"run_id":"run-1","platform":"ios","events":[{"scenario_id":"token","correlation_id":"run-1-ios-token","type":"token_issued","observed_at":"2026-01-01T00:00:00Z"}]}' > %q
`, evidence))
	cleanup := writeTestScript(t, dir, "cleanup.sh", fmt.Sprintf("touch %q\n", cleanupMarker))
	readiness := writeTestScript(t, dir, "readiness.sh", fmt.Sprintf(`
printf '{"version":"server-from-readiness"}' > %q
`, filepath.Join(dir, "server-version.json")))

	report, err := Run(context.Background(), Config{
		Environment:       "staging",
		Platform:          "ios",
		Mode:              "source",
		RunID:             "run-1",
		OutDir:            dir,
		ScenarioFiles:     []string{scenario},
		AccountManagerURL: "https://account.example",
		VideoCloudURL:     "https://video.example",
		DeviceURL:         "https://device.example",
		MQTTAddr:          "mqtt.example:8883",
		BrandCloudSlug:    "sdk-e2e-ios",
		CABundle:          ca,
		RuntimeBundle:     runtimeBundle,
		ReadinessCommand:  readiness,
		SetupCommand:      setup,
		VirtualCommand:    virtual,
		PlatformCommand:   platform,
		EvidenceCommand:   evidenceScript,
		CleanupCommand:    cleanup,
		ReadyFile:         ready,
		PlatformResult:    platformResult,
		CloudEvidenceFile: evidence,
		ResourceManifest:  manifest,
		ReadyTimeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusPass || report.ResourceCount != 1 {
		t.Fatalf("report status=%s resources=%d steps=%#v", report.Status, report.ResourceCount, report.Steps)
	}
	if report.ServerVersion != "server-from-readiness" {
		t.Fatalf("server version=%q, want readiness version", report.ServerVersion)
	}
	if _, err := os.Stat(cleanupMarker); err != nil {
		t.Fatalf("cleanup did not run: %v", err)
	}
	for _, name := range []string{"results.json", "junit.xml", "SUMMARY.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestReadServerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-version.json")
	if err := os.WriteFile(path, []byte(`{"git_commit":"server-abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readServerVersion(path); got != "server-abc" {
		t.Fatalf("version=%q, want server-abc", got)
	}
}

func TestCloudReadinessScript(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"service":"account-manager","version":"server-123"}`))
			return
		}
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"ApiVersion":"0.28.2","AppVersion":"test-build"}`))
			return
		}
		if r.URL.Path == "/api/devices/00000000-0000-4000-8000-000000000001/entitlement/revoke" {
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer video-admin-test" {
				http.Error(w, "bad cleanup probe", http.StatusUnauthorized)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	outDir := t.TempDir()
	script := filepath.Join("..", "scripts", "check-cloud-readiness.sh")
	cmd := exec.Command("/bin/bash", script)
	cmd.Env = append(os.Environ(),
		"CLOUD_VALIDATION_OUT_DIR="+outDir,
		"CLOUD_VALIDATION_ACCOUNT_MANAGER_URL="+server.URL,
		"CLOUD_VALIDATION_VIDEO_CLOUD_URL="+server.URL,
		"CLOUD_VALIDATION_DEVICE_URL="+server.URL,
		"CLOUD_VALIDATION_MQTT_ADDR="+listener.Addr().String(),
		"CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN=video-admin-test",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("readiness script failed: %v\n%s", err, output)
	}
	if got := readServerVersion(filepath.Join(outDir, "server-version.json")); got != "account-manager=server-123;video-cloud=0.28.2/test-build" {
		t.Fatalf("version=%q, want combined deployed service versions", got)
	}
}

func TestCleanupScriptRetainsRecoverySecretsAfterProviderFailure(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "resource-manifest.json")
	bundle := filepath.Join(dir, "runtime-bundle.json")
	privateFile := filepath.Join(dir, "device.key")
	privateValue := "recovery-secret-content-do-not-leak"
	if err := os.WriteFile(manifest, []byte(`{"cleanup_required":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateFile, []byte(privateValue), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"local_temporary_files":[%q]}`, privateFile)
	if err := os.WriteFile(bundle, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "scripts", "cleanup-fixture.sh")
	cmd := exec.Command("/bin/bash", script)
	cmd.Env = append(os.Environ(),
		"CLOUD_VALIDATION_RESOURCE_MANIFEST="+manifest,
		"CLOUD_VALIDATION_RUNTIME_BUNDLE="+bundle,
		"CLOUD_VALIDATION_OUT_DIR="+dir,
		"CLOUD_VALIDATION_FIXTURE_CLEANUP_COMMAND=exit 17",
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("cleanup provider failure must be returned")
	}
	for _, path := range []string{bundle, privateFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("recovery secret %s was not retained: %v", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("recovery secret %s permissions = %o, want no group/other access", path, info.Mode().Perm())
		}
	}
	retry, err := os.ReadFile(filepath.Join(dir, "cleanup-retry.txt"))
	if err != nil {
		t.Fatalf("cleanup retry instructions missing: %v", err)
	}
	if strings.Contains(string(retry), privateValue) || !strings.Contains(string(retry), "cleanup-fixture.sh") {
		t.Fatalf("cleanup retry instructions are unsafe or incomplete: %s", retry)
	}
}

func writeTestScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/bash\nset -euo pipefail\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

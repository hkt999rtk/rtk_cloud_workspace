package cloudvalidation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeBundleAndReadyArtifactBoundaries(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		RunID:          "run-1",
		Platform:       "android",
		BrandCloudSlug: "test-cloud",
		DeviceURL:      "https://device.example.test/",
	}
	bundle := filepath.Join(dir, "runtime.json")
	valid := `{
		"schema_version":1,
		"run_id":"run-1",
		"platform":"android",
		"sdk_commit":"abc123",
		"server_version":"server-1",
		"brand_cloud_slug":"test-cloud",
		"brand_cloud_active":true,
		"brandname":"Test",
		"environment_root":"run-1",
		"test_data_db":"test_run_1",
		"app":{"base_url":"https://device.example.test","device_id":"device-1"}
	}`
	if err := os.WriteFile(bundle, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeBundle(bundle, cfg); err != nil {
		t.Fatalf("valid runtime bundle: %v", err)
	}
	if err := os.Chmod(bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeBundle(bundle, cfg); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("insecure runtime bundle error = %v", err)
	}
	if err := os.Chmod(bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"malformed": `{`,
		"identity":  strings.Replace(valid, `"run-1"`, `"other-run"`, 1),
		"cloud":     strings.Replace(valid, `"test-cloud"`, `"other-cloud"`, 1),
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateRuntimeBundle(path, cfg); err == nil {
			t.Fatalf("%s runtime bundle unexpectedly valid", name)
		}
	}
	if err := validateRuntimeBundle(filepath.Join(dir, "missing.json"), cfg); err == nil {
		t.Fatal("missing runtime bundle unexpectedly valid")
	}

	ready := filepath.Join(dir, "ready.json")
	if err := os.WriteFile(ready, []byte(`{"schema_version":1,"run_id":"run-1","status":"READY","device_ids":["device-1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateReadyFile(ready, "run-1"); err != nil {
		t.Fatalf("valid ready file: %v", err)
	}
	for _, contents := range []string{
		`{`,
		`{"schema_version":1,"run_id":"other","status":"READY","device_ids":["device-1"]}`,
		`{"schema_version":1,"run_id":"run-1","status":"READY","device_ids":[]}`,
	} {
		if err := os.WriteFile(ready, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateReadyFile(ready, "run-1"); err == nil {
			t.Fatalf("invalid ready file accepted: %s", contents)
		}
	}
}

func TestScenarioManifestValidationBoundaries(t *testing.T) {
	valid := Scenario{
		ID:                "valid_case",
		TestID:            "E2E-SDK-AUTH-001",
		DeviceProfile:     "camera",
		AppAction:         "authenticate",
		ExpectedSDKResult: "success",
		Timeout:           "10s",
		Cleanup:           "delete fixture",
	}
	if err := validateScenario(valid); err != nil {
		t.Fatalf("valid scenario: %v", err)
	}
	mutations := []func(*Scenario){
		func(s *Scenario) { s.ID = "INVALID-ID" },
		func(s *Scenario) { s.TestID = "bad-id" },
		func(s *Scenario) { s.DeviceProfile = "" },
		func(s *Scenario) { s.Cleanup = "" },
		func(s *Scenario) { s.ExpectedSDKResult = "unknown" },
		func(s *Scenario) { s.Timeout = "0s" },
		func(s *Scenario) { s.Timeout = "invalid" },
	}
	for _, mutate := range mutations {
		scenario := valid
		mutate(&scenario)
		if err := validateScenario(scenario); err == nil {
			t.Fatalf("invalid scenario accepted: %+v", scenario)
		}
	}

	dir := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	validYAML := `schema_version: 1
scenarios:
  - id: valid_case
    test_id: E2E-SDK-AUTH-001
    device_profile: camera
    app_action: authenticate
    expected_sdk_result: success
    timeout: 10s
    cleanup: delete fixture
`
	first := write("first.yaml", validYAML)
	second := write("second.yaml", validYAML)
	if _, err := LoadScenarios([]string{first, second}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate scenario error = %v", err)
	}
	for name, contents := range map[string]string{
		"malformed.yaml": "schema_version: [",
		"schema.yaml":    "schema_version: 2\nscenarios: []\n",
		"empty.yaml":     "schema_version: 1\nscenarios: []\n",
	} {
		if _, err := LoadScenarios([]string{write(name, contents)}); err == nil {
			t.Fatalf("%s unexpectedly loaded", name)
		}
	}
	if _, err := LoadScenarios([]string{filepath.Join(dir, "missing.yaml")}); err == nil {
		t.Fatal("missing scenario manifest unexpectedly loaded")
	}
}

func TestCommandEvidenceAndFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{OutDir: dir, RunID: "run-1", Platform: "android"}
	if step := runCommandStep(context.Background(), cfg, "empty", "", "failed"); step.Status != StatusBlocked {
		t.Fatalf("empty command = %+v", step)
	}
	if step := runCommandStep(context.Background(), cfg, "pass", "printf ok", "failed"); step.Status != StatusPass {
		t.Fatalf("passing command = %+v", step)
	}
	if step := runCommandStep(context.Background(), cfg, "blocked", "exit 2", "failed"); step.Status != StatusBlocked {
		t.Fatalf("blocked command = %+v", step)
	}
	if step := runCommandStep(context.Background(), cfg, "fail", "exit 1", "failed"); step.Status != StatusFail {
		t.Fatalf("failed command = %+v", step)
	}
	badCfg := cfg
	badCfg.OutDir = filepath.Join(dir, "missing", "nested")
	if step := runCommandStep(context.Background(), badCfg, "log", "true", "failed"); step.Status != StatusFail {
		t.Fatalf("log open failure = %+v", step)
	}
	if _, err := startManagedCommand(context.Background(), cfg, "virtual", ""); err == nil {
		t.Fatal("empty managed command unexpectedly started")
	}
	if _, err := startManagedCommand(context.Background(), badCfg, "virtual", "true"); err == nil {
		t.Fatal("managed command with invalid log path unexpectedly started")
	}

	existing := filepath.Join(dir, "ready")
	if err := os.WriteFile(existing, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForReady(context.Background(), existing, time.Second, &managedCommand{done: make(chan error)}); err != nil {
		t.Fatalf("existing ready path: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForReady(ctx, filepath.Join(dir, "never"), time.Second, &managedCommand{done: make(chan error)}); err == nil {
		t.Fatal("cancelled ready wait unexpectedly succeeded")
	}
	if err := waitForReady(context.Background(), filepath.Join(dir, "never"), time.Millisecond, &managedCommand{done: make(chan error)}); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("ready timeout = %v", err)
	}

	if requiredCloudEvidence(nil) {
		t.Fatal("empty scenarios unexpectedly require cloud evidence")
	}
	if !requiredCloudEvidence([]Scenario{{ExpectedCloudEvidence: []string{"event"}}}) {
		t.Fatal("scenario evidence requirement was not detected")
	}
	step := failedStep("secret", "failed", "Authorization: Bearer super-secret-value")
	if strings.Contains(step.Reason, "super-secret-value") {
		t.Fatalf("failed step leaked secret: %+v", step)
	}

	payload := filepath.Join(dir, "payload")
	if err := os.WriteFile(payload, []byte("coverage"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(payload)
	if err != nil || len(sum) != 64 {
		t.Fatalf("file checksum = %q err=%v", sum, err)
	}
	if _, err := fileSHA256(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing checksum input unexpectedly succeeded")
	}

	version := filepath.Join(dir, "version.json")
	for contents, want := range map[string]string{
		`{`:                         "unknown",
		`{"other":"value"}`:         "unknown",
		`{"version":"v1"}`:          "v1",
		`{"git_commit":"commit-1"}`: "commit-1",
	} {
		if err := os.WriteFile(version, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readServerVersion(version); got != want {
			t.Fatalf("readServerVersion(%s) = %q, want %q", contents, got, want)
		}
	}
	if got := readServerVersion(filepath.Join(dir, "missing")); got != "unknown" {
		t.Fatalf("missing server version = %q", got)
	}
}

func TestReportPersistenceAndErrorBoundaries(t *testing.T) {
	started := time.Now().Add(-time.Second)
	report := RunReport{
		RunID:        "run-1",
		Platform:     "android",
		Status:       StatusFail,
		StartedAt:    started,
		CompletedAt:  time.Now(),
		Host:         map[string]string{},
		Toolchains:   map[string]string{},
		StatusCounts: map[Status]int{},
		Steps: []StepResult{
			passedStep("setup", "ok"),
			failedStep("test", "assertion", "failed"),
			blockedStep("cleanup", "blocked", "blocked"),
		},
		PlatformResult: &PlatformResult{Results: []ScenarioResult{
			{ScenarioID: "pass", Status: StatusPass, DurationMS: 10},
			{ScenarioID: "fail", Status: StatusFail, ReasonCode: "assertion", Reason: "failed", DurationMS: 20},
			{ScenarioID: "skip", Status: StatusSkip, ReasonCode: "unsupported", DurationMS: 30},
		}},
	}
	out := filepath.Join(t.TempDir(), "reports")
	if err := WriteReports(out, report); err != nil {
		t.Fatalf("write reports: %v", err)
	}
	for _, name := range []string{"results.json", "junit.xml", "SUMMARY.md"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing report %s", name)
		}
	}

	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteReports(filepath.Join(parentFile, "child"), report); err == nil {
		t.Fatal("report write into a file unexpectedly succeeded")
	}
}

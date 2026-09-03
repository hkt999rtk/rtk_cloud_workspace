package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportCloudValidationFeatureEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err != nil {
		t.Fatal(err)
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(dir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 1 || manifest.Cases[0].TestID != "E2E-SDK-AUTH-001" || manifest.Cases[0].Target != "ios" {
		t.Fatalf("unexpected imported cases: %#v", manifest.Cases)
	}
	if len(manifest.Cases[0].Workflows) != 1 || manifest.Cases[0].Workflows[0].WorkflowID != "WF-SDK-AUTH-001" || len(manifest.Cases[0].Workflows[0].Steps) != 2 {
		t.Fatalf("SDK auth workflow was not qualified step by step: %#v", manifest.Cases[0].Workflows)
	}
	if got := manifest.Cases[0].Requirements[0].Evidence[2].Type; got != "cloud-evidence" {
		t.Fatalf("cloud evidence type = %q, want cloud-evidence", got)
	}
}

func TestImportCloudValidationWebSocketWorkflowEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	native := []string{
		"Cloud command received through SDK transport callback",
		"message matched run correlation",
		"WebSocket session disconnected after receive",
	}
	dir := writeCloudValidationWebSocketImportFixture(t, workspace, native, 200)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err != nil {
		t.Fatal(err)
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(dir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 1 || len(manifest.Cases[0].Workflows) != 1 {
		t.Fatalf("unexpected websocket workflow evidence: %#v", manifest.Cases)
	}
	workflow := manifest.Cases[0].Workflows[0]
	if workflow.WorkflowID != "WF-SDK-WS-001" || workflow.Status != "PASS" || len(workflow.Steps) != 4 {
		t.Fatalf("websocket workflow was not qualified step by step: %#v", workflow)
	}
	for _, stepID := range []string{"dispatch_cloud_command", "receive_websocket_message", "verify_message_correlation", "close_websocket_session"} {
		found := false
		for _, step := range workflow.Steps {
			found = found || step.StepID == stepID
		}
		if !found {
			t.Fatalf("websocket workflow omitted %s: %#v", stepID, workflow.Steps)
		}
	}
}

func TestCloudValidationDeviceRecoveryQualifiesBootstrapAndRecoveryWorkflows(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := catalogCaseByID(catalog.Cases, "E2E-AUTH-DEVRECOVERY-001")
	if !ok {
		t.Fatal("missing E2E-AUTH-DEVRECOVERY-001 catalog case")
	}
	const scenarioID = "device_token_certificate_recovery"
	const correlationID = "sdk-import-test-ios-device_token_certificate_recovery"
	eventTypes := []string{"token_issued", "conflicting_device_identity_rejected", "token_reissued", "certificate_recovery_succeeded"}
	item := cloudValidationEventKey{CorrelationID: correlationID, Types: map[string]bool{}, Evidence: map[string]map[string]any{}}
	for _, eventType := range eventTypes {
		item.Types[eventType] = true
	}
	events := map[string]cloudValidationEventKey{scenarioID: item}
	assertions, err := cloudValidationWorkflowAssertions(tc, inventory, scenarioID, correlationID, nil, events)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, assertion := range assertions {
		got[assertion.WorkflowID] = len(assertion.Steps)
	}
	if got["WF-CONTRACT-AUTH-DEVICE-001"] != 2 || got["WF-CONTRACT-AUTH-RECOVERY-001"] != 3 {
		t.Fatalf("unexpected device workflow assertions: %#v", assertions)
	}
	delete(item.Types, "conflicting_device_identity_rejected")
	events[scenarioID] = item
	if _, err := cloudValidationWorkflowAssertions(tc, inventory, scenarioID, correlationID, nil, events); err == nil {
		t.Fatal("device bootstrap workflow accepted missing conflicting-identity rejection")
	}
}

func TestImportCloudValidationRejectsIncompleteWebSocketWorkflow(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	complete := []string{
		"Cloud command received through SDK transport callback",
		"message matched run correlation",
		"WebSocket session disconnected after receive",
	}
	for name, fixture := range map[string]struct {
		evidence []string
		status   int
	}{
		"dispatch not accepted": {evidence: complete, status: 500},
		"receive missing":       {evidence: complete[1:], status: 200},
		"correlation missing":   {evidence: []string{complete[0], complete[2]}, status: 200},
		"cleanup missing":       {evidence: complete[:2], status: 200},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeCloudValidationWebSocketImportFixture(t, workspace, fixture.evidence, fixture.status)
			if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
				t.Fatal("incomplete websocket workflow evidence was accepted")
			}
		})
	}
}

func TestImportCloudValidationFeatureEvidenceThroughCLI(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	if err := runTestFeatureCoverage([]string{"import-cloud-validation", "--input", filepath.Join(dir, "results.json")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature-evidence.json")); err != nil {
		t.Fatal(err)
	}
	if err := runTestFeatureCoverage([]string{"import-cloud-validation"}); err == nil {
		t.Fatal("import-cloud-validation without --input was accepted")
	}
}

func TestImportCloudValidationRejectsWrongOutputAndCommit(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), t.TempDir()); err == nil {
		t.Fatal("cloud-validation evidence was accepted outside its native result directory")
	}
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	report["sdk_commit"] = "0000000000000000000000000000000000000000"
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
		t.Fatal("cloud-validation evidence from a different SDK commit was accepted")
	}
}

func TestImportCloudValidationRejectsInvalidRunIdentity(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"run failed":        func(report map[string]any) { report["status"] = "FAIL" },
		"wrong environment": func(report map[string]any) { report["environment"] = "production" },
		"bad start time":    func(report map[string]any) { report["started_at"] = "not-a-time" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
			var report map[string]any
			if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
				t.Fatal(err)
			}
			mutate(report)
			writeTestJSON(t, filepath.Join(dir, "results.json"), report)
			if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
				t.Fatal("invalid cloud-validation run identity was accepted")
			}
		})
	}
}

func TestImportCloudValidationRejectsMissingCloudEvent(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated"})
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
		t.Fatal("cloud-validation evidence missing authorized_device_read was accepted")
	}
}

func TestImportCloudValidationRejectsNonPassScenario(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	platform := report["platform_result"].(map[string]any)
	platform["results"].([]any)[0].(map[string]any)["status"] = "SKIP"
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
		t.Fatal("non-PASS cloud-validation scenario was accepted")
	}
}

func TestImportCloudValidationIgnoresUnselectedProfileExcludedScenario(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	platform := report["platform_result"].(map[string]any)
	platform["results"] = append(platform["results"].([]any), map[string]any{
		"scenario_id": "repeated_connect_disconnect",
		"status":      "SKIP",
		"reason_code": "profile_excluded",
	})
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err != nil {
		t.Fatal(err)
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(dir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 1 || manifest.Cases[0].TestID != "E2E-SDK-AUTH-001" {
		t.Fatalf("profile-excluded diagnostic changed imported coverage: %#v", manifest.Cases)
	}
}

func TestImportCloudValidationRejectsUnexpectedUnselectedScenario(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for name, unexpected := range map[string]map[string]any{
		"pass":                      {"scenario_id": "unexpected", "test_id": "E2E-SDK-AUTH-001", "status": "PASS"},
		"wrong skip reason":         {"scenario_id": "unexpected", "status": "SKIP", "reason_code": "environment_unavailable"},
		"profile skip with Test ID": {"scenario_id": "unexpected", "test_id": "E2E-SDK-AUTH-001", "status": "SKIP", "reason_code": "profile_excluded"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
			var report map[string]any
			if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
				t.Fatal(err)
			}
			platform := report["platform_result"].(map[string]any)
			platform["results"] = append(platform["results"].([]any), unexpected)
			writeTestJSON(t, filepath.Join(dir, "results.json"), report)
			if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
				t.Fatal("unexpected unselected native scenario was accepted")
			}
		})
	}
}

func TestImportCloudValidationRejectsMissingSelectedScenarioResult(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	platform := report["platform_result"].(map[string]any)
	platform["results"] = []any{}
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
		t.Fatal("missing selected native scenario result was accepted")
	}
}

func TestImportCloudValidationRejectsInvalidSelectedScenarioContract(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing scenario ID": func(report map[string]any) {
			report["scenarios"].([]any)[0].(map[string]any)["id"] = ""
		},
		"missing Test ID": func(report map[string]any) {
			report["scenarios"].([]any)[0].(map[string]any)["test_id"] = ""
		},
		"duplicate scenario ID": func(report map[string]any) {
			scenarios := report["scenarios"].([]any)
			report["scenarios"] = append(scenarios, scenarios[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
			var report map[string]any
			if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
				t.Fatal(err)
			}
			mutate(report)
			writeTestJSON(t, filepath.Join(dir, "results.json"), report)
			if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
				t.Fatal("invalid selected scenario contract was accepted")
			}
		})
	}
}

func TestImportCloudValidationRejectsDuplicateSelectedScenarioResult(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCloudValidationImportFixture(t, workspace, []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"})
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	platform := report["platform_result"].(map[string]any)
	results := platform["results"].([]any)
	platform["results"] = append(results, results[0])
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	if err := importCloudValidationFeatureEvidence(workspace, catalog, filepath.Join(dir, "results.json"), dir); err == nil {
		t.Fatal("duplicate selected native scenario result was accepted")
	}
}

func TestSDKCloudWorkflowNormalizesBothNativePlatforms(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "sdk-cloud-validation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"Normalize iOS requirement evidence", "Normalize Android requirement evidence",
		"test-feature-coverage import-cloud-validation", "feature-evidence.json",
		`.platform == "ios"`, `.platform == "android"`,
		"vars.SDK_E2E_IOS_CLOUD_SLUG", "vars.SDK_E2E_ANDROID_CLOUD_SLUG",
		"CLOUD_VALIDATION_IOS_ARTIFACT:", "CLOUD_VALIDATION_IOS_ARTIFACT_SHA256:",
		"CLOUD_VALIDATION_ANDROID_ARTIFACT:", "CLOUD_VALIDATION_ANDROID_ARTIFACT_SHA256:",
		"secrets.LINODE_TOKEN", "secrets.CI_RUNNER_GITHUB_WORK_KEY",
		"Initialize private submodules", "git submodule update --init --recursive --depth=1",
		"permitted_classes: [], permitted_symbols: [], aliases: true",
		"runs-on: [self-hosted, macOS, ARM64]", "Verify Swift 6 toolchain", "xcodebuild -version", "Swift version 6",
		"needs: [contract-and-source, ios-live]",
		"(needs.ios-live.result == 'success' || needs.ios-live.result == 'skipped')",
		"Destroy any leftover 1K load generators", "if: always()",
		"HOME100K_ENV_ROOT=cloud_env/staging/runtime", "destroy-vms --live --confirm-live",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("SDK cloud workflow is missing %q", required)
		}
	}
	if strings.Contains(workflow, "submodules: recursive") || strings.Count(workflow, "Initialize private submodules") != 4 {
		t.Fatal("every SDK workflow job must bootstrap private submodules with the deploy key")
	}
	if strings.Count(workflow, "group: sdk-cloud-validation-mobile-host") != 2 {
		t.Fatal("iOS and Android live jobs must share the mobile-host concurrency guard")
	}
	if strings.Count(workflow, "runs-on: [self-hosted, macOS, ARM64]") != 4 {
		t.Fatal("every SDK cloud validation job must select generic macOS ARM64 capabilities")
	}
	if strings.Count(workflow, "CLOUD_VALIDATION_IOS_ARTIFACT:") != 1 || strings.Count(workflow, "CLOUD_VALIDATION_IOS_ARTIFACT_SHA256:") != 1 {
		t.Fatal("iOS live job must explicitly override any host-level platform artifact path and checksum")
	}
	if strings.Count(workflow, "CLOUD_VALIDATION_ANDROID_ARTIFACT:") != 1 || strings.Count(workflow, "CLOUD_VALIDATION_ANDROID_ARTIFACT_SHA256:") != 1 {
		t.Fatal("Android live job must explicitly override any host-level platform artifact path and checksum")
	}
	if strings.Count(workflow, `go-version: "1.25.x"`) != 4 || strings.Contains(workflow, `go-version: "1.24.x"`) {
		t.Fatal("every SDK workflow job must use the scripts/go Go 1.25 toolchain")
	}
	scriptRaw, err := os.ReadFile(filepath.Join(workspace, "e2e_test", "cloud_validation", "scripts", "run-cloud-validation.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"video-cloud-admin-token", "cloud-logger-token", `provider" == "lke"`, `ACCOUNT_MANAGER_BASE_URL="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:-}"`} {
		if !strings.Contains(string(scriptRaw), required) {
			t.Fatalf("SDK cloud credential discovery is missing %q", required)
		}
	}
	for _, scenario := range []string{
		"core-smoke.yaml",
		"nightly-resilience.yaml",
		"shadow-roundtrip.yaml",
		"shadow-offline-nightly.yaml",
	} {
		if !strings.Contains(string(scriptRaw), scenario) {
			t.Fatalf("SDK cloud nightly profile omits required scenario %q", scenario)
		}
	}
}

func TestFeatureQualificationCanRunLiveBeforeRequiredMode(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "feature-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"run_live:", "execute: ${{ steps.selection.outputs.execute }}", `echo "execute=$execute"`,
		"needs.select.outputs.mode == 'required' || needs.select.outputs.execute == 'true'",
		`[ "${{ needs.select.outputs.execute }}" != "true" ]`,
		"RTK_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("feature qualification observe-live wiring is missing %q", required)
		}
	}
}

func writeCloudValidationImportFixture(t *testing.T, workspace string, eventTypes []string) string {
	t.Helper()
	dir := t.TempDir()
	sdk, err := gitOutput(filepath.Join(workspace, "repos", "rtk_cloud_client"), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := gitOutput(filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc"), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	report := map[string]any{
		"run_id": "sdk-import-test", "environment": "staging", "platform": "ios", "status": "PASS",
		"started_at": now.Add(-time.Minute).Format(time.RFC3339), "completed_at": now.Format(time.RFC3339),
		"sdk_commit": strings.TrimSpace(sdk), "contracts_commit": strings.TrimSpace(contracts),
		"platform_result": map[string]any{"status": "PASS", "results": []any{map[string]any{
			"test_id": "E2E-SDK-AUTH-001", "scenario_id": "token_mtls_http", "status": "PASS", "correlation_id": "sdk-import-test-ios-token_mtls_http",
		}}},
		"scenarios": []any{map[string]any{
			"id": "token_mtls_http", "test_id": "E2E-SDK-AUTH-001", "expected_cloud_evidence": []string{"token_issued", "device_mtls_authenticated", "authorized_device_read"},
		}},
	}
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	events := make([]map[string]any, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		events = append(events, map[string]any{
			"scenario_id": "token_mtls_http", "correlation_id": "sdk-import-test-ios-token_mtls_http", "type": eventType, "observed_at": now.Format(time.RFC3339),
		})
	}
	writeTestJSON(t, filepath.Join(dir, "cloud-evidence.json"), map[string]any{"schema_version": 1, "run_id": "sdk-import-test", "platform": "ios", "events": events})
	if err := os.WriteFile(filepath.Join(dir, "junit.xml"), []byte("<testsuite tests=\"1\" failures=\"0\"/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeCloudValidationWebSocketImportFixture(t *testing.T, workspace string, nativeEvidence []string, httpStatus int) string {
	t.Helper()
	dir := writeCloudValidationImportFixture(t, workspace, nil)
	var report map[string]any
	if err := readJSONFile(filepath.Join(dir, "results.json"), &report); err != nil {
		t.Fatal(err)
	}
	correlationID := "sdk-import-test-ios-websocket_receive_roundtrip"
	report["platform_result"] = map[string]any{"status": "PASS", "results": []any{map[string]any{
		"test_id": "E2E-SDK-WS-002", "scenario_id": "websocket_receive_roundtrip", "status": "PASS",
		"correlation_id": correlationID, "evidence": nativeEvidence,
	}}}
	report["scenarios"] = []any{map[string]any{
		"id": "websocket_receive_roundtrip", "test_id": "E2E-SDK-WS-002", "expected_cloud_evidence": []string{"command_dispatched"},
	}}
	writeTestJSON(t, filepath.Join(dir, "results.json"), report)
	writeTestJSON(t, filepath.Join(dir, "cloud-evidence.json"), map[string]any{
		"schema_version": 1, "run_id": "sdk-import-test", "platform": "ios", "events": []any{map[string]any{
			"scenario_id": "websocket_receive_roundtrip", "correlation_id": correlationID, "type": "command_dispatched",
			"observed_at": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "evidence": map[string]any{"http_status": httpStatus},
		}},
	})
	return dir
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

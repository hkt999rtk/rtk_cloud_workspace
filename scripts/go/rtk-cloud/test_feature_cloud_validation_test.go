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
		"secrets.LINODE_TOKEN", "secrets.CI_RUNNER_GITHUB_WORK_KEY",
		"Initialize private submodules", "git submodule update --init --recursive --depth=1",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("SDK cloud workflow is missing %q", required)
		}
	}
	if strings.Contains(workflow, "submodules: recursive") || strings.Count(workflow, "Initialize private submodules") != 4 {
		t.Fatal("every SDK workflow job must bootstrap private submodules with the deploy key")
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

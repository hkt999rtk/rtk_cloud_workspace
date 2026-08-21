package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandUsageAndValidationBoundaries(t *testing.T) {
	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ci-runners", "--help"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"unknown"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command error = %v", err)
	}
	if err := run([]string{"ci-runners", "unknown"}); err == nil || !strings.Contains(err.Error(), "unknown ci-runners") {
		t.Fatalf("unknown CI runner command error = %v", err)
	}

	if err := runMQTTLoadTest(nil); err != nil {
		t.Fatal(err)
	}
	if err := runMQTTLoadTest([]string{"unknown"}); err == nil {
		t.Fatal("unknown MQTT load command unexpectedly passed")
	}
	printDeploymentUsage()
	if err := runMQTTTraceReport(nil); err == nil || !strings.Contains(err.Error(), "--env-root") {
		t.Fatalf("MQTT trace validation error = %v", err)
	}
	if err := runProvisionK8s(nil); err == nil || !strings.Contains(err.Error(), "--env-root") {
		t.Fatalf("provision-k8s validation error = %v", err)
	}
	if err := runStagingE2E([]string{"--steps", "invalid"}); err == nil {
		t.Fatal("invalid staging E2E steps unexpectedly passed")
	}
	if err := runTestLive([]string{"--steps", "invalid"}); err == nil {
		t.Fatal("invalid test-live steps unexpectedly passed")
	}
	if err := runTestCatalog([]string{"unknown"}); err == nil || !strings.Contains(err.Error(), "unknown test-catalog") {
		t.Fatalf("test catalog validation error = %v", err)
	}
	if err := runCollectEvidence([]string{"--env-root", ""}); err == nil {
		t.Fatal("collect-evidence without an environment unexpectedly passed")
	}
}

func TestCIRunnerListAndSmallGovernanceHelpers(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' '{"runners":[{"name":"runner-1","status":"online","busy":false,"labels":[{"name":"go"}]}]}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := runCIRunnersList(nil); err != nil {
		t.Fatal(err)
	}

	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if commits := collectContractsCommits(workspace); commits["repos/rtk_cloud_contracts_doc"] == "" {
		t.Fatal("root contracts commit was not collected")
	}
	names := sortedCapacityWorkloadNames()
	if len(names) != len(capacityWorkloadRegistry) || names[0] > names[len(names)-1] {
		t.Fatalf("sorted workload names = %v", names)
	}
	for _, value := range []any{float64(1), int(2), int64(3), uint64(4), "invalid"} {
		_ = uint64Number(value)
	}
	if exitCode(7).Error() != "exit status 7" {
		t.Fatal("exit code rendering changed")
	}
}

func TestReportAndProvisioningBoundaryHelpers(t *testing.T) {
	reportDir := t.TempDir()
	summaryPath := filepath.Join(reportDir, "summary.json")
	if err := os.WriteFile(summaryPath, []byte(`{
  "overall":"pass",
  "generated_at":"2026-07-24T01:02:03Z",
  "env_root":"/tmp/runtime",
  "stack":"qualification",
  "brandname":"RTK",
  "artifacts":{"report_file":"TEST_REPORT.md","data_setup_summary_file":"data.json","bind_validation_dir":"bind"},
  "steps":[{"name":"catalog","status":"PASS","duration_seconds":2,"log_file":"catalog.log"}]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStagingInstallReport("lke", summaryPath, "", reportDir); err != nil {
		t.Fatal(err)
	}
	install, err := os.ReadFile(filepath.Join(reportDir, "INSTALL_REPORT.md"))
	if err != nil || !strings.Contains(string(install), "Total duration seconds: 2") {
		t.Fatalf("install report = %q, error = %v", install, err)
	}
	printStagingFinalReportPaths(reportDir)
	printE2EDataSetupPlan("/workspace", "/runtime", "RTK", 2, 4, "light=4", 1, 2, 3, map[string]string{
		"create-brand": "brand", "create-users": "users", "generate-devices": "devices",
		"bind-devices": "bind", "validate-bind": "validate",
	})

	key, csr, err := generateAppCertificateCSRWithAlgorithm("coverage.example", "ed25519")
	if err != nil || !strings.Contains(key, "PRIVATE KEY") || !strings.Contains(csr, "CERTIFICATE REQUEST") {
		t.Fatalf("CSR generation failed: %v", err)
	}
	if !snapshotFailed(bindProvisioningStateSnapshot{ReadinessState: "activation_failed"}) ||
		snapshotFailed(bindProvisioningStateSnapshot{ReadinessState: "ready"}) {
		t.Fatal("provisioning failure state classification changed")
	}
	if logShellQuote("it's") != "'it'\\''s'" {
		t.Fatal("log shell quoting changed")
	}

	root := t.TempDir()
	paths := newProvisionPaths(root, filepath.Join(root, "runtime"), provisionOptions{})
	if provisionVPCCIDR(paths) != "10.42.1.0/24" ||
		accountManagerPrivateIPv4(paths) != "10.42.1.50" ||
		adminPrivateIPv4(paths) != "10.42.1.60" ||
		loggerPrivateIPv4(paths) != "10.42.1.90" ||
		videoCloudPrometheusBaseURL(paths) != "http://10.42.1.30:9090" {
		t.Fatal("provisioning defaults changed")
	}
	if (retiredVMProvider{name: "linode"}).Name() != "linode" ||
		(unsupportedKubernetesProvider{name: "aws"}).Name() != "aws" {
		t.Fatal("provider name changed")
	}
}

func TestAdditionalStagingAndTraceValidationBoundaries(t *testing.T) {
	if err := runStagingAcceptance([]string{"--steps", "invalid"}); err == nil {
		t.Fatal("staging acceptance accepted invalid steps")
	}
	if err := runEnvironmentAcceptance([]string{"--steps", "invalid"}); err == nil {
		t.Fatal("environment acceptance accepted invalid steps")
	}
	t.Setenv("RTK_CLOUD_STACK_FILE", "")
	t.Setenv("RTK_CLOUD_STAGING_ENV_ROOT", "")
	if err := runStagingResetK8s([]string{"--workspace", t.TempDir(), "--env-root", filepath.Join(t.TempDir(), "missing"), "--plan"}); err != nil {
		t.Fatal(err)
	}

	resultsPath := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(resultsPath, []byte(`{
  "brandname":"RTK",
  "overall":"pass",
  "devices":[{"device_id":"device-1","status":"PASS"}]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "trace.md")
	if err := runMQTTTraceReport([]string{"--results-file", resultsPath, "--out-file", outPath}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(outPath); err != nil || !strings.Contains(string(body), "device-1") {
		t.Fatalf("trace report = %q, error = %v", body, err)
	}
}

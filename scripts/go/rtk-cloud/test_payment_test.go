package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAggregatePaymentEvidenceMapsCatalogSelectorsAndStatuses(t *testing.T) {
	cases := []testCatalogCase{
		{
			ID: "E2E-AM-AUTOTOPUP-001", Title: "one charge and credit", Method: "fake provider flow",
			Selector: "./internal/paymentservice#TestSuccess,./internal/paymentstore#TestLedger",
		},
	}
	units := []goUnitResult{
		{CanonicalKey: "go://billing-service/internal/paymentservice#TestSuccess", Source: "internal/paymentservice/integration_test.go", StartedAt: "2026-08-15T10:00:00Z", CompletedAt: "2026-08-15T10:00:00.100Z", DurationMS: 100, Status: "PASS"},
		{CanonicalKey: "go://billing-service/internal/paymentstore#TestLedger", Source: "internal/paymentstore/integration_test.go", StartedAt: "2026-08-15T10:00:00.200Z", CompletedAt: "2026-08-15T10:00:00.500Z", DurationMS: 300, Status: "PASS"},
	}
	report, err := aggregatePaymentEvidence("run-1", "fake-e2e", cases, units)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASS" || report.DurationMS != 500 || len(report.Cases) != 1 || report.Cases[0].DurationMS != 400 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if got := string(renderPaymentEvidenceReport(report)); !strings.Contains(got, "E2E-AM-AUTOTOPUP-001") || !strings.Contains(got, "fake provider flow") {
		t.Fatalf("rendered report missing traceability: %s", got)
	}
}

func TestAggregatePaymentEvidenceFailsClosedForMissingOrSkippedOperations(t *testing.T) {
	cases := []testCatalogCase{{
		ID: "E2E-AM-AUTOTOPUP-002", Title: "reconciliation", Method: "failure injection",
		Selector: "./internal/paymentservice#TestSkipped,./internal/paymentservice#TestMissing",
	}}
	units := []goUnitResult{{
		CanonicalKey: "go://billing-service/internal/paymentservice#TestSkipped",
		StartedAt:    "2026-08-15T10:00:00Z", CompletedAt: "2026-08-15T10:00:00.010Z", DurationMS: 10, Status: "SKIP",
	}}
	report, err := aggregatePaymentEvidence("run-2", "fake-e2e", cases, units)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "INCOMPLETE" || report.Cases[0].Status != "INCOMPLETE" || !strings.Contains(report.Cases[0].Assessment, "TestMissing") {
		t.Fatalf("missing operation must be incomplete: %+v", report)
	}
}

func TestPaymentSelectorCanonicalKeyRequiresExplicitPackage(t *testing.T) {
	got, err := paymentSelectorCanonicalKey("./internal/paymentservice#TestTimeout")
	if err != nil || got != "go://billing-service/internal/paymentservice#TestTimeout" {
		t.Fatalf("canonical=%q err=%v", got, err)
	}
	for _, invalid := range []string{"TestTimeout", "./internal/paymentservice#", "#TestTimeout", "./internal/paymentservice#BenchmarkOnly"} {
		if _, err := paymentSelectorCanonicalKey(invalid); err == nil {
			t.Fatalf("selector %q should fail", invalid)
		}
	}
}

func TestReadPaymentUnitManifestRejectsMissingMalformedAndIncompleteEvidence(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := readPaymentUnitManifest(missing); err == nil || !strings.Contains(err.Error(), "read payment unit manifest") {
		t.Fatalf("missing manifest error = %v", err)
	}
	malformed := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(malformed, []byte("{bad json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPaymentUnitManifest(malformed); err == nil || !strings.Contains(err.Error(), "parse payment unit manifest") {
		t.Fatalf("malformed manifest error = %v", err)
	}
	incomplete := filepath.Join(t.TempDir(), "incomplete.json")
	if err := os.WriteFile(incomplete, []byte(`{"schema_version":1,"module":"other","tests":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPaymentUnitManifest(incomplete); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete manifest error = %v", err)
	}
	if err := runTestPayment([]string{"--profile", "unsupported"}); err == nil || !strings.Contains(err.Error(), "unsupported payment profile") {
		t.Fatalf("unsupported fake profile error = %v", err)
	}
	if err := runTestPayment([]string{"--unknown"}); err == nil {
		t.Fatal("unknown payment flag passed")
	}
}

func TestRunTestPaymentReportsCoverageAndEvidenceFailureTogether(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "unit-payment-missing-evidence"
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(workspace, ".artifacts", "test-runs", runID)) })

	oldRunner := paymentCoverageRunner
	paymentCoverageRunner = func([]string) error { return errors.New("synthetic coverage failure") }
	t.Cleanup(func() { paymentCoverageRunner = oldRunner })

	err = runTestPayment([]string{"--profile", "fake-e2e", "--run-id", runID})
	if err == nil || !strings.Contains(err.Error(), "payment coverage failed") || !strings.Contains(err.Error(), "unit evidence is unavailable") {
		t.Fatalf("combined coverage/evidence error = %v", err)
	}

	paymentCoverageRunner = func([]string) error { return nil }
	runID = "unit-payment-evidence-only-failure"
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(workspace, ".artifacts", "test-runs", runID)) })
	err = runTestPayment([]string{"--profile", "fake-e2e", "--run-id", runID})
	if err == nil || !strings.Contains(err.Error(), "read payment unit manifest") {
		t.Fatalf("missing evidence error = %v", err)
	}
}

func TestCoverageWorkflowPublishesPaymentEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "go-coverage-governance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"test-payment --profile fake-e2e",
		"billing-pr/payments/**",
		"billing-pr/coverage/**",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("coverage workflow does not publish payment evidence %q", required)
		}
	}
}

func TestFakePaymentProfileExcludesStagingLiveCases(t *testing.T) {
	if !isFakePaymentCase(testCatalogCase{ID: "E2E-AM-SIMULATOR-001", Status: "active", Runner: "test-payment", Layer: "e2e"}) {
		t.Fatal("active local payment E2E case was excluded")
	}
	if isFakePaymentCase(testCatalogCase{ID: "LIVE-STG-SIMULATOR-001", Status: "active", Runner: "test-payment", Layer: "live"}) {
		t.Fatal("fake profile included staging-live case")
	}
}

func TestRunTestPaymentBuildsTraceableEvidenceFromCoverageArtifacts(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "unit-payment-evidence"
	runRoot := filepath.Join(workspace, ".artifacts", "test-runs", runID)
	t.Cleanup(func() { _ = os.RemoveAll(runRoot) })

	oldRunner := paymentCoverageRunner
	coverageShouldFail := false
	paymentCoverageRunner = func(args []string) error {
		if got := commandFlagValue(args, "--module"); got != "billing-service" {
			t.Fatalf("coverage module = %q", got)
		}
		catalog, err := loadAndValidateTestCatalog(workspace)
		if err != nil {
			return err
		}
		units := make([]goUnitResult, 0)
		for _, tc := range catalog.Cases {
			if !isFakePaymentCase(tc) {
				continue
			}
			for _, selector := range strings.Split(tc.Selector, ",") {
				canonical, err := paymentSelectorCanonicalKey(selector)
				if err != nil {
					return err
				}
				units = append(units, goUnitResult{CanonicalKey: canonical, Source: "integration_test.go", StartedAt: "2026-08-17T00:00:00Z", CompletedAt: "2026-08-17T00:00:00.001Z", DurationMS: 1, Status: "PASS"})
			}
		}
		coverageRunID := commandFlagValue(args, "--run-id")
		coverageDir := filepath.Join(workspace, ".artifacts", "test-runs", coverageRunID, "coverage")
		moduleDir := filepath.Join(coverageDir, "modules", "billing-service")
		if err := os.MkdirAll(moduleDir, 0o755); err != nil {
			return err
		}
		manifest, _ := json.Marshal(paymentUnitManifest{SchemaVersion: 1, Module: "billing-service", Profile: "pr", Tests: units})
		files := map[string][]byte{
			filepath.Join(moduleDir, "unit-manifest.json"):            manifest,
			filepath.Join(moduleDir, "coverage.out"):                  []byte("mode: set\n"),
			filepath.Join(moduleDir, "junit.xml"):                     []byte("<testsuites/>\n"),
			filepath.Join(moduleDir, "package-coverage.json"):         []byte("{}\n"),
			filepath.Join(moduleDir, "test-events.json"):              []byte("[]\n"),
			filepath.Join(coverageDir, "logs", "billing-service.log"): []byte("billing integration PASS\n"),
		}
		for path, body := range files {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return err
			}
		}
		if coverageShouldFail {
			return errors.New("synthetic coverage gate failure")
		}
		return nil
	}
	t.Cleanup(func() { paymentCoverageRunner = oldRunner })

	if err := runTestPayment([]string{"--profile", "fake-e2e", "--run-id", runID}); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(runRoot, "payments", "fake-e2e", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"status": "PASS"`) || !strings.Contains(string(result), `"coverage_gate": "PASS"`) {
		t.Fatalf("payment result is incomplete: %s", result)
	}

	failedRunID := "unit-payment-coverage-fail"
	failedRunRoot := filepath.Join(workspace, ".artifacts", "test-runs", failedRunID)
	t.Cleanup(func() { _ = os.RemoveAll(failedRunRoot) })
	coverageShouldFail = true
	if err := runTestPayment([]string{"--profile", "fake-e2e", "--run-id", failedRunID}); err == nil {
		t.Fatal("failed coverage gate did not fail payment qualification")
	}
	failedResult, err := os.ReadFile(filepath.Join(failedRunRoot, "payments", "fake-e2e", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(failedResult), `"status": "FAIL"`) || !strings.Contains(string(failedResult), `"coverage_gate": "FAIL"`) {
		t.Fatalf("coverage failure was not preserved: %s", failedResult)
	}
}

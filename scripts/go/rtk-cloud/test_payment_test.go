package main

import (
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

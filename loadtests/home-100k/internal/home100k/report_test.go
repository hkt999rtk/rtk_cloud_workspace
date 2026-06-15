package home100k

import (
	"strings"
	"testing"
)

func TestReportRendersRequiredScenariosAndIncompleteEvidence(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-1",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  false,
		LoadGeneratorHealthy: true,
	})

	for _, want := range []string{
		"Status: INCOMPLETE",
		"## Test Conditions",
		"## Device Scenario",
		"## User Scenario",
		"## IoT Device Shadow Scenario",
		"Light: 50000",
		"Offline desired queue",
		"Missing server evidence",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportRedactsSecretsAndMarksLoadGeneratorSaturation(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-2",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: false,
		Notes:                []string{"bearer abc", "private_key_pem xyz", "normal note"},
	})

	if !strings.Contains(report, "Status: INCOMPLETE") {
		t.Fatalf("report did not mark saturation incomplete:\n%s", report)
	}
	if !strings.Contains(report, "Load-generator saturation invalidated server-capacity conclusion") {
		t.Fatalf("report missing load-generator saturation note:\n%s", report)
	}
	if !strings.Contains(report, "## Load Generator Health") || !strings.Contains(report, "load-generator saturated") {
		t.Fatalf("report missing load-generator health section:\n%s", report)
	}
	if strings.Contains(report, "bearer abc") || strings.Contains(report, "private_key_pem xyz") {
		t.Fatalf("report leaked secret note:\n%s", report)
	}
	if !strings.Contains(report, "redacted sensitive detail") || !strings.Contains(report, "normal note") {
		t.Fatalf("report did not retain redacted/non-secret notes:\n%s", report)
	}
}

func TestReportRendersRequiredStageMetrics(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-3",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		StageResults: []StageResult{{
			Name:                           "25k",
			ConnectedDevices:               25000,
			MQTTConnectSuccessRatePercent:  99.9,
			MQTTReconnectCount:             5,
			ShadowGetP50MS:                 45,
			ShadowGetP95MS:                 90,
			ShadowGetP99MS:                 130,
			DesiredUpdateP95MS:             120,
			DeltaReceiveP95MS:              135,
			DesiredReportedP95MS:           280,
			OfflineDesiredP95MS:            840,
			DeltaClearSuccessRatePercent:   100,
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			VersionConflictCount:           1,
			RejectedUpdateCount:            2,
			AuthorizationViolationCount:    3,
			ClientTokenCorrelationCount:    4,
		}},
	})
	for _, want := range []string{
		"Desired update p95",
		"Shadow get p50",
		"Shadow get p99",
		"Reconnects",
		"Delta receive p95",
		"Desired->reported p95",
		"Offline desired p95",
		"Rejected",
		"Auth violations",
		"Client tokens",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

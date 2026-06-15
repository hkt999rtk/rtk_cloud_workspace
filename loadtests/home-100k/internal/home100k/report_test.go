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
		"## Counter Scope",
		"synthetic actor sample counters",
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
			Name:             "25k",
			ConnectedDevices: 25000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:   25000,
				ConnectSuccess:    24990,
				Publishes:         50000,
				ReceivedMessages:  25000,
				DeltaReceived:     25000,
				ReportedPublishes: 25000,
				BytesSent:         1024,
				BytesReceived:     2048,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts:       1250,
				LoginSuccess:        1250,
				ListDevicesRequests: 1250,
				ReadShadowRequests:  25000,
				DesiredWrites:       25000,
				ReceivedAcks:        25000,
				BytesSent:           4096,
				BytesReceived:       8192,
			},
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
		ServerCorrelation: ServerCorrelation{
			Status: "pass",
			Checks: []CorrelationCheck{{
				Source:      "mqtt",
				Counter:     "device_mqtt.publishes",
				ClientTotal: 50000,
				ServerTotal: 50000,
				Status:      "pass",
			}},
		},
		SyncTelemetry: SyncTelemetry{
			VMs: []VMSyncTelemetry{{
				Label:            "home-100k-mixed-000",
				FilesTransferred: 6,
				BytesTransferred: 1048576,
				RemoteDiskBefore: "2.4G",
				RemoteDiskAfter:  "2.5G",
			}},
		},
	})
	for _, want := range []string{
		"## Status Summary",
		"## Counter Scope",
		"## Device MQTT Totals",
		"## APP/User Totals",
		"## Server Log Correlation",
		"## Sync/Provision Telemetry",
		"device_mqtt.publishes",
		"home-100k-mixed-000",
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

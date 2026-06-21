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
		"Runner nofile limit: 1048576",
		"Device session model: `lifetime-subscription`",
		"Runner read model: `go-netpoll-bounded-reader-goroutine`",
		"sustained MQTT reads",
		"## Gate Standards",
		"Functional success threshold: 99.50%",
		"Aggregate counter tolerance: max(5, 0.10%)",
		"## Counter Scope",
		"synthetic actor sample counters",
		"## Device Scenario",
		"## User Scenario",
		"## IoT Device Shadow Scenario",
		"Scenario profile: `home-diverse-v1`",
		"## Device Traffic Profiles",
		"camera_status",
		"## User Scenario Profiles",
		"owner_admin",
		"Light: 18000",
		"Offline desired queue",
		"Missing server evidence",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportMarksMissingPerTypeEvidenceAsCompleteFailure(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	plan.DeviceMix = map[string]int{"light": 1, "smart_meter": 1}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-missing-type-evidence",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)},
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults: []StageResult{{
			Name:             "1",
			ConnectedDevices: 1,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectSuccess:      1,
				Subscribes:          1,
				ActiveConnections:   1,
				ActiveSubscriptions: 1,
				Publishes:           1,
				ReceivedMessages:    1,
				DeltaReceived:       1,
				ReportedPublishes:   1,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 1,
				ReceivedAcks:  1,
			},
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {TelemetryPublishes: 1},
			},
		}},
	})
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: FAIL",
		"Missing per-device-type MQTT evidence: smart_meter",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportMarksFunctionalThresholdFailure(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:     "cloud_env/staging/lke",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 20,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	plan.DeviceMix = map[string]int{"light": 20}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-functional-fail",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		RuntimeLogCorrelation: RuntimeLogCorrelation{
			Status:              "pass",
			ClientCommandEvents: 1,
		},
		StageResults: []StageResult{{
			Name:             "100pct",
			ConnectedDevices: 20,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ActiveConnections:   20,
				ActiveSubscriptions: 20,
				ConnectSuccess:      20,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 1,
				ReceivedAcks:  0,
			},
			MQTTConnectSuccessRatePercent:  100,
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {TelemetryPublishes: 1},
			},
		}},
	})

	for _, want := range []string{
		"Status: COMPLETE",
		"Result: FAIL",
		"stage 100pct app ACK success rate 0.00% below 99.50% threshold",
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

func TestReportRendersFailureEventSamples(t *testing.T) {
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
		RunID:                "run-failure-events",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		StageResults: []StageResult{{
			Name: "target",
			FailureEvents: []FailureEvent{{
				Stage:       "target",
				Reason:      "device_delta_wait_failed",
				Detail:      "network EOF",
				Phase:       "device_delta_wait",
				DeviceID:    "rtk-0041",
				CommandID:   "cmd-0041",
				EventIndex:  61,
				SessionSlot: 61,
				RemainingMS: 12000,
				MQTTTarget:  "172.238.59.219:8883",
				ReaderError: "network EOF",
				OccurredAt:  "2026-06-17T02:59:00Z",
			}},
		}},
	})
	for _, want := range []string{
		"## Failure Event Samples",
		"device_delta_wait_failed",
		"rtk-0041",
		"cmd-0041",
		"network EOF",
		"172.238.59.219:8883",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
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
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {
					TelemetryPublishes: 10,
					DesiredWrites:      7,
					ReportedPublishes:  7,
					EventPublishes:     1,
				},
				"environment_sensor": {
					TelemetryPublishes: 30,
					DesiredWrites:      0,
					ReportedPublishes:  0,
					EventPublishes:     2,
				},
			},
			UserActionTotals: map[string]int64{
				"open_home_refresh": 6,
				"scene_command":     3,
			},
			UsageWindowTotals: map[string]int64{
				"evening_peak": 9,
			},
			StageDiagnostics: []map[string]any{{
				"connected_target":   25000,
				"connected_after":    10000,
				"connect_attempts":   12000,
				"connect_failures":   2000,
				"commands_scheduled": 2500,
				"skip_reason":        "device_connect_target_missed",
			}},
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
				Label:            "lg01",
				FilesTransferred: 6,
				BytesTransferred: 1048576,
				RemoteDiskBefore: "2.4G",
				RemoteDiskAfter:  "2.5G",
			}},
		},
		StartCoordination: StartCoordination{
			Mode:         "host-coordinator",
			ReadyBarrier: "1/1",
			StartDelayMS: 3000,
			MaxSkewMS:    12,
			VMs: []VMStartTelemetry{{
				Label:  "lg01",
				IP:     "192.0.2.10",
				Status: "completed",
			}},
		},
		ServerEvidence: ServerEvidence{Sources: map[string]EvidenceSource{
			"host_pod_resources": {Available: true, Samples: []EvidenceResourceSample{
				{Kind: "k8s_pod_top", Namespace: "video-cloud-staging-platform", Pod: "postgresql-0", CPUCoreMil: 120, MemoryBytes: 235 * 1024 * 1024},
				{Kind: "k8s_pod_top", Namespace: "video-cloud-staging-platform", Pod: "postgresql-0", CPUCoreMil: 220, MemoryBytes: 236 * 1024 * 1024},
				{Kind: "k8s_pod_top", Namespace: "video-cloud-staging-platform", Pod: "postgresql-0", CPUCoreMil: 180, MemoryBytes: 234 * 1024 * 1024},
			}},
		}},
	})
	for _, want := range []string{
		"## Status Summary",
		"## Counter Scope",
		"## Device MQTT Totals",
		"## APP/User Totals",
		"## Per-Type MQTT Totals",
		"environment_sensor",
		"## User Action Totals",
		"scene_command",
		"## Usage Window Totals",
		"evening_peak",
		"## Server Log Correlation",
		"## Target Diagnostics",
		"## Sync/Provision Telemetry",
		"## Load Generator Start Coordination",
		"- max start skew ms: 12\n\n| VM | IP | Status | Ready at |",
		"device_connect_target_missed",
		"device_mqtt.publishes",
		"lg01",
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
		"## Postgres Pod Resource Usage",
		"CPU p95",
		"Memory p95",
		"postgresql-0",
		"220m",
		"236Mi",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportRendersCompleteFailWhenSuccessRateBelowThreshold(t *testing.T) {
	plan := Plan{
		Conditions: TestConditions{Devices: 10000, Users: 500},
		DeviceMix:  map[string]int{"light": 1},
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "run-complete-fail",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)},
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults: []StageResult{{
			Name:             "100pct",
			ConnectedDevices: 10000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectSuccess:      9900,
				Subscribes:          9900,
				ActiveConnections:   9900,
				ActiveSubscriptions: 9900,
				Publishes:           9900,
				ReceivedMessages:    9900,
				DeltaReceived:       9900,
				ReportedPublishes:   9900,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 500,
				ReceivedAcks:  500,
			},
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {TelemetryPublishes: 9900},
			},
		}},
	})
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: FAIL",
		"connection success rate 99.00% below 99.50% threshold",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

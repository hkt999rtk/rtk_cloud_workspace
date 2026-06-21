package home100k

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReportScriptRendersTemplateWithResourceTimelines(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	script := filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "generate-report.sh"))
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	result := RunResult{
		RunID:  "script-report-test",
		Status: "INCOMPLETE",
		Plan:   plan,
		StageResults: []StageResult{{
			Name:                  "25k",
			ConnectedDevices:      25000,
			ShardConnectedDevices: 25000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:          10,
				ConnectSuccess:           10,
				TokenAttempts:            12,
				TokenSuccess:             10,
				TokenFail:                2,
				TokenFirstAttemptSuccess: 8,
				TokenFirstAttemptFail:    2,
				TokenRetryAttempts:       2,
				TokenRetrySuccess:        2,
				TokenRetryExhausted:      0,
				Subscribes:               10,
				ActiveConnections:        25000,
				ActiveSubscriptions:      25000,
				Publishes:                20,
				ReceivedMessages:         20,
				DeltaReceived:            10,
				ReportedPublishes:        10,
				BytesSent:                1000,
				BytesReceived:            2000,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts:            5,
				LoginSuccess:             5,
				TokenAttempts:            6,
				TokenSuccess:             5,
				TokenFail:                1,
				TokenFirstAttemptSuccess: 4,
				TokenFirstAttemptFail:    1,
				TokenRetryAttempts:       1,
				TokenRetrySuccess:        1,
				TokenRetryExhausted:      0,
				ListDevicesRequests:      5,
				ReadShadowRequests:       10,
				DesiredWrites:            10,
				ReceivedAcks:             10,
				BytesSent:                700,
				BytesReceived:            900,
			},
			ShadowGetP95MS:       42,
			DesiredReportedP95MS: 88,
			PhaseMetrics: map[string]PhaseMetric{
				"device_request_token": {
					Attempts: 12,
					Success:  10,
					Fail:     2,
					TotalMS:  24000,
					MaxMS:    11000,
					GT1S:     4,
					GT5S:     2,
					GT10S:    1,
				},
				"app_shadow_accepted_wait": {
					Attempts: 10,
					Success:  8,
					Fail:     2,
					TotalMS:  90000,
					MaxMS:    15000,
					GT1S:     8,
					GT5S:     4,
					GT10S:    1,
				},
			},
			BottleneckEvents: []BottleneckEvent{{
				Stage:       "25k",
				Phase:       "device_request_token",
				Actor:       "device",
				DeviceID:    "rtk-0001",
				Detail:      "device request_token context deadline exceeded",
				ElapsedMS:   11000,
				RemainingMS: 25000,
				Attempt:     2,
				IsRetry:     true,
				MQTTTarget:  "203.0.113.10:8883",
				OccurredAt:  "2026-06-15T00:00:10Z",
			}},
			FailureReasons: map[string]int64{
				"app_desired_publish_failed": 2,
			},
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {
					TelemetryPublishes: 3,
					DesiredWrites:      2,
					DeltaReceived:      2,
					ReportedPublishes:  2,
					BytesSent:          300,
					BytesReceived:      200,
				},
				"security_sensor": {
					EventPublishes:    4,
					ReportedPublishes: 4,
					BytesSent:         120,
					BytesReceived:     80,
				},
			},
			UserActionTotals: map[string]int64{
				"morning_routine": 4,
				"security_check":  2,
			},
			UsageWindowTotals: map[string]int64{
				"morning": 6,
			},
		}, {
			Name:                  "50k",
			ConnectedDevices:      50000,
			ShardConnectedDevices: 50000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     25000,
				ConnectSuccess:      25000,
				Subscribes:          25000,
				ActiveConnections:   50000,
				ActiveSubscriptions: 50000,
				Publishes:           2500,
				ReceivedMessages:    2500,
				DeltaReceived:       2500,
				ReportedPublishes:   2500,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 2500,
				ReceivedAcks:  2500,
			},
			PhaseMetrics: map[string]PhaseMetric{
				"device_request_token": {
					Attempts: 1,
					Success:  1,
					TotalMS:  10000,
					MaxMS:    10000,
					GT1S:     1,
					GT5S:     1,
				},
			},
			DeviceTypeTotals: map[string]DeviceTypeTotals{
				"light": {
					TelemetryPublishes: 6,
					DesiredWrites:      3,
					DeltaReceived:      3,
					ReportedPublishes:  3,
					BytesSent:          600,
					BytesReceived:      400,
				},
			},
			UserActionTotals: map[string]int64{
				"away_monitoring": 5,
			},
			UsageWindowTotals: map[string]int64{
				"away": 5,
			},
		}},
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts: 10,
			Publishes:       20,
		},
		AppUserTotals: AppUserTotals{
			LoginAttempts: 5,
			DesiredWrites: 10,
		},
		ServerCorrelation: ServerCorrelation{
			Status:  "incomplete",
			Reasons: []string{"missing server counters"},
		},
		ServerEvidence: ServerEvidence{Complete: true, Sources: map[string]EvidenceSource{
			"host_pod_resources": {Available: true, Samples: []EvidenceResourceSample{
				{Kind: "k8s_pod_top", Namespace: "video-cloud-staging-platform", Pod: "postgresql-0", CPUCoreMil: 110, MemoryBytes: 235 * 1024 * 1024},
				{Kind: "k8s_pod_top", Namespace: "video-cloud-staging-platform", Pod: "postgresql-0", CPUCoreMil: 190, MemoryBytes: 236 * 1024 * 1024},
			}},
			"video_cloud_api": {Available: true, Counters: map[string]int64{
				"video_cloud_api.request_token.status_5xx":       2,
				"video_cloud_api.request_token.duration_p95_ms":  11000,
				"video_cloud_api.request_token.duration_p99_ms":  11000,
				"video_cloud_api.request_token.pod_api_0.max_ms": 11000,
				"video_cloud_api.timeout":                        3,
			}},
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.conn_congestion": 1,
			}},
			"postgres_activity": {Available: true, Counters: map[string]int64{
				"postgres.activity.active":              40,
				"postgres.activity.idle":                12,
				"postgres.activity.idle_in_transaction": 1,
				"postgres.locks.waiting":                2,
			}},
		}},
		SyncTelemetry: SyncTelemetry{VMs: []VMSyncTelemetry{{
			Label:            "home-100k-mixed-000",
			FilesTransferred: 3,
			BytesTransferred: 4096,
			ElapsedMS:        1200,
		}}},
		StartCoordination: StartCoordination{
			Mode:         "host-coordinator",
			ReadyBarrier: "1/1",
			StartDelayMS: 3000,
			MaxSkewMS:    12,
			VMs: []VMStartTelemetry{{
				Label:  "home-100k-mixed-000",
				IP:     "192.0.2.10",
				Status: "completed",
			}},
		},
		ReportFile: filepath.Join(outDir, "TEST_REPORT.md"),
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile(results.json) error = %v", err)
	}
	resourceDir := filepath.Join(outDir, "resource-samples")
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(resource-samples) error = %v", err)
	}
	loadRows := strings.Join([]string{
		"time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct",
		"2026-06-15T00:00:00Z\tscript-report-test\trun-stages\thome-100k-mixed-000\t192.0.2.10\tmixed\t123\tok\t10.0\t0.50\t100\t1000\t2G\t25G\t8",
		"2026-06-15T00:00:30Z\tscript-report-test\trun-stages\thome-100k-mixed-000\t192.0.2.10\tmixed\t123\tok\t90.0\t1.50\t700\t1000\t3G\t25G\t12",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(resourceDir, "load-vms.tsv"), []byte(loadRows), 0o644); err != nil {
		t.Fatalf("WriteFile(load-vms.tsv) error = %v", err)
	}
	k8sRows := strings.Join([]string{
		"time\trun_id\tphase\tname\tstatus\tcpu\tcpu_pct\tmem\tmem_pct\treason",
		"2026-06-15T00:00:00Z\tscript-report-test\trun-stages\tlke-node-a\tok\t100m\t10\t1Gi\t30\t",
		"2026-06-15T00:00:30Z\tscript-report-test\trun-stages\tlke-node-a\tok\t850m\t85\t3Gi\t75\t",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(resourceDir, "k8s-nodes.tsv"), []byte(k8sRows), 0o644); err != nil {
		t.Fatalf("WriteFile(k8s-nodes.tsv) error = %v", err)
	}

	cmd := exec.Command("bash", script, "--out-dir", outDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate-report.sh error = %v output=%s", err, string(output))
	}
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "TEST_REPORT.md"))
	if err != nil {
		t.Fatalf("ReadFile(TEST_REPORT.md) error = %v", err)
	}
	report := string(reportRaw)
	for _, want := range []string{
		"## Load Machine Resource Usage",
		"- sample window: 2026-06-15T00:00:00Z -> 2026-06-15T00:00:30Z\n\n| VM | Role | IP | Samples |",
		"home-100k-mixed-000",
		"CPU p95",
		"## K8s Node Resource Usage During Test",
		"- sample window: 2026-06-15T00:00:00Z -> 2026-06-15T00:00:30Z\n\n| Node | Samples | CPU p95 |",
		"lke-node-a",
		"Mem max",
		"## Load Generator Start Coordination",
		"- max start skew ms: 12\n\n| VM | IP | Status | Ready at |",
		"## Report Source Artifacts",
		"resource-samples/load-vms.tsv",
		"resource-samples/k8s-nodes.tsv",
		"### Postgres Pod Resource Usage",
		"| video-cloud-staging-platform | postgresql-0 | 2 | 190m | 236Mi |",
		"## Failure Reasons",
		"app_desired_publish_failed",
		"Scenario profile: `home-diverse-v1`",
		"## Device Traffic Profiles",
		"| security_sensor | 10 | event_burst | security |",
		"## User Scenario Profiles",
		"| daily_user | 45 | single_device_command |",
		"usage window ramp_to_target",
		"## Device Token Retry Totals",
		"| 25k | 8 | 2 | 2 | 2 | 0 | 12 |",
		"## APP Token Retry Totals",
		"| 25k | 4 | 1 | 1 | 1 | 0 | 6 |",
		"## Bottleneck Summary",
		"token bootstrap",
		"video_cloud_api.request_token.status_5xx=2",
		"## Client Phase Metrics",
		"| 25k | device_request_token | 12 | 10 | 2 | 2000 | 11000 | 4 | 2 | 1 |",
		"| 50k | device_request_token | 1 | 1 | 0 | 10000 | 10000 | 1 | 1 | 0 |",
		"| total | device_request_token | 13 | 11 | 2 | 2615 | 11000 | 5 | 3 | 1 |",
		"## Bottleneck Event Samples",
		"| 25k | device_request_token | device | rtk-0001 | device request_token context deadline exceeded | 11000 | 25000 | 2 | true | 203.0.113.10:8883 |",
		"postgres.activity.active:40",
		"## Per-Type MQTT Totals",
		"| light | 9 | 0 | 5 | 5 | 5 | 900 | 600 |",
		"| security_sensor | 0 | 4 | 0 | 0 | 4 | 120 | 80 |",
		"## User Action Totals",
		"| away_monitoring | 5 |",
		"| morning_routine | 4 |",
		"## Usage Window Totals",
		"| away | 5 |",
		"| morning | 6 |",
		"| 50k | 50000 | 50000 | 25000 | 25000 | 50000 | 50000 | 2500 | 2500 | 2500 | ok |",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("generated report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "| 50k | 50000 | 50000 | 25000 | 25000 | 2500 | 2500 | insufficient-client-load |") {
		t.Fatalf("generated report used per-stage connect attempts instead of active subscription coverage:\n%s", report)
	}
	if strings.Contains(report, "{{") {
		t.Fatalf("generated report still contains template marker:\n%s", report)
	}
}

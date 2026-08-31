package home100k

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReportScriptRendersFirmwareOTAEvidence(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{
		EnvRoot: "runtime", Brandname: "RTK", Region: "us-sea", DeviceCount: 2,
		ScenarioProfile: FirmwareOTAScenarioProfile,
		OTAProfile:      OTAProfile{CampaignID: "campaign-42", TargetVersion: "2.0.0", CurrentVersion: "1.0.0", HardwareRevision: "rev-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := RunResult{RunID: "ota-run", Status: "COMPLETE", Result: "SUCCESS", Plan: plan, OTAEvidence: OTAEvidence{
		Complete: true, CampaignID: "campaign-42", TargetVersion: "2.0.0",
		DevicesSelected: 2, MQTTReady: 2, AssignmentsReceived: 2, TerminalExpected: 2, TerminalMatched: 2,
		UniqueDeviceResults: 2, ArtifactBytes: 4096, ArtifactHashVerified: 2,
		MQTTRebootDisconnects: 2, MQTTReconnectSuccesses: 2, ByActualTerminal: map[string]int{"success": 2},
	}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "generate-report.sh"))
	if output, err := exec.Command("bash", script, "--out-dir", outDir).CombinedOutput(); err != nil {
		t.Fatalf("generate-report.sh error = %v output=%s", err, output)
	}
	report, err := os.ReadFile(filepath.Join(outDir, "test_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Firmware OTA Simulation", "campaign-42", "2.0.0", "2 / 2 / 2", "ota-devices.jsonl"} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("generated report missing %q:\n%s", want, report)
		}
	}
}

func TestGenerateReportScriptRendersTemplateWithResourceTimelines(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	script := filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "generate-report.sh"))
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/runtime",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	brandPlanFile := filepath.Join(outDir, "resolved-brand-plan.json")
	plan.Conditions.BrandPlanFile = brandPlanFile
	plan.Conditions.DeveloperUsers = 2
	plan.BrandDistribution = []BrandDistributionEntry{{
		Brandname:      "RTK-BRAND-01",
		Devices:        25000,
		NormalUsers:    1250,
		DeveloperUsers: map[string]int{"owner": 1},
	}, {
		Brandname:      "RTK-BRAND-02",
		Devices:        25000,
		NormalUsers:    1250,
		DeveloperUsers: map[string]int{"owner": 1},
	}}
	result := RunResult{
		RunID:  "script-report-test",
		Status: "INCOMPLETE",
		Result: "INCOMPLETE",
		Plan:   plan,
		StageResults: []StageResult{{
			Name:                  "25k",
			ConnectedDevices:      25000,
			ShardConnectedDevices: 25000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     10,
				ConnectSuccess:      10,
				Subscribes:          10,
				ActiveConnections:   25000,
				ActiveSubscriptions: 25000,
				Publishes:           20,
				ReceivedMessages:    20,
				DeltaReceived:       10,
				ReportedPublishes:   10,
				BytesSent:           1000,
				BytesReceived:       2000,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts:       5,
				LoginSuccess:        5,
				ListDevicesRequests: 5,
				ReadShadowRequests:  10,
				DesiredWrites:       10,
				ReceivedAcks:        10,
				BytesSent:           700,
				BytesReceived:       900,
			},
			ShadowGetP95MS:       42,
			DesiredReportedP95MS: 88,
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
		}},
		SyncTelemetry: SyncTelemetry{VMs: []VMSyncTelemetry{{
			Label:            "lg01",
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
				Label:  "lg01",
				IP:     "192.0.2.10",
				Status: "completed",
			}},
		},
		ReportFile: filepath.Join(outDir, "test_report.md"),
	}
	brandPlan := map[string]any{
		"run_id": "script-report-test",
		"target": "50K",
		"brands": []map[string]any{
			{"brand_key": "B01", "normal_users": 1250},
			{"brand_key": "B02", "normal_users": 1250},
		},
	}
	brandPlanRaw, err := json.MarshalIndent(brandPlan, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(brand plan) error = %v", err)
	}
	if err := os.WriteFile(brandPlanFile, brandPlanRaw, 0o644); err != nil {
		t.Fatalf("WriteFile(brand plan) error = %v", err)
	}
	evidenceDir := filepath.Join(outDir, "owner-activation")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(owner-activation) error = %v", err)
	}
	for _, brandKey := range []string{"b01", "b02"} {
		evidenceRaw, marshalErr := json.Marshal(map[string]any{
			"schema": "rtk.load-owner-activation.evidence.v1",
			"status": "PASS",
			"run_id": "script-report-test",
		})
		if marshalErr != nil {
			t.Fatalf("Marshal(owner evidence) error = %v", marshalErr)
		}
		if err := os.WriteFile(filepath.Join(evidenceDir, brandKey+".json"), evidenceRaw, 0o644); err != nil {
			t.Fatalf("WriteFile(owner evidence) error = %v", err)
		}
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
		"time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct\trx_mbps\ttx_mbps",
		"2026-06-15T00:00:00Z\tscript-report-test\trun-stages\tlg01\t192.0.2.10\tmixed\t123\tok\t10.0\t0.50\t100\t1000\t2G\t25G\t8\t12.5\t4.5",
		"2026-06-15T00:00:30Z\tscript-report-test\trun-stages\tlg01\t192.0.2.10\tmixed\t123\tok\t90.0\t1.50\t700\t1000\t3G\t25G\t12\t98.0\t40.0",
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
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "test_report.md"))
	if err != nil {
		t.Fatalf("ReadFile(test_report.md) error = %v", err)
	}
	report := string(reportRaw)
	for _, want := range []string{
		"## Load Machine Resource Usage",
		"Result: INCOMPLETE",
		"- sample window: 2026-06-15T00:00:00Z -> 2026-06-15T00:00:30Z\n\n| VM | Role | IP | Samples |",
		"lg01",
		"CPU p95",
		"RX p95 Mbps",
		"TX p95 Mbps",
		"## K8s Node Resource Usage During Test",
		"- sample window: 2026-06-15T00:00:00Z -> 2026-06-15T00:00:30Z\n\n| Node | Samples | CPU p95 |",
		"k8s-node-01",
		"Mem max",
		"Brand clouds: 2",
		"| RTK-BRAND-01 | 25000 | 1250 | owner=1 |",
		"## Account Activation",
		"Formal email-activated owners: 2/2 (PASS)",
		"Synthetic bulk-provisioned members: 2500",
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

func TestGenerateReportScriptRendersVideoEvidence(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	script := filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "generate-report.sh"))
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/runtime",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: "video-1k-v1",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	result := RunResult{
		RunID:  "script-video-report-test",
		Status: "COMPLETE",
		Result: "SUCCESS",
		Plan:   plan,
		ServerCorrelation: ServerCorrelation{
			Status:  "incomplete",
			Reasons: []string{"client device MQTT connect attempts are zero"},
		},
		RuntimeLogCorrelation: RuntimeLogCorrelation{Status: "incomplete"},
		VideoEvidence: VideoEvidence{
			Complete: true,
			WebRTC: WebRTCTotals{
				CreateAttempts:     100,
				CreateSuccess:      100,
				SetupAttempts:      100,
				SetupSuccess:       100,
				CloseAttempts:      100,
				CloseSuccess:       100,
				SuccessRatePercent: 100,
				SetupP95MS:         27101,
				SetupP99MS:         27101,
			},
			WebRTCMedia: WebRTCMediaTotals{
				Enabled:             true,
				Attempts:            100,
				Successes:           100,
				ICEConnectedP95MS:   27101,
				TimeToFirstRTPP95MS: 27101,
				Startup: VideoStartupTotals{
					Samples:                              100,
					H264AccessUnitSamples:                100,
					AppRequestToFirstRTPP50MS:            25000,
					AppRequestToFirstRTPP95MS:            27101,
					AppRequestToFirstRTPP99MS:            28000,
					AppRequestToFirstH264AccessUnitP50MS: 25008,
					AppRequestToFirstH264AccessUnitP95MS: 27109,
					AppRequestToFirstH264AccessUnitP99MS: 28008,
					BreakdownP95: VideoStartupBreakdown{
						APICreateMS:                   75,
						OfferDeliveryMS:               12,
						DeviceAnswerMS:                63,
						ICEConnectMS:                  27101,
						FirstRTPAfterICEMS:            0,
						FirstH264AccessUnitAfterRTPMS: 8,
					},
				},
				PacketsReceived:     973000,
				BytesReceived:       209620000,
				H264PacketsReceived: 941974,
				H264BytesReceived:   206973463,
			},
			TURN: TURNEvidence{
				RegistryAvailable: true,
				ActiveNodes:       1,
				CoturnAvailable:   true,
			},
		},
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile(results.json) error = %v", err)
	}

	cmd := exec.Command("bash", script, "--out-dir", outDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate-report.sh error = %v output=%s", err, string(output))
	}
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "test_report.md"))
	if err != nil {
		t.Fatalf("ReadFile(test_report.md) error = %v", err)
	}
	report := string(reportRaw)
	for _, want := range []string{
		"## Video Load Profile",
		"Video profile: `video-1k-v1`",
		"## WebRTC Totals",
		"| setup | 100 | 100 | 100.00% |",
		"## WebRTC Media Totals",
		"First RTP p95: 27101 ms",
		"H.264 packets received: 941974",
		"## Video Startup Latency",
		"H.264 access unit samples: 100",
		"App request -> first H.264 access unit p95: 27109 ms",
		"Device answer p95: 63 ms",
		"ICE check p95: 27101 ms",
		"## TURN Evidence",
		"registry available: true",
		"coturn available: true",
		"MQTT/shadow correlation: skipped for WebRTC-only workflow",
		"skipped for WebRTC-only workflow; MQTT/shadow server/client counter correlation was not part of this run",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("generated report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "incomplete reason: client device MQTT connect attempts are zero") {
		t.Fatalf("video-only report summary should not render skipped MQTT/shadow incomplete reason:\n%s", report)
	}
}

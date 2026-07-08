package home100k

import (
	"os"
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
		"Device token request timeout: `10s`",
		"Device token request retries: 0",
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

func TestReportRendersMultiBrandConditions(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	plan.Conditions.BrandPlanFile = "loadtests/home-100k/scenarios/brand-plan-100k.json"
	plan.Conditions.DeveloperUsers = 2
	plan.BrandDistribution = []BrandDistributionEntry{{
		Brandname:      "RTK-BRAND-01",
		Devices:        10000,
		NormalUsers:    500,
		DeveloperUsers: map[string]int{"owner": 1},
	}, {
		Brandname:      "RTK-BRAND-02",
		Devices:        10000,
		NormalUsers:    500,
		DeveloperUsers: map[string]int{"owner": 1},
	}}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "multi-brand-report",
		ShadowEvidenceFound:  true,
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
	})

	for _, want := range []string{
		"Brand plan: `loadtests/home-100k/scenarios/brand-plan-100k.json`",
		"Brand clouds: 2",
		"Developer users: 2",
		"| RTK-BRAND-01 | 10000 | 500 | owner=1 |",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "- Brand: `RTK`") {
		t.Fatalf("multi-brand report still rendered single-brand condition:\n%s", report)
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
		ServerEvidence:       completeServerEvidenceWithWebRTCSignalingStore(),
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

func TestVideo1KProfilePlansVideoPilotDefaults(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/lke",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: "video-1k-v1",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Conditions.Devices != 1000 {
		t.Fatalf("video-1k-v1 devices = %d, want 1000", plan.Conditions.Devices)
	}
	if plan.Conditions.Users != 50 {
		t.Fatalf("video-1k-v1 users = %d, want 50", plan.Conditions.Users)
	}
	if plan.VideoProfile.Name != "video-1k-v1" {
		t.Fatalf("video profile name = %q", plan.VideoProfile.Name)
	}
	if plan.VideoProfile.VideoDevices != 100 || plan.VideoProfile.VideoViewers != 100 {
		t.Fatalf("video profile devices/viewers = %d/%d, want 100/100", plan.VideoProfile.VideoDevices, plan.VideoProfile.VideoViewers)
	}
	if plan.VideoProfile.WebRTCMediaSet != "h264" {
		t.Fatalf("webrtc media set = %q, want h264", plan.VideoProfile.WebRTCMediaSet)
	}
	if len(plan.Stages) != 1 || plan.Stages[0].Name != "target" || plan.Target.TargetConnects != 1000 {
		t.Fatalf("video-1k-v1 should keep one target ramp stage: %+v target=%+v", plan.Stages, plan.Target)
	}
	if !containsString(plan.Workflow, "run-video-loadtest") {
		t.Fatalf("workflow missing video runner step: %+v", plan.Workflow)
	}
}

func TestReportMarksMissingVideoEvidenceIncomplete(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/lke",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: "video-1k-v1",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "video-missing-evidence",
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       completeServerEvidenceWithWebRTCSignalingStore(),
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults: []StageResult{{
			Name:             "target",
			ConnectedDevices: 1000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectSuccess:      1000,
				Subscribes:          1000,
				ActiveConnections:   1000,
				ActiveSubscriptions: 1000,
				Publishes:           1000,
				ReceivedMessages:    1000,
				DeltaReceived:       1000,
				ReportedPublishes:   1000,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 50,
				ReceivedAcks:  50,
			},
			MQTTConnectSuccessRatePercent:  100,
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
			DeviceTypeTotals:               completeDeviceTypeEvidence(plan),
		}},
	})

	for _, want := range []string{
		"Status: INCOMPLETE",
		"Result: INCOMPLETE",
		"Missing WebRTC create/setup/close evidence",
		"## Video Load Profile",
		"Video profile: `video-1k-v1`",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportRendersVideoAndTURNEvidence(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/lke",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: "video-1k-v1",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "video-success",
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence: ServerEvidence{Complete: true, Sources: map[string]EvidenceSource{
			"emqx":            {Available: true},
			"video_cloud_api": {Available: true},
			"postgres":        {Available: true},
			"redis_valkey":    {Available: true},
			"ingress_nginx":   {Available: true},
			"host_pod_resources": {
				Available: true,
			},
			"turn_registry": {
				Available: true,
				Counters: map[string]int64{
					"active_nodes": 2,
				},
			},
			"coturn": {
				Available: true,
				Counters: map[string]int64{
					"active_sessions": 100,
					"allocations":     100,
				},
			},
		}},
		ServerCorrelation: ServerCorrelation{Status: "pass"},
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
				SetupP95MS:         250,
				SetupP99MS:         310,
				ICEServerCount:     2,
			},
			WebRTCMedia: WebRTCMediaTotals{
				Enabled:             true,
				Attempts:            100,
				Successes:           100,
				ICEConnectedP95MS:   120,
				TimeToFirstRTPP95MS: 180,
				Startup: VideoStartupTotals{
					Samples:                              100,
					H264AccessUnitSamples:                100,
					AppRequestToFirstRTPP50MS:            160,
					AppRequestToFirstRTPP95MS:            180,
					AppRequestToFirstRTPP99MS:            190,
					AppRequestToFirstH264AccessUnitP50MS: 165,
					AppRequestToFirstH264AccessUnitP95MS: 185,
					AppRequestToFirstH264AccessUnitP99MS: 195,
					BreakdownP95: VideoStartupBreakdown{
						APICreateMS:                   40,
						OfferDeliveryMS:               20,
						DeviceAnswerMS:                30,
						ICEConnectMS:                  80,
						FirstRTPAfterICEMS:            10,
						FirstH264AccessUnitAfterRTPMS: 5,
					},
				},
				PacketsReceived:     3000,
				BytesReceived:       1500000,
				H264PacketsReceived: 3000,
				H264BytesReceived:   1500000,
			},
			TURN: TURNEvidence{
				RegistryAvailable: true,
				ActiveNodes:       2,
				CoturnAvailable:   true,
				Allocations:       100,
				ActiveSessions:    100,
			},
		},
		StageResults: []StageResult{{
			Name:             "target",
			ConnectedDevices: 1000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectSuccess:      1000,
				Subscribes:          1000,
				ActiveConnections:   1000,
				ActiveSubscriptions: 1000,
				Publishes:           1000,
				ReceivedMessages:    1000,
				DeltaReceived:       1000,
				ReportedPublishes:   1000,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 50,
				ReceivedAcks:  50,
			},
			MQTTConnectSuccessRatePercent:  100,
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
			DeviceTypeTotals:               completeDeviceTypeEvidence(plan),
		}},
	})

	for _, want := range []string{
		"Status: COMPLETE",
		"Result: SUCCESS",
		"## WebRTC Totals",
		"| create | 100 | 100 | 100.00% |",
		"Setup p95: 250 ms",
		"## WebRTC Media Totals",
		"First RTP p95: 180 ms",
		"H.264 packets received: 3000",
		"## Video Startup Latency",
		"H.264 access unit samples: 100",
		"App request -> first H.264 access unit p95: 185 ms",
		"Device answer p95: 30 ms",
		"ICE check p95: 80 ms",
		"## TURN Evidence",
		"active nodes: 2",
		"allocations: 100",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestVideoEvidenceUsesServerTURNEvidenceWhenRunnerOmitsTURN(t *testing.T) {
	raw := []byte(`{
		"webrtc": {
			"success_rate": 1,
			"setup_latency_p95_ms": 250,
			"setup_latency_p99_ms": 310,
			"ice_server_count": 2,
			"create": {"operations": 100, "successes": 100},
			"setup": {"operations": 100, "successes": 100},
			"close": {"operations": 100, "successes": 100}
		},
		"webrtc_media": {
			"attempts": 100,
			"successes": 100,
			"ice_connected_p95_ms": 120,
			"time_to_first_rtp_p95_ms": 180,
			"packets_received": 3000,
			"bytes_received": 1500000
		}
	}`)
	video, err := videoEvidenceFromLoadtestJSON(raw)
	if err != nil {
		t.Fatalf("videoEvidenceFromLoadtestJSON() error = %v", err)
	}
	video = videoEvidenceWithServerEvidence(video, ServerEvidence{Sources: map[string]EvidenceSource{
		"turn_registry": {
			Available: true,
			Counters:  map[string]int64{"turn_registry.active_nodes": 2},
		},
		"coturn": {
			Available: true,
			Counters: map[string]int64{
				"coturn.allocations":     100,
				"coturn.active_sessions": 100,
			},
		},
	}})

	if !video.Complete {
		t.Fatalf("video evidence should be complete after server TURN merge: %+v", video)
	}
	if !video.TURN.RegistryAvailable || !video.TURN.CoturnAvailable || video.TURN.ActiveNodes != 2 {
		t.Fatalf("turn evidence not merged: %+v", video.TURN)
	}
}

func TestVideoEvidenceDerivesSetupFromMediaWhenRunnerOmitsSetupTotals(t *testing.T) {
	raw := []byte(`{
		"webrtc": {
			"success_rate": 0,
			"create": {"operations": 100, "successes": 100},
			"setup": {"operations": 0, "successes": 0},
			"close": {"operations": 100, "successes": 100}
		},
		"webrtc_media": {
			"attempts": 100,
			"successes": 100,
			"ice_connected_p95_ms": 27101,
			"time_to_first_rtp_p95_ms": 27101,
			"packets_received": 973000,
			"bytes_received": 209620000
		}
	}`)
	video, err := videoEvidenceFromLoadtestJSON(raw)
	if err != nil {
		t.Fatalf("videoEvidenceFromLoadtestJSON() error = %v", err)
	}
	if video.WebRTC.SetupAttempts != 100 || video.WebRTC.SetupSuccess != 100 {
		t.Fatalf("setup totals = %+v, want media-derived 100/100", video.WebRTC)
	}
	if video.WebRTC.SuccessRatePercent != 100 {
		t.Fatalf("success rate = %.2f, want 100", video.WebRTC.SuccessRatePercent)
	}
	video = videoEvidenceWithServerEvidence(video, ServerEvidence{Sources: map[string]EvidenceSource{
		"turn_registry": {Available: true, Counters: map[string]int64{"turn_registry.active_nodes": 1}},
		"coturn":        {Available: true},
	}})
	if !video.Complete {
		t.Fatalf("video evidence should be complete after media setup derivation and TURN merge: %+v", video)
	}
}

func TestVideoEvidenceParsesStartupLatencySummary(t *testing.T) {
	raw := []byte(`{
		"webrtc": {
			"success_rate": 1,
			"create": {"operations": 2, "successes": 2},
			"setup": {"operations": 2, "successes": 2},
			"close": {"operations": 2, "successes": 2}
		},
		"webrtc_media": {
			"attempts": 2,
			"successes": 2,
			"time_to_first_rtp_p95_ms": 210,
			"video_startup_latency": {
				"samples": 2,
				"h264_access_unit_samples": 2,
				"app_request_to_first_rtp_p50_ms": 180,
				"app_request_to_first_rtp_p95_ms": 210,
				"app_request_to_first_rtp_p99_ms": 210,
				"app_request_to_first_h264_access_unit_p50_ms": 185,
				"app_request_to_first_h264_access_unit_p95_ms": 217,
				"app_request_to_first_h264_access_unit_p99_ms": 217,
				"breakdown_p95": {
					"api_create_ms": 50,
					"offer_delivery_ms": 25,
					"device_answer_ms": 35,
					"remote_answer_set_ms": 140,
					"ice_check_ms": 90,
					"ice_connected_since_session_start_ms": 230,
					"first_rtp_after_ice_ms": 12,
					"first_h264_access_unit_after_rtp_ms": 7
				}
			}
		},
		"turn_evidence": {"registry_available": true, "active_nodes": 1, "coturn_available": true, "api_turn_registry_lookup_succeeded": 1, "api_dynamic_turn_count": 1, "api_turn_registry_node_count": 1}
	}`)
	video, err := videoEvidenceFromLoadtestJSON(raw)
	if err != nil {
		t.Fatalf("videoEvidenceFromLoadtestJSON() error = %v", err)
	}
	if video.WebRTCMedia.Startup.AppRequestToFirstH264AccessUnitP95MS != 217 {
		t.Fatalf("h264 AU startup p95 = %d, want 217", video.WebRTCMedia.Startup.AppRequestToFirstH264AccessUnitP95MS)
	}
	if video.WebRTCMedia.Startup.Samples != 2 || video.WebRTCMedia.Startup.H264AccessUnitSamples != 2 {
		t.Fatalf("startup sample counts = %d/%d, want 2/2", video.WebRTCMedia.Startup.Samples, video.WebRTCMedia.Startup.H264AccessUnitSamples)
	}
	if video.WebRTCMedia.Startup.BreakdownP95.DeviceAnswerMS != 35 {
		t.Fatalf("device answer p95 = %d, want 35", video.WebRTCMedia.Startup.BreakdownP95.DeviceAnswerMS)
	}
	if video.WebRTCMedia.Startup.BreakdownP95.ICECheckMS != 90 || video.WebRTCMedia.Startup.BreakdownP95.ICEConnectedSinceSessionStartMS != 230 {
		t.Fatalf("ice breakdown = %+v, want ice_check=90 and connected_since_session_start=230", video.WebRTCMedia.Startup.BreakdownP95)
	}
}

func TestVideoLadderEvidenceRendersPerStepReport(t *testing.T) {
	tmp := t.TempDir()
	videoDir := tmp + "/video"
	mustWriteVideoStep := func(name, candidates string) {
		t.Helper()
		dir := videoDir + "/" + name
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		raw := strings.ReplaceAll(`{
			"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 100, "duration_ms": 300000},
			"webrtc": {
				"success_rate": 1,
				"setup_latency_p95_ms": 250,
				"setup_latency_p99_ms": 310,
				"ice_server_count": 2,
				"create": {"operations": 100, "successes": 100},
				"setup": {"operations": 100, "successes": 100},
				"close": {"operations": 100, "successes": 100}
			},
			"webrtc_media": {
				"attempts": 100,
				"successes": 100,
				"ice_connected_p95_ms": 120,
				"time_to_first_rtp_p95_ms": 180,
				"packets_received": 3000,
				"bytes_received": 1500000,
				"h264_packets_received": 3000,
				"video_startup_latency": {
					"samples": 100,
					"h264_access_unit_samples": 100,
					"app_request_to_first_rtp_p95_ms": 180,
					"app_request_to_first_h264_access_unit_p95_ms": 220
				}
			},
			"turn_evidence": {
				"registry_available": true,
				"active_nodes": 1,
				"coturn_available": true,
				"allocations": 100,
				"active_sessions": 100,
				"api_turn_registry_lookup_succeeded": 1,
				"api_dynamic_turn_count": 1,
				"api_turn_registry_node_count": 1
			},
			"video_startup_latency": [`+candidates+`]
		}`, "\t", "")
		if err := os.WriteFile(dir+"/load-results.json", []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteVideoStep("step-100", `{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}`)
	mustWriteVideoStep("step-500", `{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}`)
	video := loadVideoEvidence(videoDir)
	if len(video.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(video.Steps), video)
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: Video100KTurnScenarioProfile})
	if err != nil {
		t.Fatal(err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "video-ladder",
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       completeServerEvidenceWithWebRTCSignalingStore(),
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults:         completeStageResultsForPlan(plan),
		VideoEvidence:        video,
	})
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: SUCCESS",
		"## Video Ladder",
		"| step-100 | 100 | relay | 100.00% | 100.00% | 180 ms | 220 ms | 1 | 0 | 100 | 100 |",
		"TURN relays encrypted DTLS-SRTP packets over the configured UDP/TCP transports",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestVideoLadderEvidenceLoadsTwoHostStepLayout(t *testing.T) {
	tmp := t.TempDir()
	stepDir := tmp + "/video/step-5000"
	if err := os.MkdirAll(stepDir+"/app-viewer", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stepDir+"/device", 0o755); err != nil {
		t.Fatal(err)
	}
	appViewer := []byte(`{
		"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 5000, "duration_ms": 1000000},
		"webrtc": {
			"success_rate": 1,
			"setup_latency_p95_ms": 0,
			"setup_latency_p99_ms": 0,
			"ice_server_count": 2,
			"create": {"operations": 5000, "successes": 5000},
			"setup": {"operations": 0, "successes": 0},
			"close": {"operations": 5000, "successes": 5000}
		},
		"webrtc_media": {
			"attempts": 5000,
			"successes": 5000,
			"ice_connected_p95_ms": 2158,
			"time_to_first_rtp_p95_ms": 3173,
			"packets_received": 10046,
			"bytes_received": 5090911,
			"h264_packets_received": 10046,
			"video_startup_latency": {
				"samples": 5000,
				"h264_access_unit_samples": 5000,
				"app_request_to_first_rtp_p95_ms": 3173,
				"app_request_to_first_h264_access_unit_p95_ms": 3176
			}
		},
		"video_startup_latency": [
			{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}
		]
	}`)
	device := []byte(`{
		"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 5000, "duration_ms": 1000000},
		"webrtc": {"success_rate": 0},
		"webrtc_media": {"ice_connected_p95_ms": 2121, "time_to_first_rtp_p95_ms": 3133}
	}`)
	if err := os.WriteFile(stepDir+"/app-viewer/load-results.json", appViewer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stepDir+"/device/load-results.json", device, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stepDir+"/turn-active-samples.tsv", []byte("time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n2026-07-03T22:28:00Z\tturn01\t198.51.100.20\t164\t0\t164\t0\t164\t164\t0\tactive\n2026-07-03T22:28:00Z\tturn02\t198.51.100.21\t162\t0\t162\t0\t162\t162\t0\tactive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	video := videoEvidenceWithServerEvidence(loadVideoEvidence(tmp+"/video"), ServerEvidence{Sources: map[string]EvidenceSource{
		"turn_registry": {Available: true, Counters: map[string]int64{"turn_registry.active_nodes": 2}},
		"coturn":        {Available: true, Counters: map[string]int64{"coturn.configured_nodes": 2}},
	}})
	if len(video.Steps) != 1 {
		t.Fatalf("steps = %d, want 1: %+v", len(video.Steps), video)
	}
	if !video.Complete || !video.Steps[0].Complete {
		t.Fatalf("two-host video evidence incomplete: %+v", video)
	}
	if video.WebRTC.CreateAttempts != 5000 || video.WebRTC.SetupAttempts != 5000 || video.WebRTC.CloseAttempts != 5000 {
		t.Fatalf("webrtc attempts = create %d setup %d close %d, want 5000 each", video.WebRTC.CreateAttempts, video.WebRTC.SetupAttempts, video.WebRTC.CloseAttempts)
	}
	if video.WebRTCMedia.Successes != 5000 || video.WebRTCMedia.Startup.H264AccessUnitSamples != 5000 {
		t.Fatalf("media successes/startup samples = %d/%d, want 5000/5000", video.WebRTCMedia.Successes, video.WebRTCMedia.Startup.H264AccessUnitSamples)
	}
	if video.RelayCandidateSamples != 1 || video.NonRelayCandidateSamples != 0 {
		t.Fatalf("relay/non-relay samples = %d/%d, want 1/0", video.RelayCandidateSamples, video.NonRelayCandidateSamples)
	}
	if video.TURN.ActiveSessions != 164 || video.TURN.ActiveNodes != 2 {
		t.Fatalf("TURN active sessions/nodes = %d/%d, want 164/2", video.TURN.ActiveSessions, video.TURN.ActiveNodes)
	}
}

func TestVideoLadderEvidenceSumsRemoteShardedViewers(t *testing.T) {
	tmp := t.TempDir()
	stepDir := tmp + "/video/step-20"
	for _, shard := range []string{"shard-01", "shard-02"} {
		if err := os.MkdirAll(stepDir+"/"+shard, 0o755); err != nil {
			t.Fatal(err)
		}
		raw := []byte(`{
			"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 10, "duration_ms": 30000},
			"webrtc": {
				"success_rate": 1,
				"setup_latency_p95_ms": 200,
				"setup_latency_p99_ms": 220,
				"ice_server_count": 2,
				"create": {"operations": 10, "successes": 10},
				"setup": {"operations": 10, "successes": 10},
				"close": {"operations": 10, "successes": 10}
			},
			"webrtc_media": {
				"attempts": 10,
				"successes": 10,
				"ice_connected_p95_ms": 120,
				"time_to_first_rtp_p95_ms": 180,
				"video_startup_latency": {
					"samples": 10,
					"h264_access_unit_samples": 10,
					"app_request_to_first_h264_access_unit_p95_ms": 220
				}
			},
			"turn_evidence": {"registry_available": true, "active_nodes": 1, "coturn_available": true, "allocations": 10, "active_sessions": 10, "api_turn_registry_lookup_succeeded": 1, "api_dynamic_turn_count": 1, "api_turn_registry_node_count": 1},
			"video_startup_latency": [
				{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}
			]
		}`)
		if err := os.WriteFile(stepDir+"/"+shard+"/load-results.json", raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	video := loadVideoEvidence(tmp + "/video")
	if len(video.Steps) != 1 {
		t.Fatalf("steps = %d, want 1: %+v", len(video.Steps), video)
	}
	if video.Steps[0].Viewers != 20 {
		t.Fatalf("step viewers = %d, want sum of remote shards 20", video.Steps[0].Viewers)
	}
	if video.WebRTC.CreateAttempts != 20 || video.WebRTCMedia.Successes != 20 {
		t.Fatalf("merged attempts/successes = %d/%d, want 20/20", video.WebRTC.CreateAttempts, video.WebRTCMedia.Successes)
	}
}

func TestVideoStepThresholdFailureMakesMergedReportFail(t *testing.T) {
	tmp := t.TempDir()
	stepDir := tmp + "/video/step-100"
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
		"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 100, "duration_ms": 300000},
		"thresholds": {"passed": false, "failures": ["p99 latency 156721 ms exceeds threshold 60000 ms"]},
		"webrtc": {
			"success_rate": 1,
			"setup_latency_p95_ms": 27832,
			"setup_latency_p99_ms": 27832,
			"create": {"operations": 100, "successes": 100},
			"setup": {"operations": 100, "successes": 100},
			"close": {"operations": 100, "successes": 100}
		},
		"webrtc_media": {
			"attempts": 100,
			"successes": 100,
			"ice_connected_p95_ms": 27832,
			"time_to_first_rtp_p95_ms": 27832,
			"video_startup_latency": {
				"samples": 100,
				"h264_access_unit_samples": 100,
				"app_request_to_first_h264_access_unit_p95_ms": 27833
			}
		},
		"turn_evidence": {"registry_available": true, "active_nodes": 2, "coturn_available": true, "allocations": 46, "active_sessions": 46, "api_turn_registry_lookup_succeeded": 1, "api_dynamic_turn_count": 1, "api_turn_registry_node_count": 2},
		"video_startup_latency": [
			{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}
		]
	}`)
	if err := os.WriteFile(stepDir+"/load-results.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	video := loadVideoEvidence(tmp + "/video")
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: Video50KTurnScenarioProfile})
	if err != nil {
		t.Fatal(err)
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "video-threshold-fail",
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       completeServerEvidenceWithWebRTCSignalingStore(),
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults:         completeStageResultsForPlan(plan),
		VideoEvidence:        video,
	})
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: FAIL",
		"video step step-100 threshold failed: p99 latency 156721 ms exceeds threshold 60000 ms",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestVideoEvidenceUsesActiveTURNSampleSidecar(t *testing.T) {
	tmp := t.TempDir()
	raw := []byte(`{
		"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 100, "duration_ms": 300000},
		"webrtc": {
			"success_rate": 1,
			"create": {"operations": 100, "successes": 100},
			"setup": {"operations": 100, "successes": 100},
			"close": {"operations": 100, "successes": 100}
		},
		"webrtc_media": {
			"attempts": 100,
			"successes": 100,
			"ice_connected_p95_ms": 120,
			"time_to_first_rtp_p95_ms": 180,
			"video_startup_latency": {
				"samples": 100,
				"h264_access_unit_samples": 100,
				"app_request_to_first_h264_access_unit_p95_ms": 220
			}
		},
		"turn_evidence": {"registry_available": true, "active_nodes": 1, "coturn_available": true, "api_turn_registry_lookup_succeeded": 1, "api_dynamic_turn_count": 1, "api_turn_registry_node_count": 1},
		"video_startup_latency": [
			{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay"}
		]
	}`)
	if err := os.WriteFile(tmp+"/load-results.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp+"/turn-active-samples.tsv", []byte("time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t7\t0\t7\t0\t7\t7\t2\tactive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	video := loadVideoEvidence(tmp)
	if video.TURN.Allocations != 7 || video.TURN.ActiveSessions != 7 {
		t.Fatalf("TURN active evidence = %+v, want allocations/sessions from sidecar", video.TURN)
	}
	for _, profile := range []string{Video50KTurnScenarioProfile, Video100KTurnScenarioProfile} {
		t.Run(profile, func(t *testing.T) {
			incomplete, reasons := videoGateFailures(Plan{ScenarioProfile: profile, VideoProfile: videoProfileForScenario(profile)}, video)
			if incomplete || len(reasons) != 0 {
				t.Fatalf("video gate incomplete=%t reasons=%v video=%+v", incomplete, reasons, video)
			}
		})
	}
}

func TestTURNEvidenceFromActiveSamplesSupportsTwoHostSamplerHeader(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "ts\tnode\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tevidence_status\n" +
		"2026-07-03T22:27:41Z\tturn01\t18\t2\t18\t2\t18\t18\tactive\n" +
		"2026-07-03T22:30:00Z\tturn01\t164\t70\t164\t70\t164\t164\tactive\n" +
		"2026-07-03T22:30:00Z\tturn02\t162\t71\t162\t71\t162\t162\tactive\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 2 || turn.ActiveSessions != 164 || turn.Allocations != 164 {
		t.Fatalf("turn evidence = %+v, want nodes=2 sessions=164", turn)
	}
}

func TestTURNEvidenceFromActiveSamplesDoesNotTreatSocketsAsSessions(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\tjournal_events\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t164\t2\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn node availability from weak sidecar", turn)
	}
	if turn.Allocations != 0 || turn.ActiveSessions != 0 {
		t.Fatalf("turn sessions = %+v, weak socket-only sidecar must not become allocation/session evidence", turn)
	}
}

func TestTURNEvidenceFromActiveSamplesKeepsRelayFlowAndJournalActiveEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tcoturn_cpu_pct\tcoturn_rss_kb\trx_bytes\ttx_bytes\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t40\t8\t31\t7\t0\t0\t12\t9\t65536\t1000\t2000\trelay_flow_observed\n" +
		"2026-07-01T00:00:05Z\tturn01\t198.51.100.20\t60\t12\t45\t9\t33\t33\t18\t27\t131072\t3000\t5000\tjournal_active\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn active node", turn)
	}
	if turn.RelayUDPFlows != 45 || turn.RelayTCPFlows != 9 {
		t.Fatalf("relay flows = %+v, want max udp/tcp relay flow evidence", turn)
	}
	if turn.Allocations != 33 || turn.ActiveSessions != 33 || turn.EvidenceStatus != "journal_active" {
		t.Fatalf("active evidence = %+v, want journal-derived active sessions", turn)
	}
	if turn.CoturnCPUPercent != 27 || turn.CoturnRSSKB != 131072 || turn.RXBytes != 3000 || turn.TXBytes != 5000 {
		t.Fatalf("coturn runtime evidence = %+v, want cpu/rss/rx/tx peak values", turn)
	}
}

func TestTURNEvidenceFromActiveSamplesKeepsSocketActivityEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t16\t0\t0\t0\t0\t0\t0\tunavailable\n" +
		"2026-07-01T00:00:05Z\tturn01\t198.51.100.20\t678\t338\t0\t0\t0\t0\t6\tsocket_activity_observed\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn active node", turn)
	}
	if turn.UDPSockets != 678 || turn.TCPEstablished != 338 || turn.JournalEvents != 6 {
		t.Fatalf("socket evidence = %+v, want udp/tcp/journal active-window values", turn)
	}
	if turn.Allocations != 0 || turn.ActiveSessions != 0 {
		t.Fatalf("turn sessions = %+v, socket-only evidence must not become allocation/session count", turn)
	}
	if turn.EvidenceStatus != "socket_activity_observed" {
		t.Fatalf("evidence status = %q, want socket_activity_observed", turn.EvidenceStatus)
	}
}

func TestTURNEvidenceFromActiveSamplesAcceptsConntrackActiveEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t40\t8\t24\t2\t60\t60\t0\tconntrack_active\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn active node", turn)
	}
	if turn.Allocations != 60 || turn.ActiveSessions != 60 {
		t.Fatalf("turn active counts = %+v, want conntrack-derived counts", turn)
	}
	if turn.RelayUDPFlows != 24 || turn.RelayTCPFlows != 2 || turn.EvidenceStatus != "conntrack_active" {
		t.Fatalf("turn conntrack evidence = %+v, want relay flow and conntrack status", turn)
	}
}

func TestTURNEvidenceFromActiveSamplesAcceptsPrometheusActiveEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t40\t8\t24\t2\t31\t31\t0\tprometheus_active\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn active node", turn)
	}
	if turn.Allocations != 31 || turn.ActiveSessions != 31 {
		t.Fatalf("turn active counts = %+v, want prometheus-derived counts", turn)
	}
	if turn.EvidenceStatus != "prometheus_active" {
		t.Fatalf("turn evidence status = %q, want prometheus_active", turn.EvidenceStatus)
	}
}

func TestTURNEvidenceFromActiveSamplesAcceptsCLIActiveEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/turn-active-samples.tsv"
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t40\t8\t24\t2\t29\t29\t0\tcli_active\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	turn := turnEvidenceFromActiveSamples(path)

	if !turn.CoturnAvailable || turn.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want coturn active node", turn)
	}
	if turn.Allocations != 29 || turn.ActiveSessions != 29 {
		t.Fatalf("turn active counts = %+v, want CLI-derived counts", turn)
	}
	if turn.EvidenceStatus != "cli_active" {
		t.Fatalf("turn evidence status = %q, want cli_active", turn.EvidenceStatus)
	}
}

func TestVideoEvidenceWithActiveTURNSamplesKeepsSocketOnlyEvidence(t *testing.T) {
	tmp := t.TempDir()
	raw := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tcoturn_cpu_pct\tcoturn_rss_kb\trx_bytes\ttx_bytes\tevidence_status\n" +
		"2026-07-01T00:00:00Z\tturn01\t198.51.100.20\t678\t338\t0\t0\t0\t0\t6\t22\t131072\t3000\t5000\tsocket_activity_observed\n"
	if err := os.WriteFile(tmp+"/turn-active-samples.tsv", []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	video := videoEvidenceWithActiveTURNSamples(VideoEvidence{
		WebRTC: WebRTCTotals{CreateAttempts: 1, SetupAttempts: 1, CloseAttempts: 1},
		TURN:   TURNEvidence{RegistryAvailable: true},
	}, tmp)

	if !video.TURN.CoturnAvailable || video.TURN.ActiveNodes != 1 {
		t.Fatalf("turn availability = %+v, want socket-only sidecar retained", video.TURN)
	}
	if video.TURN.Allocations != 0 || video.TURN.ActiveSessions != 0 {
		t.Fatalf("turn sessions = %+v, socket-only sidecar must not fabricate sessions", video.TURN)
	}
	if video.TURN.UDPSockets != 678 || video.TURN.TCPEstablished != 338 || video.TURN.CoturnCPUPercent != 22 || video.TURN.CoturnRSSKB != 131072 || video.TURN.RXBytes != 3000 || video.TURN.TXBytes != 5000 {
		t.Fatalf("turn socket/resource evidence = %+v, want active-window values", video.TURN)
	}
}

func TestVideoTurnOutcomeRequiresWebRTCSignalingStoreEvidence(t *testing.T) {
	for _, profile := range []string{Video50KTurnScenarioProfile, Video100KTurnScenarioProfile} {
		t.Run(profile, func(t *testing.T) {
			plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: profile})
			if err != nil {
				t.Fatal(err)
			}
			video := completeVideo100KTurnEvidence()
			outcome := evaluateRunOutcome(
				plan,
				ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)},
				completeStageResultsForPlan(plan),
				LoadGeneratorHealth{},
				ServerCorrelation{Status: "pass"},
				RuntimeLogCorrelation{Status: "pass"},
				video,
			)
			if outcome.Status != "INCOMPLETE" || outcome.Result != "INCOMPLETE" {
				t.Fatalf("outcome = %+v, want INCOMPLETE", outcome)
			}
			if !containsString(outcome.Reasons, "Missing multi-pod WebRTC signaling store evidence") {
				t.Fatalf("reasons = %v, want missing signaling store evidence", outcome.Reasons)
			}
		})
	}
}

func TestVideoTurnOutcomeRequiresDynamicTURNRegistryPath(t *testing.T) {
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: Video50KTurnScenarioProfile})
	if err != nil {
		t.Fatal(err)
	}
	video := completeVideo100KTurnEvidence()
	video.TURN.APIDynamicTURNCount = 0
	video.TURN.APIStaticTURNCount = 2
	video.TURN.APITURNRegistryLookupSucceeded = 0
	video.TURN.APITURNRegistryLookupEmpty = 5

	outcome := evaluateRunOutcome(
		plan,
		completeServerEvidenceWithWebRTCSignalingStore(),
		completeStageResultsForPlan(plan),
		LoadGeneratorHealth{},
		ServerCorrelation{Status: "pass"},
		RuntimeLogCorrelation{Status: "pass"},
		video,
	)

	if outcome.Status != "INCOMPLETE" || outcome.Result != "INCOMPLETE" {
		t.Fatalf("outcome = %+v, want INCOMPLETE", outcome)
	}
	if !containsString(outcome.Reasons, "API did not return dynamic TURN servers from active turnregistry nodes; static TURN URLs are not valid coturn sizing evidence") {
		t.Fatalf("reasons = %v, want dynamic TURN path blocker", outcome.Reasons)
	}
	if !containsString(outcome.Reasons, "API turnregistry lookup returned empty during WebRTC ICE server resolution") {
		t.Fatalf("reasons = %v, want turnregistry empty blocker", outcome.Reasons)
	}
}

func TestRelayOnlyVideoGateFailsOnNonRelayCandidate(t *testing.T) {
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: Video100KTurnScenarioProfile})
	if err != nil {
		t.Fatal(err)
	}
	video := VideoEvidence{
		Complete: true,
		WebRTC: WebRTCTotals{
			CreateAttempts: 100, CreateSuccess: 100,
			SetupAttempts: 100, SetupSuccess: 100,
			CloseAttempts: 100, CloseSuccess: 100,
			SuccessRatePercent: 100,
		},
		WebRTCMedia: WebRTCMediaTotals{
			Enabled: true, Attempts: 100, Successes: 100,
			ICEConnectedP95MS: 100, TimeToFirstRTPP95MS: 120,
			Startup: VideoStartupTotals{H264AccessUnitSamples: 100},
		},
		TURN: TURNEvidence{
			RegistryAvailable: true, ActiveNodes: 1, CoturnAvailable: true, Allocations: 100, ActiveSessions: 100,
			APITURNRegistryLookupSucceeded: 1, APIDynamicTURNCount: 1, APITURNRegistryNodeCount: 1,
		},
		Steps: []VideoStepEvidence{{
			Name: "step-100", Viewers: 100, ICEPolicy: "relay",
			WebRTC: WebRTCTotals{
				CreateAttempts: 100, CreateSuccess: 100,
				SetupAttempts: 100, SetupSuccess: 100,
				CloseAttempts: 100, CloseSuccess: 100,
				SuccessRatePercent: 100,
			},
			WebRTCMedia: WebRTCMediaTotals{Enabled: true, Attempts: 100, Successes: 100},
			TURN: TURNEvidence{
				RegistryAvailable: true, ActiveNodes: 1, CoturnAvailable: true, Allocations: 100, ActiveSessions: 100,
				APITURNRegistryLookupSucceeded: 1, APIDynamicTURNCount: 1, APITURNRegistryNodeCount: 1,
			},
			NonRelayCandidateSamples: 1,
			Complete:                 true,
		}},
		NonRelayCandidateSamples: 1,
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                "video-direct-candidate",
		ServerEvidenceFound:  true,
		LoadGeneratorHealthy: true,
		ServerEvidence:       completeServerEvidenceWithWebRTCSignalingStore(),
		ServerCorrelation:    ServerCorrelation{Status: "pass"},
		StageResults:         completeStageResultsForPlan(plan),
		VideoEvidence:        video,
	})
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: FAIL",
		"relay-only WebRTC selected non-relay candidates in 1 samples",
		"video step step-100 selected non-relay candidates in 1 samples",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestVideoGateFailsWhenMediaSuccessRateBelowThreshold(t *testing.T) {
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea", ScenarioProfile: Video50KTurnScenarioProfile})
	if err != nil {
		t.Fatal(err)
	}
	video := completeVideo100KTurnEvidence()
	video.WebRTCMedia.Attempts = 2000
	video.WebRTCMedia.Successes = 1859
	video.WebRTCMedia.Failures = 141
	video.Steps = []VideoStepEvidence{{
		Name:    "step-2000",
		Viewers: 2000,
		WebRTC: WebRTCTotals{
			CreateAttempts: 2000, CreateSuccess: 2000,
			SetupAttempts: 2000, SetupSuccess: 1859,
			CloseAttempts: 2000, CloseSuccess: 2000,
			SuccessRatePercent: 100,
		},
		WebRTCMedia: WebRTCMediaTotals{
			Enabled: true, Attempts: 2000, Successes: 1859, Failures: 141,
			ICEConnectedP95MS: 100, TimeToFirstRTPP95MS: 120,
			Startup: VideoStartupTotals{H264AccessUnitSamples: 1859},
		},
		TURN: TURNEvidence{
			RegistryAvailable: true, ActiveNodes: 2, CoturnAvailable: true, Allocations: 779, ActiveSessions: 779,
			APITURNRegistryLookupSucceeded: 1, APIDynamicTURNCount: 1, APITURNRegistryNodeCount: 2,
		},
		ICEPolicy:             "relay",
		RelayCandidateSamples: 1859,
		Complete:              true,
	}}
	incomplete, reasons := videoGateFailures(plan, video)
	if incomplete {
		t.Fatalf("video gate incomplete=true reasons=%v", reasons)
	}
	if !containsString(reasons, "WebRTC media success rate 92.95% below 99.50% threshold (1859/2000)") {
		t.Fatalf("reasons = %v, want total media success rate failure", reasons)
	}
	if !containsString(reasons, "video step step-2000 WebRTC media success rate 92.95% below 99.50% threshold (1859/2000)") {
		t.Fatalf("reasons = %v, want step media success rate failure", reasons)
	}
}

func completeServerEvidenceWithWebRTCSignalingStore() ServerEvidence {
	sources := requiredEvidenceSources(true)
	sources["video_cloud_api"] = EvidenceSource{
		Available: true,
		Counters: map[string]int64{
			"video_cloud_api.k8s.desired_replicas":                3,
			"video_cloud_api.k8s.running_pods":                    3,
			"video_cloud_api.webrtc_signaling_store.enabled_pods": 3,
			"video_cloud_api.webrtc_signaling_store.addr_pods":    3,
			"video_cloud_api.webrtc_signaling_store.prefix_pods":  3,
		},
	}
	return ServerEvidence{Complete: true, Sources: sources}
}

func completeVideo100KTurnEvidence() VideoEvidence {
	return VideoEvidence{
		Complete: true,
		WebRTC: WebRTCTotals{
			CreateAttempts: 100, CreateSuccess: 100,
			SetupAttempts: 100, SetupSuccess: 100,
			CloseAttempts: 100, CloseSuccess: 100,
			SuccessRatePercent: 100,
		},
		WebRTCMedia: WebRTCMediaTotals{
			Enabled: true, Attempts: 100, Successes: 100,
			ICEConnectedP95MS: 100, TimeToFirstRTPP95MS: 120,
			Startup: VideoStartupTotals{H264AccessUnitSamples: 100},
		},
		TURN: TURNEvidence{
			RegistryAvailable: true, ActiveNodes: 1, CoturnAvailable: true, Allocations: 100, ActiveSessions: 100,
			APITURNRegistryLookupSucceeded: 1, APIDynamicTURNCount: 1, APITURNRegistryNodeCount: 1,
		},
		RelayCandidateSamples: 100,
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func completeDeviceTypeEvidence(plan Plan) map[string]DeviceTypeTotals {
	out := map[string]DeviceTypeTotals{}
	for name, count := range plan.DeviceMix {
		if count <= 0 {
			continue
		}
		out[name] = DeviceTypeTotals{TelemetryPublishes: 1}
	}
	return out
}

func completeStageResultsForPlan(plan Plan) []StageResult {
	return []StageResult{{
		Name:             "target",
		ConnectedDevices: plan.Conditions.Devices,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectSuccess:      int64(plan.Conditions.Devices),
			Subscribes:          int64(plan.Conditions.Devices),
			ActiveConnections:   int64(plan.Conditions.Devices),
			ActiveSubscriptions: int64(plan.Conditions.Devices),
			Publishes:           int64(plan.Conditions.Devices),
			ReceivedMessages:    int64(plan.Conditions.Devices),
			DeltaReceived:       int64(plan.Conditions.Devices),
			ReportedPublishes:   int64(plan.Conditions.Devices),
		},
		AppUserTotals: AppUserTotals{
			DesiredWrites: int64(plan.Conditions.Users),
			ReceivedAcks:  int64(plan.Conditions.Users),
		},
		MQTTConnectSuccessRatePercent:  100,
		DesiredReportedConvergenceRate: 100,
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   100,
		DeviceTypeTotals:               completeDeviceTypeEvidence(plan),
	}}
}

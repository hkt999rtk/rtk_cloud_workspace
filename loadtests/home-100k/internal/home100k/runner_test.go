package home100k

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func correlatedEvidenceForStages(runID string, stages []StageResult) ServerEvidence {
	device, app := summarizeStageTotals(stages)
	sources := requiredEvidenceSources(true)
	sources["emqx"] = EvidenceSource{Available: true, Counters: map[string]int64{
		"emqx.broker.identity":          1,
		"mqtt.total_connect_success":    device.ConnectSuccess + app.MQTTConnackSuccess,
		"device_mqtt.connect_success":   device.ConnectSuccess,
		"device_mqtt.publishes":         device.Publishes,
		"device_mqtt.received_messages": device.ReceivedMessages,
	}}
	sources["video_cloud_api"] = EvidenceSource{Available: true, Counters: map[string]int64{
		"app_user.desired_writes": app.DesiredWrites,
	}}
	sources["iot_device_shadow"] = EvidenceSource{Available: true, Counters: map[string]int64{
		"device_mqtt.reported_publishes": device.ReportedPublishes,
		"app_user.received_acks":         app.ReceivedAcks,
	}}
	return ServerEvidence{RunID: runID, Complete: true, Sources: sources}
}

func TestRunWritesPlanResultsEvidenceAndReportArtifacts(t *testing.T) {
	outDir := t.TempDir()
	evidenceFile := filepath.Join(outDir, "input-server-evidence.json")
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(evidenceFile, correlatedEvidenceForStages("run-fixed", stages)); err != nil {
		t.Fatalf("write evidence fixture: %v", err)
	}

	result, err := Run(RunOptions{
		PlanOptions: PlanOptions{
			EnvRoot:   "cloud_env/staging/runtime",
			Brandname: "RTK",
			Region:    "us-sea",
		},
		RunID:              "run-fixed",
		OutDir:             outDir,
		EphemeralVMs:       true,
		ServerEvidenceFile: evidenceFile,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE because sampled client counters do not cover stage targets", result.Status)
	}
	for _, path := range []string{result.PlanFile, result.ResultsFile, result.ServerEvidenceFile, result.ReportFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	for _, want := range []string{
		"Status: INCOMPLETE",
		"## Target Results",
		"desired/reported convergence",
		"offline desired convergence",
		"server evidence: complete",
	} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("report missing %q:\n%s", want, string(report))
		}
	}
}

func TestRunWithoutServerEvidenceWritesIncompleteArtifacts(t *testing.T) {
	outDir := t.TempDir()
	result, err := Run(RunOptions{
		PlanOptions: PlanOptions{
			EnvRoot:   "cloud_env/staging/runtime",
			Brandname: "RTK",
			Region:    "us-sea",
		},
		RunID:        "run-missing-evidence",
		OutDir:       outDir,
		EphemeralVMs: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE", result.Status)
	}
	raw, err := os.ReadFile(result.ResultsFile)
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("results json invalid: %v", err)
	}
	if parsed["status"] != "INCOMPLETE" {
		t.Fatalf("results status = %#v, want INCOMPLETE", parsed["status"])
	}
}

func TestAggregateCollectedRunWritesRunLevelArtifacts(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"lg01", "lg02"} {
		shardDir := filepath.Join(outDir, "shards", label)
		if err := writeJSONFile(filepath.Join(shardDir, "results.json"), map[string]any{
			"run_id":        "run-agg",
			"role":          "device-mqtt",
			"shard_index":   0,
			"stage_results": stages,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), correlatedEvidenceForStages("run-agg", append(stages, stages...))); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"},
		RunID:       "run-agg",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE because sampled client counters do not cover stage targets", result.Status)
	}
	for _, path := range []string{result.PlanFile, result.ResultsFile, result.ReportFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing aggregate artifact %s: %v", path, err)
		}
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Status: INCOMPLETE") || !strings.Contains(string(report), "server evidence: complete") {
		t.Fatalf("aggregate report missing INCOMPLETE evidence:\n%s", string(report))
	}
}

func TestAggregateCollectedRunUsesConfiguredStageNames(t *testing.T) {
	outDir := t.TempDir()
	planOptions := PlanOptions{
		EnvRoot:     "cloud_env/staging/runtime",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 9000,
	}
	plan, err := NewPlan(planOptions)
	if err != nil {
		t.Fatal(err)
	}
	stages := make([]StageResult, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		shardConnected := int64(stage.ConnectedDevices / DefaultVMCount)
		stages = append(stages, StageResult{
			Name:                  stage.Name,
			ConnectedDevices:      stage.ConnectedDevices,
			ShardConnectedDevices: stage.ConnectedDevices / DefaultVMCount,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     shardConnected,
				ConnectSuccess:      shardConnected,
				Subscribes:          shardConnected,
				ActiveConnections:   shardConnected,
				ActiveSubscriptions: shardConnected,
				Publishes:           1,
				ReceivedMessages:    1,
				DeltaReceived:       1,
				ReportedPublishes:   1,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts: 1,
				LoginSuccess:  1,
				DesiredWrites: 1,
				ReceivedAcks:  1,
			},
			DesiredReportedConvergenceRate: 100,
			OfflineDesiredConvergenceRate:  100,
			DeltaClearSuccessRatePercent:   100,
		})
	}
	for _, label := range []string{"lg01", "lg02"} {
		if err := writeJSONFile(filepath.Join(outDir, "shards", label, "results.json"), map[string]any{
			"run_id":        "run-agg-9k",
			"role":          "mixed",
			"stage_results": stages,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), correlatedEvidenceForStages("run-agg-9k", append(stages, stages...))); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: planOptions,
		RunID:       "run-agg-9k",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if len(result.StageResults) != len(plan.Stages) {
		t.Fatalf("stage results len = %d, want %d", len(result.StageResults), len(plan.Stages))
	}
	if result.StageResults[0].Name != plan.Stages[0].Name {
		t.Fatalf("first stage name = %q, want %q", result.StageResults[0].Name, plan.Stages[0].Name)
	}
}

func TestAggregateCollectedRunWithoutServerEvidenceIsIncomplete(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "lg01", "results.json"), map[string]any{
		"run_id":        "run-agg-missing",
		"role":          "device-mqtt",
		"shard_index":   0,
		"stage_results": stages,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"},
		RunID:       "run-agg-missing",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE", result.Status)
	}
}

func TestAggregateCollectedRunMarksLoadGeneratorSaturationIncomplete(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "lg01", "results.json"), map[string]any{
		"run_id":        "run-agg-saturated",
		"role":          "device-mqtt",
		"shard_index":   0,
		"stage_results": stages,
		"load_generator_health": map[string]any{
			"saturated": true,
			"reason":    "cpu p95 above 90%",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), correlatedEvidenceForStages("run-agg-saturated", stages)); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"},
		RunID:       "run-agg-saturated",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE", result.Status)
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Load-generator saturation invalidated server-capacity conclusion") || !strings.Contains(string(report), "cpu p95 above 90%") {
		t.Fatalf("aggregate report missing saturation evidence:\n%s", string(report))
	}
}

func TestAggregateCollectedRunMergesFetchedSyncTelemetry(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "lg01", "results.json"), map[string]any{
		"run_id":        "run-agg-sync",
		"role":          "mixed",
		"shard_index":   0,
		"stage_results": stages,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), correlatedEvidenceForStages("run-agg-sync", stages)); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "sync-telemetry.json"), SyncTelemetry{VMs: []VMSyncTelemetry{{Label: "lg01"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "sync-telemetry.d", "lg01.json"), VMSyncTelemetry{
		Label:            "lg01",
		FilesTransferred: 9,
		BytesTransferred: 123456,
		RemoteDiskBefore: "before",
		RemoteDiskAfter:  "after",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"},
		RunID:       "run-agg-sync",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if len(result.SyncTelemetry.VMs) != 1 || result.SyncTelemetry.VMs[0].BytesTransferred != 123456 {
		t.Fatalf("sync telemetry = %#v, want fetched per-VM bytes", result.SyncTelemetry)
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "## Sync/Provision Telemetry") || !strings.Contains(string(report), "123456") {
		t.Fatalf("report missing sync telemetry:\n%s", string(report))
	}
}

func TestAggregateCollectedRunWithAvailableEvidenceButMissingCountersIsIncomplete(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "lg01", "results.json"), map[string]any{
		"run_id":        "run-agg-no-counters",
		"role":          "mixed",
		"shard_index":   0,
		"stage_results": stages,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), ServerEvidence{
		RunID:    "run-agg-no-counters",
		Complete: true,
		Sources:  requiredEvidenceSources(true),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"},
		RunID:       "run-agg-no-counters",
		OutDir:      outDir,
	})
	if err != nil {
		t.Fatalf("AggregateCollectedRun() error = %v", err)
	}
	if result.Status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE", result.Status)
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Server/client counter correlation incomplete") {
		t.Fatalf("report missing missing-counter reason:\n%s", string(report))
	}
}

func TestEvaluateRunOutcomeSeparatesCompletionFromSuccessRate(t *testing.T) {
	stages := []StageResult{{
		Name:             "100pct",
		ConnectedDevices: 10000,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:     10000,
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
			LoginAttempts: 500,
			LoginSuccess:  500,
			DesiredWrites: 500,
			ReceivedAcks:  500,
		},
		DesiredReportedConvergenceRate: 100,
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   100,
		DeviceTypeTotals: map[string]DeviceTypeTotals{
			"light": {TelemetryPublishes: 9900},
		},
	}}
	evidence := ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)}
	correlation := ServerCorrelation{Status: "pass"}

	plan := Plan{
		Conditions: TestConditions{Devices: 10000, Users: 500},
		DeviceMix:  map[string]int{"light": 1},
	}
	outcome := evaluateRunOutcome(plan, evidence, stages, LoadGeneratorHealth{}, correlation, RuntimeLogCorrelation{})
	if outcome.Status != "COMPLETE" || outcome.Result != "FAIL" {
		t.Fatalf("outcome = %#v, want COMPLETE/FAIL for completed run below 99.5%% success", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Reasons, "\n"), "99.00%") {
		t.Fatalf("outcome reasons = %#v, want connection success rate detail", outcome.Reasons)
	}

	stages[0].DeviceMQTTTotals.ConnectSuccess = 9999
	stages[0].DeviceMQTTTotals.Subscribes = 9999
	stages[0].DeviceMQTTTotals.ActiveConnections = 9999
	stages[0].DeviceMQTTTotals.ActiveSubscriptions = 9999
	outcome = evaluateRunOutcome(plan, evidence, stages, LoadGeneratorHealth{}, correlation, RuntimeLogCorrelation{})
	if outcome.Status != "COMPLETE" || outcome.Result != "SUCCESS" {
		t.Fatalf("outcome = %#v, want COMPLETE/SUCCESS when success rate is above 99.5%%", outcome)
	}
}

func TestRunStatusMarksMissingPerTypeEvidenceIncomplete(t *testing.T) {
	plan := Plan{
		Conditions: TestConditions{Devices: 1, Users: 1},
		DeviceMix:  map[string]int{"light": 1, "smart_meter": 1},
	}
	stages := []StageResult{{
		Name:             "1",
		ConnectedDevices: 1,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:     1,
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
			LoginAttempts: 1,
			LoginSuccess:  1,
			DesiredWrites: 1,
			ReceivedAcks:  1,
		},
		MQTTConnectSuccessRatePercent:  100,
		DesiredReportedConvergenceRate: 100,
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   100,
		DeviceTypeTotals: map[string]DeviceTypeTotals{
			"light": {TelemetryPublishes: 1},
		},
	}}
	evidence := correlatedEvidenceForStages("run-missing-type", stages)
	correlation := ServerCorrelation{Status: "pass"}
	if status := runStatusWithCorrelation(plan, evidence, stages, LoadGeneratorHealth{}, correlation); status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE for missing smart_meter per-type evidence", status)
	}
	stages[0].DeviceTypeTotals["smart_meter"] = DeviceTypeTotals{TelemetryPublishes: 1}
	if status := runStatusWithCorrelation(plan, evidence, stages, LoadGeneratorHealth{}, correlation); status != "PASS" {
		t.Fatalf("status = %s, want PASS after all plan device types have evidence", status)
	}
}

func TestVideoQualificationUsesMQTTStagesAsTransportCoverage(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/runtime",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: VideoCanaryScenarioProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Stages[0].ConnectedDevices
	stages := []StageResult{{
		Name:                          plan.Stages[0].Name,
		ConnectedDevices:              target,
		MQTTConnectSuccessRatePercent: 100,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:   int64(target),
			ConnectSuccess:    int64(target),
			ActiveConnections: int64(target),
		},
	}}
	video := VideoEvidence{
		Complete: true,
		WebRTC: WebRTCTotals{
			CreateAttempts: 2, CreateSuccess: 2,
			SetupAttempts: 2, SetupSuccess: 2,
			CloseAttempts: 2, CloseSuccess: 2,
			SuccessRatePercent: 100,
		},
		WebRTCMedia: WebRTCMediaTotals{
			Attempts: 2, Successes: 2, ICEConnectedP95MS: 1, TimeToFirstRTPP95MS: 1,
		},
		TURN: TURNEvidence{RegistryAvailable: true, CoturnAvailable: true, ActiveNodes: 1},
	}
	serverCorrelation, runtimeCorrelation := qualificationCorrelations(
		plan,
		ServerCorrelation{Status: "incomplete"},
		RuntimeLogCorrelation{Status: "incomplete"},
	)
	outcome := evaluateRunOutcome(
		plan,
		ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)},
		stages,
		LoadGeneratorHealth{},
		serverCorrelation,
		runtimeCorrelation,
		video,
	)
	if outcome.Status != "COMPLETE" || outcome.Result != "SUCCESS" {
		t.Fatalf("outcome = %+v, want COMPLETE/SUCCESS", outcome)
	}
	if serverCorrelation.Status != "skipped" || runtimeCorrelation.Status != "skipped" {
		t.Fatalf("correlations = %s/%s, want skipped/skipped", serverCorrelation.Status, runtimeCorrelation.Status)
	}
}

func TestClipQualificationUsesMQTTStagesAsTransportCoverage(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:         "cloud_env/staging/runtime",
		Brandname:       "RTK",
		Region:          "us-sea",
		ScenarioProfile: ClipStorageCanaryScenarioProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Stages[0].ConnectedDevices
	stages := []StageResult{{
		Name:                          plan.Stages[0].Name,
		ConnectedDevices:              target,
		MQTTConnectSuccessRatePercent: 100,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:   int64(target),
			ConnectSuccess:    int64(target),
			ActiveConnections: int64(target),
		},
	}}
	serverCorrelation, runtimeCorrelation := qualificationCorrelations(
		plan,
		ServerCorrelation{Status: "incomplete"},
		RuntimeLogCorrelation{Status: "incomplete"},
	)
	outcome := evaluateRunOutcome(
		plan,
		ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)},
		stages,
		LoadGeneratorHealth{},
		serverCorrelation,
		runtimeCorrelation,
	)
	if outcome.Status != "COMPLETE" || outcome.Result != "SUCCESS" {
		t.Fatalf("outcome = %+v, want COMPLETE/SUCCESS", outcome)
	}
	if serverCorrelation.Status != "skipped" || runtimeCorrelation.Status != "skipped" {
		t.Fatalf("correlations = %s/%s, want skipped/skipped", serverCorrelation.Status, runtimeCorrelation.Status)
	}
}

func TestSummarizeStageTotalsUsesMaxForActiveConnectionGauges(t *testing.T) {
	stages := []StageResult{
		{
			Name: "25k",
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     10,
				ConnectSuccess:      8,
				ActiveConnections:   8,
				ActiveSubscriptions: 8,
			},
		},
		{
			Name: "50k",
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     12,
				ConnectSuccess:      10,
				ActiveConnections:   10,
				ActiveSubscriptions: 10,
			},
		},
	}

	device, _ := summarizeStageTotals(stages)

	if device.ConnectAttempts != 22 || device.ConnectSuccess != 18 {
		t.Fatalf("cumulative counters not summed: %#v", device)
	}
	if device.ActiveConnections != 10 || device.ActiveSubscriptions != 10 {
		t.Fatalf("active gauges = %d/%d, want max 10/10", device.ActiveConnections, device.ActiveSubscriptions)
	}
}

func TestAggregateStageResultsSumsShardActiveSubscriptionGauges(t *testing.T) {
	items := []StageResult{}
	for i := 0; i < 5; i++ {
		items = append(items, StageResult{
			Name:                  "target",
			ConnectedDevices:      4500,
			ShardConnectedDevices: 900,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     450,
				ConnectSuccess:      450,
				Subscribes:          450,
				ActiveConnections:   900,
				ActiveSubscriptions: 900,
			},
			AppUserTotals: AppUserTotals{
				DesiredWrites: 47,
				ReceivedAcks:  47,
			},
		})
	}

	result := aggregateStageResults(items)

	if result.DeviceMQTTTotals.ActiveConnections != 4500 {
		t.Fatalf("active connections = %d, want 4500", result.DeviceMQTTTotals.ActiveConnections)
	}
	if result.DeviceMQTTTotals.ActiveSubscriptions != 4500 {
		t.Fatalf("active subscriptions = %d, want 4500", result.DeviceMQTTTotals.ActiveSubscriptions)
	}
	if result.DeviceMQTTTotals.Subscribes != 2250 {
		t.Fatalf("new subscribes = %d, want 2250", result.DeviceMQTTTotals.Subscribes)
	}
}

func TestCorrelateServerEvidenceReportsPreciseMissingShadowClientCounters(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts: 10,
		ConnectSuccess:  10,
		Subscribes:      10,
	}
	app := AppUserTotals{
		TokenAttempts: 4,
		TokenSuccess:  4,
	}
	evidence := ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "incomplete" {
		t.Fatalf("status = %s, want incomplete", correlation.Status)
	}
	got := strings.Join(correlation.Reasons, "\n")
	for _, want := range []string{
		"client device MQTT publishes are zero; shadow reported path did not run",
		"client device MQTT received messages are zero; shadow delta path did not run",
		"client app desired writes are zero; shadow desired path did not run",
		"client app received ACKs are zero; app-side shadow confirmation did not run",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing reason %q in %#v", want, correlation.Reasons)
		}
	}
	if strings.Contains(got, "client device/app totals are missing or zero") {
		t.Fatalf("got legacy vague reason: %#v", correlation.Reasons)
	}
}

func TestCorrelateServerEvidenceUsesTotalMQTTConnectsForEMQX(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10,
		ConnectSuccess:    10,
		Subscribes:        10,
		Publishes:         4,
		ReceivedMessages:  4,
		DeltaReceived:     4,
		ReportedPublishes: 4,
	}
	app := AppUserTotals{
		TokenAttempts:      4,
		TokenSuccess:       4,
		MQTTConnackSuccess: 4,
		DesiredWrites:      4,
		ReceivedAcks:       4,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 14,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        4,
				"device_mqtt.delta_received":     4,
				"device_mqtt.reported_publishes": 4,
				"app_user.received_acks":         4,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
	if correlation.Checks[0].Counter != "mqtt.total_connect_success" || correlation.Checks[0].ClientTotal != 14 || correlation.Checks[0].ServerTotal != 14 {
		t.Fatalf("emqx check = %#v, want total MQTT connect 14/14", correlation.Checks[0])
	}
}

func TestCorrelateServerEvidenceTreatsSmallAggregateMismatchAsWarning(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10000,
		ConnectSuccess:    10000,
		Subscribes:        10000,
		Publishes:         996,
		ReceivedMessages:  996,
		DeltaReceived:     996,
		ReportedPublishes: 996,
	}
	app := AppUserTotals{
		TokenAttempts:      1250,
		TokenSuccess:       1250,
		MQTTConnackSuccess: 1250,
		DesiredWrites:      1250,
		ReceivedAcks:       996,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 11252,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        1231,
				"device_mqtt.delta_received":     996,
				"device_mqtt.reported_publishes": 996,
				"app_user.received_acks":         996,
			}},
		},
	}
	thresholds := GateThresholds{
		AggregateCorrelationTolerancePercent: 2,
		AggregateCorrelationMinTolerance:     5,
	}

	correlation := correlateServerEvidenceWithThresholds(evidence, device, app, thresholds)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass for aggregate warning-only mismatch; checks=%#v", correlation.Status, correlation.Checks)
	}
	warnings := 0
	for _, check := range correlation.Checks {
		if check.Status == "warning" {
			warnings++
		}
		if check.Counter == "mqtt.total_connect_success" && check.Tolerance != 226 {
			t.Fatalf("connect tolerance = %d, want 226", check.Tolerance)
		}
	}
	if warnings != 2 {
		t.Fatalf("warnings = %d, want 2; checks=%#v", warnings, correlation.Checks)
	}
}

func TestRunStatusUsesFunctionalSuccessThresholdForAppACKs(t *testing.T) {
	plan := Plan{
		Conditions: TestConditions{
			Devices:                           1000,
			Users:                             50,
			DevicesPerUser:                    20,
			FunctionalSuccessThresholdPercent: 99.5,
			ClientTargetCompletenessPercent:   100,
		},
		DeviceMix: map[string]int{"light": 1000},
	}
	stages := []StageResult{{
		Name:             "100pct",
		ConnectedDevices: 1000,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:     1000,
			ConnectSuccess:      1000,
			Subscribes:          1000,
			ActiveConnections:   1000,
			ActiveSubscriptions: 1000,
			Publishes:           50,
			ReceivedMessages:    50,
			DeltaReceived:       50,
			ReportedPublishes:   50,
		},
		AppUserTotals: AppUserTotals{
			TokenAttempts: 50,
			DesiredWrites: 50,
			ReceivedAcks:  49,
		},
		MQTTConnectSuccessRatePercent:  100,
		DesiredReportedConvergenceRate: 100,
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   100,
		DeviceTypeTotals: map[string]DeviceTypeTotals{
			"light": {TelemetryPublishes: 1},
		},
	}}
	evidence := correlatedEvidenceForStages("run-ack-threshold", stages)
	correlation := ServerCorrelation{Status: "pass"}

	status := runStatusWithCorrelation(plan, evidence, stages, LoadGeneratorHealth{}, correlation)

	if status != "FAIL" {
		t.Fatalf("status = %s, want FAIL because 49/50 ACKs is below 99.5%% functional threshold", status)
	}
	stages[0].AppUserTotals.ReceivedAcks = 50
	if status := runStatusWithCorrelation(plan, evidence, stages, LoadGeneratorHealth{}, correlation); status != "PASS" {
		t.Fatalf("status = %s, want PASS once ACK success reaches threshold", status)
	}
}

func TestCorrelateServerEvidenceFallsBackToNativeEMQXConnackMetric(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10,
		ConnectSuccess:    10,
		Subscribes:        10,
		Publishes:         4,
		ReceivedMessages:  4,
		DeltaReceived:     4,
		ReportedPublishes: 4,
	}
	app := AppUserTotals{
		TokenAttempts:      4,
		TokenSuccess:       4,
		MQTTConnackSuccess: 4,
		DesiredWrites:      4,
		ReceivedAcks:       4,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"emqx.metric.client.connack": 14,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        4,
				"device_mqtt.delta_received":     4,
				"device_mqtt.reported_publishes": 4,
				"app_user.received_acks":         4,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
	if correlation.Checks[0].Counter != "emqx.metric.client.connack" || correlation.Checks[0].ClientTotal != 14 || correlation.Checks[0].ServerTotal != 14 {
		t.Fatalf("emqx check = %#v, want native connack 14/14", correlation.Checks[0])
	}
}

func TestCorrelateServerEvidenceAllowsSmallEMQXCounterDeltaAt10KScale(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10000,
		ConnectSuccess:    9997,
		Subscribes:        9997,
		Publishes:         9997,
		ReceivedMessages:  9997,
		DeltaReceived:     9997,
		ReportedPublishes: 9997,
	}
	app := AppUserTotals{
		TokenAttempts:      1051,
		TokenSuccess:       1051,
		MQTTConnackSuccess: 1051,
		DesiredWrites:      1051,
		ReceivedAcks:       1005,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"emqx.metric.client.connack": 11054,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        1051,
				"device_mqtt.delta_received":     9997,
				"device_mqtt.reported_publishes": 9997,
				"app_user.received_acks":         1005,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
	check := correlation.Checks[0]
	if check.ClientTotal != 11048 || check.ServerTotal != 11054 || check.Delta != 6 || check.Tolerance != 12 {
		t.Fatalf("check = %#v, want client=11048 server=11054 delta=6 tolerance=12", check)
	}
}

func TestCorrelateServerEvidenceAcceptsAppLoginWithoutRequestToken(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   1000,
		ConnectSuccess:    1000,
		Subscribes:        1000,
		Publishes:         740,
		ReceivedMessages:  50,
		DeltaReceived:     50,
		ReportedPublishes: 50,
	}
	app := AppUserTotals{
		LoginAttempts:      1,
		LoginSuccess:       1,
		MQTTConnackSuccess: 50,
		DesiredWrites:      50,
		ReceivedAcks:       50,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"emqx.metric.client.connack": 1053,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        50,
				"device_mqtt.delta_received":     50,
				"device_mqtt.reported_publishes": 50,
				"app_user.received_acks":         50,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
}

func TestCorrelateServerEvidenceRejectsLargeEMQXCounterDelta(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10000,
		ConnectSuccess:    9997,
		Subscribes:        9997,
		Publishes:         9997,
		ReceivedMessages:  9997,
		DeltaReceived:     9997,
		ReportedPublishes: 9997,
	}
	app := AppUserTotals{
		TokenAttempts:      1051,
		TokenSuccess:       1051,
		MQTTConnackSuccess: 1051,
		DesiredWrites:      1051,
		ReceivedAcks:       1005,
	}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"emqx.metric.client.connack": 11100,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        1051,
				"device_mqtt.delta_received":     9997,
				"device_mqtt.reported_publishes": 9997,
				"app_user.received_acks":         1005,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "fail" {
		t.Fatalf("status = %s, want fail; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
}

func TestCorrelateServerEvidenceRequiresEMQXIdentity(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10,
		ConnectSuccess:    10,
		Subscribes:        10,
		Publishes:         4,
		ReceivedMessages:  4,
		DeltaReceived:     4,
		ReportedPublishes: 4,
	}
	app := AppUserTotals{TokenAttempts: 4, DesiredWrites: 4, ReceivedAcks: 4}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"mqtt.total_connect_success": 10,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        4,
				"device_mqtt.delta_received":     4,
				"device_mqtt.reported_publishes": 4,
				"app_user.received_acks":         4,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "incomplete" {
		t.Fatalf("status = %s, want incomplete", correlation.Status)
	}
	if !strings.Contains(strings.Join(correlation.Reasons, "\n"), "EMQX broker identity missing") {
		t.Fatalf("missing EMQX identity reason: %#v", correlation.Reasons)
	}
}

func TestCorrelateServerEvidenceAllowsSmallEMQXConnectMetricDrift(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   100000,
		ConnectSuccess:    100000,
		Subscribes:        100000,
		Publishes:         5000,
		ReceivedMessages:  5000,
		DeltaReceived:     5000,
		ReportedPublishes: 5000,
	}
	app := AppUserTotals{TokenAttempts: 5000, MQTTConnackSuccess: 5000, DesiredWrites: 5000, ReceivedAcks: 5000}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 105087,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        5000,
				"device_mqtt.delta_received":     5000,
				"device_mqtt.reported_publishes": 5000,
				"app_user.received_acks":         5000,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v reasons=%#v", correlation.Status, correlation.Checks, correlation.Reasons)
	}
	if check := correlation.Checks[0]; check.Counter != "mqtt.total_connect_success" || check.Delta != 87 || check.Tolerance != 106 || check.Status != "warning" {
		t.Fatalf("emqx check = %#v, want delta 87 within tolerance 106 as warning", check)
	}
}

func TestCorrelateServerEvidenceWarnsOnSharedEMQXConnectOverage(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   50000,
		ConnectSuccess:    50000,
		Subscribes:        50000,
		Publishes:         2502,
		ReceivedMessages:  2502,
		DeltaReceived:     2502,
		ReportedPublishes: 2502,
	}
	app := AppUserTotals{TokenAttempts: 2502, TokenSuccess: 2502, MQTTConnackSuccess: 2502, DesiredWrites: 2502, ReceivedAcks: 2502}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 52826,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        2502,
				"device_mqtt.delta_received":     2502,
				"device_mqtt.reported_publishes": 2502,
				"app_user.received_acks":         2502,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass with EMQX shared-cluster overage warning; checks=%#v", correlation.Status, correlation.Checks)
	}
	if check := correlation.Checks[0]; check.Counter != "mqtt.total_connect_success" || check.Delta != 324 || check.Tolerance != 53 || check.Status != "warning" {
		t.Fatalf("emqx check = %#v, want overage warning", check)
	}
}

func TestCorrelateServerEvidenceUsesActiveEMQXConnectedGaugeWhenBaselineDeltaIsStale(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   50000,
		ConnectSuccess:    50000,
		Subscribes:        50000,
		Publishes:         2502,
		ReceivedMessages:  2502,
		DeltaReceived:     2502,
		ReportedPublishes: 2502,
	}
	app := AppUserTotals{TokenAttempts: 2502, TokenSuccess: 2502, MQTTConnackSuccess: 2502, DesiredWrites: 2502, ReceivedAcks: 2502}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":             1,
				"mqtt.total_connect_success":       17749,
				"emqx.metric.client.connected":     53662,
				"emqx.metric.packets.connack.sent": 53662,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        2502,
				"device_mqtt.delta_received":     2502,
				"device_mqtt.reported_publishes": 2502,
				"app_user.received_acks":         2502,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass with active EMQX connected gauge; checks=%#v", correlation.Status, correlation.Checks)
	}
	if check := correlation.Checks[0]; check.Counter != "emqx.metric.client.connected" || check.ServerTotal != 53662 || check.Status != "warning" {
		t.Fatalf("emqx check = %#v, want active connected gauge warning", check)
	}
}

func TestCorrelateServerEvidenceFailsOnMissingEMQXConnects(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   50000,
		ConnectSuccess:    50000,
		Subscribes:        50000,
		Publishes:         2502,
		ReceivedMessages:  2502,
		DeltaReceived:     2502,
		ReportedPublishes: 2502,
	}
	app := AppUserTotals{TokenAttempts: 2502, TokenSuccess: 2502, MQTTConnackSuccess: 2502, DesiredWrites: 2502, ReceivedAcks: 2502}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 52000,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        2502,
				"device_mqtt.delta_received":     2502,
				"device_mqtt.reported_publishes": 2502,
				"app_user.received_acks":         2502,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "fail" {
		t.Fatalf("status = %s, want fail for missing EMQX connects; checks=%#v", correlation.Status, correlation.Checks)
	}
	if check := correlation.Checks[0]; check.Delta >= 0 || check.Status != "fail" {
		t.Fatalf("emqx check = %#v, want negative delta fail", check)
	}
}

func TestCorrelateServerEvidenceDoesNotTolerateShadowCounterDrift(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10,
		ConnectSuccess:    10,
		Subscribes:        10,
		Publishes:         4,
		ReceivedMessages:  4,
		DeltaReceived:     4,
		ReportedPublishes: 4,
	}
	app := AppUserTotals{TokenAttempts: 4, MQTTConnackSuccess: 4, DesiredWrites: 4, ReceivedAcks: 4}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity":       1,
				"mqtt.total_connect_success": 14,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        4,
				"device_mqtt.delta_received":     4,
				"device_mqtt.reported_publishes": 4,
				"app_user.received_acks":         3,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; checks=%#v", correlation.Status, correlation.Checks)
	}
	for _, check := range correlation.Checks {
		if check.Counter == "app_user.received_acks" && (check.Status != "warning" || check.Tolerance != 5) {
			t.Fatalf("shadow ack check = %#v, want tolerated warning", check)
		}
	}
}

func TestCorrelateServerEvidenceAcceptsEMQXListenerStatsIdentity(t *testing.T) {
	device := DeviceMQTTTotals{
		ConnectAttempts:   10,
		ConnectSuccess:    10,
		Subscribes:        10,
		Publishes:         4,
		ReceivedMessages:  4,
		DeltaReceived:     4,
		ReportedPublishes: 4,
	}
	app := AppUserTotals{TokenAttempts: 4, DesiredWrites: 4, ReceivedAcks: 4}
	evidence := ServerEvidence{
		Complete: true,
		Sources: map[string]EvidenceSource{
			"emqx": {Available: true, Counters: map[string]int64{
				"mqtt.total_connect_success": 10,
			}},
			"emqx_listener_stats": {Available: true, Counters: map[string]int64{
				"emqx.broker.identity": 1,
			}},
			"iot_device_shadow": {Available: true, Counters: map[string]int64{
				"app_user.desired_writes":        4,
				"device_mqtt.delta_received":     4,
				"device_mqtt.reported_publishes": 4,
				"app_user.received_acks":         4,
			}},
		},
	}

	correlation := correlateServerEvidence(evidence, device, app)

	if correlation.Status != "pass" {
		t.Fatalf("status = %s, want pass; reasons=%#v", correlation.Status, correlation.Reasons)
	}
}

func TestCorrelateRuntimeLogsFindsMissingStreamsAndSequences(t *testing.T) {
	stages := []StageResult{{
		Name: "target",
		CommandEvents: []CommandEvent{
			{
				Stage:              "target",
				DeviceID:           "rtk-0001",
				CommandID:          "cmd-1",
				RuntimeLogStreamID: "stream-1",
				ExpectedLogs: []LogExpect{
					{Seq: 1, Source: "app_controller", Message: "desired"},
					{Seq: 2, Source: "device_client", Message: "delta"},
				},
			},
			{
				Stage:              "target",
				DeviceID:           "rtk-0002",
				CommandID:          "cmd-2",
				RuntimeLogStreamID: "stream-2",
				ExpectedLogs: []LogExpect{
					{Seq: 1, Source: "app_controller", Message: "desired"},
					{Seq: 2, Source: "device_client", Message: "delta"},
				},
			},
		},
	}}
	evidence := ServerEvidence{Sources: map[string]EvidenceSource{
		"iot_device_shadow_streams": {
			Available: true,
			Counters: map[string]int64{
				"runtime_log_streams.total":                           1,
				"runtime_log_stream.stream-1.entries":                 1,
				"runtime_log_stream.stream-1.device.rtk-0001.entries": 1,
				"runtime_log_stream.stream-1.seq.1":                   1,
			},
		},
	}}

	correlation := correlateRuntimeLogs(evidence, stages)

	if correlation.Status != "fail" {
		t.Fatalf("status = %s, want fail", correlation.Status)
	}
	if correlation.ClientCommandEvents != 2 || correlation.ServerRuntimeStreams != 1 {
		t.Fatalf("unexpected totals: %+v", correlation)
	}
	if correlation.MissingStreamCount != 1 || correlation.MissingStreamSamples[0].RuntimeLogStreamID != "stream-2" {
		t.Fatalf("missing streams = %+v", correlation)
	}
	if correlation.MissingSequenceCount != 1 || correlation.MissingSequenceSamples[0].Seq != 2 {
		t.Fatalf("missing sequences = %+v", correlation)
	}
}

func TestCorrelateRuntimeLogsSkipsEventsWithoutRuntimeExpectations(t *testing.T) {
	stages := []StageResult{{
		Name: "target",
		CommandEvents: []CommandEvent{{
			Stage:     "target",
			DeviceID:  "rtk-0041",
			CommandID: "cmd-1",
		}},
	}}
	evidence := ServerEvidence{Sources: map[string]EvidenceSource{
		"iot_device_shadow_streams": {
			Available: true,
			Counters:  map[string]int64{},
		},
	}}

	correlation := correlateRuntimeLogs(evidence, stages)

	if correlation.Status != "skipped" {
		t.Fatalf("status = %s, want skipped", correlation.Status)
	}
	if correlation.ClientCommandEvents != 1 || correlation.MissingStreamCount != 0 || correlation.MissingSequenceCount != 0 {
		t.Fatalf("unexpected correlation: %+v", correlation)
	}
}

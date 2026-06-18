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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
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
			EnvRoot:   "cloud_env/staging/lke",
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
		"## Stage Results",
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
			EnvRoot:   "cloud_env/staging/lke",
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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"home-100k-mixed-000", "home-100k-mixed-001"} {
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
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"},
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
		EnvRoot:     "cloud_env/staging/lke",
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
	for _, label := range []string{"home-100k-mixed-000", "home-100k-mixed-001"} {
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
	if result.StageResults[0].Name != "25pct" {
		t.Fatalf("first stage name = %q, want 25pct", result.StageResults[0].Name)
	}
}

func TestAggregateCollectedRunWithoutServerEvidenceIsIncomplete(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "home-100k-mixed-000", "results.json"), map[string]any{
		"run_id":        "run-agg-missing",
		"role":          "device-mqtt",
		"shard_index":   0,
		"stage_results": stages,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"},
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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "home-100k-mixed-000", "results.json"), map[string]any{
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
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"},
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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "home-100k-mixed-000", "results.json"), map[string]any{
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
	if err := writeJSONFile(filepath.Join(outDir, "sync-telemetry.json"), SyncTelemetry{VMs: []VMSyncTelemetry{{Label: "home-100k-mixed-000"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "sync-telemetry.d", "home-100k-mixed-000.json"), VMSyncTelemetry{
		Label:            "home-100k-mixed-000",
		FilesTransferred: 9,
		BytesTransferred: 123456,
		RemoteDiskBefore: "before",
		RemoteDiskAfter:  "after",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"},
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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "home-100k-mixed-000", "results.json"), map[string]any{
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
		PlanOptions: PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"},
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

func TestRunStatusWithCorrelationRequiresClientTargetCoverage(t *testing.T) {
	stages := []StageResult{{
		Name:             "25k",
		ConnectedDevices: 25000,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:   60,
			ConnectSuccess:    60,
			Publishes:         60,
			ReceivedMessages:  60,
			DeltaReceived:     60,
			ReportedPublishes: 60,
		},
		AppUserTotals: AppUserTotals{
			LoginAttempts: 60,
			DesiredWrites: 60,
			ReceivedAcks:  60,
		},
		DesiredReportedConvergenceRate: 100,
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   100,
	}}
	evidence := ServerEvidence{Complete: true, Sources: requiredEvidenceSources(true)}
	correlation := ServerCorrelation{Status: "pass"}

	plan := Plan{
		Conditions: TestConditions{Devices: 100000, Users: 5000},
		DeviceMix:  map[string]int{"light": 1},
	}
	status := runStatusWithCorrelation(plan, evidence, stages, LoadGeneratorHealth{}, correlation)
	if status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE for 60 connect attempts against 25k target", status)
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
			Name:                  "50pct",
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
		Name: "25pct",
		CommandEvents: []CommandEvent{
			{
				Stage:              "25pct",
				DeviceID:           "rtk-0001",
				CommandID:          "cmd-1",
				RuntimeLogStreamID: "stream-1",
				ExpectedLogs: []LogExpect{
					{Seq: 1, Source: "app_controller", Message: "desired"},
					{Seq: 2, Source: "device_client", Message: "delta"},
				},
			},
			{
				Stage:              "25pct",
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
				"runtime_log_streams.total":           1,
				"runtime_log_stream.stream-1.entries": 1,
				"runtime_log_stream.stream-1.seq.1":   1,
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

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
	if result.Status != "PASS" {
		t.Fatalf("status = %s, want PASS", result.Status)
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
		"Status: PASS",
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
	if result.Status != "PASS" {
		t.Fatalf("status = %s, want PASS", result.Status)
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
	if !strings.Contains(string(report), "Status: PASS") || !strings.Contains(string(report), "server evidence: complete") {
		t.Fatalf("aggregate report missing PASS evidence:\n%s", string(report))
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

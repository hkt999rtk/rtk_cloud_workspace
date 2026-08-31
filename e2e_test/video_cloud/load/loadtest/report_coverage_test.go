package loadtest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReportPersistenceAndPublicThresholdWrappers(t *testing.T) {
	dir := t.TempDir()
	result := &Result{
		Schema:   "load-result/v1",
		RunID:    "coverage-report",
		Actors:   map[string]ActorMetrics{},
		Metadata: map[string]string{},
		Thresholds: ThresholdEvaluation{
			Passed: true,
		},
	}
	jsonPath := filepath.Join(dir, "results.json")
	if err := WriteJSON(jsonPath, result); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadJSON(jsonPath)
	if err != nil || loaded.RunID != result.RunID {
		t.Fatalf("ReadJSON() = %#v, %v", loaded, err)
	}
	if err := WriteMarkdown(filepath.Join(dir, "test_report.md"), loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "test_report.md")); err != nil {
		t.Fatal(err)
	}

	evaluation := EvaluateThresholds(
		Summary{SuccessRate: 0.5, P95LatencyMS: 200, P99LatencyMS: 300},
		Thresholds{MinSuccessRate: 0.9, MaxP95Latency: 100, MaxP99Latency: 200},
	)
	ApplyWebRTCMediaThreshold(&evaluation, WebRTCMediaMetrics{Attempts: 2, Successes: 1}, Thresholds{MinWebRTCMediaSuccessRate: 0.9})
	if evaluation.Passed || len(evaluation.Failures) != 4 {
		t.Fatalf("threshold evaluation = %#v", evaluation)
	}
}

func TestReportPersistenceRejectsInvalidPathsAndJSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadJSON(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("ReadJSON accepted a missing file")
	}
	invalidJSON := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalidJSON, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJSON(invalidJSON); err == nil {
		t.Fatal("ReadJSON accepted malformed JSON")
	}
	if err := WriteJSON(dir, &Result{}); err == nil {
		t.Fatal("WriteJSON accepted a directory path")
	}
	if err := WriteMarkdown(dir, &Result{}); err == nil {
		t.Fatal("WriteMarkdown accepted a directory path")
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoCoverageTimingsUsesWallSpanAcrossUnorderedShards(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events.json")
	output := filepath.Join(dir, "timings.json")
	data := `{"Time":"2026-10-31T00:00:02Z","Action":"start","Package":"example/pkg"}
{"Time":"2026-10-31T00:00:06Z","Action":"pass","Package":"example/pkg","Test":"TestSlow/child","Elapsed":3}
{"Time":"2026-10-31T00:00:07Z","Action":"pass","Package":"example/pkg","Test":"TestSlow","Elapsed":5}
{"Time":"2026-10-31T00:00:07Z","Action":"pass","Package":"example/pkg","Elapsed":5}
{"Time":"2026-10-31T00:00:00Z","Action":"start","Package":"example/pkg"}
{"Time":"2026-10-31T00:00:01Z","Action":"skip","Package":"example/pkg","Test":"TestSkipped","Elapsed":0}
{"Time":"2026-10-31T00:00:03Z","Action":"fail","Package":"example/pkg","Test":"TestFailed","Elapsed":2.5}
{"Time":"2026-10-31T00:00:03Z","Action":"fail","Package":"example/pkg","Elapsed":3}
`
	if err := os.WriteFile(events, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeGoCoverageTimings(events, output); err != nil {
		t.Fatal(err)
	}
	var report goCoverageTimingReport
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if report.EventSpanMS == nil || *report.EventSpanMS != 7000 {
		t.Fatalf("wall span, not sum of parallel runs: %#v", report)
	}
	if report.Counts.Passed != 1 || report.Counts.Failed != 1 || report.Counts.Skipped != 1 {
		t.Fatalf("subtest counted twice: %#v", report.Counts)
	}
	if len(report.SlowestTests) != 3 || report.SlowestTests[0].Test != "TestSlow" || report.SlowestTests[0].DurationMS != 5000 {
		t.Fatalf("slowest tests: %#v", report.SlowestTests)
	}
	if len(report.Packages) != 1 || report.Packages[0].Runs != 2 || report.Packages[0].ElapsedSumMS != 8000 || report.Packages[0].Status != "FAIL" {
		t.Fatalf("package executions: %#v", report.Packages)
	}
}

func TestGoCoverageTimingsDoesNotInventMissingTimestamps(t *testing.T) {
	dir := t.TempDir()
	events, output := filepath.Join(dir, "events"), filepath.Join(dir, "timings")
	if err := os.WriteFile(events, []byte("{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestOne\",\"Elapsed\":0.125}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeGoCoverageTimings(events, output); err != nil {
		t.Fatal(err)
	}
	var report goCoverageTimingReport
	b, _ := os.ReadFile(output)
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if report.EventSpanMS != nil || report.StartedAt != "" || report.CompletedAt != "" || report.SlowestTests[0].DurationMS != 125 {
		t.Fatalf("unexpected inferred timing: %#v", report)
	}
}

func TestGoCoverageTimingsRejectsInvalidEvents(t *testing.T) {
	for _, data := range []string{
		`not-json`,
		`{"Action":"pass","Package":"p","Elapsed":-1}`,
		`{"Time":"invalid","Action":"pass","Package":"p"}`,
		`{"Action":"pass","Test":"TestMissingPackage"}`,
		"{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestDuplicate\"}\n{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestDuplicate\"}",
	} {
		t.Run(strings.ReplaceAll(data[:min(20, len(data))], "/", "_"), func(t *testing.T) {
			dir := t.TempDir()
			events, output := filepath.Join(dir, "events"), filepath.Join(dir, "timings")
			if err := os.WriteFile(events, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(output, []byte(`{"schema_version":1,"stale":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeGoCoverageTimings(events, output); err == nil {
				t.Fatal("invalid events accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("invalid input published a report")
			}
		})
	}
}

func TestGoCoverageTimingsLimitsAndSortsTopLevelTests(t *testing.T) {
	dir := t.TempDir()
	events, output := filepath.Join(dir, "events"), filepath.Join(dir, "timings")
	f, err := os.Create(events)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i := 24; i >= 0; i-- {
		if err := enc.Encode(map[string]any{"Action": "pass", "Package": "p", "Test": "Test" + string(rune('A'+i)), "Elapsed": float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if err := writeGoCoverageTimings(events, output); err != nil {
		t.Fatal(err)
	}
	var report goCoverageTimingReport
	b, _ := os.ReadFile(output)
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.SlowestTests) != 20 || report.Counts.Passed != 25 || report.SlowestTests[0].Test != "TestY" || report.SlowestTests[19].Test != "TestF" {
		t.Fatalf("top twenty: %#v", report)
	}
}

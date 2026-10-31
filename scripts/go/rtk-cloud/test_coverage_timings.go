package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

type goCoverageTimingCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type goCoverageTestTiming struct {
	Package    string `json:"package"`
	Test       string `json:"test"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

type goCoveragePackageTiming struct {
	Package      string `json:"package"`
	Runs         int    `json:"runs"`
	Status       string `json:"status"`
	ElapsedSumMS int64  `json:"elapsed_sum_ms"`
	LongestRunMS int64  `json:"longest_run_ms"`
}

type goCoverageTimingReport struct {
	SchemaVersion int                       `json:"schema_version"`
	StartedAt     string                    `json:"started_at,omitempty"`
	CompletedAt   string                    `json:"completed_at,omitempty"`
	EventSpanMS   *int64                    `json:"event_span_ms,omitempty"`
	Counts        goCoverageTimingCounts    `json:"top_level_counts"`
	SlowestTests  []goCoverageTestTiming    `json:"slowest_tests"`
	Packages      []goCoveragePackageTiming `json:"packages"`
}

// EventSpanMS measures the observed stream, not compilation or CPU time.
// Package elapsed sums may exceed that span when shards run concurrently.
// Subtests are excluded because their time is already in their top-level test.
func writeGoCoverageTimings(eventsPath, timingsPath string) error {
	if err := os.Remove(timingsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.Open(eventsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	report := goCoverageTimingReport{SchemaVersion: 1, SlowestTests: []goCoverageTestTiming{}, Packages: []goCoveragePackageTiming{}}
	packages := map[string]*goCoveragePackageTiming{}
	tests := map[string]bool{}
	var first, last time.Time
	decoder := json.NewDecoder(f)
	for {
		var event goTestEvent
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("read coverage timing event: %w", err)
		}
		if event.Time != "" {
			stamp, err := time.Parse(time.RFC3339Nano, event.Time)
			if err != nil {
				return fmt.Errorf("coverage event timestamp: %w", err)
			}
			if first.IsZero() || stamp.Before(first) {
				first = stamp
			}
			if last.IsZero() || stamp.After(last) {
				last = stamp
			}
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		if event.Package == "" || event.Elapsed < 0 || event.Elapsed >= float64(math.MaxInt64)/1000 {
			return errors.New("coverage timing terminal event has invalid package or elapsed time")
		}
		elapsed := int64(math.Round(event.Elapsed * 1000))
		status := strings.ToUpper(event.Action)
		if event.Test == "" {
			p := packages[event.Package]
			if p == nil {
				p = &goCoveragePackageTiming{Package: event.Package, Status: "SKIP"}
				packages[event.Package] = p
			}
			p.Runs++
			if elapsed > math.MaxInt64-p.ElapsedSumMS {
				return errors.New("coverage package timing overflow")
			}
			p.ElapsedSumMS += elapsed
			p.LongestRunMS = max(p.LongestRunMS, elapsed)
			if status == "FAIL" || p.Status != "FAIL" && status == "PASS" {
				p.Status = status
			}
			continue
		}
		if strings.Contains(event.Test, "/") {
			continue
		}
		key := event.Package + "/" + event.Test
		if tests[key] {
			return fmt.Errorf("duplicate top-level coverage result: %s", key)
		}
		tests[key] = true
		switch event.Action {
		case "pass":
			report.Counts.Passed++
		case "fail":
			report.Counts.Failed++
		case "skip":
			report.Counts.Skipped++
		}
		report.SlowestTests = append(report.SlowestTests, goCoverageTestTiming{Package: event.Package, Test: event.Test, Status: status, DurationMS: elapsed})
	}
	if !first.IsZero() {
		report.StartedAt = first.UTC().Format(time.RFC3339Nano)
		report.CompletedAt = last.UTC().Format(time.RFC3339Nano)
		span := last.Sub(first).Milliseconds()
		report.EventSpanMS = &span
	}
	sort.Slice(report.SlowestTests, func(i, j int) bool {
		a, b := report.SlowestTests[i], report.SlowestTests[j]
		if a.DurationMS != b.DurationMS {
			return a.DurationMS > b.DurationMS
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Test < b.Test
	})
	if len(report.SlowestTests) > 20 {
		report.SlowestTests = report.SlowestTests[:20]
	}
	for _, p := range packages {
		report.Packages = append(report.Packages, *p)
	}
	sort.Slice(report.Packages, func(i, j int) bool { return report.Packages[i].Package < report.Packages[j].Package })
	return writeJSON(timingsPath, report)
}

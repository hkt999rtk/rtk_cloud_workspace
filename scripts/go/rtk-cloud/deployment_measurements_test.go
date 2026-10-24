package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeploymentMeasurementsCountAttemptsAndCacheHits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	m := &deploymentMeasurements{log: &environmentProgressLog{file: f}, run: &deploymentMeasurementScope{stage: "deployment.preflight", start: time.Now()}}
	ctx := context.WithValue(context.Background(), deploymentMeasurementsKey{}, m)
	gate := beginDeploymentMeasurement(ctx, "deployment.pki")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); done := beginDeploymentQuery(ctx, "kubectl"); done(nil) }()
	}
	wg.Wait()
	failed := beginDeploymentQuery(ctx, "pki-http")
	failed(errors.New("secret-bearing error is never serialized"))
	deploymentMeasurementCacheHit(ctx)
	gate(errors.New("safe failure"))
	gate(nil) // Finalized scopes are emitted once.
	if err := m.log.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-bearing") {
		t.Fatal("raw error leaked")
	}
	var completed deploymentMeasurement
	probes, gates := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row deploymentMeasurement
		if json.Unmarshal([]byte(line), &row) != nil {
			t.Fatal("invalid JSONL")
		}
		if row.Event == "probe-complete" {
			probes++
			if row.CompletedAt == nil || row.DurationMS < 0 {
				t.Fatal("missing probe time")
			}
		}
		if row.Event == "gate-complete" {
			completed = row
			gates++
		}
	}
	if probes != 13 || gates != 1 || completed.QueryCount != 13 || completed.CacheHits != 1 || completed.Status != "FAIL" || completed.CompletedAt == nil {
		t.Fatalf("incorrect measurements: probes=%d gates=%d row=%+v", probes, gates, completed)
	}
}

func TestDeploymentMeasurementsCacheDoesNotCountAsQuery(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "metrics")
	if err != nil {
		t.Fatal(err)
	}
	m := &deploymentMeasurements{log: &environmentProgressLog{file: f}, run: &deploymentMeasurementScope{start: time.Now()}}
	ctx := context.WithValue(context.Background(), deploymentMeasurementsKey{}, m)
	r := newDeploymentCheckRuntime(ctx, "dev")
	loads := 0
	for i := 0; i < 3; i++ {
		_, err := r.cached("private-key-never-logged", func() ([]byte, error) {
			loads++
			done := beginDeploymentQuery(ctx, "kubectl")
			done(nil)
			return []byte("private-output-never-logged"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads != 1 || m.run.queries != 1 || m.run.hits != 2 {
		t.Fatal("cache accounting is incorrect")
	}
	if err := m.log.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.Name())
	if strings.Contains(string(raw), "private-") {
		t.Fatal("private cache key or value leaked")
	}
}

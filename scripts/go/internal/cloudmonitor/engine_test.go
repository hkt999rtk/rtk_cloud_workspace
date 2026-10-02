package cloudmonitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func engineConfig() Config {
	return Config{Environment: "dev", Stack: "video-cloud-dev", Timezone: "Asia/Taipei", DailyAt: "08:00", RetentionDays: 30}
}
func waitEngine(t *testing.T, ch <-chan Snapshot) Snapshot {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not sample")
		return Snapshot{}
	}
}

func TestWatchFailureDoesNotDuplicateOrPauseSampling(t *testing.T) {
	at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 4)
	samples := make(chan Snapshot, 4)
	reports := make(chan error, 2)
	var metricsCalls atomic.Int32
	collector := func(context.Context, time.Time) Collection {
		return Collection{Results: []Result{{Service: "api", CheckID: "http", Layer: "liveness", Required: true, Status: Pass, ObservedAt: at, Reason: "ready"}}}
	}
	deps := watchDependencies{now: func() time.Time { return at }, ticks: ticks, collect: map[string]func(context.Context, time.Time) Collection{"service": collector, "certificate": collector, "synthetic": collector, "metrics": func(_ context.Context, n time.Time) Collection {
		v := metricsCalls.Add(1)
		return Collection{Results: []Result{{Service: "postgres", CheckID: "metric", Required: true, Status: Pass, ObservedAt: n}}, Samples: []Sample{{Service: "postgres", Name: "transactions", Kind: "counter", Unit: "count", Value: float64(v * 60), ObservedAt: n, Identity: "server"}}}
	}}, report: func(context.Context, time.Time, time.Time, string) error {
		return errors.New("sensitive-renderer-error")
	}}
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- watchWithDependencies(ctx, engineConfig(), Inventory{}, Runtime{OutDir: dir}, deps, func(s Snapshot) { samples <- s }, func(e error) { reports <- e })
	}()
	first := waitEngine(t, samples)
	select {
	case <-reports:
	case <-time.After(3 * time.Second):
		t.Fatal("report callback missing")
	}
	ticks <- at.Add(30*time.Second - 200*time.Millisecond)
	second := waitEngine(t, samples)
	if first.RunID == second.RunID {
		t.Fatal("duplicated run identity")
	}
	if first.ReportError == "" && second.ReportError == "" {
		t.Fatal("report failure not preserved")
	}
	if metricsCalls.Load() != 2 {
		t.Fatal("report failure blocked collection")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch cancellation hung")
	}
	got, err := LoadHistory(dir, "dev", at, at.Add(time.Minute))
	if err != nil || len(got) != 2 {
		t.Fatalf("duplicate failure snapshot: %d %v", len(got), err)
	}
	if _, err = os.Stat(filepath.Join(dir, "history", "dev", "latest-report.json")); err != nil {
		t.Fatal("report status missing")
	}
}

func TestWatchSlowCertificateDoesNotBlockOrOverlap(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 4)
	samples := make(chan Snapshot, 4)
	started := make(chan struct{}, 1)
	certificateBudget := make(chan time.Duration, 1)
	var certCalls atomic.Int32
	deps := watchDependencies{now: func() time.Time { return at }, ticks: ticks, collect: map[string]func(context.Context, time.Time) Collection{"certificate": func(c context.Context, n time.Time) Collection {
		certCalls.Add(1)
		deadline, ok := c.Deadline()
		if !ok {
			certificateBudget <- 0
		} else {
			certificateBudget <- time.Until(deadline)
		}
		started <- struct{}{}
		<-c.Done()
		return Collection{}
	}, "metrics": func(_ context.Context, n time.Time) Collection {
		return Collection{Results: []Result{{Service: "postgres", CheckID: "stats", Required: true, Status: Pass, ObservedAt: n}}}
	}}}
	done := make(chan error, 1)
	go func() {
		done <- watchWithDependencies(ctx, engineConfig(), Inventory{}, Runtime{OutDir: t.TempDir()}, deps, func(s Snapshot) { samples <- s }, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("certificate collector never started")
	}
	if budget := <-certificateBudget; budget < 4*time.Minute || budget > 5*time.Minute {
		t.Fatalf("certificate discovery needs a bounded budget that admits the measured 3–4 minute audit: %v", budget)
	}
	first := waitEngine(t, samples)
	pending := false
	for _, r := range first.Results {
		if r.CheckID == "collector/certificate" && r.Status == Unknown {
			pending = true
		}
	}
	if !pending {
		t.Fatal("pending certificate did not create coverage gap")
	}
	ticks <- at.Add(30 * time.Second)
	_ = waitEngine(t, samples)
	ticks <- at.Add(16 * time.Minute)
	_ = waitEngine(t, samples)
	if certCalls.Load() != 1 {
		t.Fatal("overlapping certificate collectors")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop collectors")
	}
}

func TestWriterLockExclusiveAndReleased(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireWriterLock(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireWriterLock(dir, "dev"); err == nil {
		second.Close()
		t.Fatal("second writer admitted")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireWriterLock(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestCompactWatchHistoryDropsResultsAndOldCounters(t *testing.T) {
	now := time.Now().UTC()
	s := historyFixture(now.Add(-time.Hour))
	s.Samples = []Sample{{Name: "disk_used_bytes", Kind: "gauge", ObservedAt: s.StartedAt}, {Name: "transactions", Kind: "counter", ObservedAt: s.StartedAt}, {Name: "transactions_per_second", Kind: "gauge", ObservedAt: s.StartedAt}}
	fresh := historyFixture(now)
	fresh.Samples = []Sample{{Name: "transactions", Kind: "counter", ObservedAt: now}, {Name: "probe_latency_ms", Kind: "gauge", ObservedAt: now}}
	got := compactWatchHistory([]Snapshot{s, fresh}, now)
	if len(got) != 2 || len(got[0].Samples) != 1 || len(got[1].Samples) != 1 || len(got[0].Results) != 0 || got[0].Samples[0].Name != "disk_used_bytes" {
		t.Fatalf("cache retains repeated data %#v", got)
	}
}

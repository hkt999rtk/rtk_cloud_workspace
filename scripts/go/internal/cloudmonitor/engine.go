package cloudmonitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// WriterLock is held for the whole check/watch run. The kernel releases it after
// a crash; the file is intentionally retained so a second writer cannot unlink it.
type WriterLock struct{ file *os.File }

func AcquireWriterLock(outDir, environment string) (*WriterLock, error) {
	dir, err := historyDirectory(outDir, environment)
	if err != nil {
		return nil, err
	}
	if err = ensurePrivateDirectory(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	if err = ensurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "writer.lock")
	if i, e := os.Lstat(path); e == nil && (!i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("invalid writer lock file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another monitor owns this environment output")
	}
	return &WriterLock{file: f}, nil
}
func (l *WriterLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func snapshot(cfg Config, inv Inventory, now time.Time, col Collection, profile string) Snapshot {
	var id [12]byte
	_, _ = rand.Read(id[:])
	sort.SliceStable(col.Results, func(i, j int) bool {
		return col.Results[i].Service+"/"+col.Results[i].CheckID < col.Results[j].Service+"/"+col.Results[j].CheckID
	})
	overall, coverage := Summarize(col.Results)
	return Snapshot{SchemaVersion: SchemaVersion, RunID: hex.EncodeToString(id[:]), Environment: cfg.Environment, Stack: cfg.Stack, SourceFingerprint: inv.SourceFingerprint, Profile: profile, StartedAt: now, FinishedAt: time.Now().UTC(), Overall: overall, Coverage: coverage, Results: col.Results, Samples: col.Samples}
}

func syntheticSkipped(now time.Time) Collection {
	return Collection{Results: []Result{{Service: "synthetic", CheckID: "synthetic/disabled", Layer: "functional", Required: true, Status: NotRun, ObservedAt: now, Reason: "合成探測尚未啟用；使用專用身分後明確指定 --enable-synthetic。"}}}
}

func Check(ctx context.Context, cfg Config, inv Inventory, rt Runtime, runner CommandRunner, synthetic bool) (Snapshot, error) {
	lock, err := AcquireWriterLock(rt.OutDir, cfg.Environment)
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Close()
	now := time.Now().UTC()
	col := CollectReadOnly(ctx, cfg, inv, rt, runner, now)
	if synthetic {
		col = Merge(col, CollectSynthetic(ctx, cfg, rt, runner, now))
	} else {
		col = Merge(col, syntheticSkipped(now))
	}
	s := snapshot(cfg, inv, now, col, "check")
	s.Results = append(s.Results, ConfigurationCoverage(cfg, inv, now).Results...)
	prior, err := LoadHistory(rt.OutDir, cfg.Environment, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return s, err
	}
	s = EnrichHistory(s, prior)
	if err = StoreSnapshot(rt.OutDir, s); err != nil {
		return s, err
	}
	return s, RetainHistory(rt.OutDir, cfg.Environment, cfg.RetentionDays, now)
}

// Watch samples metrics every 30 seconds, active service/identity state every
// minute, full certificate discovery every 15 minutes, and synthetics every 5
// minutes. It reuses old results with their original observation time.
func Watch(ctx context.Context, cfg Config, inv Inventory, rt Runtime, runner CommandRunner, synthetic bool, onSample func(Snapshot), onReport func(error)) error {
	ctx, _ = collectorBudget(ctx, cfg)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	deps := watchDependencies{ticks: ticker.C, now: func() time.Time { return time.Now().UTC() }, collect: map[string]func(context.Context, time.Time) Collection{
		"service": func(c context.Context, n time.Time) Collection {
			return collectWatchParts(
				func() Collection { return CollectKubernetes(c, cfg, inv, rt, runner, n) },
				func() Collection { return CollectHTTP(c, cfg, inv, rt, n) },
				func() Collection { return CollectTLS(c, cfg, rt, n) },
				func() Collection { return CollectTokens(c, cfg, rt, n) },
			)
		},
		"certificate": func(c context.Context, n time.Time) Collection { return CollectCertificates(c, cfg, rt, runner, n) },
		"synthetic": func(c context.Context, n time.Time) Collection {
			if synthetic {
				return CollectSynthetic(c, cfg, rt, runner, n)
			}
			return syntheticSkipped(n)
		},
		"metrics": func(c context.Context, n time.Time) Collection {
			return collectWatchParts(func() Collection { return CollectDatabases(c, cfg, rt, runner, n) }, func() Collection { return CollectMetrics(c, cfg, rt, n) })
		},
	}, report: func(c context.Context, from, to time.Time, name string) error {
		_, _, err := GenerateReport(c, cfg, rt, runner, from, to, name)
		return err
	}}
	return watchWithDependencies(ctx, cfg, inv, rt, deps, onSample, onReport)
}

func collectWatchParts(parts ...func() Collection) Collection {
	results := make([]Collection, len(parts))
	var wg sync.WaitGroup
	for i, fn := range parts {
		wg.Add(1)
		go func(i int, fn func() Collection) { defer wg.Done(); results[i] = fn() }(i, fn)
	}
	wg.Wait()
	return Merge(results...)
}

// This private seam lets engine tests drive time and substitute collectors.
type watchDependencies struct {
	ticks   <-chan time.Time
	now     func() time.Time
	collect map[string]func(context.Context, time.Time) Collection
	report  func(context.Context, time.Time, time.Time, string) error
}
type watchJob struct {
	name               string
	interval, deadline time.Duration
	lastStarted        time.Time
	running            bool
	collection         Collection
	pendingSamples     []Sample
}
type watchEvent struct {
	name       string
	started    time.Time
	collection Collection
	reportDay  string
	err        error
}
type reportState struct {
	Day       string    `json:"day"`
	Success   bool      `json:"success"`
	UpdatedAt time.Time `json:"updated_at"`
	Message   string    `json:"message,omitempty"`
}

func watchWithDependencies(ctx context.Context, cfg Config, inv Inventory, rt Runtime, deps watchDependencies, onSample func(Snapshot), onReport func(error)) error {
	lock, err := AcquireWriterLock(rt.OutDir, cfg.Environment)
	if err != nil {
		return err
	}
	defer lock.Close()
	if ctx.Err() != nil {
		return nil
	}
	if deps.now == nil || deps.ticks == nil {
		return errors.New("watch clock is unavailable")
	}
	zone := cfg.Timezone
	if zone == "" {
		zone = "Asia/Taipei"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return errors.New("invalid report timezone")
	}
	daily := cfg.DailyAt
	if daily == "" {
		daily = "08:00"
	}
	clock, err := time.Parse("15:04", daily)
	if err != nil {
		return errors.New("invalid daily report time")
	}
	now := deps.now().UTC()
	loaded, err := LoadHistory(rt.OutDir, cfg.Environment, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return err
	}
	prior := compactWatchHistory(loaded, now)
	loaded = nil
	stateDir, _ := historyDirectory(rt.OutDir, cfg.Environment)
	debouncePath := filepath.Join(stateDir, "latest-debounce.json")
	debounce, err := loadConnectionDebounce(debouncePath, cfg.Environment, inv.SourceFingerprint)
	if err != nil {
		return err
	}
	statePath := filepath.Join(stateDir, "latest-report.json")
	var saved reportState
	if data, e := os.ReadFile(statePath); e == nil {
		_ = json.Unmarshal(data, &saved)
	}
	reported := ""
	if saved.Success {
		reported = saved.Day
	}
	pendingReportError := ""
	if saved.Day != "" && !saved.Success {
		pendingReportError = "前次 PDF 日報產生失敗；本次啟動重新嘗試。"
	}
	jobs := []*watchJob{{name: "service", interval: time.Minute, deadline: 55 * time.Second}, {name: "certificate", interval: 15 * time.Minute, deadline: 5 * time.Minute}, {name: "synthetic", interval: 5 * time.Minute, deadline: 90 * time.Second}, {name: "metrics", interval: 30 * time.Second, deadline: 25 * time.Second}}
	jobByName := map[string]*watchJob{}
	for _, j := range jobs {
		jobByName[j.name] = j
	}
	events := make(chan watchEvent, 8)
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	launch := func(at time.Time) {
		for _, j := range jobs {
			// Timer creation and initialization can be a few milliseconds apart.
			// A one-second tolerance prevents nominal 30s ticks from becoming 60s
			// sampling merely because their timestamps differ by clock jitter.
			if j.running || (!j.lastStarted.IsZero() && at.Sub(j.lastStarted) < j.interval-time.Second) {
				continue
			}
			j.running = true
			j.lastStarted = at
			wg.Add(1)
			go func(job *watchJob) {
				defer wg.Done()
				c, stop := context.WithTimeout(runCtx, job.deadline)
				defer stop()
				col := Collection{}
				if fn := deps.collect[job.name]; fn != nil {
					col = fn(c, at)
				}
				select {
				case events <- watchEvent{name: job.name, started: at, collection: col}:
				case <-runCtx.Done():
				}
			}(j)
		}
	}
	reportRunning := false
	launchReport := func(at time.Time) {
		local := at.In(loc)
		day := local.Format("2006-01-02")
		due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if local.Before(due) || reported == day || reportRunning {
			return
		}
		reported = day
		reportRunning = true
		end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, stop := context.WithTimeout(runCtx, 2*time.Minute)
			defer stop()
			e := errors.New("PDF renderer is unavailable")
			if deps.report != nil {
				e = deps.report(c, end.AddDate(0, 0, -1), end, "daily-"+day)
			}
			select {
			case events <- watchEvent{name: "report", reportDay: day, err: e}:
			case <-runCtx.Done():
			}
		}()
	}
	lastRetention := ""
	launch(now)
	launchReport(now)
	for {
		select {
		case <-ctx.Done():
			return nil
		case at, ok := <-deps.ticks:
			if !ok {
				return nil
			}
			launch(at.UTC())
			launchReport(at.UTC())
		case ev := <-events:
			if ctx.Err() != nil {
				return nil
			}
			if ev.name == "report" {
				reportRunning = false
				state := reportState{Day: ev.reportDay, Success: ev.err == nil, UpdatedAt: deps.now().UTC()}
				if ev.err != nil {
					state.Message = "PDF 報告產生失敗；採樣繼續。"
					pendingReportError = state.Message
				}
				encoded, _ := json.Marshal(state)
				if e := atomicPrivateWrite(statePath, append(encoded, '\n')); e != nil {
					pendingReportError = "PDF 報告狀態無法保存；採樣繼續。"
					if ev.err == nil {
						ev.err = e
					}
				}
				if onReport != nil {
					onReport(ev.err)
				}
				continue
			}
			job := jobByName[ev.name]
			job.running = false
			job.collection = ev.collection
			job.pendingSamples = append(job.pendingSamples, ev.collection.Samples...)
			if ev.name != "metrics" {
				continue
			}
			combined := Collection{}
			for _, j := range jobs {
				part := j.collection
				if len(part.Results) == 0 {
					part.Results = []Result{{Service: "monitor", CheckID: "collector/" + j.name, Layer: "observation", Required: true, Status: Unknown, ObservedAt: ev.started, Reason: "此類採集尚未完成或沒有可用結果。"}}
				}
				part.Samples = nil
				combined = Merge(combined, part)
				combined.Samples = append(combined.Samples, j.pendingSamples...)
				j.pendingSamples = nil
			}
			combined = Merge(combined, ConfigurationCoverage(cfg, inv, ev.started))
			s := snapshot(cfg, inv, ev.started, combined, "watch")
			s = EnrichHistory(s, prior)
			debounceChanged := debounce.apply(&s)
			s.ReportError = pendingReportError
			if e := StoreSnapshot(rt.OutDir, s); e != nil {
				return e
			}
			if debounceChanged {
				if e := debounce.save(debouncePath); e != nil {
					return e
				}
			}
			pendingReportError = ""
			prior = compactWatchHistory(append(prior, s), ev.started)
			day := ev.started.UTC().Format("2006-01-02")
			if day != lastRetention {
				days := cfg.RetentionDays
				if days == 0 {
					days = 30
				}
				if e := RetainHistory(rt.OutDir, cfg.Environment, days, ev.started); e != nil {
					return e
				}
				lastRetention = day
			}
			if onSample != nil {
				onSample(s)
			}
		}
	}
}

// Forecasts need seven days of capacity gauges; rates need only the most recent
// raw counters. Repeated results, derived rates and unrelated gauges stay on disk.
func compactWatchHistory(all []Snapshot, now time.Time) []Snapshot {
	var out []Snapshot
	for _, s := range all {
		var samples []Sample
		for _, v := range s.Samples {
			keep := v.Kind == "counter" && !v.ObservedAt.Before(now.Add(-90*time.Second))
			capacity := v.Kind == "gauge" && (strings.HasSuffix(v.Name, "_used_bytes") || strings.HasSuffix(v.Name, "_capacity_bytes") || strings.HasSuffix(v.Name, "_limit_bytes"))
			if keep || (capacity && !v.ObservedAt.Before(now.Add(-7*24*time.Hour))) {
				samples = append(samples, v)
			}
		}
		if len(samples) > 0 {
			out = append(out, Snapshot{StartedAt: s.StartedAt, Samples: samples})
		}
	}
	return out
}

func GenerateReport(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, from, to time.Time, name string) (string, string, error) {
	history, err := LoadHistory(rt.OutDir, cfg.Environment, from, to)
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = fmt.Sprintf("report-%s", to.UTC().Format("20060102T150405Z"))
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return "", "", errors.New("invalid report name")
	}
	return RenderReport(ctx, BuildReport(history, cfg.Environment, from, to, cfg.Timezone), filepath.Join(rt.OutDir, "reports", cfg.Environment, name), cfg.Chromium, runner)
}

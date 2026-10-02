package cloudmonitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reportRunner struct {
	args []string
	fail bool
}

func (r *reportRunner) Run(_ context.Context, _ string, args []string, _ []byte, _ []string) ([]byte, int, error) {
	r.args = args
	if r.fail {
		return []byte("password=raw-sensitive-error"), 1, errors.New("raw-sensitive-error")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--print-to-pdf=") {
			return nil, 0, os.WriteFile(strings.TrimPrefix(a, "--print-to-pdf="), []byte("%PDF-1.7\nfixture"), 0600)
		}
	}
	return nil, 0, nil
}

func TestReportOnlySelectedHistory(t *testing.T) {
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	s := historyFixture(at)
	foreign := s
	foreign.Environment = "prod"
	foreign.Results = append([]Result(nil), s.Results...)
	foreign.Results[0].Status = Fail
	next := historyFixture(at.Add(30 * time.Second))
	next.Results[0].Status = Warn
	next.Results[0].Reason = "latency"
	next.Samples = []Sample{{Service: "pg", Name: "disk_used_bytes", Kind: "gauge", Unit: "bytes", Value: 20, ObservedAt: next.StartedAt}}
	s.Samples = []Sample{{Service: "pg", Name: "disk_used_bytes", Kind: "gauge", Unit: "bytes", Value: 10, ObservedAt: s.StartedAt}}
	r := BuildReport([]Snapshot{foreign, next, s}, "dev", at, at.Add(time.Minute), "Asia/Taipei")
	if r.Snapshots != 2 || r.Overall != Warn || r.HistoryCoverage != 100 || len(r.Charts) != 1 || r.Charts[0].Peak != 20 || len(r.Services) != 1 {
		t.Fatalf("bad report %#v", r)
	}
	if r.Services[0].Liveness != Warn || r.Services[0].Function != NotRun {
		t.Fatalf("bad layers %#v", r.Services)
	}
	if len(r.Events) != 1 || r.Events[0].Status != Warn {
		t.Fatal("events lost")
	}
}

func TestReportEmptyAndUnknown(t *testing.T) {
	at := time.Now().UTC()
	r := BuildReport(nil, "dev", at.Add(-24*time.Hour), at, "Asia/Taipei")
	if r.Overall != Unknown || r.Coverage.Percent != 0 || len(r.Notes) == 0 {
		t.Fatal("empty report passed")
	}
	s := historyFixture(at)
	s.Results[0].Status = Unknown
	r = BuildReport([]Snapshot{s}, "dev", at, at.Add(time.Second), "invalid timezone")
	if r.Timezone != "UTC" || r.Overall != Unknown || r.Coverage.Percent != 0 {
		t.Fatal("unknown report passed")
	}
}

func TestRenderReportLocalAndPrivate(t *testing.T) {
	at := time.Now().UTC()
	s := historyFixture(at)
	expires := at.Add(48 * time.Hour)
	s.Results = append(s.Results, Result{Service: "api", CheckID: "tls", Layer: "certificate", Required: true, Status: Warn, ObservedAt: at, ExpiresAt: &expires, Reason: "<script>alert('bad')</script> Bearer sensitive-secret", Fingerprint: "sha256:123"})
	r := BuildReport([]Snapshot{s}, "dev", at, at.Add(time.Second), "Asia/Taipei")
	runner := &reportRunner{}
	h, p, err := RenderReport(context.Background(), r, filepath.Join(t.TempDir(), "reports", "dev report"), "chromium", runner)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(h)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sensitive-secret") || strings.Contains(string(b), "<script>alert") {
		t.Fatal("raw unsafe content rendered")
	}
	if !strings.Contains(string(b), "48") && !strings.Contains(string(b), "2.0 天") {
		t.Fatal("expiry remaining missing")
	}
	for _, path := range []string{h, p} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("output permissions %v %v", st, err)
		}
	}
	joined := strings.Join(runner.args, " ")
	if strings.Contains(joined, "--no-sandbox") || !strings.Contains(joined, "file:") || !strings.Contains(joined, "--print-to-pdf=") {
		t.Fatal("unsafe renderer args")
	}
	if strings.Contains(string(b), "https://") || strings.Contains(string(b), "http://") {
		t.Fatal("remote report asset")
	}
}

func TestRenderFailurePreservesHTMLAndOldPDF(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "report")
	if err := os.WriteFile(prefix+".pdf", []byte("old pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	r := BuildReport(nil, "dev", time.Now().Add(-time.Hour), time.Now(), "Asia/Taipei")
	h, p, err := RenderReport(context.Background(), r, prefix, "chromium", &reportRunner{fail: true})
	if err == nil || h == "" || p != "" {
		t.Fatalf("failure result %s %s %v", h, p, err)
	}
	if strings.Contains(err.Error(), "raw-sensitive") {
		t.Fatal("renderer error leaked")
	}
	if _, err = os.Stat(h); err != nil {
		t.Fatal("HTML lost")
	}
	b, _ := os.ReadFile(prefix + ".pdf")
	if string(b) != "old pdf" {
		t.Fatal("old pdf overwritten")
	}
}

func TestChartsBreakAcrossGaps(t *testing.T) {
	at := time.Now().UTC()
	var ss []Snapshot
	for i, offset := range []time.Duration{0, 30 * time.Second, 10 * time.Minute, 10*time.Minute + 30*time.Second} {
		s := historyFixture(at.Add(offset))
		s.Samples = []Sample{{Service: "redis", Name: "memory_used_bytes", Kind: "gauge", Value: float64(i), ObservedAt: s.StartedAt}}
		ss = append(ss, s)
	}
	charts := buildCharts(ss)
	if len(charts) != 1 || strings.Count(string(charts[0].SVG), `stroke="#126e82"`) != 2 {
		t.Fatal("chart joined across missing data")
	}
}

func TestChartsCapAndPreferCapacity(t *testing.T) {
	at := time.Now().UTC()
	first := historyFixture(at)
	second := historyFixture(at.Add(30 * time.Second))
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("redis_command_cmd%d_calls_per_second", i)
		first.Samples = append(first.Samples, Sample{Service: "redis", Name: name, Kind: "gauge", Value: 1, ObservedAt: at})
		second.Samples = append(second.Samples, Sample{Service: "redis", Name: name, Kind: "gauge", Value: 2, ObservedAt: second.StartedAt})
	}
	for i, s := range []*Snapshot{&first, &second} {
		s.Samples = append(s.Samples, Sample{Service: "postgres", Name: "postgres_volume_used_bytes", Kind: "gauge", Value: float64(100 + i), ObservedAt: s.StartedAt}, Sample{Service: "postgres", Name: "container_restarts_per_second", Kind: "gauge", Value: 0, ObservedAt: s.StartedAt})
	}
	r := BuildReport([]Snapshot{first, second}, "dev", at, at.Add(time.Minute), "Asia/Taipei")
	if len(r.Charts) != 16 || r.Charts[0].Title != "postgres / postgres_volume_used_bytes" {
		t.Fatal("chart cap/priority failed")
	}
	for _, c := range r.Charts {
		if strings.Contains(c.Title, "restarts") {
			t.Fatal("all-zero chart retained")
		}
	}
	note := false
	for _, n := range r.Notes {
		if strings.Contains(n, "JSONL") {
			note = true
		}
	}
	if !note {
		t.Fatal("omitted metric disclosure missing")
	}
}

func TestReportCategoryFreshness(t *testing.T) {
	at := time.Now().UTC()
	s := historyFixture(at)
	s.Results = []Result{
		{Service: "api", CheckID: "http/health", Layer: "liveness", Required: true, Status: Pass, ObservedAt: at.Add(-3 * time.Minute)},
		{Service: "api", CheckID: "tls/https", Layer: "credential", Required: true, Status: Pass, ObservedAt: at.Add(-3 * time.Minute)},
		{Service: "api", CheckID: "token/access", Layer: "credential", Required: true, Status: Pass, ObservedAt: at.Add(-3 * time.Minute)},
		{Service: "api", CheckID: "k8s/api", Layer: "liveness", Required: true, Status: Pass, ObservedAt: at.Add(-3 * time.Minute)},
		{Service: "turn", CheckID: "pki/recent-sweep", Layer: "readiness", Required: true, Status: Pass, ObservedAt: at.Add(-3 * time.Minute)},
		{Service: "postgres", CheckID: "postgres/statistics", Layer: "capacity", Required: true, Status: Pass, ObservedAt: at.Add(-100 * time.Second)},
		{Service: "prometheus", CheckID: "metric/p95", Layer: "performance", Required: true, Status: Pass, ObservedAt: at.Add(-100 * time.Second)},
		{Service: "pki", CheckID: "certificate-inventory/leaf", Layer: "credential", Required: true, Status: Pass, ObservedAt: at.Add(-10 * time.Minute)},
		{Service: "test", CheckID: "synthetic/mqtt", Layer: "functional", Required: true, Status: Pass, ObservedAt: at.Add(-8 * time.Minute)},
	}
	r := BuildReport([]Snapshot{s}, "dev", at, at.Add(time.Second), "Asia/Taipei")
	if r.Overall != Unknown {
		t.Fatal("stale report passed")
	}
	statuses := map[string]Result{}
	for _, g := range r.Groups {
		for _, v := range g.Results {
			statuses[v.CheckID] = v
		}
	}
	for _, v := range s.Results {
		got := statuses[v.CheckID]
		fresh := strings.HasPrefix(v.CheckID, "certificate-inventory") || strings.HasPrefix(v.CheckID, "synthetic/")
		if fresh && got.Status != Pass {
			t.Fatalf("fresh category stale: %s", v.CheckID)
		}
		if !fresh && (got.Status != Unknown || got.ObservedStatus != Pass || !strings.Contains(got.Reason, "歷史紀錄")) {
			t.Fatalf("stale evidence lost: %+v", got)
		}
	}
}

func TestSparseHistoryCannotReportHealthyPeriod(t *testing.T) {
	at := time.Now().UTC()
	s := historyFixture(at.Add(9*time.Minute + 30*time.Second))
	r := BuildReport([]Snapshot{s}, "dev", at, at.Add(10*time.Minute), "Asia/Taipei")
	if r.Overall != Unknown || r.Coverage.Unknown == 0 {
		t.Fatal("sparse period reported healthy")
	}
	s.Results[0].Status = Fail
	r = BuildReport([]Snapshot{s}, "dev", at, at.Add(10*time.Minute), "Asia/Taipei")
	if r.Overall != Fail {
		t.Fatal("coverage gap hid known current failure")
	}
	s = historyFixture(at)
	s.Profile = "check"
	s.FinishedAt = at.Add(75 * time.Second)
	s.Results[0].ObservedAt = s.FinishedAt.Add(-time.Second)
	r = BuildReport([]Snapshot{s}, "dev", at, s.FinishedAt, "Asia/Taipei")
	if r.HistoryCoverage != 100 || r.Overall != Pass {
		t.Fatal("single check collection window mistaken for missing watch ticks")
	}
}

func TestReportLatestInventoryRetiresOldChecks(t *testing.T) {
	at := time.Now().UTC()
	old := historyFixture(at)
	old.SourceFingerprint = "old"
	old.Results[0].CheckID = "http/retired"
	old.Results[0].Status = Fail
	current := historyFixture(at.Add(30 * time.Second))
	current.SourceFingerprint = "current"
	current.Results[0].CheckID = "http/current"
	r := BuildReport([]Snapshot{old, current}, "dev", at, at.Add(time.Minute), "Asia/Taipei")
	if r.Overall != Pass || r.SourceFingerprint != "current" {
		t.Fatal("retired checks still affect current health")
	}
	for _, g := range r.Groups {
		for _, v := range g.Results {
			if v.CheckID == "http/retired" {
				t.Fatal("retired check remains current")
			}
		}
	}
	if len(r.Events) != 1 || r.Events[0].CheckID != "http/retired" {
		t.Fatal("historical event disappeared")
	}
	note := false
	for _, n := range r.Notes {
		if strings.Contains(n, "設定來源已變更") {
			note = true
		}
	}
	if !note {
		t.Fatal("configuration change undisclosed")
	}
}

func TestReportPaginationPreservesEveryRow(t *testing.T) {
	var rows []Result
	for i := 0; i < 23; i++ {
		rows = append(rows, Result{Service: "service", CheckID: fmt.Sprintf("check%d", i), Reason: strings.Repeat("中文證據", 40), Source: "source", Status: Unknown})
	}
	pages := resultPages(rows)
	index := 0
	for _, page := range pages {
		if len(page) > 5 {
			t.Fatal("overlarge detail block")
		}
		for _, row := range page {
			if row.CheckID != rows[index].CheckID || row.Reason != rows[index].Reason {
				t.Fatal("pagination altered evidence")
			}
			index++
		}
	}
	if index != len(rows) {
		t.Fatal("pagination dropped rows")
	}
	services := make([]ServiceReport, 35)
	for i := range services {
		services[i].Name = strings.Repeat("service-", 10)
	}
	count := 0
	for _, p := range servicePages(services) {
		if len(p) > 16 {
			t.Fatal("overlarge service block")
		}
		count += len(p)
	}
	if count != len(services) {
		t.Fatal("service pagination lost rows")
	}
	charts := make([]ReportChart, 16)
	count = 0
	for _, p := range chartPages(charts) {
		if len(p) > 4 {
			t.Fatal("overlarge chart block")
		}
		count += len(p)
	}
	if count != 16 {
		t.Fatal("chart pagination dropped plots")
	}
}

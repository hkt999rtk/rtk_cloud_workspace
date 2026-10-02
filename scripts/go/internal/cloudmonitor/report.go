package cloudmonitor

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed templates/report.html
var reportTemplate string

type Report struct {
	SchemaVersion     int             `json:"schema_version"`
	Environment       string          `json:"environment"`
	From              time.Time       `json:"from"`
	To                time.Time       `json:"to"`
	GeneratedAt       time.Time       `json:"generated_at"`
	Timezone          string          `json:"timezone"`
	Version           string          `json:"version"`
	Stack             string          `json:"stack"`
	Profile           string          `json:"profile"`
	SourceFingerprint string          `json:"source_fingerprint"`
	Overall           Status          `json:"overall"`
	Coverage          Coverage        `json:"coverage"`
	HistoryCoverage   float64         `json:"history_coverage"`
	Snapshots         int             `json:"snapshots"`
	Counts            map[Status]int  `json:"counts"`
	Services          []ServiceReport `json:"services"`
	Groups            []ReportGroup   `json:"groups"`
	Charts            []ReportChart   `json:"charts"`
	Events            []ReportEvent   `json:"events"`
	Notes             []string        `json:"notes"`
}

type ServiceReport struct {
	Name                          string
	Overall                       Status
	Liveness, Readiness, Function Status
	Latest                        time.Time
}

type ReportGroup struct {
	Title   string
	Results []Result
}
type ReportChart struct {
	Title, Unit string
	SVG         template.HTML
	Samples     int
	Peak        float64
}
type ReportEvent struct {
	Time             time.Time
	Service, CheckID string
	Status           Status
	Reason           string
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [^-]*(?:PRIVATE KEY|CERTIFICATE)[^-]*-----.*?-----END [^-]+-----`),
	regexp.MustCompile(`(?i)\bBearer\s+[a-zA-Z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`),
	regexp.MustCompile(`(?i)(?:password|passwd|access_token|refresh_token|client_secret|authorization|token|secret)\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s&,;]+)`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`),
}

// ScrubReportText is a defense at the persistence boundary. Collectors should
// still return curated diagnostics rather than raw response or credential text.
func ScrubReportText(s string) string {
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[已隱藏敏感資料]")
	}
	return s
}

func safeSnapshot(s Snapshot) Snapshot {
	s.Results = append([]Result(nil), s.Results...)
	s.Samples = append([]Sample(nil), s.Samples...)
	s.ReportError = ScrubReportText(s.ReportError)
	s.SourceFingerprint = ScrubReportText(s.SourceFingerprint)
	for i := range s.Results {
		r := &s.Results[i]
		r.Service = ScrubReportText(r.Service)
		r.CheckID = ScrubReportText(r.CheckID)
		r.Layer = ScrubReportText(r.Layer)
		r.Source = ScrubReportText(r.Source)
		r.Reason = ScrubReportText(r.Reason)
		r.Recommendation = ScrubReportText(r.Recommendation)
		r.Threshold = ScrubReportText(r.Threshold)
		r.Fingerprint = ScrubReportText(r.Fingerprint)
		r.Unit = ScrubReportText(r.Unit)
	}
	for i := range s.Samples {
		v := &s.Samples[i]
		v.Identity = ScrubReportText(v.Identity)
		v.Service = ScrubReportText(v.Service)
		v.Name = ScrubReportText(v.Name)
	}
	return s
}

// BuildReport uses saved observations only. It never queries a live service.
func BuildReport(snapshots []Snapshot, env string, from, to time.Time, timezone string) Report {
	if timezone == "" {
		timezone = "Asia/Taipei"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "UTC"
	}
	r := Report{SchemaVersion: SchemaVersion, Environment: env, From: from, To: to, GeneratedAt: time.Now().UTC(), Timezone: timezone, Version: fmt.Sprintf("schema %d", SchemaVersion), Overall: Unknown, Counts: map[Status]int{}}
	var selected []Snapshot
	for _, s := range snapshots {
		if s.Environment == env && (from.IsZero() || !s.StartedAt.Before(from)) && (to.IsZero() || s.StartedAt.Before(to)) {
			selected = append(selected, safeSnapshot(s))
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].StartedAt.Before(selected[j].StartedAt) })
	r.Snapshots = len(selected)
	if len(selected) == 0 {
		r.Notes = append(r.Notes, "報告期間沒有採集資料；無法判定服務正常。")
		gap := reportCoverageResult(r)
		r.Overall, r.Coverage = Summarize([]Result{gap})
		r.Counts[Unknown] = 1
		r.Groups = []ReportGroup{{Title: "觀測與檢查覆蓋", Results: []Result{gap}}}
		return r
	}
	if r.From.IsZero() {
		r.From = selected[0].StartedAt
	}
	if r.To.IsZero() {
		r.To = selected[len(selected)-1].FinishedAt
		if !r.To.After(r.From) {
			r.To = selected[len(selected)-1].StartedAt.Add(time.Nanosecond)
		}
	}
	latest := map[string]Result{}
	previous := map[string]Status{}
	var coverageSamples []time.Time
	for _, s := range selected {
		if s.SourceFingerprint != "" && r.SourceFingerprint != "" && s.SourceFingerprint != r.SourceFingerprint {
			latest = map[string]Result{}
			previous = map[string]Status{}
			r.Notes = append(r.Notes, "報告期間設定來源已變更；目前健康狀態以最新設定為準，舊設定事件保留於時間線。")
		}
		if s.Stack != "" {
			r.Stack = s.Stack
		}
		if s.Profile != "" {
			r.Profile = s.Profile
		}
		if s.SourceFingerprint != "" {
			r.SourceFingerprint = s.SourceFingerprint
		}
		if s.ReportError != "" {
			r.Notes = append(r.Notes, "報告產生錯誤："+s.ReportError)
		}
		if len(s.Results) > 0 && s.Results[0].CheckID != "history.truncated" {
			coverageSamples = append(coverageSamples, s.StartedAt)
		}
		for _, v := range s.Results {
			key := v.Service + "\x00" + v.CheckID
			old, ok := latest[key]
			if !ok || !v.ObservedAt.Before(old.ObservedAt) {
				latest[key] = v
			}
			prev, seen := previous[key]
			if (v.Status != Pass && v.Status != NotApplicable && (!seen || prev != v.Status)) || (seen && prev != Pass && v.Status == Pass) {
				r.Events = append(r.Events, ReportEvent{Time: v.ObservedAt, Service: v.Service, CheckID: v.CheckID, Status: v.Status, Reason: v.Reason})
			}
			previous[key] = v.Status
			if v.CheckID == "history.truncated" {
				r.Notes = append(r.Notes, v.Reason)
			}
		}
	}
	span := r.To.Sub(r.From)
	if span > 0 {
		var covered time.Duration
		var endCovered time.Time
		for _, at := range coverageSamples {
			start := at
			end := at.Add(30 * time.Second)
			if start.Before(r.From) {
				start = r.From
			}
			if end.After(r.To) {
				end = r.To
			}
			if start.Before(endCovered) {
				start = endCovered
			}
			if end.After(start) {
				covered += end.Sub(start)
				endCovered = end
			}
		}
		r.HistoryCoverage = math.Min(100, 100*float64(covered)/float64(span))
	}
	// A requested single-check report describes its collection window. It does
	// not claim continuous sampling outside that window or invent missing ticks.
	if len(selected) == 1 && selected[0].Profile == "check" && !r.From.Before(selected[0].StartedAt.Add(-2*time.Second)) && !r.To.After(selected[0].FinishedAt.Add(2*time.Second)) {
		r.HistoryCoverage = 100
		r.Notes = append(r.Notes, "這份報告為單次巡檢；採樣覆蓋率限於本輪採集窗口，不表示持續可用率。")
	}
	if r.HistoryCoverage < 90 {
		r.Notes = append(r.Notes, "期間採樣覆蓋率不足 90%；趨勢與可用率只代表已觀測時段。")
		gap := reportCoverageResult(r)
		latest[gap.Service+"\x00"+gap.CheckID] = gap
	}
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	groups := []ReportGroup{{Title: "服務、依賴與功能"}, {Title: "Certificate、CA、CRL 與 Token"}, {Title: "資料容量、吞吐量與效能"}, {Title: "觀測與檢查覆蓋"}}
	serviceResults := map[string][]Result{}
	var all []Result
	for _, key := range keys {
		v := latest[key]
		maxAge := reportResultMaxAge(v)
		if v.Status != NotApplicable && v.Status != NotRun && v.CheckID != "history.truncated" && (v.ObservedAt.IsZero() || r.To.Sub(v.ObservedAt) > maxAge) {
			if v.ObservedStatus == "" {
				v.ObservedStatus = v.Status
			}
			v.Status = Unknown
			v.Reason = fmt.Sprintf("最後觀測已過期（允許 %s）；原始狀態及連續次數為歷史紀錄。%s", maxAge, v.Reason)
		}
		all = append(all, v)
		r.Counts[v.Status]++
		serviceResults[v.Service] = append(serviceResults[v.Service], v)
		idx := 0
		layer := strings.ToLower(v.Layer + " " + v.CheckID)
		if strings.Contains(layer, "cert") || strings.Contains(layer, "token") || strings.Contains(layer, "tls") || strings.Contains(layer, "crl") || strings.Contains(layer, "pki") {
			idx = 1
		} else if strings.Contains(layer, "capacity") || strings.Contains(layer, "throughput") || strings.Contains(layer, "metric") || strings.Contains(layer, "database") || strings.Contains(layer, "performance") {
			idx = 2
		} else if strings.Contains(layer, "coverage") || strings.Contains(layer, "observation") {
			idx = 3
		}
		groups[idx].Results = append(groups[idx].Results, v)
	}
	r.Overall, r.Coverage = Summarize(all)
	for name, results := range serviceResults {
		sr := ServiceReport{Name: name, Liveness: NotRun, Readiness: NotRun, Function: NotRun}
		sr.Overall, _ = Summarize(results)
		layers := map[string][]Result{}
		for _, v := range results {
			if v.ObservedAt.After(sr.Latest) {
				sr.Latest = v.ObservedAt
			}
			layers[v.Layer] = append(layers[v.Layer], v)
		}
		for layer, rs := range layers {
			status, _ := Summarize(withRequired(rs))
			switch layer {
			case "liveness":
				sr.Liveness = status
			case "readiness", "dependency":
				sr.Readiness = worseStatus(sr.Readiness, status)
			case "function", "functional", "synthetic":
				sr.Function = worseStatus(sr.Function, status)
			}
		}
		r.Services = append(r.Services, sr)
	}
	sort.Slice(r.Services, func(i, j int) bool { return r.Services[i].Name < r.Services[j].Name })
	for _, g := range groups {
		if len(g.Results) > 0 {
			r.Groups = append(r.Groups, g)
		}
	}
	var omitted int
	r.Charts, omitted = buildChartsLimited(selected)
	if omitted > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("主報告保留最多 16 條容量、吞吐量與延遲趨勢；另有 %d 條數值序列保存在 JSONL 歷史檔案。", omitted))
	}
	if r.Profile != "synthetic" {
		r.Notes = append(r.Notes, "本報告的健康判定依所選巡檢範圍；未開啟的功能測試不能證明實際功能正常。")
	}
	r.Notes = uniqueStrings(r.Notes)
	return r
}

func reportCoverageResult(r Report) Result {
	value := r.HistoryCoverage
	return Result{Service: "monitor", CheckID: "history/coverage", Layer: "observation", Required: true, Status: Unknown, ObservedAt: r.To, Value: &value, Unit: "percent", Threshold: "≥90% sampling coverage", Source: "saved history", Reason: "報告期間採樣覆蓋率不足，不能據此判定整個期間正常。"}
}

func reportResultMaxAge(r Result) time.Duration {
	id := strings.ToLower(r.CheckID)
	if strings.HasPrefix(id, "certificate-inventory") || strings.HasPrefix(id, "certificate-coverage") {
		return 20 * time.Minute
	}
	if strings.HasPrefix(id, "synthetic/") || r.Layer == "functional" || r.Layer == "function" || r.Layer == "synthetic" {
		return 10 * time.Minute
	}
	if strings.HasPrefix(id, "postgres/") || strings.HasPrefix(id, "redis/") || strings.HasPrefix(id, "metric/") || r.Layer == "performance" || r.Layer == "capacity" || r.Layer == "throughput" {
		return 90 * time.Second
	}
	return 120 * time.Second
}

func withRequired(rs []Result) []Result {
	out := append([]Result(nil), rs...)
	for i := range out {
		out[i].Required = true
	}
	return out
}
func worseStatus(a, b Status) Status {
	rank := map[Status]int{NotApplicable: 0, Pass: 1, NotRun: 2, Warn: 3, Unknown: 4, Fail: 5}
	if a == NotRun {
		return b
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
func uniqueStrings(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func buildCharts(snapshots []Snapshot) []ReportChart {
	charts, _ := buildChartsLimited(snapshots)
	return charts
}

func chartPriority(name string) int {
	if strings.HasSuffix(name, "_used_bytes") || strings.Contains(name, "working_set") || strings.Contains(name, "usage_percent") || strings.Contains(name, "cpu") {
		return 0
	}
	if strings.Contains(name, "p95") || strings.Contains(name, "p99") || strings.Contains(name, "quantile") || strings.Contains(name, "commands_per_second") || strings.Contains(name, "transactions_committed_per_second") {
		return 1
	}
	if strings.Contains(name, "days_to_warning") || strings.Contains(name, "postgres_probe_latency") || strings.Contains(name, "redis_probe_latency") || strings.Contains(name, "connections") || strings.Contains(name, "blocked_clients") || strings.Contains(name, "lock_waits") {
		return 2
	}
	if strings.HasSuffix(name, "_capacity_bytes") || strings.HasSuffix(name, "_limit_bytes") || strings.Contains(name, "fragmentation") {
		return 3
	}
	if strings.Contains(name, "http_probe_latency") || strings.Contains(name, "throughput") || strings.HasSuffix(name, "_per_second") {
		return 4
	}
	return 5
}

func buildChartsLimited(snapshots []Snapshot) ([]ReportChart, int) {
	series := map[string][]Sample{}
	for _, s := range snapshots {
		for _, v := range s.Samples {
			if v.Kind == "gauge" && !math.IsNaN(v.Value) && !math.IsInf(v.Value, 0) {
				series[sampleKey(v)] = append(series[sampleKey(v)], v)
			}
		}
	}
	keys := make([]string, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := chartPriority(series[keys[i]][0].Name), chartPriority(series[keys[j]][0].Name)
		if a == b {
			return keys[i] < keys[j]
		}
		return a < b
	})
	var charts []ReportChart
	omitted := 0
	for _, key := range keys {
		points := series[key]
		sort.Slice(points, func(i, j int) bool { return points[i].ObservedAt.Before(points[j].ObservedAt) })
		if len(points) < 2 {
			continue
		}
		// Keep every metric available; charts bound drawing cost without altering the
		// saved sample count or peak. Gaps have separate paths rather than false zeros.
		peak := points[0].Value
		min := peak
		for _, p := range points {
			if p.Value > peak {
				peak = p.Value
			}
			if p.Value < min {
				min = p.Value
			}
		}
		start, end := points[0].ObservedAt, points[len(points)-1].ObservedAt
		if !end.After(start) {
			continue
		}
		if peak == 0 && min == 0 {
			continue
		}
		if len(charts) >= 16 {
			omitted++
			continue
		}
		stride := 1
		if len(points) > 600 {
			stride = (len(points) + 599) / 600
		}
		var paths []string
		var path strings.Builder
		prev := start
		for i := 0; i < len(points); i += stride {
			p := points[i]
			if i > 0 && p.ObservedAt.Sub(prev) > time.Duration(stride)*90*time.Second {
				if path.Len() > 0 {
					paths = append(paths, path.String())
					path.Reset()
				}
			}
			x := 36 + 604*p.ObservedAt.Sub(start).Seconds()/end.Sub(start).Seconds()
			y := 112.0
			if peak > min {
				y = 112 - 88*(p.Value-min)/(peak-min)
			}
			if path.Len() == 0 {
				fmt.Fprintf(&path, "M %.2f %.2f", x, y)
			} else {
				fmt.Fprintf(&path, " L %.2f %.2f", x, y)
			}
			prev = p.ObservedAt
		}
		if path.Len() > 0 {
			paths = append(paths, path.String())
		}
		var svg strings.Builder
		svg.WriteString(`<svg viewBox="0 0 660 146" role="img" aria-label="已保存樣本趨勢"><path d="M36 18V112H640" fill="none" stroke="#b6c2cf"/>`)
		for _, p := range paths {
			fmt.Fprintf(&svg, `<path d="%s" fill="none" stroke="#126e82" stroke-width="2"/>`, p)
		}
		fmt.Fprintf(&svg, `<text x="36" y="136" font-size="11">%s</text><text x="640" y="136" text-anchor="end" font-size="11">%s</text><text x="36" y="15" font-size="11">%.3g – %.3g</text></svg>`, start.UTC().Format("01-02 15:04Z"), end.UTC().Format("01-02 15:04Z"), min, peak)
		charts = append(charts, ReportChart{Title: points[0].Service + " / " + points[0].Name, Unit: points[0].Unit, SVG: template.HTML(svg.String()), Samples: len(points), Peak: peak})
	}
	return charts, omitted
}

// RenderReport saves HTML first, then asks a sandbox-enabled Chromium to print.
// A failure leaves the reviewable HTML and returns an error to the caller.
func RenderReport(ctx context.Context, r Report, outPrefix, chromium string, runner CommandRunner) (htmlPath, pdfPath string, err error) {
	r = safeReport(r)
	if outPrefix == "" {
		return "", "", errors.New("report output prefix is required")
	}
	abs, err := filepath.Abs(outPrefix)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Dir(abs)
	if err = ensurePrivateDirectory(dir); err != nil {
		return "", "", err
	}
	htmlPath, pdfPath = abs+".html", abs+".pdf"
	loc, e := time.LoadLocation(r.Timezone)
	if e != nil {
		loc = time.UTC
	}
	funcs := template.FuncMap{"servicePages": servicePages, "resultPages": resultPages, "eventPages": eventPages, "chartPages": chartPages, "when": func(t time.Time) string {
		if t.IsZero() {
			return "未知"
		}
		return t.In(loc).Format("2006-01-02 15:04:05 MST")
	}, "count": func(s string) int { return r.Counts[Status(s)] }, "num": func(v *float64) string {
		if v == nil {
			return "—"
		}
		return fmt.Sprintf("%.3g", *v)
	}, "pct": func(v float64) string { return fmt.Sprintf("%.1f%%", v) }, "status": func(s Status) string {
		switch s {
		case Pass:
			return "通過"
		case Warn:
			return "警告"
		case Fail:
			return "異常"
		case Unknown:
			return "未知"
		case NotApplicable:
			return "不適用"
		case NotRun:
			return "未執行"
		default:
			return "未知"
		}
	}, "remaining": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return fmt.Sprintf("%.1f 天", t.Sub(r.To).Hours()/24)
	}}
	t, err := template.New("report").Funcs(funcs).Parse(reportTemplate)
	if err != nil {
		return "", "", err
	}
	var html bytes.Buffer
	if err = t.Execute(&html, r); err != nil {
		return "", "", err
	}
	if err = atomicPrivateWrite(htmlPath, html.Bytes()); err != nil {
		return "", "", err
	}
	encoded, encodeErr := json.MarshalIndent(r, "", "  ")
	if encodeErr != nil {
		return htmlPath, "", errors.New("report JSON serialization failed")
	}
	if err = atomicPrivateWrite(abs+".json", append(encoded, '\n')); err != nil {
		return htmlPath, "", err
	}
	if chromium == "" || runner == nil {
		return htmlPath, "", errors.New("Chromium renderer is unavailable")
	}
	profile, err := os.MkdirTemp(dir, ".chromium-profile-")
	if err != nil {
		return htmlPath, "", err
	}
	defer os.RemoveAll(profile)
	// Printing to a temporary location prevents a failed browser invocation from
	// making an old PDF look like this report's result.
	stage, err := os.MkdirTemp(dir, ".pdf-stage-")
	if err != nil {
		return htmlPath, "", err
	}
	defer os.RemoveAll(stage)
	stagedPDF := filepath.Join(stage, "report.pdf")
	fileURL := (&url.URL{Scheme: "file", Path: htmlPath}).String()
	args := []string{"--headless", "--disable-gpu", "--no-pdf-header-footer", "--user-data-dir=" + profile, "--print-to-pdf=" + stagedPDF, fileURL}
	renderCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, code, err := runner.Run(renderCtx, chromium, args, nil, nil)
	if err != nil || code != 0 {
		return htmlPath, "", fmt.Errorf("Chromium PDF rendering failed (exit %d)", code)
	}
	f, err := os.Open(stagedPDF)
	if err != nil {
		return htmlPath, "", errors.New("Chromium produced no PDF")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 5 || info.Size() > 64<<20 {
		return htmlPath, "", errors.New("Chromium produced invalid PDF size")
	}
	data, err := os.ReadFile(stagedPDF)
	if err != nil {
		return htmlPath, "", err
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return htmlPath, "", errors.New("Chromium output is not a PDF")
	}
	if err = atomicPrivateWrite(pdfPath, data); err != nil {
		return htmlPath, "", err
	}
	return htmlPath, pdfPath, nil
}

func safeReport(r Report) Report {
	r.Environment = ScrubReportText(r.Environment)
	r.Stack = ScrubReportText(r.Stack)
	r.Profile = ScrubReportText(r.Profile)
	r.SourceFingerprint = ScrubReportText(r.SourceFingerprint)
	r.Version = ScrubReportText(r.Version)
	r.Notes = append([]string(nil), r.Notes...)
	for i := range r.Notes {
		r.Notes[i] = ScrubReportText(r.Notes[i])
	}
	r.Services = append([]ServiceReport(nil), r.Services...)
	for i := range r.Services {
		r.Services[i].Name = ScrubReportText(r.Services[i].Name)
	}
	r.Groups = append([]ReportGroup(nil), r.Groups...)
	for i := range r.Groups {
		r.Groups[i].Title = ScrubReportText(r.Groups[i].Title)
		r.Groups[i].Results = safeSnapshot(Snapshot{Results: r.Groups[i].Results}).Results
	}
	r.Events = append([]ReportEvent(nil), r.Events...)
	for i := range r.Events {
		r.Events[i].Service = ScrubReportText(r.Events[i].Service)
		r.Events[i].CheckID = ScrubReportText(r.Events[i].CheckID)
		r.Events[i].Reason = ScrubReportText(r.Events[i].Reason)
	}
	r.Charts = append([]ReportChart(nil), r.Charts...)
	for i := range r.Charts {
		r.Charts[i].Title = ScrubReportText(r.Charts[i].Title)
		r.Charts[i].Unit = ScrubReportText(r.Charts[i].Unit)
	}
	return r
}

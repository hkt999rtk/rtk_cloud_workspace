package home100k

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TokenOnlyReport struct {
	BaseURL      string            `json:"base_url"`
	Requests     int               `json:"requests"`
	Concurrency  int               `json:"concurrency"`
	Success      int               `json:"success"`
	Non200       int               `json:"non_200"`
	Timeouts     int               `json:"timeouts"`
	Errors       int               `json:"errors"`
	StatusCodes  map[string]int    `json:"status_codes"`
	ErrorTypes   map[string]int    `json:"error_types,omitempty"`
	SampleErrors []string          `json:"sample_errors,omitempty"`
	Latency      TokenOnlyLatency  `json:"latency"`
	StartedAt    time.Time         `json:"started_at"`
	CompletedAt  time.Time         `json:"completed_at"`
	DurationMS   float64           `json:"duration_ms"`
	Stages       []TokenOnlyReport `json:"stages,omitempty"`
}

type TokenOnlyLatency struct {
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

type tokenOnlyResult struct {
	status  int
	latency time.Duration
	err     error
	errType string
	timeout bool
}

func executeTokenOnly(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("home-100k token-only", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseURL := fs.String("base-url", "", "Video Cloud API base URL")
	outDir := fs.String("out-dir", "", "artifact output directory")
	requests := fs.Int("requests", 1000, "total /request_token requests")
	concurrency := fs.Int("concurrency", 100, "concurrent /request_token requests")
	profile := fs.String("profile", "", "comma-separated concurrency profile, for example 1000,5000,10000,20000,50000")
	timeout := fs.Duration("timeout", 10*time.Second, "per-request timeout")
	body := fs.String("body", `{"scope":"device"}`, "request_token JSON body")
	certFile := fs.String("cert-file", "", "optional client certificate PEM")
	keyFile := fs.String("key-file", "", "optional client private key PEM")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*baseURL) == "" {
		fmt.Fprintln(stderr, "--base-url is required")
		return 2
	}
	if *requests <= 0 || *concurrency <= 0 {
		fmt.Fprintln(stderr, "--requests and --concurrency must be positive")
		return 2
	}
	report, err := runTokenOnlyLoad(TokenOnlyOptions{
		BaseURL:     *baseURL,
		OutDir:      *outDir,
		Requests:    *requests,
		Concurrency: *concurrency,
		Profile:     *profile,
		Timeout:     *timeout,
		Body:        *body,
		CertFile:    *certFile,
		KeyFile:     *keyFile,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if strings.TrimSpace(*outDir) != "" {
		if err := writeJSONFile(filepath.Join(*outDir, "token-only-results.json"), report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "token-only requests=%d concurrency=%d success=%d non_200=%d timeouts=%d errors=%d p50=%.3fms p95=%.3fms p99=%.3fms max=%.3fms\n",
		report.Requests, report.Concurrency, report.Success, report.Non200, report.Timeouts, report.Errors,
		report.Latency.P50MS, report.Latency.P95MS, report.Latency.P99MS, report.Latency.MaxMS)
	return 0
}

type TokenOnlyOptions struct {
	BaseURL     string
	OutDir      string
	Requests    int
	Concurrency int
	Profile     string
	Timeout     time.Duration
	Body        string
	CertFile    string
	KeyFile     string
}

func runTokenOnlyLoad(opts TokenOnlyOptions) (TokenOnlyReport, error) {
	profile, err := parseTokenOnlyProfile(opts.Profile)
	if err != nil {
		return TokenOnlyReport{}, err
	}
	if len(profile) > 0 {
		return runTokenOnlyProfile(opts, profile)
	}
	client, err := tokenOnlyHTTPClient(opts)
	if err != nil {
		return TokenOnlyReport{}, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	endpoint := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/") + "/request_token"
	body := []byte(firstNonEmpty(opts.Body, `{"scope":"device"}`))
	started := time.Now().UTC()
	jobs := make(chan int)
	results := make(chan tokenOnlyResult, opts.Requests)
	var wg sync.WaitGroup
	for worker := 0; worker < opts.Concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				results <- executeTokenOnlyRequest(client, endpoint, body, opts.Timeout)
			}
		}()
	}
	for i := 0; i < opts.Requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	report := TokenOnlyReport{
		BaseURL:     opts.BaseURL,
		Requests:    opts.Requests,
		Concurrency: opts.Concurrency,
		StatusCodes: map[string]int{},
		ErrorTypes:  map[string]int{},
		StartedAt:   started,
		CompletedAt: time.Now().UTC(),
	}
	latencies := make([]time.Duration, 0, opts.Requests)
	for result := range results {
		if result.latency > 0 {
			latencies = append(latencies, result.latency)
		}
		if result.timeout {
			report.Timeouts++
		}
		if result.err != nil {
			report.Errors++
			report.ErrorTypes[firstNonEmpty(result.errType, "other")]++
			if len(report.SampleErrors) < 10 {
				report.SampleErrors = append(report.SampleErrors, result.err.Error())
			}
			continue
		}
		report.StatusCodes[fmt.Sprintf("%d", result.status)]++
		if result.status >= 200 && result.status < 300 {
			report.Success++
		} else {
			report.Non200++
		}
	}
	report.Latency = summarizeTokenOnlyLatency(latencies)
	report.DurationMS = float64(report.CompletedAt.Sub(report.StartedAt).Microseconds()) / 1000
	return report, nil
}

func runTokenOnlyProfile(opts TokenOnlyOptions, profile []int) (TokenOnlyReport, error) {
	started := time.Now().UTC()
	report := TokenOnlyReport{
		BaseURL:     opts.BaseURL,
		Requests:    opts.Requests * len(profile),
		Concurrency: profile[len(profile)-1],
		StatusCodes: map[string]int{},
		ErrorTypes:  map[string]int{},
		StartedAt:   started,
	}
	for _, concurrency := range profile {
		stageOpts := opts
		stageOpts.Profile = ""
		stageOpts.Concurrency = concurrency
		stage, err := runTokenOnlyLoad(stageOpts)
		if err != nil {
			return TokenOnlyReport{}, err
		}
		report.Stages = append(report.Stages, stage)
		report.Success += stage.Success
		report.Non200 += stage.Non200
		report.Timeouts += stage.Timeouts
		report.Errors += stage.Errors
		if len(report.SampleErrors) < 10 {
			remaining := 10 - len(report.SampleErrors)
			if len(stage.SampleErrors) < remaining {
				remaining = len(stage.SampleErrors)
			}
			report.SampleErrors = append(report.SampleErrors, stage.SampleErrors[:remaining]...)
		}
		for errType, count := range stage.ErrorTypes {
			report.ErrorTypes[errType] += count
		}
		for status, count := range stage.StatusCodes {
			report.StatusCodes[status] += count
		}
	}
	report.CompletedAt = time.Now().UTC()
	report.DurationMS = float64(report.CompletedAt.Sub(report.StartedAt).Microseconds()) / 1000
	report.Latency = report.Stages[len(report.Stages)-1].Latency
	return report, nil
}

func executeTokenOnlyRequest(client *http.Client, endpoint string, body []byte, timeout time.Duration) tokenOnlyResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return tokenOnlyResult{err: err, errType: "build_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		timeoutErr := ctx.Err() == context.DeadlineExceeded
		return tokenOnlyResult{latency: latency, err: err, errType: classifyTokenOnlyError(err, timeoutErr), timeout: timeoutErr}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return tokenOnlyResult{status: resp.StatusCode, latency: latency}
}

func classifyTokenOnlyError(err error, timeout bool) string {
	if timeout {
		return "timeout"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "connection reset"):
		return "connection_reset"
	case strings.Contains(text, "connection refused"):
		return "connection_refused"
	case strings.Contains(text, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(text, "too many open files"):
		return "too_many_open_files"
	case strings.Contains(text, "can't assign requested address"):
		return "local_addr_exhausted"
	case strings.Contains(text, "no such host"):
		return "dns"
	case strings.Contains(text, "tls"):
		return "tls"
	default:
		return "other"
	}
}

func tokenOnlyHTTPClient(opts TokenOnlyOptions) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	idleLimit := opts.Concurrency * 2
	if idleLimit < 100 {
		idleLimit = 100
	}
	transport.MaxIdleConns = idleLimit
	transport.MaxIdleConnsPerHost = idleLimit
	if strings.TrimSpace(opts.CertFile) != "" || strings.TrimSpace(opts.KeyFile) != "" {
		if strings.TrimSpace(opts.CertFile) == "" || strings.TrimSpace(opts.KeyFile) == "" {
			return nil, fmt.Errorf("--cert-file and --key-file must be set together")
		}
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport}, nil
}

func summarizeTokenOnlyLatency(values []time.Duration) TokenOnlyLatency {
	if len(values) == 0 {
		return TokenOnlyLatency{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return TokenOnlyLatency{
		P50MS: durationMS(percentileDuration(values, 0.50)),
		P95MS: durationMS(percentileDuration(values, 0.95)),
		P99MS: durationMS(percentileDuration(values, 0.99)),
		MaxMS: durationMS(values[len(values)-1]),
	}
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * percentile)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func durationMS(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1000
}

func parseTokenOnlyProfile(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("invalid token-only profile concurrency %q", part)
		}
		out = append(out, value)
	}
	return out, nil
}

func readJSONFile(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func executeSeedTokenProjections(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("home-100k seed-token-projections", flag.ContinueOnError)
	fs.SetOutput(stderr)
	redisAddr := fs.String("redis-addr", "", "Redis/Valkey address")
	devices := fs.Int("devices", 0, "number of device projections to seed")
	devicePrefix := fs.String("device-prefix", "load-device-", "device id prefix")
	keyPrefix := fs.String("key-prefix", "video_cloud", "Redis key prefix")
	ttl := fs.Duration("ttl", 24*time.Hour, "projection TTL")
	orgID := fs.String("org-id", "loadtest-brand-cloud", "brand cloud id stored in device projection")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*redisAddr) == "" || *devices <= 0 {
		fmt.Fprintln(stderr, "--redis-addr and positive --devices are required")
		return 2
	}
	if err := seedTokenProjections(context.Background(), SeedTokenProjectionOptions{
		RedisAddr:    *redisAddr,
		Devices:      *devices,
		DevicePrefix: *devicePrefix,
		KeyPrefix:    *keyPrefix,
		TTL:          *ttl,
		OrgID:        *orgID,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "seeded token projections devices=%d redis=%s ttl=%s\n", *devices, *redisAddr, ttl.String())
	return 0
}

type SeedTokenProjectionOptions struct {
	RedisAddr    string
	Devices      int
	DevicePrefix string
	KeyPrefix    string
	TTL          time.Duration
	OrgID        string
}

func seedTokenProjections(ctx context.Context, opts SeedTokenProjectionOptions) error {
	prefix := strings.Trim(strings.TrimSpace(opts.KeyPrefix), ":")
	if prefix == "" {
		prefix = "video_cloud"
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for idx := 0; idx < opts.Devices; idx++ {
		deviceID := fmt.Sprintf("%s%06d", opts.DevicePrefix, idx)
		devicePayload := map[string]any{
			"devid":      deviceID,
			"org_id":     strings.TrimSpace(opts.OrgID),
			"activated":  true,
			"updated_at": now,
		}
		entitlementPayload := map[string]any{
			"state":            "active",
			"allowed_services": []string{"mqtt"},
			"updated_at":       now,
		}
		if err := redisSetJSON(ctx, opts.RedisAddr, prefix+":device:"+deviceID, devicePayload, opts.TTL); err != nil {
			return err
		}
		if err := redisSetJSON(ctx, opts.RedisAddr, prefix+":entitlement:"+deviceID, entitlementPayload, opts.TTL); err != nil {
			return err
		}
	}
	return nil
}

func redisSetJSON(ctx context.Context, addr string, key string, value any, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	args := []string{"SET", key, string(raw), "PX", strconv.FormatInt(ttl.Milliseconds(), 10)}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("redis dial %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := writeRESPArray(conn, args); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "+OK") {
		return fmt.Errorf("redis set %s: unexpected response %q", key, strings.TrimSpace(line))
	}
	return nil
}

func writeRESPArray(w io.Writer, args []string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

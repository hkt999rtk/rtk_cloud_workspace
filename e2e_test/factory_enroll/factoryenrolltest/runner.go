package factoryenrolltest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	client *http.Client
}

func NewRunner(client *http.Client) *Runner {
	if client == nil {
		client = &http.Client{}
	}
	return &Runner{client: client}
}

func DefaultConfigFromEnv() Config {
	return Config{
		FactoryURL:   envDefault("FACTORY_ENROLL_TEST_URL", "http://127.0.0.1:18443"),
		AuthKey:      os.Getenv("FACTORY_ENROLL_TEST_AUTH_KEY"),
		Count:        envInt("FACTORY_ENROLL_TEST_COUNT", 100),
		RunID:        envDefault("FACTORY_ENROLL_TEST_RUN_ID", time.Now().UTC().Format("20060102T150405Z")),
		FactoryID:    envDefault("FACTORY_ENROLL_TEST_FACTORY_ID", "factory-local"),
		LineID:       envDefault("FACTORY_ENROLL_TEST_LINE_ID", "line-local"),
		StationID:    envDefault("FACTORY_ENROLL_TEST_STATION_ID", "station-local"),
		FixtureID:    envDefault("FACTORY_ENROLL_TEST_FIXTURE_ID", "fixture-local"),
		OperatorID:   envDefault("FACTORY_ENROLL_TEST_OPERATOR_ID", "operator-local"),
		BatchID:      os.Getenv("FACTORY_ENROLL_TEST_BATCH_ID"),
		Timeout:      envDuration("FACTORY_ENROLL_TEST_TIMEOUT", 30*time.Second),
		Concurrency:  envInt("FACTORY_ENROLL_TEST_CONCURRENCY", 8),
		ArtifactDir:  os.Getenv("FACTORY_ENROLL_TEST_ARTIFACT_DIR"),
		SerialPrefix: envDefault("FACTORY_ENROLL_TEST_SERIAL_PREFIX", "FTEST"),
	}
}

func (c *Config) Normalize() error {
	c.FactoryURL = strings.TrimRight(strings.TrimSpace(c.FactoryURL), "/")
	if c.FactoryURL == "" {
		return fmt.Errorf("factory URL is required")
	}
	if _, err := url.ParseRequestURI(c.FactoryURL); err != nil {
		return fmt.Errorf("factory URL is invalid: %w", err)
	}
	if strings.TrimSpace(c.AuthKey) == "" {
		return fmt.Errorf("auth key is required")
	}
	if c.Count <= 0 {
		return fmt.Errorf("count must be > 0")
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.Concurrency > c.Count {
		c.Concurrency = c.Count
	}
	if strings.TrimSpace(c.RunID) == "" {
		c.RunID = time.Now().UTC().Format("20060102T150405Z")
	}
	if strings.TrimSpace(c.BatchID) == "" {
		c.BatchID = "batch-" + c.RunID
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, cfg Config) (*Result, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	result := &Result{
		Schema:    ResultSchema,
		RunID:     cfg.RunID,
		StartedAt: started,
		Config: ResultConfig{
			FactoryURL:  cfg.FactoryURL,
			Count:       cfg.Count,
			Concurrency: cfg.Concurrency,
			FactoryID:   cfg.FactoryID,
			LineID:      cfg.LineID,
			StationID:   cfg.StationID,
			FixtureID:   cfg.FixtureID,
			BatchID:     cfg.BatchID,
		},
		Devices:  make([]DeviceResult, cfg.Count),
		Errors:   map[string]int{},
		Metadata: map[string]string{"runner": "rtk-factory-enroll-test"},
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.Concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				result.Devices[idx] = r.enrollOne(ctx, cfg, idx+1)
			}
		}()
	}
	for idx := 0; idx < cfg.Count; idx++ {
		select {
		case <-ctx.Done():
			result.Devices[idx] = DeviceResult{Index: idx + 1, Success: false, ErrorClass: "cancelled", Error: ctx.Err().Error()}
		case jobs <- idx:
		}
	}
	close(jobs)
	wg.Wait()
	result.EndedAt = time.Now().UTC()
	summarize(result)
	return result, nil
}

func (r *Runner) enrollOne(parent context.Context, cfg Config, index int) DeviceResult {
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	started := time.Now()
	deviceKey, deviceID, csrPEM, keyPEM, err := GenerateDeviceIdentity()
	if err != nil {
		return failed(index, "", 0, started, "keygen", err)
	}
	requestID := fmt.Sprintf("%s-%03d", cfg.RunID, index)
	serial := fmt.Sprintf("%s-%s-%03d", cfg.SerialPrefix, cfg.RunID, index)
	body := EnrollRequest{
		RequestID:    requestID,
		DeviceID:     deviceID,
		CSRPem:       string(csrPEM),
		SerialNumber: serial,
		FactoryID:    cfg.FactoryID,
		LineID:       cfg.LineID,
		StationID:    cfg.StationID,
		FixtureID:    cfg.FixtureID,
		OperatorID:   cfg.OperatorID,
		BatchID:      cfg.BatchID,
		Metadata: map[string]any{
			"test_suite": "workspace_factory_enroll_v1",
			"run_id":     cfg.RunID,
			"index":      index,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return failed(index, deviceID, 0, started, "marshal", err)
	}
	endpoint := cfg.FactoryURL + enrollPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return failed(index, deviceID, 0, started, "request", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Video-Cloud-Request-ID", requestID)
	req.Header.Set("X-Video-Cloud-Timestamp", timestamp)
	req.Header.Set("X-Video-Cloud-Signature", SignHMAC([]byte(cfg.AuthKey), req.Method, enrollPath, timestamp, requestID, raw))
	resp, err := r.client.Do(req)
	if err != nil {
		return failed(index, deviceID, 0, started, classifyError(0, err), err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(limited)
		return failed(index, deviceID, resp.StatusCode, started, classifyHTTP(resp.StatusCode), fmt.Errorf("factory enrollment failed: %s", strings.TrimSpace(string(body))))
	}
	var out EnrollResponse
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return failed(index, deviceID, resp.StatusCode, started, "decode", err)
	}
	if out.DeviceID != deviceID {
		return failed(index, deviceID, resp.StatusCode, started, "identity", fmt.Errorf("response devid %q does not match request devid %q", out.DeviceID, deviceID))
	}
	certFields, err := ValidateCertificate(deviceID, deviceKey, out.CertificatePEM)
	if err != nil {
		return failed(index, deviceID, resp.StatusCode, started, "certificate", err)
	}
	if cfg.WriteKeyFiles && cfg.ArtifactDir != "" {
		_ = writeDeviceMaterial(cfg.ArtifactDir, index, keyPEM, csrPEM, []byte(out.CertificatePEM), []byte(out.CertificateChainPEM))
	}
	certFields.Index = index
	certFields.RequestID = requestID
	certFields.DeviceID = deviceID
	certFields.SerialNumber = out.SerialNumber
	certFields.Success = true
	certFields.StatusCode = resp.StatusCode
	certFields.LatencyMillis = time.Since(started).Milliseconds()
	return certFields
}

func failed(index int, deviceID string, status int, started time.Time, class string, err error) DeviceResult {
	return DeviceResult{Index: index, DeviceID: deviceID, Success: false, StatusCode: status, LatencyMillis: time.Since(started).Milliseconds(), ErrorClass: class, Error: err.Error()}
}

func writeDeviceMaterial(root string, index int, key, csr, cert, chain []byte) error {
	dir := filepath.Join(root, "device-material", fmt.Sprintf("device-%03d", index))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string]struct {
		content []byte
		mode    os.FileMode
	}{
		"device.key":       {key, 0o600},
		"device.csr":       {csr, 0o644},
		"device.crt":       {cert, 0o644},
		"device-chain.crt": {chain, 0o644},
	}
	for name, file := range files {
		if err := os.WriteFile(filepath.Join(dir, name), file.content, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func summarize(result *Result) {
	result.Summary.Total = len(result.Devices)
	latencies := make([]int64, 0, len(result.Devices))
	for _, device := range result.Devices {
		if device.Success {
			result.Summary.Successes++
			latencies = append(latencies, device.LatencyMillis)
		} else {
			result.Summary.Failures++
			if device.ErrorClass != "" {
				result.Errors[device.ErrorClass]++
			}
		}
	}
	if result.Summary.Total > 0 {
		result.Summary.SuccessRate = float64(result.Summary.Successes) / float64(result.Summary.Total)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result.Summary.P95LatencyMS = percentile(latencies, 0.95)
	result.Summary.P99LatencyMS = percentile(latencies, 0.99)
	result.Summary.DurationMillis = result.EndedAt.Sub(result.StartedAt).Milliseconds()
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(float64(len(values)-1) * p)
	return values[idx]
}

func classifyHTTP(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth"
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusConflict:
		return "conflict"
	default:
		return "http"
	}
}

func classifyError(status int, err error) string {
	if err == nil {
		return classifyHTTP(status)
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") || strings.Contains(strings.ToLower(err.Error()), "deadline") {
		return "timeout"
	}
	return "network"
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

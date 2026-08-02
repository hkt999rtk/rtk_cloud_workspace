package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	platformPrometheusWaitTimeout  = time.Minute
	platformPrometheusWaitInterval = 5 * time.Second
)

var requiredPrometheusJobs = []string{
	"account-manager",
	"cloud-admin",
	"frontend",
	"redis-exporter",
	"video-cloud-api",
	"video-cloud-clip-verifier",
	"video-cloud-factoryenroll",
	"video-cloud-grafana",
	"video-cloud-logingester",
	"video-cloud-metrics-exporter",
	"video-cloud-mqttusage",
	"video-cloud-prometheus",
	"video-cloud-turnregistry",
}

func runPlatformLiveEvidence(args []string) error {
	fs := flag.NewFlagSet("test-platform-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	runID := fs.String("run-id", "", "run identity")
	outDir := fs.String("out-dir", "", "evidence output root")
	prometheusURL := fs.String("prometheus-url", "", "Prometheus API base URL")
	bffURL := fs.String("bff-url", "", "Cloud Admin BFF base URL")
	platformSession := fs.String("platform-session", "", "platform session id")
	customerSession := fs.String("customer-session", "", "customer session id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*outDir) == "" || strings.TrimSpace(*prometheusURL) == "" || strings.TrimSpace(*bffURL) == "" || strings.TrimSpace(*platformSession) == "" || strings.TrimSpace(*customerSession) == "" {
		return errors.New("--run-id, --out-dir, --prometheus-url, --bff-url, --platform-session, and --customer-session are required")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	started := time.Now().UTC()
	scrapeDir := filepath.Join(*outDir, "scrape")
	if err := waitForPrometheusInventory(*prometheusURL, scrapeDir, *runID, platformPrometheusWaitTimeout, platformPrometheusWaitInterval); err != nil {
		return err
	}
	if err := writeCaseFeatureEvidence(workspace, scrapeDir, "LIVE-CA-SCRAPE-001", *runID, "staging", "", started, time.Now().UTC()); err != nil {
		return err
	}
	bffStarted := time.Now().UTC()
	bffDir := filepath.Join(*outDir, "bff-sources")
	if err := qualifyBFFProductionSources(*bffURL, *platformSession, *customerSession, bffDir, *runID); err != nil {
		return err
	}
	return writeCaseFeatureEvidence(workspace, bffDir, "LIVE-CA-BFF-SOURCES-001", *runID, "staging", "", bffStarted, time.Now().UTC())
}

func waitForPrometheusInventory(baseURL, outDir, runID string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := qualifyPrometheusInventory(baseURL, outDir, runID); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			return lastErr
		}
		wait := interval
		if wait <= 0 {
			wait = time.Millisecond
		}
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		time.Sleep(wait)
	}
}

func qualifyPrometheusInventory(baseURL, outDir, runID string) error {
	var targets struct {
		Status string `json:"status"`
		Data   struct {
			Active []struct {
				Labels map[string]string `json:"labels"`
				Health string            `json:"health"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := getJSON(strings.TrimRight(baseURL, "/")+"/api/v1/targets", "", &targets); err != nil {
		return err
	}
	if targets.Status != "success" {
		return errors.New("Prometheus targets API did not return success")
	}
	discovered := map[string][]string{}
	for _, target := range targets.Data.Active {
		job := strings.TrimSpace(target.Labels["job"])
		if job != "" {
			discovered[job] = append(discovered[job], strings.ToLower(strings.TrimSpace(target.Health)))
		}
	}
	missing := []string{}
	down := []string{}
	for _, job := range requiredPrometheusJobs {
		healths, ok := discovered[job]
		if !ok || len(healths) == 0 {
			missing = append(missing, job)
			continue
		}
		for _, health := range healths {
			if health != "up" {
				down = append(down, job)
				break
			}
		}
	}
	var names struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := getJSON(strings.TrimRight(baseURL, "/")+"/api/v1/label/__name__/values", "", &names); err != nil {
		return err
	}
	metricNames := map[string]bool{}
	for _, name := range names.Data {
		metricNames[name] = true
	}
	if names.Status != "success" || !metricNames["up"] {
		return errors.New("Prometheus metric-name inventory is missing the up metric")
	}
	if len(missing) > 0 || len(down) > 0 {
		return fmt.Errorf("Prometheus scrape inventory incomplete: missing=%s down=%s", strings.Join(missing, ","), strings.Join(down, ","))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	jobs := append([]string(nil), requiredPrometheusJobs...)
	sort.Strings(jobs)
	result := map[string]any{
		"schema_version": "rtk-prometheus-live-evidence/v1", "run_id": runID, "status": "PASS",
		"required_job_count": len(jobs), "healthy_job_count": len(jobs), "required_jobs": jobs,
		"metric_name_count": len(names.Data), "up_metric_discoverable": true,
		"raw_target_addresses_included": false,
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), result); err != nil {
		return err
	}
	if err := writePassingLiveArtifacts(outDir, "cloud-admin-prometheus-scrape", "LIVE-CA-SCRAPE-001", runID); err != nil {
		return err
	}
	return nil
}

func qualifyBFFProductionSources(baseURL, platformSession, customerSession, outDir, runID string) error {
	type probe struct {
		Path    string
		Session string
	}
	probes := []probe{
		{Path: "/api/me", Session: customerSession},
		{Path: "/api/customers", Session: customerSession},
		{Path: "/api/devices", Session: customerSession},
		{Path: "/api/service-health", Session: customerSession},
		{Path: "/api/admin/summary", Session: platformSession},
		{Path: "/api/admin/service-health", Session: platformSession},
	}
	statuses := map[string]int{}
	for _, item := range probes {
		body, status, err := getBFF(strings.TrimRight(baseURL, "/")+item.Path, item.Session)
		if err != nil {
			return err
		}
		statuses[item.Path] = status
		if status != http.StatusOK {
			return fmt.Errorf("authenticated BFF probe %s returned HTTP %d", item.Path, status)
		}
		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("decode BFF probe %s: %w", item.Path, err)
		}
		if containsUnacceptableBFFEvidence(parsed) {
			return fmt.Errorf("BFF probe %s returned demo, seed, or sample evidence", item.Path)
		}
	}
	missingID := "qualification-missing-" + runID
	_, missingStatus, err := getBFF(strings.TrimRight(baseURL, "/")+"/api/devices/"+url.PathEscape(missingID)+"/telemetry", customerSession)
	if err != nil {
		return err
	}
	if missingStatus == http.StatusOK {
		return errors.New("configured upstream miss silently returned a successful fallback")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	paths := make([]string, 0, len(statuses))
	for path := range statuses {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := map[string]any{
		"schema_version": "rtk-bff-production-source-evidence/v1", "run_id": runID, "status": "PASS",
		"authenticated_probe_paths": paths, "authenticated_probe_count": len(paths),
		"demo_seed_sample_evidence_absent": true, "configured_upstream_failure_not_fallback": true,
		"missing_upstream_status": missingStatus, "sessions_redacted": true,
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), result); err != nil {
		return err
	}
	return writePassingLiveArtifacts(outDir, "cloud-admin-bff-production-sources", "LIVE-CA-BFF-SOURCES-001", runID)
}

func getJSON(endpoint, bearer string, out any) error {
	body, status, err := getHTTP(endpoint, bearer, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET returned HTTP %d", status)
	}
	return json.Unmarshal(body, out)
}

func getBFF(endpoint, session string) ([]byte, int, error) {
	return getHTTP(endpoint, "", "rtk_admin_session="+session)
}

func getHTTP(endpoint, bearer, cookie string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return body, resp.StatusCode, err
}

func containsUnacceptableBFFEvidence(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "demo_mode") {
				if enabled, ok := child.(bool); ok && enabled {
					return true
				}
			}
			if containsUnacceptableBFFEvidence(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsUnacceptableBFFEvidence(child) {
				return true
			}
		}
	case string:
		lower := strings.ToLower(strings.TrimSpace(typed))
		for _, marker := range []string{"demo@example", "example.local", "seeded sample", "sample tenant", "demo tenant"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func writePassingLiveArtifacts(outDir, suite, testID, runID string) error {
	junit := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="%s" tests="1" failures="0"><testcase classname="platform-live" name="%s"/></testsuite>`+"\n", suite, testID)
	if err := os.WriteFile(filepath.Join(outDir, "junit.xml"), []byte(junit), 0o644); err != nil {
		return err
	}
	report := fmt.Sprintf("# %s\n\n- Run ID: `%s`\n- Test ID: `%s`\n- Status: **PASS**\n- Credentials and target addresses: redacted\n", suite, runID, testID)
	if err := os.WriteFile(filepath.Join(outDir, "TEST_REPORT.md"), []byte(report), 0o644); err != nil {
		return err
	}
	logLine := fmt.Sprintf("run_id=%s test_id=%s status=PASS sensitive_fields=redacted\n", runID, testID)
	return os.WriteFile(filepath.Join(outDir, "qualification.log"), []byte(logLine), 0o644)
}

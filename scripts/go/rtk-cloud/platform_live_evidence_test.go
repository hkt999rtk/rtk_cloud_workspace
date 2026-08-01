package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQualifyPrometheusInventoryRecordsOnlyJobLevelEvidence(t *testing.T) {
	targets := []map[string]any{}
	for _, job := range requiredPrometheusJobs {
		targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up", "scrapeUrl": "http://10.0.0.1:9999/metrics"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/targets":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
		case "/api/v1/label/__name__/values":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"go_info", "up"}})
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	outDir := t.TempDir()
	if err := qualifyPrometheusInventory(server.URL, outDir, "run-1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "10.0.0.1") || !strings.Contains(string(raw), `"healthy_job_count": 13`) {
		t.Fatalf("results = %s", raw)
	}
}

func TestQualifyPrometheusInventoryRejectsMissingJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "targets") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up"]}`))
	}))
	defer server.Close()
	if err := qualifyPrometheusInventory(server.URL, t.TempDir(), "run-1"); err == nil {
		t.Fatal("missing Prometheus jobs unexpectedly passed")
	}
}

func TestQualifyPrometheusInventoryRejectsAnyDownReplica(t *testing.T) {
	targets := []map[string]any{}
	for _, job := range requiredPrometheusJobs {
		targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up"})
	}
	targets = append(targets, map[string]any{"labels": map[string]string{"job": requiredPrometheusJobs[0]}, "health": "down"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "targets") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up"]}`))
	}))
	defer server.Close()
	if err := qualifyPrometheusInventory(server.URL, t.TempDir(), "run-1"); err == nil {
		t.Fatal("a down Prometheus replica unexpectedly passed")
	}
}

func TestWaitForPrometheusInventoryAllowsScrapeReadinessToConverge(t *testing.T) {
	targetCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "targets") {
			targetCalls++
			targets := make([]map[string]any, 0, len(requiredPrometheusJobs))
			for _, job := range requiredPrometheusJobs {
				health := "up"
				if job == "video-cloud-grafana" && targetCalls == 1 {
					health = "down"
				}
				targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": health})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up"]}`))
	}))
	defer server.Close()
	if err := waitForPrometheusInventory(server.URL, t.TempDir(), "run-1", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if targetCalls != 2 {
		t.Fatalf("target API calls = %d, want 2", targetCalls)
	}
}

func TestWaitForPrometheusInventoryFailsClosedAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "targets") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up"]}`))
	}))
	defer server.Close()
	if err := waitForPrometheusInventory(server.URL, t.TempDir(), "run-1", 5*time.Millisecond, time.Millisecond); err == nil || !strings.Contains(err.Error(), "Prometheus scrape inventory incomplete") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestQualifyBFFProductionSourcesRequiresAuthenticatedUpstreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cookie, err := req.Cookie("rtk_admin_session")
		if err != nil || (cookie.Value != "platform-session" && cookie.Value != "customer-session") {
			t.Fatalf("cookie = %#v err=%v", cookie, err)
		}
		if strings.Contains(req.URL.Path, "qualification-missing-run-1") {
			http.Error(w, `{"error":"upstream not found"}`, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","source":"account_manager","demo_mode":false}`))
	}))
	defer server.Close()
	outDir := t.TempDir()
	if err := qualifyBFFProductionSources(server.URL, "platform-session", "customer-session", outDir, "run-1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "session") && !strings.Contains(string(raw), `"sessions_redacted": true`) {
		t.Fatalf("results may contain a session: %s", raw)
	}
}

func TestQualifyBFFProductionSourcesRejectsDemoAndFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"email":"demo@example.local"}`))
	}))
	defer server.Close()
	if err := qualifyBFFProductionSources(server.URL, "platform", "customer", t.TempDir(), "run-1"); err == nil {
		t.Fatal("demo BFF evidence unexpectedly passed")
	}
	if !containsUnacceptableBFFEvidence(map[string]any{"demo_mode": true}) || containsUnacceptableBFFEvidence(map[string]any{"demo_mode": false}) {
		t.Fatal("demo_mode classification is incorrect")
	}
}

func TestRunPlatformLiveEvidenceWritesBothCaseManifests(t *testing.T) {
	targets := make([]map[string]any, 0, len(requiredPrometheusJobs))
	for _, job := range requiredPrometheusJobs {
		targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/targets":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
		case "/api/v1/label/__name__/values":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"up"}})
		default:
			if strings.Contains(req.URL.Path, "qualification-missing-run-platform") {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","source":"live"}`))
		}
	}))
	defer server.Close()
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	err = runPlatformLiveEvidence([]string{
		"--workspace", workspace, "--run-id", "run-platform", "--out-dir", outDir,
		"--prometheus-url", server.URL, "--bff-url", server.URL,
		"--platform-session", "platform-session", "--customer-session", "customer-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"scrape", "bff-sources"} {
		for _, name := range []string{"results.json", "junit.xml", "TEST_REPORT.md", "feature-evidence.json"} {
			if _, err := os.Stat(filepath.Join(outDir, dir, name)); err != nil {
				t.Fatalf("missing %s/%s: %v", dir, name, err)
			}
		}
	}
}

func TestPlatformLiveEvidenceFailsClosedOnInvalidUpstreams(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		run     func(string) error
	}{
		{
			name:    "prometheus status",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"error"}`)) },
			run:     func(base string) error { return qualifyPrometheusInventory(base, t.TempDir(), "run-1") },
		},
		{
			name: "prometheus missing up metric",
			handler: func(w http.ResponseWriter, req *http.Request) {
				if strings.Contains(req.URL.Path, "targets") {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": []any{}}})
					return
				}
				_, _ = w.Write([]byte(`{"status":"success","data":["go_info"]}`))
			},
			run: func(base string) error { return qualifyPrometheusInventory(base, t.TempDir(), "run-1") },
		},
		{
			name: "prometheus metric inventory decode failure",
			handler: func(w http.ResponseWriter, req *http.Request) {
				if strings.Contains(req.URL.Path, "targets") {
					targets := make([]map[string]any, 0, len(requiredPrometheusJobs))
					for _, job := range requiredPrometheusJobs {
						targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
					return
				}
				_, _ = w.Write([]byte(`not-json`))
			},
			run: func(base string) error { return qualifyPrometheusInventory(base, t.TempDir(), "run-1") },
		},
		{
			name:    "bff non ok",
			handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"error":"down"}`, http.StatusBadGateway) },
			run: func(base string) error {
				return qualifyBFFProductionSources(base, "platform", "customer", t.TempDir(), "run-1")
			},
		},
		{
			name:    "bff malformed json",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not-json`)) },
			run: func(base string) error {
				return qualifyBFFProductionSources(base, "platform", "customer", t.TempDir(), "run-1")
			},
		},
		{
			name:    "bff successful missing fallback",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) },
			run: func(base string) error {
				return qualifyBFFProductionSources(base, "platform", "customer", t.TempDir(), "run-1")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			if err := tc.run(server.URL); err == nil {
				t.Fatal("invalid upstream unexpectedly passed")
			}
		})
	}
	if !containsUnacceptableBFFEvidence([]any{"nested", map[string]any{"name": "sample tenant"}}) {
		t.Fatal("nested sample evidence was not detected")
	}
	if err := runPlatformLiveEvidence(nil); err == nil {
		t.Fatal("missing platform-live arguments unexpectedly passed")
	}
}

func TestQualifyPrometheusInventoryRejectsUnwritableEvidencePath(t *testing.T) {
	targets := make([]map[string]any, 0, len(requiredPrometheusJobs))
	for _, job := range requiredPrometheusJobs {
		targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "targets") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"activeTargets": targets}})
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up"]}`))
	}))
	defer server.Close()
	outFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(outFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := qualifyPrometheusInventory(server.URL, outFile, "run-1"); err == nil {
		t.Fatal("file evidence path unexpectedly accepted as a directory")
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

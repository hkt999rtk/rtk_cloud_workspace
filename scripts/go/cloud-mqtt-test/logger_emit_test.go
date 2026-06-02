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

func TestEmitCentralLoggerEventPostsMQTTSummaryWithoutLeakingToken(t *testing.T) {
	temp := t.TempDir()
	envRoot := filepath.Join(temp, "env")
	loggerDir := filepath.Join(envRoot, "services", "cloud-logger")
	if err := os.MkdirAll(loggerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const token = "super-secret-ingest-token"
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.URL.Path != "/v1/logs/ingest" {
			t.Fatalf("unexpected ingest path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode ingest body: %v", err)
		}
		raw, _ := json.Marshal(body)
		if string(raw) == "" || strings.Contains(string(raw), token) {
			t.Fatalf("ingest payload leaked token: %s", string(raw))
		}
		events := body["events"].([]any)
		event := events[0].(map[string]any)
		if event["service"] != "workspace-mqtt-test" {
			t.Fatalf("unexpected service: %v", event["service"])
		}
		if event["unit"] != "stg.sh mqtt" {
			t.Fatalf("unexpected unit: %v", event["unit"])
		}
		if event["operation_id"] != "home-mqtt-loadtest" {
			t.Fatalf("unexpected operation_id: %v", event["operation_id"])
		}
		fields := event["fields"].(map[string]any)
		if fields["brandname"] != "RTK" || fields["overall"] != "pass" {
			t.Fatalf("unexpected fields: %#v", fields)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"results":[{"event_id":"ok","status":"accepted"}]}`))
	}))
	defer server.Close()

	envBody := "CLOUD_LOGGER_ENDPOINT=" + server.URL + "\nCLOUD_LOGGER_INGEST_TOKEN=" + token + "\n"
	if err := os.WriteFile(filepath.Join(loggerDir, "logger.env"), []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}

	err := emitCentralLoggerEvent(envRoot, map[string]any{
		"generated_at":       "2026-06-02T15:53:47Z",
		"brandname":          "RTK",
		"profile":            "smoke",
		"duration_seconds":   120,
		"status":             "PASS",
		"overall":            "pass",
		"results_file":       filepath.Join(envRoot, "artifacts", "results.json"),
		"report_file":        filepath.Join(envRoot, "artifacts", "TEST_REPORT.md"),
		"metrics":            map[string]any{"commands_attempted": 6, "commands_passed": 6, "devices_selected": 6},
		"mqtt":               map[string]any{"probe_result": "PASS"},
		"capability_metrics": []map[string]any{},
	})
	if err != nil {
		t.Fatalf("emitCentralLoggerEvent: %v", err)
	}
	if !sawRequest {
		t.Fatal("expected ingest request")
	}
}

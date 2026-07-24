package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseE2EStepsFullAndSelective(t *testing.T) {
	full, err := parseE2ESteps("all", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !full.Reset || !full.Provision || !full.Data || !full.MQTT || !full.RuntimeLogs || !full.BillingLog || !full.BillingDB {
		t.Fatalf("full selection = %+v", full)
	}
	selected, err := parseE2ESteps("mqtt,billing-log", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Reset || selected.Provision || selected.Data || !selected.MQTT || selected.RuntimeLogs || !selected.BillingLog || selected.BillingDB {
		t.Fatalf("selective selection = %+v", selected)
	}
}

func TestParseE2EStepsRejectsUnknownStep(t *testing.T) {
	if _, err := parseE2ESteps("mqtt,unknown", false, false); err == nil {
		t.Fatal("parseE2ESteps accepted unknown step")
	}
}

func TestParseBillingVerifyChecks(t *testing.T) {
	checks, err := parseBillingVerifyChecks("log,db")
	if err != nil {
		t.Fatal(err)
	}
	if !checks.Log || !checks.DB {
		t.Fatalf("checks = %+v", checks)
	}
	if _, err := parseBillingVerifyChecks("nope"); err == nil {
		t.Fatal("parseBillingVerifyChecks accepted unknown check")
	}
}

func TestQueryBillingUsageLogsFiltersBrandAndSumsMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "billing_usage" || r.URL.Query().Get("source") != "billing_usage" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[
{"fields":{"usage_event":{"service_code":"mqtt","brand_cloud_id":"other","measurements":[{"metric_code":"publish_bytes","quantity":999} ]}}},
{"fields":{"usage_event":{"service_code":"mqtt","brand_cloud_id":"brand-1","measurements":[{"metric_code":"publish_bytes","quantity":10},{"metric_code":"delivery_bytes","quantity":20},{"metric_code":"publish_count","quantity":1},{"metric_code":"delivery_count","quantity":2}]}}}
]}`))
	}))
	defer server.Close()
	result, err := queryBillingUsageLogs(server.URL, "billing-token", "brand-1", time.Now().Add(-time.Hour), time.Now(), billingUsageSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageEvents != 1 || result.PublishBytes != 10 || result.DeliveryBytes != 20 || result.PublishCount != 1 || result.DeliveryCount != 2 {
		t.Fatalf("summary = %+v", result)
	}
}

func TestRunStagingE2EBillingVerifyProducesLogEvidenceSummary(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	kubeconfig := filepath.Join(envRoot, "state", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(kubeconfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	dbPath := filepath.Join(workspace, "test-data.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table device_bindings (brand_cloud_id text, assignment_index integer, device_id text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into device_bindings values ('brand-001', 0, 'device-001')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	kubectl := filepath.Join(binDir, "kubectl")
	secret := base64.StdEncoding.EncodeToString([]byte("billing-token"))
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' '{\"data\":{\"RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN\":\""+secret+"\"}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	logger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer billing-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []map[string]any{{
			"fields": map[string]any{"usage_event": map[string]any{
				"service_code": "mqtt", "brand_cloud_id": "brand-001",
				"measurements": []map[string]any{
					{"metric_code": "publish_bytes", "quantity": 100},
					{"metric_code": "delivery_bytes", "quantity": 200},
					{"metric_code": "publish_count", "quantity": 1},
					{"metric_code": "delivery_count", "quantity": 2},
				},
			}},
		}}})
	}))
	defer logger.Close()
	t.Setenv("VIDEO_CLOUD_LOGGER_ENDPOINT", logger.URL)
	outDir := filepath.Join(workspace, "billing")

	var runErr error
	output := captureStdout(t, func() {
		runErr = runStagingE2EBillingVerify([]string{
			"--workspace", workspace,
			"--env-root", envRoot,
			"--test-data-db", dbPath,
			"--out-dir", outDir,
			"--checks", "log",
			"--timeout", "1s",
		})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(output, `"overall":"pass"`) {
		t.Fatalf("output = %s", output)
	}
	body, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"publish_bytes": 100`) || !strings.Contains(string(body), `"delivery_count": 2`) {
		t.Fatalf("summary = %s", body)
	}
}

func TestBillingHelpersRejectMissingDataAndParseGeneratedAt(t *testing.T) {
	if _, err := billingBrandCloudID(filepath.Join(t.TempDir(), "missing.sqlite")); err == nil {
		t.Fatal("missing billing database unexpectedly passed")
	}
	if _, err := envValue([]string{"TOKEN="}, "TOKEN"); err == nil {
		t.Fatal("empty secret unexpectedly passed")
	}
	path := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(path, []byte(`{"generated_at":"2026-07-24T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := billingResultsGeneratedAt(path)
	if err != nil || !got.Equal(time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("generated at = %v, %v", got, err)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
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

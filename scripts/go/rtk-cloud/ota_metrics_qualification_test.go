package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	otaMetricsTestEnvironment = "staging"
	otaMetricsTestBucket      = "rtk-ota-firmware-staging-sg-sin-2"
	otaMetricsTestRegion      = "sg-sin-2"
	otaMetricsTestEndpoint    = "https://sg-sin-1.linodeobjects.com"
)

func writeOTAMetricsReceipt(t *testing.T, root string, receipt otaMetricsQualification) {
	t.Helper()
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", "ota-metrics-qualification.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func otaMetricsMatrixFixture(t *testing.T, bucketHostname, endpointHost string, at time.Time, gets, bytes int64) []byte {
	t.Helper()
	data := map[string]any{
		"status": "success", "isPartial": false, "stats": map[string]any{"seriesFetched": "2"},
		"data": map[string]any{"resultType": "matrix", "result": []any{
			map[string]any{"metric": map[string]any{"entity_id": bucketHostname, "endpoint": endpointHost, "metric_name": "sum_obj_requests_get"}, "values": []any{[]any{at.Unix(), fmt.Sprint(gets)}}},
			map[string]any{"metric": map[string]any{"entity_id": bucketHostname, "endpoint": endpointHost, "metric_name": "sum_obj_bytes_downloaded"}, "values": []any{[]any{at.Unix(), fmt.Sprint(bytes)}}},
		}},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func otaMetricsFixture(t *testing.T, now time.Time) (string, otaMetricsQualification) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"state", filepath.Join("artifacts", "ota-metrics")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	archive := otaMetricsMatrixFixture(t, otaMetricsTestBucket+".sg-sin-1.linodeobjects.com", "sg-sin-1.linodeobjects.com", now.Add(-90*time.Minute), 2, 4096)
	relative := filepath.Join("artifacts", "ota-metrics", "sample.json")
	if err := os.WriteFile(filepath.Join(root, relative), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	receipt := otaMetricsQualification{
		Source:                "akamai_cloud_pulse",
		Environment:           otaMetricsTestEnvironment,
		Bucket:                otaMetricsTestBucket,
		BucketHostname:        otaMetricsTestBucket + ".sg-sin-1.linodeobjects.com",
		Region:                otaMetricsTestRegion,
		Endpoint:              otaMetricsTestEndpoint,
		WindowStart:           now.Add(-2 * time.Hour).Format(time.RFC3339),
		WindowEnd:             now.Add(-time.Hour).Format(time.RFC3339),
		ExportedAt:            now.Add(-time.Hour).UTC().Format(time.RFC3339),
		RecordedBy:            "staging-operator",
		ExportFile:            relative,
		ExportSHA256:          hex.EncodeToString(sum[:]),
		GETMetric:             "obj_requests_get",
		GETRequests:           2,
		DownloadedBytesMetric: "obj_bytes_downloaded",
		DownloadedBytes:       4096,
	}
	writeOTAMetricsReceipt(t, root, receipt)
	return root, receipt
}

func TestOTAMetricsQualificationAcceptsMatchingRecentArchive(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	root, _ := otaMetricsFixture(t, now)
	if err := validateOTAMetricsQualification(root, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, now); err != nil {
		t.Fatal(err)
	}
}

func TestOTAMetricsQualificationFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		change func(*otaMetricsQualification)
	}{
		{"wrong source", func(r *otaMetricsQualification) { r.Source = "other" }},
		{"wrong environment", func(r *otaMetricsQualification) { r.Environment = "prod" }},
		{"wrong bucket", func(r *otaMetricsQualification) { r.Bucket = "other" }},
		{"wrong bucket hostname", func(r *otaMetricsQualification) { r.BucketHostname = "other.example" }},
		{"wrong region", func(r *otaMetricsQualification) { r.Region = "us-sea" }},
		{"wrong endpoint", func(r *otaMetricsQualification) { r.Endpoint = "https://other.example" }},
		{"missing window", func(r *otaMetricsQualification) { r.WindowStart = "" }},
		{"old window", func(r *otaMetricsQualification) { r.WindowEnd = now.Add(-74 * time.Hour).Format(time.RFC3339) }},
		{"stale export", func(r *otaMetricsQualification) { r.ExportedAt = now.Add(-73 * time.Hour).Format(time.RFC3339) }},
		{"future export", func(r *otaMetricsQualification) { r.ExportedAt = now.Add(time.Minute).Format(time.RFC3339) }},
		{"missing operator", func(r *otaMetricsQualification) { r.RecordedBy = "" }},
		{"wrong GET metric", func(r *otaMetricsQualification) { r.GETMetric = "obj_requests_put" }},
		{"zero GET requests", func(r *otaMetricsQualification) { r.GETRequests = 0 }},
		{"wrong GET count", func(r *otaMetricsQualification) { r.GETRequests = 3 }},
		{"wrong bytes metric", func(r *otaMetricsQualification) { r.DownloadedBytesMetric = "obj_bytes_uploaded" }},
		{"zero downloaded bytes", func(r *otaMetricsQualification) { r.DownloadedBytes = 0 }},
		{"wrong byte count", func(r *otaMetricsQualification) { r.DownloadedBytes = 4097 }},
		{"wrong digest", func(r *otaMetricsQualification) { r.ExportSHA256 = strings.Repeat("0", 64) }},
		{"missing export", func(r *otaMetricsQualification) {
			r.ExportFile = filepath.Join("artifacts", "ota-metrics", "missing.json")
		}},
		{"path escape", func(r *otaMetricsQualification) { r.ExportFile = "../other.prom" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, receipt := otaMetricsFixture(t, now)
			tc.change(&receipt)
			writeOTAMetricsReceipt(t, root, receipt)
			if err := validateOTAMetricsQualification(root, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, now); err == nil {
				t.Fatal("invalid OTA metrics evidence was accepted")
			}
		})
	}
	t.Run("missing receipt", func(t *testing.T) {
		if err := validateOTAMetricsQualification(t.TempDir(), otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, now); err == nil {
			t.Fatal("missing OTA metrics receipt was accepted")
		}
	})
	t.Run("tampered archive", func(t *testing.T) {
		root, receipt := otaMetricsFixture(t, now)
		if err := os.WriteFile(filepath.Join(root, receipt.ExportFile), []byte("unrelated evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateOTAMetricsQualification(root, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, now); err == nil {
			t.Fatal("tampered OTA metrics archive was accepted")
		}
	})
	t.Run("archive symlink escape", func(t *testing.T) {
		root, receipt := otaMetricsFixture(t, now)
		outside := filepath.Join(t.TempDir(), "outside.json")
		body, err := os.ReadFile(filepath.Join(root, receipt.ExportFile))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, receipt.ExportFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, receipt.ExportFile)); err != nil {
			t.Fatal(err)
		}
		if err := validateOTAMetricsQualification(root, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, now); err == nil {
			t.Fatal("OTA metrics archive symlink escape was accepted")
		}
	})
}

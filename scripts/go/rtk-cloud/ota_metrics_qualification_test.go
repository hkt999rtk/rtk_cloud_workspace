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

func otaMetricsProbeFixture(environment, bucket, region, endpoint string, start, end time.Time, gets, downloaded int64) otaMetricsProbe {
	digest := sha256.Sum256([]byte("verified-test-fixture"))
	return otaMetricsProbe{
		Version: 1, Environment: environment, Bucket: bucket, Region: region, Endpoint: endpoint,
		StartedAt: start.Add(time.Second).Format(time.RFC3339Nano), CompletedAt: start.Add(2 * time.Second).Format(time.RFC3339Nano),
		WindowStart: start.Format(time.RFC3339), WindowEnd: end.Format(time.RFC3339),
		FixtureSHA256: hex.EncodeToString(digest[:]), SuccessfulGETRequests: gets, SuccessfulDownloadedBytes: downloaded,
	}
}

func writeOTAMetricsProbeFixture(t *testing.T, path string, proof otaMetricsProbe) []byte {
	t.Helper()
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return body
}

func attachOTAMetricsProbeFixture(t *testing.T, root string, receipt otaMetricsQualification) otaMetricsQualification {
	t.Helper()
	start, _ := time.Parse(time.RFC3339, receipt.WindowStart)
	end, _ := time.Parse(time.RFC3339, receipt.WindowEnd)
	receipt.ProbeFile = filepath.Join("artifacts", "ota-metrics", "sample-probe.json")
	body := writeOTAMetricsProbeFixture(t, filepath.Join(root, receipt.ProbeFile), otaMetricsProbeFixture(receipt.Environment, receipt.Bucket, receipt.Region, receipt.Endpoint, start, end, receipt.GETRequests, receipt.DownloadedBytes))
	sum := sha256.Sum256(body)
	receipt.ProbeSHA256 = hex.EncodeToString(sum[:])
	return receipt
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
	receipt = attachOTAMetricsProbeFixture(t, root, receipt)
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

func TestOTAMetricsProbeRejectsMismatchedOrIncompleteProof(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	for _, tc := range []struct {
		name   string
		change func(*otaMetricsProbe)
	}{
		{"version", func(p *otaMetricsProbe) { p.Version = 2 }},
		{"environment", func(p *otaMetricsProbe) { p.Environment = "dev" }},
		{"bucket", func(p *otaMetricsProbe) { p.Bucket = "other-bucket" }},
		{"region", func(p *otaMetricsProbe) { p.Region = "us-sea" }},
		{"endpoint", func(p *otaMetricsProbe) { p.Endpoint = "https://other.example" }},
		{"window start", func(p *otaMetricsProbe) { p.WindowStart = start.Add(time.Minute).Format(time.RFC3339) }},
		{"window end", func(p *otaMetricsProbe) { p.WindowEnd = end.Add(time.Minute).Format(time.RFC3339) }},
		{"started outside window", func(p *otaMetricsProbe) { p.StartedAt = start.Add(-time.Second).Format(time.RFC3339) }},
		{"completed at excluded end", func(p *otaMetricsProbe) { p.CompletedAt = end.Format(time.RFC3339) }},
		{"completion before start", func(p *otaMetricsProbe) { p.CompletedAt = p.StartedAt }},
		{"non UTC start", func(p *otaMetricsProbe) { p.StartedAt = strings.TrimSuffix(p.StartedAt, "Z") + "+00:00" }},
		{"invalid time", func(p *otaMetricsProbe) { p.CompletedAt = "invalid" }},
		{"invalid digest", func(p *otaMetricsProbe) { p.FixtureSHA256 = "123" }},
		{"zero successful GETs", func(p *otaMetricsProbe) { p.SuccessfulGETRequests = 0 }},
		{"negative bytes", func(p *otaMetricsProbe) { p.SuccessfulDownloadedBytes = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof := otaMetricsProbeFixture(otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, start, end, 3, 16777216)
			tc.change(&proof)
			body, _ := json.Marshal(proof)
			if _, err := parseOTAMetricsProbe(body, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, start, end, now); err == nil {
				t.Fatal("invalid controlled probe evidence accepted")
			}
		})
	}
	proof := otaMetricsProbeFixture(otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, start, end, 3, 16777216)
	body, _ := json.Marshal(proof)
	for _, invalid := range [][]byte{append(append([]byte{}, body...), []byte(` {}`)...), []byte(`{"unknown":1}`), nil, make([]byte, (1<<20)+1)} {
		if _, err := parseOTAMetricsProbe(invalid, otaMetricsTestEnvironment, otaMetricsTestBucket, otaMetricsTestRegion, otaMetricsTestEndpoint, start, end, now); err == nil {
			t.Fatal("incomplete, oversized or trailing proof accepted")
		}
	}
}

func TestOTAMetricsProbeFileMustBePrivateRegularAndBounded(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		make func(string) error
	}{
		{"public", func(path string) error { return os.WriteFile(path, []byte(`{}`), 0o644) }},
		{"directory", func(path string) error { return os.Mkdir(path, 0o700) }},
		{"empty", func(path string) error { return os.WriteFile(path, nil, 0o600) }},
		{"oversized", func(path string) error { return os.WriteFile(path, make([]byte, (1<<20)+1), 0o600) }},
		{"missing", func(path string) error { return nil }},
		{"symlink", func(path string) error {
			outside := filepath.Join(root, "target.json")
			if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
				return err
			}
			return os.Symlink(outside, path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if err := tc.make(path); err != nil {
				t.Fatal(err)
			}
			if _, err := readPrivateOTAMetricsProbe(path); err == nil {
				t.Fatal("unsafe probe input accepted")
			}
		})
	}
}

func TestOTAMetricsQualificationRejectsUndercountAndTamperedProbe(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for _, downloaded := range []int64{74, 274} {
		root, receipt := otaMetricsFixture(t, now)
		archive := otaMetricsMatrixFixture(t, receipt.BucketHostname, "sg-sin-1.linodeobjects.com", now.Add(-90*time.Minute), 5, downloaded)
		if err := os.WriteFile(filepath.Join(root, receipt.ExportFile), archive, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(archive)
		receipt.ExportSHA256, receipt.GETRequests, receipt.DownloadedBytes = hex.EncodeToString(sum[:]), 5, downloaded
		start, _ := time.Parse(time.RFC3339, receipt.WindowStart)
		end, _ := time.Parse(time.RFC3339, receipt.WindowEnd)
		proof := otaMetricsProbeFixture(receipt.Environment, receipt.Bucket, receipt.Region, receipt.Endpoint, start, end, 3, 16777216)
		body := writeOTAMetricsProbeFixture(t, filepath.Join(root, receipt.ProbeFile), proof)
		probeSum := sha256.Sum256(body)
		receipt.ProbeSHA256 = hex.EncodeToString(probeSum[:])
		writeOTAMetricsReceipt(t, root, receipt)
		if err := validateOTAMetricsQualification(root, receipt.Environment, receipt.Bucket, receipt.Region, receipt.Endpoint, now); err == nil || !strings.Contains(err.Error(), "undercount") {
			t.Fatalf("%d bytes falsely qualified the verified16MiB probe: %v", downloaded, err)
		}
	}
	root, receipt := otaMetricsFixture(t, now)
	if err := os.WriteFile(filepath.Join(root, receipt.ProbeFile), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAMetricsQualification(root, receipt.Environment, receipt.Bucket, receipt.Region, receipt.Endpoint, now); err == nil || !strings.Contains(err.Error(), "probe SHA-256") {
		t.Fatalf("tampered probe was accepted: %v", err)
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
		{"missing probe", func(r *otaMetricsQualification) { r.ProbeFile = "" }},
		{"probe path escape", func(r *otaMetricsQualification) { r.ProbeFile = "../other.json" }},
		{"wrong probe digest", func(r *otaMetricsQualification) { r.ProbeSHA256 = strings.Repeat("0", 64) }},
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

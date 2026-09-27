package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const otaMetricsQualificationMaxAge = 72 * time.Hour

// otaMetricsQualification is an operator attestation about a short Cloud Pulse
// export. The export itself is archived under runtime/artifacts/ota-metrics/.
type otaMetricsQualification struct {
	Source                string `json:"source"`
	Environment           string `json:"environment"`
	Bucket                string `json:"bucket"`
	Region                string `json:"region"`
	Endpoint              string `json:"endpoint"`
	ExportedAt            string `json:"exported_at"`
	RecordedBy            string `json:"recorded_by"`
	ExportFile            string `json:"export_file"`
	ExportSHA256          string `json:"export_sha256"`
	GETMetric             string `json:"get_metric"`
	GETRequests           int64  `json:"get_requests"`
	DownloadedBytesMetric string `json:"downloaded_bytes_metric"`
	DownloadedBytes       int64  `json:"downloaded_bytes"`
}

// validateOTAMetricsQualification checks a current operator attestation and the
// archived export digest before billable OTA direct delivery. It does not parse
// provider metric series or authenticate Cloud Pulse provenance: the named
// operator must compare the recorded counts to the export. endpoint is the
// HTTPS URL from live bucket inventory; now permits deterministic tests.
func validateOTAMetricsQualification(runtimeRoot, environment, bucket, region, endpoint string, now time.Time) error {
	if runtimeRoot == "" || environment == "" || bucket == "" || region == "" || endpoint == "" || now.IsZero() {
		return fmt.Errorf("OTA metrics qualification requires a complete target and current time")
	}
	body, err := os.ReadFile(filepath.Join(runtimeRoot, "state", "ota-metrics-qualification.json"))
	if err != nil {
		return fmt.Errorf("OTA metrics qualification receipt is missing or unreadable: %w", err)
	}
	var receipt otaMetricsQualification
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return fmt.Errorf("OTA metrics qualification receipt is invalid: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("OTA metrics qualification receipt contains trailing data")
	}
	if receipt.Source != "akamai_cloud_pulse" || receipt.Environment != environment || receipt.Bucket != bucket || receipt.Region != region {
		return fmt.Errorf("OTA metrics qualification does not match the selected source, environment, bucket, or region")
	}
	wantEndpoint, wantErr := normalizeLinodeS3Endpoint(endpoint)
	gotEndpoint, gotErr := normalizeLinodeS3Endpoint(receipt.Endpoint)
	if wantErr != nil || gotErr != nil || gotEndpoint != wantEndpoint {
		return fmt.Errorf("OTA metrics qualification endpoint does not match the live bucket")
	}
	observed, err := time.Parse(time.RFC3339, receipt.ExportedAt)
	if err != nil || observed.After(now) || now.Sub(observed) > otaMetricsQualificationMaxAge {
		return fmt.Errorf("OTA metrics qualification export time is missing, future, or older than 72 hours")
	}
	if strings.TrimSpace(receipt.RecordedBy) == "" || receipt.GETMetric != "obj_requests_get" || receipt.DownloadedBytesMetric != "obj_bytes_downloaded" || receipt.GETRequests <= 0 || receipt.DownloadedBytes <= 0 {
		return fmt.Errorf("OTA metrics qualification requires an operator and positive GET/downloaded-byte metrics")
	}
	archive, err := otaMetricsArchivePath(runtimeRoot, receipt.ExportFile)
	if err != nil {
		return err
	}
	archiveBody, err := os.ReadFile(archive)
	if err != nil {
		return fmt.Errorf("OTA metrics qualification export is missing or unreadable: %w", err)
	}
	if len(archiveBody) == 0 || len(archiveBody) > 10<<20 {
		return fmt.Errorf("OTA metrics qualification export must be nonempty and at most 10 MiB")
	}
	parsedEndpoint, _ := url.Parse(wantEndpoint)
	for _, required := range []string{receipt.GETMetric, receipt.DownloadedBytesMetric, bucket, parsedEndpoint.Host} {
		if !bytes.Contains(archiveBody, []byte(required)) {
			return fmt.Errorf("OTA metrics qualification export lacks required metric or bucket/endpoint dimension")
		}
	}
	if len(receipt.ExportSHA256) != 64 {
		return fmt.Errorf("OTA metrics qualification export SHA-256 is invalid")
	}
	if _, err := hex.DecodeString(receipt.ExportSHA256); err != nil {
		return fmt.Errorf("OTA metrics qualification export SHA-256 is invalid")
	}
	sum := sha256.Sum256(archiveBody)
	if !strings.EqualFold(receipt.ExportSHA256, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("OTA metrics qualification export SHA-256 does not match archived bytes")
	}
	return nil
}

func otaMetricsArchivePath(runtimeRoot, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("OTA metrics qualification export path must be relative")
	}
	archiveRoot := filepath.Join(runtimeRoot, "artifacts", "ota-metrics")
	path := filepath.Join(runtimeRoot, filepath.Clean(relative))
	within := func(root, candidate string) bool {
		rel, err := filepath.Rel(root, candidate)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if !within(archiveRoot, path) {
		return "", fmt.Errorf("OTA metrics qualification export must be under artifacts/ota-metrics")
	}
	realRoot, rootErr := filepath.EvalSymlinks(archiveRoot)
	realPath, pathErr := filepath.EvalSymlinks(path)
	if rootErr != nil || pathErr != nil || !within(realRoot, realPath) {
		return "", fmt.Errorf("OTA metrics qualification export path is missing or escapes the archive")
	}
	return realPath, nil
}

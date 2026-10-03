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
	BucketHostname        string `json:"bucket_hostname"`
	Region                string `json:"region"`
	Endpoint              string `json:"endpoint"`
	WindowStart           string `json:"window_start"`
	WindowEnd             string `json:"window_end"`
	ExportedAt            string `json:"exported_at"`
	RecordedBy            string `json:"recorded_by"`
	ExportFile            string `json:"export_file"`
	ExportSHA256          string `json:"export_sha256"`
	GETMetric             string `json:"get_metric"`
	GETRequests           int64  `json:"get_requests"`
	DownloadedBytesMetric string `json:"downloaded_bytes_metric"`
	DownloadedBytes       int64  `json:"downloaded_bytes"`
	ProbeFile             string `json:"probe_file"`
	ProbeSHA256           string `json:"probe_sha256"`
}

// otaMetricsProbe is the operator's record of successfully verified GetObject
// bodies. LIST responses and rejected GETs do not contribute to these totals.
type otaMetricsProbe struct {
	Version                   int    `json:"version"`
	Environment               string `json:"environment"`
	Bucket                    string `json:"bucket"`
	Region                    string `json:"region"`
	Endpoint                  string `json:"endpoint"`
	StartedAt                 string `json:"started_at"`
	CompletedAt               string `json:"completed_at"`
	WindowStart               string `json:"window_start"`
	WindowEnd                 string `json:"window_end"`
	FixtureSHA256             string `json:"fixture_sha256"`
	SuccessfulGETRequests     int64  `json:"successful_get_requests"`
	SuccessfulDownloadedBytes int64  `json:"successful_downloaded_bytes"`
}

func parseOTAMetricsProbe(body []byte, environment, bucket, region, endpoint string, start, end, now time.Time) (otaMetricsProbe, error) {
	var proof otaMetricsProbe
	if len(body) == 0 || len(body) > 1<<20 {
		return proof, fmt.Errorf("OTA probe evidence must be nonempty and at most 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return proof, fmt.Errorf("OTA probe evidence is invalid: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return proof, fmt.Errorf("OTA probe evidence contains trailing data")
	}
	wantEndpoint, wantErr := normalizeLinodeS3Endpoint(endpoint)
	gotEndpoint, gotErr := normalizeLinodeS3Endpoint(proof.Endpoint)
	if proof.Version != 1 || proof.Environment != environment || proof.Bucket != bucket || proof.Region != region ||
		wantErr != nil || gotErr != nil || gotEndpoint != wantEndpoint {
		return proof, fmt.Errorf("OTA probe evidence does not match the selected version, environment, bucket, region or endpoint")
	}
	started, startedErr := time.Parse(time.RFC3339Nano, proof.StartedAt)
	completed, completedErr := time.Parse(time.RFC3339Nano, proof.CompletedAt)
	if proof.WindowStart != start.Format(time.RFC3339) || proof.WindowEnd != end.Format(time.RFC3339) ||
		startedErr != nil || completedErr != nil || !strings.HasSuffix(proof.StartedAt, "Z") || !strings.HasSuffix(proof.CompletedAt, "Z") ||
		started.Before(start) || !started.Before(completed) || !completed.Before(end) || completed.After(now) {
		return proof, fmt.Errorf("OTA probe evidence must match the export window and contain a completed UTC probe within [start,end)")
	}
	digest, digestErr := hex.DecodeString(proof.FixtureSHA256)
	if digestErr != nil || len(digest) != sha256.Size || proof.FixtureSHA256 != strings.ToLower(proof.FixtureSHA256) ||
		proof.SuccessfulGETRequests <= 0 || proof.SuccessfulDownloadedBytes <= 0 {
		return proof, fmt.Errorf("OTA probe evidence requires a fixture SHA-256 and positive verified GetObject body totals")
	}
	return proof, nil
}

func validateOTAMetricsProbeCounts(proof otaMetricsProbe, gets, downloaded int64) error {
	if gets < proof.SuccessfulGETRequests || downloaded < proof.SuccessfulDownloadedBytes {
		return fmt.Errorf("OTA provider metrics undercount the verified probe: GET=%d (need at least %d), downloaded_bytes=%d (need at least %d)", gets, proof.SuccessfulGETRequests, downloaded, proof.SuccessfulDownloadedBytes)
	}
	return nil
}

// validateOTAMetricsQualification checks the current operator attestation,
// archived OTA-only provider series, and exact bucket totals before direct delivery.
// It cannot authenticate provider provenance independently of the operator's
// archived export. endpoint is the HTTPS URL from live bucket inventory.
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
	start, end, err := parseOTAMetricsWindow(receipt.WindowStart, receipt.WindowEnd, now)
	if err != nil || end.After(observed) || now.Sub(end) > otaMetricsQualificationMaxAge {
		return fmt.Errorf("OTA metrics qualification window is invalid, too old, or later than its export")
	}
	parsedEndpoint, _ := url.Parse(wantEndpoint)
	if receipt.BucketHostname != bucket+"."+parsedEndpoint.Host {
		return fmt.Errorf("OTA metrics qualification bucket hostname does not match the target")
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
	gets, downloaded, err := parseOTAMetricsMatrix(archiveBody, receipt.BucketHostname, parsedEndpoint.Host, start, end)
	if err != nil {
		return fmt.Errorf("OTA metrics qualification archived bucket series is invalid: %w", err)
	}
	if gets != receipt.GETRequests || downloaded != receipt.DownloadedBytes {
		return fmt.Errorf("OTA metrics qualification archived bucket counts disagree with the receipt")
	}
	probePath, err := otaMetricsArchivePath(runtimeRoot, receipt.ProbeFile)
	if err != nil {
		return fmt.Errorf("OTA metrics qualification probe archive: %w", err)
	}
	probeBody, err := readPrivateOTAMetricsProbe(probePath)
	if err != nil {
		return err
	}
	probeSum := sha256.Sum256(probeBody)
	if receipt.ProbeSHA256 != hex.EncodeToString(probeSum[:]) {
		return fmt.Errorf("OTA metrics qualification probe SHA-256 does not match archived bytes")
	}
	proof, err := parseOTAMetricsProbe(probeBody, environment, bucket, region, endpoint, start, end, observed)
	if err != nil {
		return err
	}
	if err := validateOTAMetricsProbeCounts(proof, gets, downloaded); err != nil {
		return err
	}
	return nil
}

func readPrivateOTAMetricsProbe(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("OTA probe evidence must be a private regular file of at most 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("OTA probe evidence could not be opened: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("OTA probe evidence changed while being opened")
	}
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(body) == 0 || len(body) > 1<<20 {
		return nil, fmt.Errorf("OTA probe evidence could not be read completely within its size limit")
	}
	return body, nil
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

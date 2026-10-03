package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const otaMetricsMaxWindow = 24 * time.Hour

// Cloud Pulse retains Object Storage metrics for 93 days and its query API
// accepts absolute UTC windows. Keep qualification exports small and recent.
func parseOTAMetricsWindow(startText, endText string, now time.Time) (time.Time, time.Time, error) {
	start, startErr := time.Parse(time.RFC3339, startText)
	end, endErr := time.Parse(time.RFC3339, endText)
	if startErr != nil || endErr != nil || start.Format(time.RFC3339) != startText || end.Format(time.RFC3339) != endText ||
		!strings.HasSuffix(startText, "Z") || !strings.HasSuffix(endText, "Z") || start.Second() != 0 || end.Second() != 0 ||
		end.Sub(start) < time.Minute || end.Sub(start) > otaMetricsMaxWindow ||
		end.After(now) || start.Before(now.Add(-93*24*time.Hour)) {
		return time.Time{}, time.Time{}, errors.New("OTA metrics window must be a past, minute-aligned UTC interval of 1 minute to 24 hours within Cloud Pulse retention")
	}
	return start, end, nil
}

// selectOTAMetricsMatrix discards other buckets' series before any provider
// response is archived. Object Storage Cloud Pulse currently issues only an
// account-wide service token and does not accept an entity_id query filter.
func selectOTAMetricsMatrix(raw []byte, bucketHostname, endpointHost string, end time.Time) ([]byte, error) {
	var response struct {
		Status    string `json:"status"`
		IsPartial *bool  `json:"isPartial"`
		Data      struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]json.RawMessage `json:"metric"`
				Values [][]json.RawMessage        `json:"values"`
			} `json:"result"`
		} `json:"data"`
		Stats struct {
			SeriesFetched string `json:"seriesFetched"`
		} `json:"stats"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Cloud Pulse response: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("Cloud Pulse response has trailing data")
	}
	if response.Status != "success" || response.IsPartial == nil || *response.IsPartial ||
		response.Data.ResultType != "matrix" || strings.TrimSpace(response.Stats.SeriesFetched) == "" {
		return nil, errors.New("Cloud Pulse returned incomplete or unexpected metric series")
	}
	selected := response.Data.Result[:0:0]
	for _, series := range response.Data.Result {
		var entityID, endpoint string
		if json.Unmarshal(series.Metric["entity_id"], &entityID) == nil &&
			json.Unmarshal(series.Metric["endpoint"], &endpoint) == nil &&
			entityID == bucketHostname && endpoint == endpointHost {
			// The provider can include its inclusive end boundary. Archive only
			// [start,end); every other out-of-window point still fails validation.
			points := series.Values[:0:0]
			for _, point := range series.Values {
				var epoch int64
				if len(point) == 2 && json.Unmarshal(point[0], &epoch) == nil && epoch == end.Unix() {
					continue
				}
				points = append(points, point)
			}
			series.Values = points
			selected = append(selected, series)
		}
	}
	// Rebuild a narrow snapshot; never persist account-wide stats or unrelated
	// bucket series. The exact two selected metrics are validated by the caller.
	return json.Marshal(struct {
		Status    string `json:"status"`
		IsPartial bool   `json:"isPartial"`
		Data      any    `json:"data"`
		Stats     any    `json:"stats"`
	}{"success", false, struct {
		ResultType string `json:"resultType"`
		Result     any    `json:"result"`
	}{"matrix", selected}, struct {
		SeriesFetched string `json:"seriesFetched"`
	}{fmt.Sprint(len(selected))}})
}

// parseOTAMetricsMatrix accepts only the two requested sum series for the
// dedicated bucket and endpoint. Cloud Pulse returns minute values as strings.
func parseOTAMetricsMatrix(raw []byte, bucketHostname, endpointHost string, start, end time.Time) (int64, int64, error) {
	var response struct {
		Status    string `json:"status"`
		IsPartial *bool  `json:"isPartial"`
		Data      struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]json.RawMessage `json:"metric"`
				Values [][]json.RawMessage        `json:"values"`
			} `json:"result"`
		} `json:"data"`
		Stats struct {
			SeriesFetched string `json:"seriesFetched"`
		} `json:"stats"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&response); err != nil {
		return 0, 0, fmt.Errorf("decode Cloud Pulse response: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return 0, 0, errors.New("Cloud Pulse response has trailing data")
	}
	if response.Status != "success" || response.IsPartial == nil || *response.IsPartial || response.Data.ResultType != "matrix" || strings.TrimSpace(response.Stats.SeriesFetched) == "" || len(response.Data.Result) != 2 {
		return 0, 0, errors.New("Cloud Pulse returned incomplete or unexpected metric series")
	}
	want := map[string]bool{"obj_requests_get": true, "obj_bytes_downloaded": true}
	counts := make(map[string]int64, 2)
	for _, series := range response.Data.Result {
		var entityID, endpoint, metricName string
		for _, field := range []struct {
			key    string
			target *string
		}{{"entity_id", &entityID}, {"endpoint", &endpoint}, {"metric_name", &metricName}} {
			value, present := series.Metric[field.key]
			if !present || json.Unmarshal(value, field.target) != nil {
				return 0, 0, fmt.Errorf("Cloud Pulse series lacks a valid %s label", field.key)
			}
		}
		if entityID != bucketHostname || endpoint != endpointHost {
			return 0, 0, errors.New("Cloud Pulse series is not for the exact OTA bucket and endpoint")
		}
		name := strings.TrimPrefix(metricName, "sum_")
		if !want[name] || (metricName != name && metricName != "sum_"+name) {
			return 0, 0, errors.New("Cloud Pulse returned an unrequested metric")
		}
		if _, duplicate := counts[name]; duplicate {
			return 0, 0, errors.New("Cloud Pulse returned duplicate bucket metric series")
		}
		seenTimes := map[int64]bool{}
		var total int64
		for _, pair := range series.Values {
			if len(pair) != 2 {
				return 0, 0, errors.New("Cloud Pulse metric point is not a timestamp/value pair")
			}
			var epoch int64
			var number string
			if json.Unmarshal(pair[0], &epoch) != nil || json.Unmarshal(pair[1], &number) != nil {
				return 0, 0, errors.New("Cloud Pulse metric point has invalid timestamp or value")
			}
			stamp := time.Unix(epoch, 0).UTC()
			if stamp.Before(start) || !stamp.Before(end) || seenTimes[epoch] {
				return 0, 0, errors.New("Cloud Pulse metric point is outside the UTC window or duplicated")
			}
			seenTimes[epoch] = true
			// Cloud Pulse can include an empty placeholder for an inactive minute.
			// It is missing data, not a measured zero or a fractional count.
			if number == "" {
				continue
			}
			value, ok := new(big.Rat).SetString(number)
			if !ok || !value.IsInt() || value.Sign() < 0 || !value.Num().IsInt64() {
				return 0, 0, fmt.Errorf("Cloud Pulse %s point at %s must be a nonnegative whole count; got %q", name, stamp.Format(time.RFC3339), number)
			}
			part := value.Num().Int64()
			if part > math.MaxInt64-total {
				return 0, 0, errors.New("Cloud Pulse metric sum overflows int64")
			}
			total += part
		}
		counts[name] = total
	}
	if len(counts) != 2 {
		return 0, 0, errors.New("Cloud Pulse omitted an OTA bucket metric")
	}
	return counts["obj_requests_get"], counts["obj_bytes_downloaded"], nil
}

func runDeploymentOTAMetricsExport(cfg deploymentConfig, environmentFile, startText, endText, recorder, probeFile string) error {
	if cfg.Storage.OTAMode != "dedicated" || strings.TrimSpace(recorder) == "" {
		return errors.New("OTA metrics export requires dedicated OTA storage and --recorded-by")
	}
	values, check := deploymentCredentialProfileValues(cfg.Environment, environmentFile, defaultDeploymentSharedCredentialFile())
	if !check.Passed {
		return errors.New(check.Detail)
	}
	return defaultDeploymentCredentialChecker().exportOTAMetrics(cfg, values["LINODE_TOKEN"], startText, endText, recorder, probeFile, time.Now().UTC())
}

func (c deploymentCredentialChecker) exportOTAMetrics(cfg deploymentConfig, linodeToken, startText, endText, recorder, probeFile string, now time.Time) error {
	start, end, err := parseOTAMetricsWindow(startText, endText, now)
	if err != nil {
		return err
	}
	if now.Sub(end) > otaMetricsQualificationMaxAge || strings.TrimSpace(recorder) == "" {
		return errors.New("OTA cutover metric window must end within 72 hours and name its operator")
	}
	if strings.TrimSpace(linodeToken) == "" {
		return errors.New("LINODE_TOKEN is required for Cloud Pulse export")
	}
	probeBody, err := readPrivateOTAMetricsProbe(probeFile)
	if err != nil {
		return err
	}
	bucket, err := c.resolveStorageBucket(linodeToken, cfg.Storage.OTAFirmware)
	if err != nil {
		return err
	}
	if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	parsedEndpoint, _ := url.Parse(endpoint)
	bucketHostname := bucket.Label + "." + parsedEndpoint.Host
	if bucket.Hostname != bucketHostname {
		return errors.New("OTA bucket inventory hostname does not match its selected endpoint")
	}
	proof, err := parseOTAMetricsProbe(probeBody, cfg.Environment, bucket.Label, cfg.Storage.OTAFirmware.Region, endpoint, start, end, now)
	if err != nil {
		return err
	}
	// The provider rejects entity_ids for Object Storage. The service token is
	// account-wide and remains in memory only for this regional query.
	tokenBody, err := c.linodeAuthorizedRequest(linodeToken, http.MethodPost, "/monitor/services/objectstorage/token", []byte("{}"))
	if err != nil {
		return fmt.Errorf("Cloud Pulse service token request failed: %w", err)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(tokenBody, &issued) != nil || issued.Token == "" || issued.Token != strings.TrimSpace(issued.Token) {
		return errors.New("Cloud Pulse service token response is invalid")
	}
	query, _ := json.Marshal(struct {
		EntityRegion string   `json:"entity_region"`
		GroupBy      []string `json:"group_by"`
		Metrics      []struct {
			Name              string `json:"name"`
			AggregateFunction string `json:"aggregate_function"`
		} `json:"metrics"`
		TimeGranularity struct {
			Unit  string `json:"unit"`
			Value int    `json:"value"`
		} `json:"time_granularity"`
		AbsoluteTimeDuration struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"absolute_time_duration"`
	}{
		EntityRegion: cfg.Storage.OTAFirmware.Region,
		GroupBy:      []string{"entity_id", "endpoint"},
		Metrics: []struct {
			Name              string `json:"name"`
			AggregateFunction string `json:"aggregate_function"`
		}{{"obj_requests_get", "sum"}, {"obj_bytes_downloaded", "sum"}},
		TimeGranularity: struct {
			Unit  string `json:"unit"`
			Value int    `json:"value"`
		}{"min", 1},
		AbsoluteTimeDuration: struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}{startText, endText},
	})
	monitorRoot := c.monitorAPIRoot
	if monitorRoot == "" {
		monitorRoot = "https://monitor-api.linode.com/v2"
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(monitorRoot, "/")+"/monitor/services/objectstorage/metrics", bytes.NewReader(query))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.request(req)
	if err != nil {
		return fmt.Errorf("Cloud Pulse bucket metrics request failed: %w", err)
	}
	selected, err := selectOTAMetricsMatrix(response, bucketHostname, parsedEndpoint.Host, end)
	if err != nil {
		return err
	}
	response = nil // Drop the account-wide response before writing an archive.
	gets, downloaded, err := parseOTAMetricsMatrix(selected, bucketHostname, parsedEndpoint.Host, start, end)
	if err != nil {
		return err
	}
	if gets <= 0 || downloaded <= 0 {
		return errors.New("controlled OTA signed GET must yield positive bucket GET and downloaded-byte metrics")
	}
	if err := validateOTAMetricsProbeCounts(proof, gets, downloaded); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.RuntimeRoot, "artifacts", "ota-metrics"), 0o700); err != nil {
		return err
	}
	relative := filepath.Join("artifacts", "ota-metrics", fmt.Sprintf("qualification-%d-%d.json", start.Unix(), now.UnixNano()))
	archivePath := filepath.Join(cfg.RuntimeRoot, relative)
	if err := writeOTAMetricsArchive(archivePath, selected); err != nil {
		return err
	}
	probeRelative := strings.TrimSuffix(relative, ".json") + "-probe.json"
	if err := writeOTAMetricsArchive(filepath.Join(cfg.RuntimeRoot, probeRelative), probeBody); err != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("OTA probe archive could not be written: %w", err)
	}
	sum := sha256.Sum256(selected)
	probeSum := sha256.Sum256(probeBody)
	receipt := otaMetricsQualification{
		Source: "akamai_cloud_pulse", Environment: cfg.Environment, Bucket: bucket.Label,
		BucketHostname: bucketHostname, Region: cfg.Storage.OTAFirmware.Region, Endpoint: endpoint,
		WindowStart: startText, WindowEnd: endText, ExportedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339),
		RecordedBy: recorder, ExportFile: relative, ExportSHA256: hex.EncodeToString(sum[:]),
		GETMetric: "obj_requests_get", GETRequests: gets,
		DownloadedBytesMetric: "obj_bytes_downloaded", DownloadedBytes: downloaded,
		ProbeFile: probeRelative, ProbeSHA256: hex.EncodeToString(probeSum[:]),
	}
	if err := writeOTAMetricsQualificationReceipt(cfg.RuntimeRoot, receipt); err != nil {
		return err
	}
	if err := validateOTAMetricsQualification(cfg.RuntimeRoot, cfg.Environment, bucket.Label, cfg.Storage.OTAFirmware.Region, endpoint, now); err != nil {
		return err
	}
	fmt.Printf("OTA Cloud Pulse qualification: bucket=%s window=%s..%s GET=%d downloaded_bytes=%d archive=%s\n", bucket.Label, startText, endText, gets, downloaded, relative)
	return nil
}

func writeOTAMetricsArchive(path string, body []byte) error {
	archive, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := archive.Write(body)
	if writeErr == nil {
		writeErr = archive.Sync()
	}
	closeErr := archive.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("OTA metrics archive could not be written completely")
	}
	return nil
}

func writeOTAMetricsQualificationReceipt(runtimeRoot string, receipt otaMetricsQualification) error {
	stateDir := filepath.Join(runtimeRoot, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(stateDir, ".ota-metrics-qualification-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(body, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, filepath.Join(stateDir, "ota-metrics-qualification.json"))
}

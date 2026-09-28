package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExportOTAMetricsArchivesExactBucketResponse(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	var server *httptest.Server
	seen := map[string]int{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		endpointHost := strings.TrimPrefix(server.URL, "http://")
		bucketHostname := otaMetricsTestBucket + "." + endpointHost
		switch r.URL.Path {
		case "/v4/object-storage/buckets":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer linode-pat" {
				t.Errorf("bucket inventory request = %s %q", r.Method, r.Header.Get("Authorization"))
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":%q,"hostname":%q,"s3_endpoint":%q,"endpoint_type":"E3"}]}`, otaMetricsTestBucket, otaMetricsTestRegion, bucketHostname, server.URL)
		case "/v4/monitor/services/objectstorage/token":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer linode-pat" {
				t.Errorf("service token request = %s %q", r.Method, r.Header.Get("Authorization"))
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Errorf("Object Storage token request must use the provider's empty body: %v, %v", body, err)
			}
			_, _ = io.WriteString(w, `{"token":"monitor-ephemeral"}`)
		case "/v2/monitor/services/objectstorage/metrics":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer monitor-ephemeral" {
				t.Errorf("metrics request = %s %q", r.Method, r.Header.Get("Authorization"))
			}
			var query struct {
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
			}
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Errorf("decode metrics query: %v", err)
			}
			if query.EntityRegion != otaMetricsTestRegion || !reflect.DeepEqual(query.GroupBy, []string{"entity_id", "endpoint"}) ||
				query.TimeGranularity.Unit != "min" || query.TimeGranularity.Value != 1 ||
				query.AbsoluteTimeDuration.Start != start.Format(time.RFC3339) || query.AbsoluteTimeDuration.End != end.Add(-time.Second).Format(time.RFC3339) ||
				len(query.Metrics) != 2 || query.Metrics[0].Name != "obj_requests_get" || query.Metrics[1].Name != "obj_bytes_downloaded" ||
				query.Metrics[0].AggregateFunction != "sum" || query.Metrics[1].AggregateFunction != "sum" {
				t.Errorf("metrics query is not the exact bucket GET/bytes UTC query: %+v", query)
			}
			_, _ = w.Write(otaMetricsRegionalFixture(t, bucketHostname, endpointHost, start.Add(30*time.Minute)))
		default:
			t.Errorf("unexpected API request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	cfg := deploymentConfig{Environment: otaMetricsTestEnvironment, RuntimeRoot: root,
		Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Bucket: otaMetricsTestBucket, Region: otaMetricsTestRegion}}}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4", monitorAPIRoot: server.URL + "/v2"}
	if err := checker.exportOTAMetrics(cfg, "linode-pat", start.Format(time.RFC3339), end.Format(time.RFC3339), "staging-operator", now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v4/object-storage/buckets", "/v4/monitor/services/objectstorage/token", "/v2/monitor/services/objectstorage/metrics"} {
		if seen[path] != 1 {
			t.Fatalf("request count for %s = %d", path, seen[path])
		}
	}
	var receipt otaMetricsQualification
	contents, err := os.ReadFile(filepath.Join(root, "state", "ota-metrics-qualification.json"))
	if err != nil || json.Unmarshal(contents, &receipt) != nil {
		t.Fatalf("qualification receipt missing: %v", err)
	}
	if receipt.BucketHostname != otaMetricsTestBucket+"."+strings.TrimPrefix(server.URL, "http://") || receipt.GETRequests != 2 || receipt.DownloadedBytes != 4096 || receipt.WindowStart != start.Format(time.RFC3339) || receipt.WindowEnd != end.Format(time.RFC3339) {
		t.Fatalf("qualification receipt mismatch: %+v", receipt)
	}
	archive, err := os.ReadFile(filepath.Join(root, receipt.ExportFile))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	if receipt.ExportSHA256 != hex.EncodeToString(sum[:]) || bytes.Contains(archive, []byte("unrelated-bucket")) || bytes.Contains(archive, []byte("account-wide-marker")) {
		t.Fatal("archive digest mismatch or unrelated account data persisted")
	}
	gets, downloaded, err := parseOTAMetricsMatrix(archive, receipt.BucketHostname, strings.TrimPrefix(server.URL, "http://"), start, end)
	if err != nil || gets != 2 || downloaded != 4096 {
		t.Fatalf("archived OTA-only matrix is invalid: GET=%d bytes=%d error=%v", gets, downloaded, err)
	}
	if err := validateOTAMetricsQualification(root, cfg.Environment, otaMetricsTestBucket, otaMetricsTestRegion, server.URL, now); err != nil {
		t.Fatal(err)
	}
}

func otaMetricsRegionalFixture(t *testing.T, bucketHostname, endpointHost string, at time.Time) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(otaMetricsMatrixFixture(t, bucketHostname, endpointHost, at, 2, 4096), &body); err != nil {
		t.Fatal(err)
	}
	result := body["data"].(map[string]any)["result"].([]any)
	other := map[string]any{"metric": map[string]any{
		"entity_id": "unrelated-bucket." + endpointHost,
		"endpoint":  endpointHost, "metric_name": "sum_obj_requests_get",
	}, "values": []any{[]any{at.Unix(), "100"}}}
	body["data"].(map[string]any)["result"] = append(result, other)
	body["stats"] = map[string]any{"seriesFetched": "3", "otherAccountData": "account-wide-marker"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOTAMetricsMatrixRejectsWrongOrIncompleteEvidence(t *testing.T) {
	start := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	bucketHostname := otaMetricsTestBucket + ".sg-sin-1.linodeobjects.com"
	endpointHost := "sg-sin-1.linodeobjects.com"
	base := otaMetricsMatrixFixture(t, bucketHostname, endpointHost, start.Add(30*time.Minute), 2, 4096)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"partial", func(v map[string]any) { v["isPartial"] = true }},
		{"missing partial flag", func(v map[string]any) { delete(v, "isPartial") }},
		{"missing query stats", func(v map[string]any) { delete(v, "stats") }},
		{"status", func(v map[string]any) { v["status"] = "error" }},
		{"wrong bucket", func(v map[string]any) {
			otaMetricTestSeries(v, 0)["metric"].(map[string]any)["entity_id"] = "another-bucket.example"
		}},
		{"wrong endpoint", func(v map[string]any) {
			otaMetricTestSeries(v, 0)["metric"].(map[string]any)["endpoint"] = "other.example"
		}},
		{"unknown metric", func(v map[string]any) {
			otaMetricTestSeries(v, 0)["metric"].(map[string]any)["metric_name"] = "sum_obj_requests_put"
		}},
		{"duplicate series", func(v map[string]any) {
			otaMetricTestSeries(v, 1)["metric"].(map[string]any)["metric_name"] = "sum_obj_requests_get"
		}},
		{"missing series", func(v map[string]any) {
			v["data"].(map[string]any)["result"] = v["data"].(map[string]any)["result"].([]any)[:1]
		}},
		{"outside window", func(v map[string]any) {
			otaMetricTestSeries(v, 0)["values"].([]any)[0].([]any)[0] = float64(end.Unix())
		}},
		{"duplicate timestamp", func(v map[string]any) {
			point := otaMetricTestSeries(v, 0)["values"].([]any)[0]
			otaMetricTestSeries(v, 0)["values"] = []any{point, point}
		}},
		{"fractional count", func(v map[string]any) { otaMetricTestSeries(v, 0)["values"].([]any)[0].([]any)[1] = "1.5" }},
		{"negative bytes", func(v map[string]any) { otaMetricTestSeries(v, 1)["values"].([]any)[0].([]any)[1] = "-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(base, &value); err != nil {
				t.Fatal(err)
			}
			tc.change(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseOTAMetricsMatrix(mutated, bucketHostname, endpointHost, start, end); err == nil {
				t.Fatal("invalid Cloud Pulse evidence was accepted")
			}
		})
	}
}

func otaMetricTestSeries(value map[string]any, index int) map[string]any {
	return value["data"].(map[string]any)["result"].([]any)[index].(map[string]any)
}

func TestOTAMetricsWindowRejectsFutureOrNonUTC(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for _, pair := range [][2]string{
		{"2026-09-28T06:00:00+00:00", "2026-09-28T07:00:00Z"},
		{"2026-09-28T06:00:00Z", "2026-09-28T09:00:00Z"},
		{"2026-09-26T06:00:00Z", "2026-09-28T07:00:00Z"},
		{"2026-09-28T06:00:01Z", "2026-09-28T07:00:00Z"},
		{"2026-06-01T06:00:00Z", "2026-06-01T07:00:00Z"},
	} {
		if _, _, err := parseOTAMetricsWindow(pair[0], pair[1], now); err == nil {
			t.Fatalf("invalid OTA Cloud Pulse window was accepted: %v", pair)
		}
	}
}

func TestExportOTAMetricsRejectsUnqualifiedProviderEvidence(t *testing.T) {
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, bucketType, hostname, tokenBody, metricsBody, want string
		tokenStatus, metricsStatus                               int
		oldWindow, noToken, blockedArchive, blockedState         bool
	}{
		{name: "stale window", oldWindow: true, want: "within 72 hours"},
		{name: "missing Linode token", noToken: true, want: "LINODE_TOKEN"},
		{name: "unmonitored endpoint", bucketType: "E1", want: "E2/E3"},
		{name: "wrong bucket hostname", hostname: "wrong.example.test", want: "hostname"},
		{name: "service token request failed", tokenStatus: http.StatusForbidden, want: "service token request failed"},
		{name: "invalid service token", tokenBody: `{"token":""}`, want: "service token response is invalid"},
		{name: "metrics request failed", metricsStatus: http.StatusServiceUnavailable, want: "bucket metrics request failed"},
		{name: "partial provider response", metricsBody: `{"status":"success","isPartial":true}`, want: "incomplete"},
		{name: "unrelated bucket only", metricsBody: "other", want: "incomplete"},
		{name: "no controlled GET", metricsBody: "zero", want: "positive bucket GET"},
		{name: "archive directory unavailable", blockedArchive: true, want: "not a directory"},
		{name: "receipt directory unavailable", blockedState: true, want: "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				endpointHost := strings.TrimPrefix(server.URL, "http://")
				bucketHostname := otaMetricsTestBucket + "." + endpointHost
				switch r.URL.Path {
				case "/v4/object-storage/buckets":
					bucketType := tc.bucketType
					if bucketType == "" {
						bucketType = "E3"
					}
					hostname := tc.hostname
					if hostname == "" {
						hostname = bucketHostname
					}
					_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":%q,"hostname":%q,"s3_endpoint":%q,"endpoint_type":%q}]}`, otaMetricsTestBucket, otaMetricsTestRegion, hostname, server.URL, bucketType)
				case "/v4/monitor/services/objectstorage/token":
					if tc.tokenStatus != 0 {
						w.WriteHeader(tc.tokenStatus)
						return
					}
					body := tc.tokenBody
					if body == "" {
						body = `{"token":"monitor-test"}`
					}
					_, _ = io.WriteString(w, body)
				case "/v2/monitor/services/objectstorage/metrics":
					if tc.metricsStatus != 0 {
						w.WriteHeader(tc.metricsStatus)
						return
					}
					if tc.metricsBody == "other" {
						_, _ = w.Write(otaMetricsMatrixFixture(t, "unrelated-bucket."+endpointHost, endpointHost, now.Add(-90*time.Minute), 1, 4096))
					} else if tc.metricsBody == "zero" {
						_, _ = w.Write(otaMetricsMatrixFixture(t, bucketHostname, endpointHost, now.Add(-90*time.Minute), 0, 4096))
					} else if tc.metricsBody != "" {
						_, _ = io.WriteString(w, tc.metricsBody)
					} else {
						_, _ = w.Write(otaMetricsMatrixFixture(t, bucketHostname, endpointHost, now.Add(-90*time.Minute), 1, 4096))
					}
				default:
					t.Errorf("unexpected provider request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			if tc.blockedArchive {
				if err := os.WriteFile(filepath.Join(root, "artifacts"), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.blockedState {
				if err := os.WriteFile(filepath.Join(root, "state"), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := deploymentConfig{Environment: otaMetricsTestEnvironment, RuntimeRoot: root,
				Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Bucket: otaMetricsTestBucket, Region: otaMetricsTestRegion}}}
			checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4", monitorAPIRoot: server.URL + "/v2"}
			start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
			if tc.oldWindow {
				start, end = now.Add(-100*time.Hour), now.Add(-99*time.Hour)
			}
			token := "linode-pat"
			if tc.noToken {
				token = ""
			}
			err := checker.exportOTAMetrics(cfg, token, start.Format(time.RFC3339), end.Format(time.RFC3339), "operator", now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("export error = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "state", "ota-metrics-qualification.json")); !os.IsNotExist(err) && !tc.blockedState {
				t.Fatalf("failed export wrote qualification receipt: %v", err)
			}
			if tc.metricsBody == "other" {
				if _, err := os.Stat(filepath.Join(root, "artifacts", "ota-metrics")); !os.IsNotExist(err) {
					t.Fatalf("unrelated bucket evidence created an archive: %v", err)
				}
			}
		})
	}
}

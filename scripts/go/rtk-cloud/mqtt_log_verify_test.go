package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryMissingK8SMQTTRuntimeLogsScopesLokiCandidatesToLogIngester(t *testing.T) {
	want := mqttLogExpectation{
		DeviceID: "device-1",
		StreamID: "stream-1",
		Seq:      1,
		Source:   "device_client",
		Message:  "mqtt_e2e telemetry device_client publish",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("service"); got != "video_cloud" {
			t.Fatalf("service query = %q, want video_cloud", got)
		}
		if got := r.URL.Query().Get("unit"); got != "video_cloud-logingester.service" {
			t.Fatalf("unit query = %q, want video_cloud-logingester.service", got)
		}
		if got := r.URL.Query().Get("device_id"); got != want.DeviceID {
			t.Fatalf("device_id query = %q, want %q", got, want.DeviceID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{{
				"msg": want.Message,
				"fields": map[string]any{
					"stream_id": want.StreamID,
					"source":    want.Source,
					"seq":       want.Seq,
				},
			}},
		})
	}))
	defer server.Close()

	t.Setenv("VIDEO_CLOUD_LOGGER_ENDPOINT", server.URL)
	t.Setenv("CLOUD_LOGGER_ENDPOINT", "")
	missing, err := queryMissingK8SMQTTRuntimeLogs("unused", "unused", []mqttLogExpectation{want})
	if err != nil {
		t.Fatalf("queryMissingK8SMQTTRuntimeLogs: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %#v, want none", missing)
	}
}

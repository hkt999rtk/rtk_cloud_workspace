package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderMQTTTraceReportFromResults(t *testing.T) {
	results := mqttTraceResults{
		Status:      "PASS",
		Overall:     "pass",
		GeneratedAt: "2026-06-04T09:12:30Z",
		Brandname:   "RTK",
		Profile:     "smoke",
		MQTT: mqttTraceSummary{
			ProbeModel:         "actor_separated_iot",
			ClientIdentityMode: "app_token_and_device_token",
			TelemetryReceiver:  "app_observer",
			CommandReceiver:    "device_client",
			ProbeResult:        "PASS",
		},
		Metrics: map[string]any{"commands_attempted": float64(12), "commands_passed": float64(12)},
		Devices: []mqttTraceDevice{{
			DeviceID:   "rtk-0041",
			DeviceType: "light",
			MQTTStatus: "PASS",
			TraceChain: []mqttTraceStep{
				{Step: 1, Timestamp: "2026-06-04T09:12:30Z", Phase: "app_token", Actor: "app_actor", Action: "request_token", Status: "PASS", Detail: "scope=app"},
				{Step: 8, Timestamp: "2026-06-04T09:12:31Z", Phase: "telemetry", Actor: "device_client", Action: "publish", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report message_id=msg-1"},
				{Step: 9, Timestamp: "2026-06-04T09:12:32Z", Phase: "telemetry", Actor: "app_observer", Action: "receive", Topic: "devices/rtk-0041/up/messages", Status: "PASS", Data: "message_type=status_report message_id=msg-1"},
				{Step: 10, Timestamp: "2026-06-04T09:12:33Z", Phase: "command", Actor: "app_controller", Action: "publish", Topic: "devices/rtk-0041/down/commands", Status: "PASS", Data: "message_type=command command_id=cmd-1"},
				{Step: 11, Timestamp: "2026-06-04T09:12:34Z", Phase: "command", Actor: "device_client", Action: "receive", Topic: "devices/rtk-0041/down/commands", Status: "PASS", Data: "message_type=command command_id=cmd-1"},
			},
		}},
	}

	report := renderMQTTTraceReport(results, "/tmp/results.json")

	for _, want := range []string{
		"# Home MQTT E2E Trace Chain Report",
		"Source results: `/tmp/results.json`",
		"Probe model: `actor_separated_iot`",
		"`device_client` | `publish` | `devices/rtk-0041/up/messages`",
		"message_type=status_report message_id=msg-1",
		"`app_observer` | `receive` | `devices/rtk-0041/up/messages`",
		"`app_controller` | `publish` | `devices/rtk-0041/down/commands`",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	for _, forbidden := range []string{"access_token", "PRIVATE KEY", "BEGIN CERTIFICATE", "bearer"} {
		if strings.Contains(strings.ToLower(report), strings.ToLower(forbidden)) {
			t.Fatalf("report leaked %q:\n%s", forbidden, report)
		}
	}
}

func TestLatestMQTTTraceResultsFileSelectsNewestBrandResult(t *testing.T) {
	root := t.TempDir()
	olderDir := filepath.Join(root, "artifacts", "home-mqtt-loadtest", "20260604T010000Z")
	newerDir := filepath.Join(root, "artifacts", "home-mqtt-loadtest", "20260604T020000Z")
	otherDir := filepath.Join(root, "artifacts", "home-mqtt-loadtest", "20260604T030000Z")
	mkdirAll(t, olderDir)
	mkdirAll(t, newerDir)
	mkdirAll(t, otherDir)
	writeFile(t, filepath.Join(olderDir, "results.json"), `{"brandname":"RTK","generated_at":"2026-06-04T01:00:00Z","devices":[]}`)
	writeFile(t, filepath.Join(newerDir, "results.json"), `{"brandname":"RTK","generated_at":"2026-06-04T02:00:00Z","devices":[]}`)
	writeFile(t, filepath.Join(otherDir, "results.json"), `{"brandname":"OTHER","generated_at":"2026-06-04T03:00:00Z","devices":[]}`)
	oldTime := time.Date(2026, 6, 4, 1, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 6, 4, 2, 0, 0, 0, time.UTC)
	otherTime := time.Date(2026, 6, 4, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(olderDir, "results.json"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(newerDir, "results.json"), newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(otherDir, "results.json"), otherTime, otherTime); err != nil {
		t.Fatal(err)
	}

	got, err := latestMQTTTraceResultsFile(root, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(newerDir, "results.json")
	if got != want {
		t.Fatalf("latestMQTTTraceResultsFile = %q, want %q", got, want)
	}
}

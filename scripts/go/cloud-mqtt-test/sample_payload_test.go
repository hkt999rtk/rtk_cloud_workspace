package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSampleHomeMessageUsesDocumentedEnvelope(t *testing.T) {
	now := time.Date(2026, 6, 3, 1, 2, 3, 0, time.UTC)
	messageID := "msg-test-1"

	topic, payload, err := sampleHomeStatusReport("light-001", "light", "RTK", messageID, now)
	if err != nil {
		t.Fatalf("sampleHomeStatusReport: %v", err)
	}
	if topic != "devices/light-001/up/messages" {
		t.Fatalf("topic = %q, want devices/light-001/up/messages", topic)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not json: %v", err)
	}
	want := map[string]any{
		"sample_type":    "home_device_message",
		"schema_version": float64(1),
		"message_type":   "status_report",
		"message_id":     messageID,
		"device_id":      "light-001",
		"capability":     "light",
		"occurred_at":    "2026-06-03T01:02:03Z",
	}
	for key, expected := range want {
		if body[key] != expected {
			t.Fatalf("%s = %#v, want %#v; payload=%s", key, body[key], expected, string(payload))
		}
	}
	if body["correlation_id"] != nil {
		t.Fatalf("correlation_id = %#v, want nil", body["correlation_id"])
	}
	if body["command_id"] != nil {
		t.Fatalf("command_id = %#v, want nil", body["command_id"])
	}
	report, ok := body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload field = %#v, want object", body["payload"])
	}
	if report["brand"] != "RTK" || report["transport"] != "mqtt" {
		t.Fatalf("unexpected report payload: %#v", report)
	}
}

func TestSampleHomeCommandsUseDeviceSpecificActions(t *testing.T) {
	now := time.Date(2026, 6, 4, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		capability string
		action     string
		want       map[string]any
	}{
		{"light", "set_power", map[string]any{"power": true}},
		{"air_conditioner", "set_hvac", map[string]any{"mode": "cool", "target_temperature_c": float64(24), "fan": "auto"}},
		{"smart_meter", "read_meter", map[string]any{"reading": "instantaneous"}},
	}
	for _, tc := range cases {
		t.Run(tc.capability, func(t *testing.T) {
			payload, err := sampleHomeCommand("device-001", tc.capability, "cmd-1", now)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatal(err)
			}
			if body["message_type"] != "command" || body["capability"] != tc.capability || body["command_id"] != "cmd-1" {
				t.Fatalf("unexpected command envelope: %#v", body)
			}
			gotPayload, ok := body["payload"].(map[string]any)
			if !ok {
				t.Fatalf("payload = %#v, want object", body["payload"])
			}
			if gotPayload["clientToken"] != "cmd-1" || gotPayload["action"] != tc.action {
				t.Fatalf("unexpected AWS-style command metadata: %#v", gotPayload)
			}
			state, ok := gotPayload["state"].(map[string]any)
			if !ok {
				t.Fatalf("payload.state = %#v, want object", gotPayload["state"])
			}
			desired, ok := state["desired"].(map[string]any)
			if !ok {
				t.Fatalf("payload.state.desired = %#v, want object", state["desired"])
			}
			for key, want := range tc.want {
				if desired[key] != want {
					t.Fatalf("payload.state.desired[%s] = %#v, want %#v; payload=%s", key, desired[key], want, string(payload))
				}
			}
		})
	}
}

func TestSampleHomeCommandResultsReflectDeviceSpecificState(t *testing.T) {
	now := time.Date(2026, 6, 4, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		capability string
		want       map[string]any
	}{
		{"light", map[string]any{"power": true}},
		{"air_conditioner", map[string]any{"mode": "cool", "target_temperature_c": float64(24), "fan": "auto"}},
		{"smart_meter", map[string]any{"reading": "instantaneous", "telemetry_report_requested": true}},
	}
	for _, tc := range cases {
		t.Run(tc.capability, func(t *testing.T) {
			payload, err := sampleHomeCommandResult("device-001", tc.capability, "cmd-1", now)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatal(err)
			}
			if body["message_type"] != "command_result" || body["capability"] != tc.capability || body["command_id"] != "cmd-1" {
				t.Fatalf("unexpected command result envelope: %#v", body)
			}
			gotPayload, ok := body["payload"].(map[string]any)
			if !ok {
				t.Fatalf("payload = %#v, want object", body["payload"])
			}
			if gotPayload["clientToken"] != "cmd-1" || gotPayload["status"] != "accepted" {
				t.Fatalf("unexpected AWS-style command result metadata: %#v", gotPayload)
			}
			state, ok := gotPayload["state"].(map[string]any)
			if !ok {
				t.Fatalf("payload.state = %#v, want object", gotPayload["state"])
			}
			reported, ok := state["reported"].(map[string]any)
			if !ok {
				t.Fatalf("payload.state.reported = %#v, want object", state["reported"])
			}
			for key, want := range tc.want {
				if reported[key] != want {
					t.Fatalf("payload.state.reported[%s] = %#v, want %#v; payload=%s", key, reported[key], want, string(payload))
				}
			}
		})
	}
}

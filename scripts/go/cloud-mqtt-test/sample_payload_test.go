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

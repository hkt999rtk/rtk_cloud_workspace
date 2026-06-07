package main

import "testing"

func TestShardAssignmentsDistributesDevicesDeterministically(t *testing.T) {
	items := []assignment{
		{DeviceID: "dev-1"},
		{DeviceID: "dev-2"},
		{DeviceID: "dev-3"},
		{DeviceID: "dev-4"},
		{DeviceID: "dev-5"},
	}
	got := shardAssignments(items, 1, 2)
	want := []string{"dev-2", "dev-4"}
	if len(got) != len(want) {
		t.Fatalf("shard length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].DeviceID != want[i] {
			t.Fatalf("shard[%d] = %s, want %s", i, got[i].DeviceID, want[i])
		}
	}
}

func TestBaseline10KDefaults(t *testing.T) {
	opts := baseline10KDefaults(loadOptions{})
	if opts.RampUp != "10m" {
		t.Fatalf("RampUp = %q, want 10m", opts.RampUp)
	}
	if opts.TelemetryInterval != "5m" {
		t.Fatalf("TelemetryInterval = %q, want 5m", opts.TelemetryInterval)
	}
	if opts.StateInterval != "1h" {
		t.Fatalf("StateInterval = %q, want 1h", opts.StateInterval)
	}
	if opts.CommandRatePerDevicePerDay != "1" {
		t.Fatalf("CommandRatePerDevicePerDay = %q, want 1", opts.CommandRatePerDevicePerDay)
	}
	if opts.Concurrency != 250 {
		t.Fatalf("Concurrency = %d, want 250", opts.Concurrency)
	}
}

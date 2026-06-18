package main

import (
	"fmt"
	"testing"
	"time"
)

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

func TestLoadOptionsAcceptsSustainedHome100KModel(t *testing.T) {
	opts := loadOptions{LoadModel: "home-100k-sustained"}
	if err := opts.validateLoadModel(); err != nil {
		t.Fatalf("validateLoadModel() error = %v", err)
	}
}

func TestLoadOptionsAcceptsHomeDiverseTrafficProfile(t *testing.T) {
	opts := loadOptions{LoadModel: "home-100k-sustained", DeviceTrafficProfile: "home-diverse-v1"}
	if err := opts.validateLoadModel(); err != nil {
		t.Fatalf("validateLoadModel() error = %v", err)
	}
}

func TestTelemetryScheduleUsesDeterministicDevicePhaseJitter(t *testing.T) {
	first := telemetrySchedule("device-a", 20260616, 5*time.Minute, 15*time.Minute)
	second := telemetrySchedule("device-a", 20260616, 5*time.Minute, 15*time.Minute)
	other := telemetrySchedule("device-b", 20260616, 5*time.Minute, 15*time.Minute)
	if len(first) != 3 {
		t.Fatalf("schedule len = %d, want 3: %v", len(first), first)
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("same device schedule not deterministic: %v vs %v", first, second)
	}
	if fmt.Sprint(first) == fmt.Sprint(other) {
		t.Fatalf("different devices should not share the same phase jitter: %v", first)
	}
	for _, at := range first {
		if at < 0 || at >= 15*time.Minute {
			t.Fatalf("schedule contains out-of-window offset: %v", first)
		}
	}
}

func TestUserCommandScheduleUsesDeterministicPoissonArrivals(t *testing.T) {
	first := userCommandSchedule(1000, 24, 10*time.Minute, 20260616)
	second := userCommandSchedule(1000, 24, 10*time.Minute, 20260616)
	if len(first) == 0 {
		t.Fatalf("expected at least one command arrival")
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("poisson schedule not deterministic for seed: %v vs %v", first, second)
	}
	for i, at := range first {
		if at < 0 || at >= 10*time.Minute {
			t.Fatalf("schedule contains out-of-window offset: %v", at)
		}
		if i > 0 && at < first[i-1] {
			t.Fatalf("schedule not monotonic: %v", first)
		}
	}
}

func TestUserCommandScheduleCoversShortHome100KStageUsers(t *testing.T) {
	got := userCommandSchedule(2500, 3600, 3*time.Second, int64(20260531)+int64(2500)*7919)
	if len(got) < 250 {
		t.Fatalf("command arrivals = %d, want at least 250 user writes", len(got))
	}
}

func TestHomeDiverseEventsDependOnDeviceTypeAndUsageWindow(t *testing.T) {
	sessions := []sustainedDeviceSession{}
	for idx, typ := range []string{"light", "light", "light", "light", "environment_sensor", "environment_sensor", "security_sensor", "camera_status"} {
		sessions = append(sessions, sustainedDeviceSession{Record: certRecord{DeviceID: fmt.Sprintf("device-%02d", idx), DeviceType: typ}})
	}
	opts := loadOptions{
		TelemetryInterval:          "off",
		CommandRatePerDevicePerDay: "0",
		DeviceTrafficProfile:       "home-diverse-v1",
		StageUsageWindow:           "evening_peak",
		StageMinCommands:           "12",
	}
	events := sustainedEventsWithCommandWindow(sessions, opts, 1234, 2*time.Minute, 90*time.Second)
	commandsByType := map[string]int{}
	telemetryByType := map[string]int{}
	for _, event := range events {
		typ := sessions[event.Index].Record.DeviceType
		switch event.Kind {
		case "command":
			commandsByType[typ]++
		case "telemetry", "event":
			telemetryByType[typ]++
		}
	}
	if commandsByType["light"] <= commandsByType["environment_sensor"] {
		t.Fatalf("light commands = %d, environment_sensor commands = %d, want command-heavy lights in evening peak", commandsByType["light"], commandsByType["environment_sensor"])
	}
	if telemetryByType["environment_sensor"] == 0 {
		t.Fatalf("environment sensors should still report telemetry/events: commands=%#v telemetry=%#v events=%#v", commandsByType, telemetryByType, events)
	}
	if telemetryByType["security_sensor"] == 0 && telemetryByType["camera_status"] == 0 {
		t.Fatalf("security/camera status devices should produce event-style reports: telemetry=%#v events=%#v", telemetryByType, events)
	}
}

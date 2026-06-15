package home100k

import "testing"

func TestDesiredUpdateCreatesDeltaAndReportedUpdateClearsDelta(t *testing.T) {
	doc := NewShadowDocument("device-1")
	desired, err := doc.ApplyDesired("app", map[string]any{"power": true}, "cmd-1", 0)
	if err != nil {
		t.Fatalf("ApplyDesired() error = %v", err)
	}
	if desired.Version != 1 {
		t.Fatalf("desired version = %d, want 1", desired.Version)
	}
	if got := desired.Delta["power"]; got != true {
		t.Fatalf("delta power = %#v, want true", got)
	}
	if desired.ClientToken != "cmd-1" {
		t.Fatalf("client token = %q, want cmd-1", desired.ClientToken)
	}

	reported, err := doc.ApplyReported("device", map[string]any{"power": true}, "ack-1", desired.Version)
	if err != nil {
		t.Fatalf("ApplyReported() error = %v", err)
	}
	if reported.Version != 2 {
		t.Fatalf("reported version = %d, want 2", reported.Version)
	}
	if len(reported.Delta) != 0 {
		t.Fatalf("delta after reported = %#v, want empty", reported.Delta)
	}
}

func TestDeviceCannotWriteDesiredAndStaleVersionConflicts(t *testing.T) {
	doc := NewShadowDocument("device-1")
	if _, err := doc.ApplyDesired("device", map[string]any{"power": true}, "bad", 0); err != ErrDesiredRequiresApp {
		t.Fatalf("device ApplyDesired() error = %v, want %v", err, ErrDesiredRequiresApp)
	}
	first, err := doc.ApplyDesired("app", map[string]any{"power": true}, "cmd-1", 0)
	if err != nil {
		t.Fatalf("ApplyDesired() error = %v", err)
	}
	if _, err := doc.ApplyReported("device", map[string]any{"power": true}, "ack-1", first.Version+1); err != ErrVersionConflict {
		t.Fatalf("stale ApplyReported() error = %v, want %v", err, ErrVersionConflict)
	}
}

func TestOfflineDesiredConvergesAfterReconnectAndGet(t *testing.T) {
	device := NewDeviceActor("device-1", PresenceOfflineDesiredQueue)
	doc := NewShadowDocument("device-1")
	if err := device.Disconnect(); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	desired, err := doc.ApplyDesired("app", map[string]any{"brightness": 70}, "cmd-offline", 0)
	if err != nil {
		t.Fatalf("ApplyDesired() error = %v", err)
	}
	if len(desired.Delta) == 0 {
		t.Fatal("desired while offline did not create delta")
	}

	if err := device.ReconnectAndSync(doc); err != nil {
		t.Fatalf("ReconnectAndSync() error = %v", err)
	}
	if got := device.AppliedCount("brightness"); got != 1 {
		t.Fatalf("applied brightness count = %d, want 1", got)
	}
	if len(doc.Snapshot().Delta) != 0 {
		t.Fatalf("delta after reconnect sync = %#v, want empty", doc.Snapshot().Delta)
	}
}

func TestFlappingReconnectDoesNotDuplicateCompletedDesired(t *testing.T) {
	device := NewDeviceActor("device-1", PresenceFlappingReconnect)
	doc := NewShadowDocument("device-1")
	if _, err := doc.ApplyDesired("app", map[string]any{"power": true}, "cmd-1", 0); err != nil {
		t.Fatalf("ApplyDesired() error = %v", err)
	}
	if err := device.ReconnectAndSync(doc); err != nil {
		t.Fatalf("first ReconnectAndSync() error = %v", err)
	}
	if err := device.ReconnectAndSync(doc); err != nil {
		t.Fatalf("second ReconnectAndSync() error = %v", err)
	}
	if got := device.AppliedCount("power"); got != 1 {
		t.Fatalf("applied power count = %d, want 1", got)
	}
}

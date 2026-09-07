package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func scheduleFixture(t *testing.T) (BaseBackupConfig, BaseBackupSchedule) {
	t.Helper()
	return baseConfigFixture(t), BaseBackupSchedule{Version: 1, Name: "daily", IntervalSeconds: 300, StateFile: filepath.Join(privateTemp(t), "schedule.json")}
}
func readSchedule(t *testing.T, s BaseBackupSchedule) baseScheduleState {
	t.Helper()
	b, err := os.ReadFile(s.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state baseScheduleState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func TestBaseScheduleRetryAndCadence(t *testing.T) {
	c, s := scheduleFixture(t)
	ctx := context.Background()
	now := time.Unix(601, 0)
	failed := errors.New("ambiguous upload")
	var ids []string
	create := func(ctx context.Context, id string) error {
		state := readSchedule(t, s)
		if state.PendingID != id || state.PendingSlot == nil {
			t.Fatal("capture started without journal")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		ids = append(ids, id)
		if len(ids) == 1 {
			return failed
		}
		return nil
	}
	first, err := runBaseSchedule(ctx, c, s, now, create)
	if !errors.Is(err, failed) || first.Status != "pending" {
		t.Fatalf("%+v %v", first, err)
	}
	retry, err := runBaseSchedule(ctx, c, s, time.Unix(1201, 0), create)
	if err != nil || retry.Status != "completed" || retry.BackupID != first.BackupID || retry.Slot != 600 {
		t.Fatalf("%+v %v", retry, err)
	}
	next, err := runBaseSchedule(ctx, c, s, time.Unix(1201, 0), create)
	if err != nil || next.Status != "completed" || next.Slot != 1200 || next.BackupID == first.BackupID {
		t.Fatalf("%+v %v", next, err)
	}
	skipped, err := runBaseSchedule(ctx, c, s, time.Unix(1250, 0), create)
	if err != nil || skipped.Status != "not-due" || len(ids) != 3 {
		t.Fatalf("%+v %v %v", skipped, err, ids)
	}
	state := readSchedule(t, s)
	if state.PendingSlot != nil || state.LastCompletedSlot == nil || *state.LastCompletedSlot != 1200 {
		t.Fatalf("%+v", state)
	}
	if _, err := runBaseSchedule(ctx, c, s, now, create); err == nil {
		t.Fatal("clock regression accepted")
	}
}
func TestBaseScheduleConcurrentInvocation(t *testing.T) {
	c, s := scheduleFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := runBaseSchedule(context.Background(), c, s, time.Unix(600, 0), func(context.Context, string) error { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	_, err := runBaseSchedule(context.Background(), c, s, time.Unix(600, 0), func(context.Context, string) error { t.Error("duplicate capture"); return nil })
	close(release)
	if err == nil {
		t.Error("overlap accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestBaseScheduleCancellationPreservesPending(t *testing.T) {
	c, s := scheduleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	first, err := runBaseSchedule(ctx, c, s, time.Unix(600, 0), func(context.Context, string) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || first.Status != "pending" {
		t.Fatalf("%+v %v", first, err)
	}
	state := readSchedule(t, s)
	if state.PendingID != first.BackupID || state.LastCompletedSlot != nil {
		t.Fatal("cancellation advanced checkpoint")
	}
	retry, err := runBaseSchedule(context.Background(), c, s, time.Unix(900, 0), func(_ context.Context, id string) error {
		if id != first.BackupID {
			t.Error("retry changed ID")
		}
		return nil
	})
	if err != nil || retry.Status != "completed" {
		t.Fatalf("%+v %v", retry, err)
	}
}
func TestBaseScheduleRejectsUntrustedState(t *testing.T) {
	for _, mode := range []string{"empty", "missing-digest", "mismatch", "bad-id", "future", "public", "symlink", "lock-symlink", "config-drift", "policy-drift"} {
		t.Run(mode, func(t *testing.T) {
			c, s := scheduleFixture(t)
			slot := int64(600)
			state := baseScheduleState{Version: 1, ConfigurationSHA256: Digest(c), ScheduleSHA256: Digest(s), PendingSlot: &slot, PendingID: baseScheduledID(s, slot)}
			switch mode {
			case "empty":
				state = baseScheduleState{}
			case "missing-digest":
				state.ConfigurationSHA256 = ""
			case "mismatch":
				state.ConfigurationSHA256 = "wrong"
			case "bad-id":
				state.PendingID = "other"
			case "future":
				slot = 900
				state.PendingID = baseScheduledID(s, slot)
			case "config-drift":
				c.TimeoutSeconds++
			case "policy-drift":
				s.IntervalSeconds = 600
			}
			if err := WriteJSON(s.StateFile, state); err != nil {
				t.Fatal(err)
			}
			if mode == "empty" {
				if err := os.WriteFile(s.StateFile, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "public" {
				if err := os.Chmod(s.StateFile, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Rename(s.StateFile, s.StateFile+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(s.StateFile+".real", s.StateFile); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "lock-symlink" {
				if err := os.Symlink(s.StateFile, s.StateFile+".lock"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runBaseSchedule(context.Background(), c, s, time.Unix(600, 0), func(context.Context, string) error { t.Error("capture ran with invalid state"); return nil }); err == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
}

func TestBaseScheduleRejectsInvalidPolicy(t *testing.T) {
	_, s := scheduleFixture(t)
	for _, mutate := range []func(*BaseBackupSchedule){
		func(s *BaseBackupSchedule) { s.Version = 0 },
		func(s *BaseBackupSchedule) { s.Name = "../daily" },
		func(s *BaseBackupSchedule) { s.IntervalSeconds = 299 },
		func(s *BaseBackupSchedule) { s.IntervalSeconds = 604801 },
		func(s *BaseBackupSchedule) { s.StateFile = "relative" },
		func(s *BaseBackupSchedule) { s.StateFile = "/state.json" },
		func(s *BaseBackupSchedule) { s.StateFile += "\n" },
	} {
		bad := s
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted invalid policy: %+v", bad)
		}
	}
}

func TestBaseScheduleEngineFailureIsRetryable(t *testing.T) {
	c, s := scheduleFixture(t)
	failure := errors.New("native tool unavailable")
	calls := 0
	engine := BaseBackupEngine{Config: c, Exec: func(context.Context, []string, io.Reader, io.Writer) error { calls++; return failure }}
	first, err := engine.Scheduled(context.Background(), s)
	if err == nil || first.Status != "pending" || calls == 0 {
		t.Fatalf("%+v %v calls=%d", first, err, calls)
	}
	second, err := engine.Scheduled(context.Background(), s)
	if err == nil || second.BackupID != first.BackupID || second.Status != "pending" {
		t.Fatalf("%+v %v", second, err)
	}
	state := readSchedule(t, s)
	if state.LastCompletedSlot != nil || state.PendingID != first.BackupID {
		t.Fatal("failed engine advanced checkpoint")
	}
}

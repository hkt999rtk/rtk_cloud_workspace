package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// BaseBackupSchedule is invoked by an OS scheduler. Interval controls base-backup
// cadence, not WAL RPO; a pending capture always takes priority over a newer slot.
type BaseBackupSchedule struct {
	Version         int    `json:"version"`
	Name            string `json:"name"`
	IntervalSeconds int64  `json:"interval_seconds"`
	StateFile       string `json:"state_file"`
}

func (s BaseBackupSchedule) Validate() error {
	if s.Version != 1 || !Name.MatchString(s.Name) || len(s.Name) > 32 || s.IntervalSeconds < 300 || s.IntervalSeconds > 7*24*60*60 || !filepath.IsAbs(s.StateFile) || filepath.Clean(s.StateFile) != s.StateFile || filepath.Dir(s.StateFile) == "/" || strings.ContainsAny(s.StateFile, "\x00\r\n") {
		return errors.New("invalid physical backup schedule, interval or private state path")
	}
	return nil
}

type baseScheduleState struct {
	Version             int    `json:"version"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	ScheduleSHA256      string `json:"schedule_sha256"`
	LastCompletedSlot   *int64 `json:"last_completed_slot,omitempty"`
	LastBackupID        string `json:"last_backup_id,omitempty"`
	PendingSlot         *int64 `json:"pending_slot,omitempty"`
	PendingID           string `json:"pending_id,omitempty"`
}
type BaseScheduleResult struct {
	Status   string `json:"status"`
	BackupID string `json:"backup_id,omitempty"`
	Slot     int64  `json:"slot"`
}

func baseScheduledID(s BaseBackupSchedule, slot int64) string {
	return "scheduled-" + s.Name + "-" + strconv.FormatInt(slot, 36)
}
func (e BaseBackupEngine) Scheduled(ctx context.Context, s BaseBackupSchedule) (BaseScheduleResult, error) {
	return runBaseSchedule(ctx, e.Config, s, time.Now(), e.Create)
}
func runBaseSchedule(ctx context.Context, c BaseBackupConfig, s BaseBackupSchedule, now time.Time, create func(context.Context, string) error) (BaseScheduleResult, error) {
	var result BaseScheduleResult
	if err := c.Validate(); err != nil {
		return result, err
	}
	if err := s.Validate(); err != nil {
		return result, err
	}
	if now.Unix() < 0 {
		return result, errors.New("invalid schedule clock")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := PrivateDirectory(filepath.Dir(s.StateFile)); err != nil {
		return result, err
	}
	fd, err := unix.Open(s.StateFile+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return result, err
	}
	lock := os.NewFile(uintptr(fd), s.StateFile+".lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return result, errors.New("invalid schedule lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return result, errors.New("physical backup schedule busy; retry")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	state := baseScheduleState{Version: 1, ConfigurationSHA256: Digest(c), ScheduleSHA256: Digest(s)}
	info, err = os.Lstat(s.StateFile)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
			return result, errors.New("invalid private schedule state")
		}
		f, err := os.Open(s.StateFile)
		if err != nil {
			return result, err
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			f.Close()
			return result, errors.New("schedule state changed while opening")
		}
		state = baseScheduleState{}
		err = Decode(io.LimitReader(f, 16384), &state)
		f.Close()
		if err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if state.Version != 1 || state.ConfigurationSHA256 != Digest(c) || state.ScheduleSHA256 != Digest(s) {
		return result, errors.New("schedule state configuration mismatch; preserve pending state")
	}
	slot := now.Unix() / s.IntervalSeconds * s.IntervalSeconds
	result.Slot = slot
	for _, item := range []struct {
		slot *int64
		id   string
	}{{state.LastCompletedSlot, state.LastBackupID}, {state.PendingSlot, state.PendingID}} {
		if item.slot == nil {
			if item.id != "" {
				return result, errors.New("inconsistent schedule checkpoint")
			}
			continue
		}
		if *item.slot < 0 || *item.slot%s.IntervalSeconds != 0 || item.id != baseScheduledID(s, *item.slot) || *item.slot > slot {
			return result, errors.New("invalid schedule checkpoint or clock regression")
		}
	}
	if state.PendingSlot != nil && state.LastCompletedSlot != nil && *state.PendingSlot <= *state.LastCompletedSlot {
		return result, errors.New("pending schedule does not advance completed capture")
	}
	if state.PendingSlot == nil {
		if state.LastCompletedSlot != nil && *state.LastCompletedSlot == slot {
			return BaseScheduleResult{Status: "not-due", BackupID: state.LastBackupID, Slot: slot}, nil
		}
		state.PendingSlot = &slot
		state.PendingID = baseScheduledID(s, slot)
		// Journal before capture: a crash or remote ambiguity retries this exact ID.
		if err = saveBaseSchedule(s.StateFile, state); err != nil {
			return result, err
		}
	}
	result = BaseScheduleResult{Status: "pending", BackupID: state.PendingID, Slot: *state.PendingSlot}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = create(ctx, state.PendingID); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	state.LastCompletedSlot = state.PendingSlot
	state.LastBackupID = state.PendingID
	state.PendingSlot = nil
	state.PendingID = ""
	if err = saveBaseSchedule(s.StateFile, state); err != nil {
		return result, err
	}
	result.Status = "completed"
	return result, nil
}
func saveBaseSchedule(path string, state baseScheduleState) error {
	if err := WriteJSON(path, state); err != nil {
		return err
	}
	return syncWALDirectory(filepath.Dir(path))
}

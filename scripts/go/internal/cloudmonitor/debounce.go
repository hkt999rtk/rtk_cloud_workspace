package cloudmonitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const transientHTTPReason = "HTTP 連線、TLS 驗證或逾時失敗"

type connectionDebounce struct {
	SchemaVersion     int                        `json:"schema_version"`
	Environment       string                     `json:"environment"`
	SourceFingerprint string                     `json:"source_fingerprint"`
	Checks            map[string]connectionState `json:"checks"`
}
type connectionState struct {
	ObservedAt     time.Time `json:"observed_at"`
	Status         Status    `json:"status"`
	Failures       int       `json:"consecutive_failures"`
	Successes      int       `json:"consecutive_successes"`
	LatchedFailure bool      `json:"latched_failure"`
}

func loadConnectionDebounce(path, env, fingerprint string) (*connectionDebounce, error) {
	fresh := &connectionDebounce{SchemaVersion: SchemaVersion, Environment: env, SourceFingerprint: fingerprint, Checks: map[string]connectionState{}}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fresh, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return nil, errors.New("invalid connection debounce state file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var saved connectionDebounce
	if json.Unmarshal(raw, &saved) != nil {
		return nil, errors.New("connection debounce state is malformed")
	}
	if saved.SchemaVersion != SchemaVersion || saved.Environment != env || saved.SourceFingerprint != fingerprint {
		return fresh, nil
	}
	if saved.Checks == nil {
		saved.Checks = map[string]connectionState{}
	}
	for key, value := range saved.Checks {
		if value.Failures < 0 || value.Failures > 3 || value.Successes < 0 || value.Successes > 2 || value.ObservedAt.IsZero() || (!strings.Contains(key, "\x00http/")) {
			return nil, errors.New("invalid connection debounce record")
		}
	}
	return &saved, nil
}

func (d *connectionDebounce) save(path string) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return atomicPrivateWrite(path, append(raw, '\n'))
}

// apply changes only transient HTTP connection failures in watch mode. Raw
// observations remain in the result; cached observations never advance a streak.
func (d *connectionDebounce) apply(s *Snapshot) bool {
	changed := false
	for i := range s.Results {
		r := &s.Results[i]
		if !strings.HasPrefix(r.CheckID, "http/") {
			continue
		}
		key := r.Service + "\x00" + r.CheckID
		state, known := d.Checks[key]
		transient := r.Status == Fail && r.Reason == transientHTTPReason
		if r.Status == Fail && !transient {
			if known {
				delete(d.Checks, key)
				changed = true
			}
			continue
		}
		if !known && !transient {
			continue
		}
		r.ObservedStatus = r.Status
		if r.ObservedAt.IsZero() {
			r.Status = Unknown
			r.Reason = "HTTP 觀測沒有時間戳，無法更新連線判定。"
			continue
		}
		if !known || r.ObservedAt.After(state.ObservedAt) {
			if known && r.ObservedAt.Sub(state.ObservedAt) > 90*time.Second {
				state.Failures = 0
				state.Successes = 0
			}
			state.ObservedAt = r.ObservedAt
			switch r.Status {
			case Fail:
				state.Failures++
				if state.Failures > 3 {
					state.Failures = 3
				}
				state.Successes = 0
				if state.Failures >= 3 {
					state.LatchedFailure = true
				}
				state.Status = Warn
				if state.LatchedFailure {
					state.Status = Fail
				}
			case Pass:
				state.Failures = 0
				state.Successes++
				if state.Successes > 2 {
					state.Successes = 2
				}
				state.Status = Pass
				if state.LatchedFailure && state.Successes < 2 {
					state.Status = Fail
				} else {
					state.LatchedFailure = false
				}
			default:
				state.Failures = 0
				state.Successes = 0
				state.Status = r.Status
			}
			d.Checks[key] = state
			changed = true
		}
		r.Status = state.Status
		if r.ObservedStatus == Unknown || r.ObservedStatus == NotRun {
			r.Status = r.ObservedStatus
		}
		r.ConsecutiveFailures = state.Failures
		r.ConsecutiveSuccesses = state.Successes
		if r.ObservedStatus == Fail && state.Status == Warn {
			r.Reason = fmt.Sprintf("%s；連續失敗 %d/3 次，暫列警告。", r.Reason, state.Failures)
		}
		if r.ObservedStatus == Pass && state.Status == Fail {
			r.Reason = "本次 HTTP 已成功；連續成功 1/2 次，持續異常尚待解除。"
		}
	}
	s.Overall, s.Coverage = Summarize(s.Results)
	return changed
}

package cloudmonitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func debounceFixture(at time.Time, status Status, reason string) Snapshot {
	return Snapshot{Results: []Result{{Service: "api", CheckID: "http/health", Required: true, Status: status, ObservedAt: at, Reason: reason}}}
}

func TestHTTPWatchDebounceStreakAndRecovery(t *testing.T) {
	state, err := loadConnectionDebounce(filepath.Join(t.TempDir(), "state.json"), "dev", "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i, want := range []Status{Warn, Warn, Fail} {
		s := debounceFixture(at.Add(time.Duration(i)*time.Minute), Fail, transientHTTPReason)
		if !state.apply(&s) {
			t.Fatal("new failure unchanged")
		}
		r := s.Results[0]
		if r.Status != want || r.ObservedStatus != Fail || r.ConsecutiveFailures != i+1 {
			t.Fatalf("wrong streak %+v", r)
		}
		cached := debounceFixture(r.ObservedAt, Fail, transientHTTPReason)
		if state.apply(&cached) || cached.Results[0].ConsecutiveFailures != i+1 {
			t.Fatal("cached failure advanced streak")
		}
	}
	for i, want := range []Status{Fail, Pass} {
		s := debounceFixture(at.Add(time.Duration(i+3)*time.Minute), Pass, "ready")
		state.apply(&s)
		r := s.Results[0]
		if r.Status != want || r.ObservedStatus != Pass || r.ConsecutiveSuccesses != i+1 {
			t.Fatalf("wrong recovery %+v", r)
		}
	}
	stale := debounceFixture(at, Unknown, "pending observation")
	if state.apply(&stale) || stale.Results[0].Status != Unknown || stale.Overall == Pass {
		t.Fatal("cached unknown became successful")
	}
}

func TestHTTPWatchUnknownAndCriticalFailures(t *testing.T) {
	state, _ := loadConnectionDebounce(filepath.Join(t.TempDir(), "state.json"), "dev", "fingerprint")
	at := time.Now().UTC()
	s := debounceFixture(at, Fail, transientHTTPReason)
	state.apply(&s)
	s = debounceFixture(at.Add(time.Minute), Unknown, "credentials unavailable")
	state.apply(&s)
	if s.Results[0].Status != Unknown || s.Results[0].ConsecutiveFailures != 0 || s.Overall == Pass {
		t.Fatal("unknown became successful")
	}
	s = debounceFixture(at.Add(2*time.Minute), Fail, "HTTP 狀態碼不符合預期")
	state.apply(&s)
	if s.Results[0].Status != Fail || s.Results[0].ObservedStatus != "" {
		t.Fatal("HTTP application failure debounced")
	}
	for _, id := range []string{"k8s/workload", "tls/certificate", "token/access", "postgres/volume", "redis/rejected-writes"} {
		s.Results = []Result{{Service: "api", CheckID: id, Required: true, Status: Fail, ObservedAt: at}}
		state.apply(&s)
		if s.Results[0].Status != Fail || s.Results[0].ObservedStatus != "" {
			t.Fatalf("critical failure debounced: %s", id)
		}
	}
}

func TestHTTPWatchDebouncePersistenceScopeAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest-debounce.json")
	state, _ := loadConnectionDebounce(path, "dev", "fingerprint")
	at := time.Now().UTC()
	for i := 0; i < 2; i++ {
		s := debounceFixture(at.Add(time.Duration(i)*time.Minute), Fail, transientHTTPReason)
		state.apply(&s)
	}
	if err := state.save(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("state permissions")
	}
	restored, err := loadConnectionDebounce(path, "dev", "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	s := debounceFixture(at.Add(2*time.Minute), Fail, transientHTTPReason)
	restored.apply(&s)
	if s.Results[0].Status != Fail {
		t.Fatal("restart lost previous streak")
	}
	different, err := loadConnectionDebounce(path, "dev", "newfingerprint")
	if err != nil || len(different.Checks) != 0 {
		t.Fatal("state crossed inventory version")
	}
	s = debounceFixture(at.Add(5*time.Minute), Fail, transientHTTPReason)
	state.apply(&s)
	if s.Results[0].ConsecutiveFailures != 1 || s.Results[0].Status != Warn {
		t.Fatal("gap created consecutive failures")
	}
}

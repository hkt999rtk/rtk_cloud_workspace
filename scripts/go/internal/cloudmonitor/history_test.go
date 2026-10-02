package cloudmonitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyFixture(at time.Time) Snapshot {
	return Snapshot{SchemaVersion: SchemaVersion, Environment: "dev", StartedAt: at, FinishedAt: at.Add(time.Second), Profile: "read-only", Results: []Result{{Service: "api", CheckID: "http", Layer: "liveness", Required: true, Status: Pass, ObservedAt: at, Reason: "ready"}}}
}

func TestHistoryRoundTripAndRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now.Add(-72 * time.Hour), now.Add(-time.Hour), now} {
		s := historyFixture(at)
		if err := StoreSnapshot(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadHistory(dir, "dev", now.Add(-2*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].StartedAt.Before(got[1].StartedAt) {
		t.Fatalf("bad history %v", got)
	}
	path := filepath.Join(dir, "history", "dev", "latest.json")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", st.Mode())
	}
	owned := filepath.Join(dir, "history", "dev")
	st, _ = os.Stat(owned)
	if st.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v", st.Mode())
	}
	if err = os.WriteFile(filepath.Join(owned, "customer-notes.jsonl"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = RetainHistory(dir, "dev", 2, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(owned, "2026-09-29.jsonl")); !os.IsNotExist(err) {
		t.Fatal("old history survived")
	}
	if _, err = os.Stat(filepath.Join(owned, "customer-notes.jsonl")); err != nil {
		t.Fatal("unowned file removed")
	}
	if _, err = LoadHistory(dir, "../dev", time.Time{}, time.Time{}); err == nil {
		t.Fatal("unsafe environment accepted")
	}
}

func TestHistoryCorruptionIsNeverSuccess(t *testing.T) {
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	s := historyFixture(at)
	b, _ := json.Marshal(s)
	got, err := readHistoryFile(strings.NewReader(string(b)+"\n{\"environment\":"), "dev", at.Truncate(24*time.Hour), at, at.Add(time.Hour))
	if err != nil || len(got) != 2 || got[1].Overall != Unknown || got[1].Results[0].CheckID != "history.truncated" {
		t.Fatalf("missing explicit gap: %#v %v", got, err)
	}
	if _, err = readHistoryFile(strings.NewReader(string(b)+"\n{}\n"), "dev", at, at, at.Add(time.Hour)); err == nil {
		t.Fatal("invalid complete record accepted")
	}
	if _, err = readHistoryFile(strings.NewReader(strings.Repeat("x", maxHistoryRecord+1)), "dev", at, at, at.Add(time.Hour)); err == nil {
		t.Fatal("oversize record accepted")
	}
}

func TestHistoryRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	if err := StoreSnapshot(dir, historyFixture(at)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "history", "dev", "2026-10-02.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "other"), path); err != nil {
		t.Fatal(err)
	}
	if err := StoreSnapshot(dir, historyFixture(at)); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestHistoryRecoversCrashTail(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	if err := StoreSnapshot(dir, historyFixture(at)); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "history", "dev", "2026-10-02.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"environment":"dev"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = StoreSnapshot(dir, historyFixture(at.Add(30*time.Second))); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHistory(dir, "dev", at, at.Add(time.Minute))
	if err != nil || len(got) != 3 {
		t.Fatalf("bad recovery %#v %v", got, err)
	}
	if got[1].Results[0].CheckID != "history.truncated" || got[1].Overall != Unknown {
		t.Fatal("crash gap disappeared")
	}
	r := BuildReport(got, "dev", at, at.Add(time.Minute), "Asia/Taipei")
	if r.Coverage.Percent == 100 || r.Overall == Pass {
		t.Fatal("crash gap reported full coverage")
	}
}

func TestCounterRateResetAndGap(t *testing.T) {
	at := time.Now().UTC()
	base := historyFixture(at.Add(-30 * time.Second))
	base.Samples = []Sample{{Service: "pg", Name: "transactions", Value: 100, Kind: "counter", Unit: "transactions", ObservedAt: base.StartedAt, Identity: "server1"}}
	current := historyFixture(at)
	current.Samples = []Sample{{Service: "pg", Name: "transactions", Value: 160, Kind: "counter", Unit: "transactions", ObservedAt: at, Identity: "server1"}}
	got := EnrichHistory(current, []Snapshot{base})
	if len(got.Samples) != 2 || got.Samples[1].Value != 2 {
		t.Fatalf("bad counter rate %v", got.Samples)
	}
	for _, sample := range []Sample{{Service: "pg", Name: "transactions", Value: 10, Kind: "counter", ObservedAt: at, Identity: "server1"}, {Service: "pg", Name: "transactions", Value: 160, Kind: "counter", ObservedAt: at, Identity: "server2"}, {Service: "pg", Name: "transactions", Value: 160, Kind: "counter", ObservedAt: at.Add(5 * time.Minute), Identity: "server1"}} {
		current.Samples = []Sample{sample}
		got = EnrichHistory(current, []Snapshot{base})
		if len(got.Samples) != 1 {
			t.Fatal("reset or gap produced rate")
		}
	}
}

func TestCapacityForecastRequiresEvidence(t *testing.T) {
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	var prior []Snapshot
	for i := 0; i < 8640; i++ {
		when := at.Add(-72*time.Hour + time.Duration(i)*30*time.Second)
		prior = append(prior, Snapshot{StartedAt: when, Samples: []Sample{{Service: "pg", Name: "disk_used_bytes", Kind: "gauge", Value: 200 + float64(i)*300/8640, ObservedAt: when, Identity: "volume1"}}})
	}
	current := historyFixture(at)
	current.Samples = []Sample{{Service: "pg", Name: "disk_used_bytes", Kind: "gauge", Value: 500, ObservedAt: at, Identity: "volume1"}, {Service: "pg", Name: "disk_capacity_bytes", Kind: "gauge", Value: 1000, ObservedAt: at, Identity: "volume1"}}
	got := EnrichHistory(current, prior)
	last := got.Results[len(got.Results)-1]
	if last.Value == nil || last.Status != Warn || *last.Value < 1.9 || *last.Value > 2.1 {
		t.Fatalf("bad forecast %#v", last)
	}
	got = EnrichHistory(current, prior[:10])
	last = got.Results[len(got.Results)-1]
	if last.Value != nil || last.Status != Unknown {
		t.Fatal("insufficient history forecasted")
	}
	var sparse []Snapshot
	for i, p := range prior {
		if i%2 == 0 {
			sparse = append(sparse, p)
		}
	}
	got = EnrichHistory(current, sparse)
	last = got.Results[len(got.Results)-1]
	if last.Value != nil || last.Status != Unknown {
		t.Fatal("50 percent coverage forecasted")
	}
	prior[0].Samples = append(prior[0].Samples, Sample{Service: "pg", Name: "disk_capacity_bytes", Kind: "gauge", Value: 800, ObservedAt: prior[0].StartedAt, Identity: "volume1"})
	got = EnrichHistory(current, prior)
	last = got.Results[len(got.Results)-1]
	if last.Value != nil || last.Status != Unknown {
		t.Fatal("changed capacity forecasted")
	}
}

func TestHistoryRedactsCredentials(t *testing.T) {
	dir := t.TempDir()
	s := historyFixture(time.Now().UTC())
	s.Results[0].Reason = "Authorization: Bearer very-secret password=hidden postgres://alice:dsnsecret@localhost/db eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"
	if err := StoreSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "history", "dev", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"very-secret", "hidden", "dsnsecret", "eyJhbGci"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}

func TestRedisSustainedCapacityAndNewWriteErrors(t *testing.T) {
	at := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	current := historyFixture(at)
	current.Results = []Result{{Service: "redis", CheckID: "redis/memory", Required: true, Status: Warn, ObservedAt: at}}
	current.Samples = []Sample{{Service: "redis", Name: "redis_memory_used_bytes", Value: 90, Kind: "gauge", ObservedAt: at, Identity: "run1"}, {Service: "redis", Name: "redis_memory_limit_bytes", Value: 100, Kind: "gauge", ObservedAt: at, Identity: "run1"}, {Service: "redis", Name: "redis_error_oom", Value: 3, Kind: "counter", ObservedAt: at, Identity: "run1"}}
	var prior []Snapshot
	for i := 10; i > 0; i-- {
		n := at.Add(-time.Duration(i) * 30 * time.Second)
		prior = append(prior, Snapshot{StartedAt: n, Samples: []Sample{{Service: "redis", Name: "redis_memory_used_bytes", Value: 90, Kind: "gauge", ObservedAt: n, Identity: "run1"}, {Service: "redis", Name: "redis_error_oom", Value: 2, Kind: "counter", ObservedAt: n, Identity: "run1"}}})
	}
	got := EnrichHistory(current, prior)
	if got.Results[0].Status != Fail {
		t.Fatal("sustained85% did not escalate")
	}
	found := false
	for _, r := range got.Results {
		if r.CheckID == "redis/rejected-writes/redis_error_oom" && r.Status == Fail && r.Required {
			found = true
		}
	}
	if !found {
		t.Fatal("new write rejection missing")
	}
	current.Samples[2].Value = 2
	got = EnrichHistory(current, prior)
	for _, r := range got.Results {
		if strings.Contains(r.CheckID, "rejected-writes") {
			t.Fatal("historical errors became new failure")
		}
	}
	current.Samples[2].Value = 1
	got = EnrichHistory(current, prior)
	for _, r := range got.Results {
		if strings.Contains(r.CheckID, "rejected-writes") {
			t.Fatal("counter reset became write failure")
		}
	}
	got = EnrichHistory(current, prior[1:])
	if got.Results[0].Status == Fail {
		t.Fatal("short window became sustained failure")
	}
	prior[5].Samples[0].Value = 20
	got = EnrichHistory(current, prior)
	if got.Results[0].Status == Fail {
		t.Fatal("low memory interval did not break sustained window")
	}
	prior[5].Samples[0].Value = 90
	prior = append(prior[:2], prior[7:]...)
	got = EnrichHistory(current, prior)
	if got.Results[0].Status == Fail {
		t.Fatal("missing samples did not break sustained window")
	}
	if !redisWriteRejectionCounter("redis_command_hset_rejected_calls") || redisWriteRejectionCounter("redis_command_get_rejected_calls") {
		t.Fatal("write rejection classification wrong")
	}
}

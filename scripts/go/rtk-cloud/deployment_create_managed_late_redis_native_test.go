package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type redisNativeFixture struct {
	source, target                      managedLateRedisNativeState
	rdb                                 []byte
	paused, saved, transferred, stopped bool
	pauseCalls, unpauseCalls            int
	targetRewrite                       bool
	fail                                func(string, []string) ([]byte, error, bool)
	config                              string
}

func newRedisNativeFixture() *redisNativeFixture {
	s := managedLateRedisNativeState{RunID: strings.Repeat("a", 40), DatasetSHA1: strings.Repeat("b", 40), Keys: 3, RDBStatus: "ok", AOFStatus: "ok", AOFWriteStatus: "ok"}
	target := s
	target.RunID = strings.Repeat("c", 40)
	return &redisNativeFixture{source: s, target: target, rdb: []byte("REDIS0011-private-test-data-must-not-escape"), config: "dir\n/data\ndbfilename\ndump.rdb\n"}
}
func redisNativeInfo(s managedLateRedisNativeState) []byte {
	return []byte(fmt.Sprintf("# Server\r\nrun_id:%s\r\nrole:master\r\nloading:%d\r\naof_enabled:%d\r\nrdb_changes_since_last_save:%d\r\nrdb_bgsave_in_progress:%d\r\naof_rewrite_in_progress:%d\r\naof_rewrite_scheduled:%d\r\nrdb_last_bgsave_status:%s\r\naof_last_bgrewrite_status:%s\r\naof_last_write_status:%s\r\ndb0:keys=%d,expires=%d,avg_ttl=0\r\n", s.RunID, s.Loading, s.AOF, s.RDBChanges, s.RDBSaving, s.AOFRewriting, s.AOFScheduled, s.RDBStatus, s.AOFStatus, s.AOFWriteStatus, s.Keys, s.Expires))
}
func (f *redisNativeFixture) run(ctx context.Context, endpoint string, input []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.fail != nil {
		if raw, err, yes := f.fail(endpoint, args); yes {
			return raw, err
		}
	}
	if len(args) < 3 {
		return nil, errors.New("unexpected invocation")
	}
	if args[0] == "valkey-cli" {
		switch args[2] {
		case "INFO":
			s := f.source
			if endpoint == "target" {
				s = f.target
				if f.targetRewrite {
					s.AOFRewriting = 1
				}
			}
			return redisNativeInfo(s), nil
		case "EVAL_RO":
			if len(args) != 5 || args[3] != managedLateRedisDataDigest || args[4] != "0" {
				return nil, errors.New("foreign script")
			}
			s := f.source
			if endpoint == "target" {
				s = f.target
			}
			return []byte(fmt.Sprintf("%d\n%s\n", s.Keys, s.DatasetSHA1)), nil
		case "CLIENT":
			if endpoint != "source" {
				return nil, errors.New("foreign pause endpoint")
			}
			if args[3] == "PAUSE" {
				f.paused = true
				f.pauseCalls++
			} else if args[3] == "UNPAUSE" {
				f.paused = false
				f.unpauseCalls++
			} else {
				return nil, errors.New("foreign client command")
			}
			return []byte("OK\n"), nil
		case "SAVE":
			if endpoint != "source" || !f.paused {
				return nil, errors.New("unpaused SAVE")
			}
			f.saved = true
			f.source.RDBChanges = 0
			return []byte("OK\n"), nil
		case "CONFIG":
			if args[3] == "GET" {
				return []byte(f.config), nil
			}
			if endpoint != "target" || !f.transferred || args[3] != "SET" || args[4] != "appendonly" || args[5] != "yes" {
				return nil, errors.New("foreign config change")
			}
			f.target.AOF = 1
			return []byte("OK\n"), nil
		case "SHUTDOWN":
			if endpoint != "target" || f.target.AOF != 1 || f.targetRewrite || args[3] != "NOSAVE" {
				return nil, errors.New("unsafe shutdown")
			}
			f.stopped = true
			return nil, nil
		}
	}
	if args[0] != "/bin/sh" || args[1] != "-ec" || len(args) != 3 {
		return nil, errors.New("foreign executable")
	}
	script := args[2]
	sha := environmentPreflightBytesSHA256(f.rdb)
	if endpoint == "source" {
		if strings.Contains(script, "cat /data/dump.rdb") {
			if !f.saved {
				return nil, errors.New("missing SAVE")
			}
			return f.rdb, nil
		}
		if strings.Contains(script, "sha256sum /data/dump.rdb") {
			return []byte(sha + "  /data/dump.rdb\n"), nil
		}
	} else {
		if strings.Contains(script, "cat > /data/rtk-migration/source.rdb") {
			if !bytes.Equal(input, f.rdb) || !f.paused {
				return nil, errors.New("partial transfer")
			}
			f.transferred = true
			return []byte(sha + "  /data/dump.rdb\n" + sha + "  /data/rtk-migration/source.rdb\n"), nil
		}
		if strings.Contains(script, "valkey-server --bind ") {
			if !f.transferred || !strings.Contains(script, "--bind 127.0.0.1") || !strings.Contains(script, "--appendonly no") {
				return nil, errors.New("unsafe launch")
			}
			return nil, nil
		}
		if strings.Contains(script, "valkey-check-aof") {
			if !f.stopped {
				return nil, errors.New("server still running")
			}
			return []byte(sha + "  /data/dump.rdb\n" + strings.Repeat("d", 64) + "  /data/appendonlydir/appendonly.aof.manifest\n" + strings.Repeat("e", 64) + "  -\n"), nil
		}
	}
	return nil, errors.New("unexpected protocol step")
}
func redisNativeSnapshot(t *testing.T, f *redisNativeFixture) (*managedLateRedisPause, managedLateRedisSnapshot) {
	t.Helper()
	ctx := context.Background()
	before, err := readManagedLateRedisNative(ctx, f.run, "source")
	if err != nil {
		t.Fatal(err)
	}
	p, err := beginManagedLateRedisPause(ctx, f.run, time.Now().Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s, err := snapshotManagedLateRedis(ctx, f.run, p, before)
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}
func TestManagedLateRedisNativeProtocolPreservesDataset(t *testing.T) {
	f := newRedisNativeFixture()
	p, s := redisNativeSnapshot(t, f)
	if !f.paused || !f.saved || s.SHA != environmentPreflightBytesSHA256(f.rdb) || !bytes.Equal(s.RDB, f.rdb) || s.State.DatasetSHA1 != f.source.DatasetSHA1 {
		t.Fatal("source snapshot proof missing")
	}
	evidence, err := prepareManagedLateRedisTarget(context.Background(), f.run, p, s)
	if err != nil {
		t.Fatal(err)
	}
	if !f.stopped || !f.transferred || !f.paused || len(evidence) != 4 {
		t.Fatal("target not durably stopped or source resumed early")
	}
	for k, v := range evidence {
		if !environmentPreflightDigest.MatchString(v) {
			t.Fatalf("non-SHA256 public evidence %s", k)
		}
	}
	if err := p.Unpause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.paused || f.unpauseCalls != 1 {
		t.Fatal("exact source not restored")
	}
}
func TestManagedLateRedisNativeSnapshotRejectsInconsistency(t *testing.T) {
	cases := map[string]func(*redisNativeFixture){
		"restart":          func(f *redisNativeFixture) { f.source.RunID = strings.Repeat("f", 40) },
		"changed-dataset":  func(f *redisNativeFixture) { f.source.DatasetSHA1 = strings.Repeat("f", 40) },
		"expiring-key":     func(f *redisNativeFixture) { f.source.Expires = 1 },
		"foreign-rdb-path": func(f *redisNativeFixture) { f.config = "dir\n/other\ndbfilename\ndump.rdb\n" },
		"corrupt-rdb":      func(f *redisNativeFixture) { f.rdb = []byte("not-an-rdb-private-canary") },
		"wrong-native-sha": func(f *redisNativeFixture) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if e == "source" && a[0] == "/bin/sh" && strings.Contains(a[2], "sha256sum") {
					return []byte(strings.Repeat("f", 64) + "  /data/dump.rdb\n"), nil, true
				}
				return nil, nil, false
			}
		},
		"SAVE-unknown": func(f *redisNativeFixture) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if a[0] == "valkey-cli" && a[2] == "SAVE" {
					return nil, errors.New("private password canary"), true
				}
				return nil, nil, false
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRedisNativeFixture()
			expected := f.source
			p, err := beginManagedLateRedisPause(context.Background(), f.run, time.Now().Add(20*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			mutate(f)
			_, err = snapshotManagedLateRedis(context.Background(), f.run, p, expected)
			if err == nil || f.paused || f.unpauseCalls != 1 || strings.Contains(err.Error(), "canary") {
				t.Fatal("inconsistent/unknown source did not fail closed and restore source")
			}
		})
	}
}
func TestManagedLateRedisNativeTargetRejectsPartialOrUnsafeState(t *testing.T) {
	cases := map[string]func(*redisNativeFixture, *managedLateRedisPause, *managedLateRedisSnapshot){
		"partial-local-bytes": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			s.RDB = s.RDB[:len(s.RDB)-1]
		},
		"empty-transfer-output": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if e == "target" && a[0] == "/bin/sh" && strings.Contains(a[2], "cat >") {
					return nil, nil, true
				}
				return nil, nil, false
			}
		},
		"foreign-loaded-dataset": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.target.DatasetSHA1 = strings.Repeat("f", 40)
		},
		"aof-write-failed": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.target.AOFWriteStatus = "err"
		},
		"aof-rewrite-failed": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.target.AOFStatus = "err"
		},
		"shutdown-unknown": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if e == "target" && a[0] == "valkey-cli" && a[2] == "SHUTDOWN" {
					return nil, errors.New("private canary"), true
				}
				return nil, nil, false
			}
		},
		"partial-durable-proof": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if e == "target" && a[0] == "/bin/sh" && strings.Contains(a[2], "valkey-check-aof") {
					return []byte(s.SHA + "  /data/dump.rdb\n"), nil, true
				}
				return nil, nil, false
			}
		},
		"source-restarted-during-transfer": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			f.fail = func(e string, a []string) ([]byte, error, bool) {
				if f.stopped && e == "source" && a[0] == "valkey-cli" && a[2] == "INFO" {
					changed := f.source
					changed.RunID = strings.Repeat("f", 40)
					return redisNativeInfo(changed), nil, true
				}
				return nil, nil, false
			}
		},
		"pause-expired": func(f *redisNativeFixture, p *managedLateRedisPause, s *managedLateRedisSnapshot) {
			p.Until = time.Now().Add(-time.Second)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRedisNativeFixture()
			p, s := redisNativeSnapshot(t, f)
			mutate(f, p, &s)
			evidence, err := prepareManagedLateRedisTarget(context.Background(), f.run, p, s)
			if err == nil || evidence != nil || f.paused || f.unpauseCalls != 1 || strings.Contains(err.Error(), "canary") {
				t.Fatal("unsafe target did not fail closed and restore source")
			}
		})
	}
}
func TestManagedLateRedisNativePauseDeadlineAndCancellation(t *testing.T) {
	f := newRedisNativeFixture()
	deadline := time.Now().Add(20 * time.Minute)
	p, err := beginManagedLateRedisPause(context.Background(), f.run, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if p.Until.After(time.Now().Add(5*time.Minute)) || !p.Deadline.Equal(deadline) {
		t.Fatal("pause advanced lease or immutable deadline")
	}
	p.Until = time.Now().Add(30 * time.Second)
	if err := p.ensure(context.Background()); err != nil || f.pauseCalls != 2 || !p.Deadline.Equal(deadline) {
		t.Fatal("bounded renewal failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Unpause(ctx); err != nil || f.paused || f.unpauseCalls != 1 {
		t.Fatal("canceled caller prevented bounded source restoration")
	}
	p.Until = time.Now().Add(-time.Second)
	p.closed = false
	if p.Renew(context.Background()) == nil {
		t.Fatal("expired pause was silently revived")
	}
	f = newRedisNativeFixture()
	f.targetRewrite = true
	p, s := redisNativeSnapshot(t, f)
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := prepareManagedLateRedisTarget(short, f.run, p, s); err == nil || f.paused || f.stopped {
		t.Fatal("incomplete AOF rewrite did not restore source without claiming shutdown")
	}
}
func TestManagedLateRedisNativeWireAndRedaction(t *testing.T) {
	for name, raw := range map[string][]byte{"duplicate-empty": []byte("x:\nx:\n"), "nul": []byte("x:\x00\n")} {
		t.Run(name, func(t *testing.T) {
			if _, err := managedLateRedisInfo(raw); err == nil {
				t.Fatal("invalid INFO accepted")
			}
		})
	}
	f := newRedisNativeFixture()
	f.fail = func(e string, a []string) ([]byte, error, bool) {
		if a[2] == "INFO" {
			return append(redisNativeInfo(f.source), []byte("db1:keys=1,expires=0,avg_ttl=0\r\n")...), nil, true
		}
		return nil, nil, false
	}
	if _, err := readManagedLateRedisNative(context.Background(), f.run, "source"); err == nil {
		t.Fatal("foreign database accepted")
	}
	for _, out := range [][]byte{nil, []byte("x"), []byte(strings.Repeat("a", 64) + "  /other\n"), []byte(strings.Repeat("a", 64) + "  /data/dump.rdb\nextra")} {
		if managedLateRedisFileSHA(out, "/data/dump.rdb", "") {
			t.Fatal("malformed file proof accepted")
		}
	}
	f = newRedisNativeFixture()
	f.fail = func(e string, a []string) ([]byte, error, bool) { return nil, errors.New("token=private canary"), true }
	_, err := readManagedLateRedisNative(context.Background(), f.run, "source")
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("private runner cause escaped")
	}
}

func redisNativeSupplementFixture() *createManagedLateNormalTailSupplementAuthority {
	now := time.Now().UTC()
	start := now.Add(-30 * time.Minute)
	end := start.Add(managedLateNormalTailSupplementLimit)
	sha := strings.Repeat("a", 64)
	return &createManagedLateNormalTailSupplementAuthority{Schema: managedLateNormalTailSupplementSchema, ParentSHA: sha, SourceSHA: sha, SourceRawSHA: sha, OwnerSHA: sha, ProposalSHA: sha, ApprovalSHA: sha, OriginalAuthoritySHA: sha, OriginalAuthorityRawSHA: sha, OriginalReview: sha, OriginalQualifiedBytesSHA: sha, IntentSHA: sha, Review: sha, CheckerSHA: sha, ArtifactSHA: sha, QualifiedBytesSHA: sha, DescriptorSHA: sha, CredentialSHA: sha, OriginalStartedAt: start, OriginalDeadline: start.Add(managedLateOverallLimit), ExtendedDeadline: end, FirstCallerDeadline: end, AdmittedAt: now, Deadline: end, TotalLimit: managedLateNormalTailSupplementLimit}
}

func TestManagedLateRedisNativePauseSupplementIsTypedAndKeepsShortLease(t *testing.T) {
	a := redisNativeSupplementFixture()
	if !managedLateNormalTailSupplementPauseMatches(a, a.Deadline) {
		t.Fatal("synthetic typed policy fixture malformed")
	}
	f := newRedisNativeFixture()
	if _, err := beginManagedLateRedisPause(t.Context(), f.run, a.Deadline); err == nil || f.pauseCalls != 0 {
		t.Fatal("ordinary pause inherited supplemental180")
	}
	f = newRedisNativeFixture()
	p, err := beginManagedLateRedisPause(t.Context(), f.run, a.Deadline, a)
	if err != nil {
		t.Fatal(err)
	}
	if p.Until.After(time.Now().Add(5*time.Minute)) || !p.Deadline.Equal(a.OriginalStartedAt.Add(180*time.Minute)) || managedLateOverallLimit != 90*time.Minute {
		t.Fatal("supplement reset original anchor, global90, or short lease")
	}
	before := f.pauseCalls
	p.supplement.TotalLimit++
	if p.Check(time.Now().UTC()) == nil || p.Renew(t.Context()) == nil || f.pauseCalls != before {
		t.Fatal("changed private authority retained native permission")
	}
	if err := p.Unpause(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"schema", "original-anchor", "authority-pin", "wrong-deadline", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			a := redisNativeSupplementFixture()
			deadline := a.Deadline
			f := newRedisNativeFixture()
			sup := []*createManagedLateNormalTailSupplementAuthority{a}
			switch mode {
			case "schema":
				a.Schema = "foreign"
			case "original-anchor":
				a.OriginalStartedAt = a.OriginalStartedAt.Add(time.Minute)
			case "authority-pin":
				a.OriginalAuthoritySHA = ""
			case "wrong-deadline":
				deadline = deadline.Add(time.Minute)
			case "duplicate":
				sup = append(sup, a)
			}
			if _, err := beginManagedLateRedisPause(t.Context(), f.run, deadline, sup...); err == nil || f.pauseCalls != 0 || f.unpauseCalls != 0 {
				t.Fatal("invalid typed authority reached native command")
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strconv"
	"strings"
	"time"
)

// The caller maps these two fixed endpoints to independently fenced Pod UIDs,
// the original pinned image, and the isolated target's original claim UID.
// Neither binary RDB bytes nor command diagnostics may be printed or journaled.
type managedLateRedisNativeRun func(context.Context, string, []byte, ...string) ([]byte, error)

type managedLateRedisNativeState struct {
	RunID, DatasetSHA1                    string
	Keys, Expires                         int64
	AOF, Loading, RDBChanges              int64
	RDBSaving, AOFRewriting, AOFScheduled int64
	RDBStatus, AOFStatus, AOFWriteStatus  string
}

type managedLateRedisSnapshot struct {
	State   managedLateRedisNativeState
	SHA     string
	SavedAt time.Time
	RDB     []byte `json:"-"`
}

type managedLateRedisPause struct {
	Deadline, Until time.Time
	run             managedLateRedisNativeRun
	closed          bool
	supplement      *createManagedLateNormalTailSupplementAuthority
	supplementSHA   string
	extension       *createManagedLateNormalTailExtensionAuthority
	extensionSHA    string
}

const managedLateRedisDataDigest = `local keys=redis.call('KEYS','*'); table.sort(keys); local values={}; for _,key in ipairs(keys) do if redis.call('TYPE',key).ok~='string' then return redis.error_reply('migration requires string keys') end; if redis.call('PTTL',key)~=-1 then return redis.error_reply('migration requires nonexpiring keys') end; local value=redis.call('DUMP',key); values[#values+1]=tostring(#key)..':'..key..tostring(#value)..':'..value end; return {#keys,redis.sha1hex(table.concat(values))}`

func managedLateRedisNativeError(stage string) error {
	return createOpenBaoACKBlock("redis-native-" + stage)
}

func managedLateRedisCall(ctx context.Context, run managedLateRedisNativeRun, endpoint string, input []byte, args ...string) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || run == nil || endpoint != "source" && endpoint != "target" {
		return nil, managedLateRedisNativeError("command-context")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := run(bounded, endpoint, input, args...)
	if err != nil || bounded.Err() != nil {
		return nil, managedLateRedisNativeError("command-refused")
	}
	return raw, nil
}

func managedLateRedisInfo(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || bytes.IndexByte(raw, 0) >= 0 {
		return nil, managedLateRedisNativeError("info-shape")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		_, exists := values[key]
		if !ok || key == "" || exists {
			return nil, managedLateRedisNativeError("info-shape")
		}
		values[key] = value
	}
	return values, nil
}

func readManagedLateRedisNative(ctx context.Context, run managedLateRedisNativeRun, endpoint string) (managedLateRedisNativeState, error) {
	var state managedLateRedisNativeState
	raw, err := managedLateRedisCall(ctx, run, endpoint, nil, "valkey-cli", "--raw", "INFO", "all")
	if err != nil {
		return state, err
	}
	info, err := managedLateRedisInfo(raw)
	if err != nil || info["role"] != "master" || len(info["run_id"]) != 40 {
		return state, managedLateRedisNativeError("native-state")
	}
	if _, err := hex.DecodeString(info["run_id"]); err != nil {
		return state, managedLateRedisNativeError("native-run-id")
	}
	state.RunID = info["run_id"]
	for key, output := range map[string]*int64{"loading": &state.Loading, "aof_enabled": &state.AOF, "rdb_changes_since_last_save": &state.RDBChanges, "rdb_bgsave_in_progress": &state.RDBSaving, "aof_rewrite_in_progress": &state.AOFRewriting, "aof_rewrite_scheduled": &state.AOFScheduled} {
		value, ok := info[key]
		n, err := strconv.ParseInt(value, 10, 64)
		if !ok || err != nil || n < 0 {
			return state, managedLateRedisNativeError("native-persistence")
		}
		*output = n
	}
	state.RDBStatus, state.AOFStatus, state.AOFWriteStatus = info["rdb_last_bgsave_status"], info["aof_last_bgrewrite_status"], info["aof_last_write_status"]
	if state.Loading != 0 || state.RDBSaving != 0 {
		return state, managedLateRedisNativeError("native-loading")
	}
	for key := range info {
		if strings.HasPrefix(key, "db") && key != "db0" {
			if _, err := strconv.Atoi(strings.TrimPrefix(key, "db")); err == nil {
				return state, managedLateRedisNativeError("foreign-database")
			}
		}
	}
	fields := strings.Split(info["db0"], ",")
	if len(fields) != 3 {
		return state, managedLateRedisNativeError("keyspace")
	}
	for i, wanted := range []string{"keys", "expires", "avg_ttl"} {
		key, value, ok := strings.Cut(fields[i], "=")
		n, err := strconv.ParseInt(value, 10, 64)
		if !ok || key != wanted || err != nil || n < 0 {
			return state, managedLateRedisNativeError("keyspace")
		}
		if i == 0 {
			state.Keys = n
		}
		if i == 1 {
			state.Expires = n
		}
	}
	if state.Keys != 3 || state.Expires != 0 {
		return state, managedLateRedisNativeError("exact-three-nonexpiring-keys")
	}
	raw, err = managedLateRedisCall(ctx, run, endpoint, nil, "valkey-cli", "--raw", "EVAL_RO", managedLateRedisDataDigest, "0")
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	if err != nil || len(lines) != 2 || lines[0] != "3" || len(lines[1]) != 40 {
		return state, managedLateRedisNativeError("dataset-digest")
	}
	if _, err := hex.DecodeString(lines[1]); err != nil {
		return state, managedLateRedisNativeError("dataset-digest")
	}
	state.DatasetSHA1 = lines[1]
	return state, nil
}

func beginManagedLateRedisPause(ctx context.Context, run managedLateRedisNativeRun, deadline time.Time, supplements ...*createManagedLateNormalTailSupplementAuthority) (*managedLateRedisPause, error) {
	if len(supplements) > 1 {
		return nil, managedLateRedisNativeError("pause-supplement-authority")
	}
	p := &managedLateRedisPause{Deadline: deadline, run: run}
	if len(supplements) == 1 && supplements[0] != nil {
		copy := *supplements[0]
		p.supplement = &copy
		p.supplementSHA = deploymentplan.Digest(copy)
	}
	return startManagedLateRedisPause(ctx, p)
}

func beginManagedLateRedisPauseWithExtension(ctx context.Context, run managedLateRedisNativeRun, deadline time.Time, authority *createManagedLateNormalTailExtensionAuthority) (*managedLateRedisPause, error) {
	if !managedLateNormalTailExtensionPauseMatches(authority, deadline) {
		return nil, managedLateRedisNativeError("pause-extension-authority")
	}
	copy := *authority
	p := &managedLateRedisPause{Deadline: deadline, run: run, extension: &copy, extensionSHA: deploymentplan.Digest(copy)}
	return startManagedLateRedisPause(ctx, p)
}

func startManagedLateRedisPause(ctx context.Context, p *managedLateRedisPause) (*managedLateRedisPause, error) {
	if _, err := p.limit(); err != nil {
		return nil, err
	}
	if err := p.Renew(ctx); err != nil {
		if cleanup := p.Unpause(ctx); cleanup != nil {
			return nil, cleanup
		}
		return nil, err
	}
	return p, nil
}

// Renew never advances the approved deadline. The conservative lease begins
// BEFORE the request, so a delayed OK cannot invent extra paused time.
func (p *managedLateRedisPause) Renew(ctx context.Context) error {
	if p == nil || p.closed || ctx == nil || ctx.Err() != nil {
		return managedLateRedisNativeError("pause-context")
	}
	started := time.Now().UTC()
	if !p.Until.IsZero() && !started.Before(p.Until) {
		return managedLateRedisNativeError("pause-expired")
	}
	limit, err := p.limit()
	if err != nil {
		return err
	}
	remaining := p.Deadline.Sub(started) - 5*time.Second
	if remaining < 30*time.Second || remaining > limit {
		return managedLateRedisNativeError("pause-deadline")
	}
	if remaining > 5*time.Minute {
		remaining = 5 * time.Minute
	}
	p.Until = started.Add(time.Duration(remaining.Milliseconds()) * time.Millisecond)
	raw, err := managedLateRedisCall(ctx, p.run, "source", nil, "valkey-cli", "--raw", "CLIENT", "PAUSE", strconv.FormatInt(remaining.Milliseconds(), 10), "WRITE")
	if err != nil || string(bytes.TrimSpace(raw)) != "OK" || p.Check(time.Now().UTC()) != nil {
		return managedLateRedisNativeError("pause-unknown")
	}
	return nil
}

func (p *managedLateRedisPause) Check(now time.Time) error {
	if p == nil || p.closed || p.run == nil || p.Until.IsZero() || !now.Before(p.Until) || p.Until.After(p.Deadline) {
		return managedLateRedisNativeError("pause-expired")
	}
	if _, err := p.limit(); err != nil {
		return err
	}
	return nil
}

// A longer remaining bound is accepted only from the separately authenticated
// typed supplement. The native runner rechecks its canonical authority at every
// command; this immutable local copy changes neither the original90 nor lease5.
func (p *managedLateRedisPause) limit() (time.Duration, error) {
	if p == nil {
		return 0, managedLateRedisNativeError("pause-supplement-authority")
	}
	if p.extension != nil {
		if p.supplement != nil || p.extensionSHA != deploymentplan.Digest(p.extension) || !managedLateNormalTailExtensionPauseMatches(p.extension, p.Deadline) {
			return 0, managedLateRedisNativeError("pause-extension-authority")
		}
		return managedLateNormalTailExtensionLimit, nil
	}
	if p.supplement == nil {
		return managedLateOverallLimit, nil
	}
	if p.supplementSHA != deploymentplan.Digest(p.supplement) || !managedLateNormalTailSupplementPauseMatches(p.supplement, p.Deadline) {
		return 0, managedLateRedisNativeError("pause-supplement-authority")
	}
	return managedLateNormalTailSupplementLimit, nil
}

func (p *managedLateRedisPause) Unpause(ctx context.Context) error {
	if p == nil || p.closed {
		return nil
	}
	if ctx == nil {
		return managedLateRedisNativeError("unpause-context")
	}
	// A caller cancellation must not leave the original source paused. Restoration
	// remains bounded by the SAME approved deadline, never a new native phase.
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(ctx), p.Deadline)
	defer cancel()
	raw, err := managedLateRedisCall(cleanup, p.run, "source", nil, "valkey-cli", "--raw", "CLIENT", "UNPAUSE")
	p.closed = true
	if err != nil || string(bytes.TrimSpace(raw)) != "OK" {
		return managedLateRedisNativeError("unpause-unknown")
	}
	return nil
}

func managedLateRedisNativeStateSHA(s managedLateRedisNativeState) string {
	return deploymentplan.Digest(s)
}

func (p *managedLateRedisPause) ensure(ctx context.Context) error {
	if err := p.Check(time.Now().UTC()); err != nil {
		return err
	}
	if time.Until(p.Until) < time.Minute {
		return p.Renew(ctx)
	}
	return nil
}

func managedLateRedisSameData(a, b managedLateRedisNativeState) bool {
	return a.Keys == 3 && b.Keys == 3 && a.Expires == 0 && b.Expires == 0 && a.DatasetSHA1 != "" && a.DatasetSHA1 == b.DatasetSHA1
}

func snapshotManagedLateRedis(ctx context.Context, run managedLateRedisNativeRun, pause *managedLateRedisPause, expected managedLateRedisNativeState) (snapshot managedLateRedisSnapshot, result error) {
	defer func() {
		if result != nil {
			if err := pause.Unpause(ctx); err != nil {
				result = err
			}
		}
	}()
	if pause.ensure(ctx) != nil {
		return snapshot, managedLateRedisNativeError("snapshot-pause")
	}
	before, err := readManagedLateRedisNative(ctx, run, "source")
	if err != nil || before.RunID != expected.RunID || before.AOF != 0 || before.AOFRewriting != 0 || before.AOFScheduled != 0 || !managedLateRedisSameData(before, expected) {
		return snapshot, managedLateRedisNativeError("source-drift")
	}
	raw, err := managedLateRedisCall(ctx, run, "source", nil, "valkey-cli", "--raw", "CONFIG", "GET", "dir", "dbfilename")
	config := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	if err != nil || len(config) != 4 {
		return snapshot, managedLateRedisNativeError("source-rdb-config")
	}
	values := map[string]string{}
	for i := 0; i < len(config); i += 2 {
		if _, exists := values[config[i]]; exists {
			return snapshot, managedLateRedisNativeError("source-rdb-config")
		}
		values[config[i]] = config[i+1]
	}
	if values["dir"] != "/data" || values["dbfilename"] != "dump.rdb" {
		return snapshot, managedLateRedisNativeError("source-rdb-config")
	}
	raw, err = managedLateRedisCall(ctx, run, "source", nil, "valkey-cli", "--raw", "SAVE")
	if err != nil || string(bytes.TrimSpace(raw)) != "OK" {
		return snapshot, managedLateRedisNativeError("snapshot-save")
	}
	// Only the original /data/dump.rdb may be exported. The caller archives
	// these private bytes using its exclusive canonical 0600 producer.
	raw, err = managedLateRedisCall(ctx, run, "source", nil, "/bin/sh", "-ec", `test -f /data/dump.rdb; test ! -L /data/dump.rdb; test "$(stat -c %h /data/dump.rdb)" = 1; valkey-check-rdb /data/dump.rdb >/dev/null; cat /data/dump.rdb`)
	if err != nil || len(raw) < 17 || len(raw) > 64<<20 || !bytes.HasPrefix(raw, []byte("REDIS")) {
		return snapshot, managedLateRedisNativeError("snapshot-rdb")
	}
	rdb := raw
	proof, err := managedLateRedisCall(ctx, run, "source", nil, "/bin/sh", "-ec", `test -f /data/dump.rdb; test ! -L /data/dump.rdb; sha256sum /data/dump.rdb`)
	if err != nil || !managedLateRedisFileSHA(proof, "/data/dump.rdb", environmentPreflightBytesSHA256(rdb)) {
		return snapshot, managedLateRedisNativeError("source-rdb-byte-proof")
	}
	after, err := readManagedLateRedisNative(ctx, run, "source")
	if err != nil || after.RunID != before.RunID || after.AOF != 0 || after.RDBChanges != 0 || after.RDBStatus != "ok" || !managedLateRedisSameData(before, after) || pause.Check(time.Now().UTC()) != nil {
		return snapshot, managedLateRedisNativeError("snapshot-consistency")
	}
	return managedLateRedisSnapshot{State: after, SHA: environmentPreflightBytesSHA256(rdb), SavedAt: time.Now().UTC(), RDB: rdb}, nil
}

func prepareManagedLateRedisTarget(ctx context.Context, run managedLateRedisNativeRun, pause *managedLateRedisPause, snapshot managedLateRedisSnapshot) (evidence map[string]string, result error) {
	defer func() {
		if result != nil {
			if err := pause.Unpause(ctx); err != nil {
				result = err
			}
		}
	}()
	if pause.ensure(ctx) != nil || len(snapshot.RDB) < 17 || len(snapshot.RDB) > 64<<20 || snapshot.SHA != environmentPreflightBytesSHA256(snapshot.RDB) || snapshot.SavedAt.IsZero() || snapshot.SavedAt.After(time.Now().UTC()) {
		return nil, managedLateRedisNativeError("target-snapshot")
	}
	raw, err := managedLateRedisCall(ctx, run, "target", snapshot.RDB, "/bin/sh", "-ec", `umask 077; test -d /data; test ! -L /data; test ! -e /data/rtk-migration; test ! -L /data/rtk-migration; test ! -e /data/dump.rdb; test ! -L /data/dump.rdb; test ! -e /data/appendonlydir; test ! -L /data/appendonlydir; mkdir -m 700 /data/rtk-migration; set -C; cat > /data/rtk-migration/source.rdb; cat /data/rtk-migration/source.rdb > /data/dump.rdb; test "$(stat -c %a /data/rtk-migration/source.rdb)" = 600; test "$(stat -c %h /data/dump.rdb)" = 1; valkey-check-rdb /data/dump.rdb >/dev/null; sha256sum /data/dump.rdb /data/rtk-migration/source.rdb`)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if err != nil || len(lines) != 2 || !managedLateRedisFileSHA([]byte(lines[0]), "/data/dump.rdb", snapshot.SHA) || !managedLateRedisFileSHA([]byte(lines[1]), "/data/rtk-migration/source.rdb", snapshot.SHA) {
		return nil, managedLateRedisNativeError("target-byte-proof")
	}
	_, err = managedLateRedisCall(ctx, run, "target", nil, "/bin/sh", "-ec", `umask 077; valkey-server --bind 127.0.0.1 --protected-mode yes --port 6379 --save '' --appendonly no --dir /data --dbfilename dump.rdb --daemonize yes --pidfile /data/rtk-migration/server.pid --logfile /data/rtk-migration/server.log`)
	if err != nil {
		return nil, err
	}
	loaded, err := waitManagedLateRedisTarget(ctx, run, pause)
	if err != nil || loaded.AOF != 0 || !managedLateRedisSameData(loaded, snapshot.State) {
		return nil, managedLateRedisNativeError("target-loaded-data")
	}
	raw, err = managedLateRedisCall(ctx, run, "target", nil, "valkey-cli", "--raw", "CONFIG", "SET", "appendonly", "yes")
	if err != nil || string(bytes.TrimSpace(raw)) != "OK" {
		return nil, managedLateRedisNativeError("target-enable-aof")
	}
	for {
		if pause.ensure(ctx) != nil {
			return nil, managedLateRedisNativeError("target-pause")
		}
		state, err := readManagedLateRedisNative(ctx, run, "target")
		if err != nil || state.RunID != loaded.RunID || state.AOF != 1 || !managedLateRedisSameData(state, snapshot.State) {
			return nil, managedLateRedisNativeError("target-aof-data")
		}
		if state.AOFRewriting == 0 && state.AOFScheduled == 0 {
			if state.AOFStatus != "ok" || state.AOFWriteStatus != "ok" {
				return nil, managedLateRedisNativeError("target-aof-status")
			}
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, managedLateRedisNativeError("target-aof-deadline")
		case <-timer.C:
		}
	}
	// A successful SHUTDOWN flushes AOF. No FORCE/NOW or appendonly=no is
	// permitted: an incomplete initial rewrite must never be discarded.
	raw, err = managedLateRedisCall(ctx, run, "target", nil, "valkey-cli", "--raw", "SHUTDOWN", "NOSAVE")
	if err != nil || len(bytes.TrimSpace(raw)) != 0 {
		return nil, managedLateRedisNativeError("target-shutdown-unknown")
	}
	raw, err = managedLateRedisCall(ctx, run, "target", nil, "/bin/sh", "-ec", `test ! -e /data/rtk-migration/server.pid; test ! -L /data/rtk-migration/server.pid; test -f /data/rtk-migration/source.rdb; test ! -L /data/rtk-migration/source.rdb; test "$(stat -c %a /data/rtk-migration/source.rdb)" = 600; archive=$(sha256sum /data/rtk-migration/source.rdb); current=$(sha256sum /data/dump.rdb); test "${archive%% *}" = "${current%% *}"; test -f /data/dump.rdb; test ! -L /data/dump.rdb; test -d /data/appendonlydir; test ! -L /data/appendonlydir; test -s /data/appendonlydir/appendonly.aof.manifest; test -z "$(find /data/appendonlydir -type l -print)"; valkey-check-aof /data/appendonlydir/appendonly.aof.manifest >/dev/null; sync; sha256sum /data/dump.rdb /data/appendonlydir/appendonly.aof.manifest; set -C; find /data/appendonlydir -type f > /data/rtk-migration/aof-files.list; test -s /data/rtk-migration/aof-files.list; sort /data/rtk-migration/aof-files.list > /data/rtk-migration/aof-files.sorted; while IFS= read -r file; do test -f "$file"; test ! -L "$file"; sha256sum "$file"; done < /data/rtk-migration/aof-files.sorted > /data/rtk-migration/aof-files.sha256; sum=$(sha256sum /data/rtk-migration/aof-files.sha256); printf '%s  -\n' "${sum%% *}"`)
	lines = strings.Split(strings.TrimSpace(string(raw)), "\n")
	if err != nil || len(lines) != 3 || !managedLateRedisFileSHA([]byte(lines[0]), "/data/dump.rdb", snapshot.SHA) || !managedLateRedisFileSHA([]byte(lines[1]), "/data/appendonlydir/appendonly.aof.manifest", "") || !managedLateRedisFileSHA([]byte(lines[2]), "-", "") || pause.Check(time.Now().UTC()) != nil {
		return nil, managedLateRedisNativeError("target-durable-proof")
	}
	current, err := readManagedLateRedisNative(ctx, run, "source")
	if err != nil || current.RunID != snapshot.State.RunID || current.AOF != 0 || current.RDBChanges != 0 || !managedLateRedisSameData(current, snapshot.State) || pause.ensure(ctx) != nil {
		return nil, managedLateRedisNativeError("source-before-switch")
	}
	return map[string]string{"rdb": snapshot.SHA, "dataset": managedLateRedisNativeStateSHA(snapshot.State), "aof-manifest": strings.Fields(lines[1])[0], "aof-files": strings.Fields(lines[2])[0]}, nil
}

func managedLateRedisFileSHA(raw []byte, path, expected string) bool {
	fields := strings.Fields(string(raw))
	return len(fields) == 2 && fields[1] == path && environmentPreflightDigest.MatchString(fields[0]) && (expected == "" || fields[0] == expected)
}

// Daemonization may return before a small RDB has finished loading. Retry only
// fresh read-only observations under a short child deadline and the pause lease.
func waitManagedLateRedisTarget(ctx context.Context, run managedLateRedisNativeRun, pause *managedLateRedisPause) (managedLateRedisNativeState, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if pause.ensure(bounded) != nil {
			return managedLateRedisNativeState{}, managedLateRedisNativeError("target-loading-pause")
		}
		state, err := readManagedLateRedisNative(bounded, run, "target")
		if err == nil {
			return state, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return managedLateRedisNativeState{}, managedLateRedisNativeError("target-loading-deadline")
		case <-timer.C:
		}
	}
}

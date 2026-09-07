package recovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Promote only the disposable recovered copy. Recover the original base across
// the real fork using native history and WAL published/fetched by the actual CLI.
func exercisePITRTimeline(t *testing.T, ctx context.Context, docker, container, directory, encrypted, key, source string, e BaseBackupEngine, plan PITRPlan, env []string, query func(string) (string, error)) {
	t.Helper()
	run := func(argv ...string) {
		t.Helper()
		args := append(append([]string{}, env...), argv...)
		if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("timeline fixture command %s: %v %s", argv[0], err, out)
		}
	}
	mustQuery := func(sql string) string {
		t.Helper()
		out, err := query(sql)
		if err != nil {
			t.Fatalf("timeline fixture query: %v", err)
		}
		return out
	}
	if got := mustQuery("SELECT pg_promote(true, 30)"); got != "t" {
		t.Fatal("fixture promotion failed", got)
	}
	mustQuery("CHECKPOINT")
	if got := mustQuery("SELECT timeline_id FROM pg_control_checkpoint()"); got != "2" {
		t.Fatal("expected native timeline 2", got)
	}
	mustQuery("INSERT INTO physical_fixture VALUES(20,'new-timeline-before-target')")
	plan.TargetLSN = mustQuery("SELECT pg_create_restore_point('pki-timeline-two-target')")
	plan.TargetTimeline = 2
	mustQuery("INSERT INTO physical_fixture VALUES(21,'new-timeline-after-target')")
	last := mustQuery("SELECT pg_walfile_name(pg_current_wal_lsn())")
	mustQuery("SELECT pg_switch_wal()")
	walDir := filepath.Join(source, "pgdata", "pg_wal")
	publish := func(name string) {
		t.Helper()
		run(plan.Executable, "wal-archive", "--config", plan.WALConfigFile, "--confirm-environment", e.Config.WAL.Environment, "--confirm-stack", e.Config.WAL.Stack, "--name", name, "--source", filepath.Join(walDir, name))
	}
	publish("00000002.history")
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) == 24 && strings.HasPrefix(name, "00000002") && name <= last {
			publish(name)
			count++
		}
	}
	if count == 0 {
		t.Fatal("no native promoted WAL published")
	}
	destination := filepath.Join(directory, "pitr-timeline-two")
	if err := e.restoreFile(ctx, "fixture", encrypted, destination, key); err != nil {
		t.Fatal(err)
	}
	if err := e.preparePITR(ctx, "fixture", destination, key, plan); err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(ctx, docker, "exec", container, "chown", "-R", "postgres:postgres", destination).Run(); err != nil {
		t.Fatal(err)
	}
	socket := "/tmp/rtk-pitr-timeline-two"
	run("mkdir", "-m", "700", socket)
	run("pg_ctl", "-D", filepath.Join(destination, "pgdata"), "-l", filepath.Join(destination, "server.log"), "-o", "-c unix_socket_directories="+socket, "-w", "start")
	recoveredQuery := func(sql string) (string, error) {
		args := append(append([]string{}, env...), "psql", "-X", "-h", socket, "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		return strings.TrimSpace(string(out)), err
	}
	for {
		state, err := recoveredQuery("SELECT pg_get_wal_replay_pause_state()")
		if err == nil && state == "paused" {
			break
		}
		if ctx.Err() != nil {
			log, _ := os.ReadFile(filepath.Join(destination, "server.log"))
			t.Fatalf("timeline target not reached: %v\n%s", ctx.Err(), log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got, err := recoveredQuery("SELECT string_agg(id::text, ',' ORDER BY id) FROM physical_fixture"); err != nil || got != "1,2,20" {
		t.Fatal("timeline data boundary failed", got, err)
	}
	if got, err := recoveredQuery("SELECT pg_is_in_recovery() AND pg_last_wal_replay_lsn() >= current_setting('recovery_target_lsn')::pg_lsn AND current_setting('recovery_target_timeline') = '2' AND current_setting('listen_addresses') = ''"); err != nil || got != "t" {
		t.Fatal("timeline recovery isolation/target failed", got, err)
	}
	if _, err := recoveredQuery("INSERT INTO physical_fixture VALUES(99,'must fail')"); err == nil {
		t.Fatal("timeline recovery accepted writes")
	}
	log, err := os.ReadFile(filepath.Join(directory, "downloads.log"))
	if err != nil || !strings.Contains(string(log), "history-00000002.age") || !strings.Contains(string(log), "wal-"+strings.ToLower(last)+".age") {
		t.Fatal("native recovery did not fetch encrypted history and promoted WAL", err)
	}
	run("pg_ctl", "-D", filepath.Join(destination, "pgdata"), "-m", "fast", "-w", "stop")
	// A fresh base has no descendant history to fall back to. Keep its completion
	// metadata but remove the ciphertext, so restore must reject the missing link.
	historyObject := filepath.Join(directory, "objects", e.Config.WAL.Remote.Prefix, "wal-v1", e.Config.WAL.SystemIdentifier, e.Config.WAL.Stack, "history-00000002.age")
	if err := os.Remove(historyObject); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(directory, "pitr-missing-timeline")
	if err := e.restoreFile(ctx, "fixture", encrypted, missing, key); err != nil {
		t.Fatal(err)
	}
	if err := e.preparePITR(ctx, "fixture", missing, key, plan); err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(ctx, docker, "exec", container, "chown", "-R", "postgres:postgres", missing).Run(); err != nil {
		t.Fatal(err)
	}
	missingSocket := "/tmp/rtk-pitr-missing-timeline"
	run("mkdir", "-m", "700", missingSocket)
	args := append(append([]string{}, env...), "pg_ctl", "-D", filepath.Join(missing, "pgdata"), "-l", filepath.Join(missing, "server.log"), "-o", "-c unix_socket_directories="+missingSocket, "-w", "start")
	if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err == nil {
		t.Fatalf("missing timeline history accepted: %s", out)
	}
	failureLog, err := os.ReadFile(filepath.Join(missing, "server.log"))
	if err != nil || !strings.Contains(string(failureLog), "recovery target timeline 2 does not exist") || !strings.Contains(string(failureLog), "database system is shut down") {
		t.Fatalf("missing history did not fail at timeline validation: %v\n%s", err, failureLog)
	}
	t.Log("native PostgreSQL recovered timeline 1 base onto timeline 2; old-branch and post-target writes excluded; missing history rejected")
}

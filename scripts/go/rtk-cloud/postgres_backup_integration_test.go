package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
)

func TestPostgresRestoreRecoveryParameters(t *testing.T) {
	control := "max_connections setting: 800\nmax_worker_processes setting: 16\nmax_wal_senders setting: 12\nmax_prepared_xacts setting: 10\nmax_locks_per_xact setting: 128\n"
	want := "max_connections = 800\nmax_worker_processes = 16\nmax_wal_senders = 12\nmax_prepared_transactions = 10\nmax_locks_per_transaction = 128\n"
	if got, err := postgresBackupRecoverySettings(control); err != nil || got != want {
		t.Fatalf("recovery parameters: %q %v", got, err)
	}
	for _, bad := range []string{"", "WARNING: invalid checksum\n" + control, strings.Replace(control, "800", "800; shared_preload_libraries=bad", 1), strings.Replace(control, "16", "-1", 1)} {
		if _, err := postgresBackupRecoverySettings(bad); err == nil {
			t.Fatal("unsafe control parameters accepted")
		}
	}
}

func TestPostgresRestoreAuthenticatedManifestMatchesRequestedCompletion(t *testing.T) {
	c := postgresBackupTestConfig(t).Worker
	c.Directory = t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	m := postgresbackup.Manifest{Version: 1, Scope: postgresbackup.Scope, ID: "postgres-fixture", Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, PostgresMajor: 16, PostgresImage: c.Source.PostgresImage, SystemIdentifier: c.Source.SystemIdentifier, StartedAt: now.Add(-time.Minute), FinishedAt: now, NativeManifestSHA256: strings.Repeat("a", 64)}
	b, _ := json.Marshal(postgresbackup.Completion{Manifest: m})
	if err := os.WriteFile(filepath.Join(c.Directory, m.ID+".complete.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := postgresBackupVerifyDrillManifest(c, m.ID, m); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*postgresbackup.Manifest){
		"old backup id":         func(v *postgresbackup.Manifest) { v.ID = "postgres-older" },
		"old capture time":      func(v *postgresbackup.Manifest) { v.FinishedAt = v.FinishedAt.Add(-time.Hour) },
		"different native data": func(v *postgresbackup.Manifest) { v.NativeManifestSHA256 = strings.Repeat("b", 64) },
		"different source":      func(v *postgresbackup.Manifest) { v.SystemIdentifier = "9999999" },
	} {
		t.Run(name, func(t *testing.T) {
			other := m
			mutate(&other)
			if postgresBackupVerifyDrillManifest(c, m.ID, other) == nil {
				t.Fatal("completion falsely certified another authenticated backup")
			}
		})
	}
}

// Uses only a disposable local container with networking disabled. The same
// helper used by the drill must recover a source tuned above PostgreSQL defaults.
func TestPostgresRestoreTunedSourceDocker(t *testing.T) {
	if os.Getenv("RTK_POSTGRES_BACKUP_INTEGRATION") != "1" {
		t.Skip("set RTK_POSTGRES_BACKUP_INTEGRATION=1")
	}
	docker, err := exec.LookPath("docker")
	if err != nil || exec.Command(docker, "image", "inspect", "postgres:16-alpine").Run() != nil {
		t.Fatal("opt-in drill test requires Docker and a local postgres:16-alpine image")
	}
	root, err := os.MkdirTemp("/tmp", "pg-drill-")
	if err != nil {
		t.Fatal(err)
	}
	hostOwner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	target := filepath.Join(root, "restore")
	name := "rtk-pg-drill-" + postgresBackupRandomID()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		// Restore private host ownership before Go reads or removes bind-mounted files.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, docker, "exec", "--user", "postgres", name, "pg_ctl", "-D", filepath.Join(target, "pgdata"), "-m", "fast", "-w", "-t", "5", "stop").Run()
		_ = exec.CommandContext(cleanupCtx, docker, "exec", "--user", "root", name, "chown", "-R", hostOwner, root).Run()
		_ = exec.Command(docker, "rm", "-fv", name).Run()
		_ = os.RemoveAll(root)
	})
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture command %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	command("run", "-d", "--network", "none", "--name", name, "--label", "rtk.postgres-backup.test=true", "--mount", "type=bind,source="+root+",target="+root, "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16-alpine", "postgres", "-c", "max_connections=800", "-c", "max_worker_processes=16", "-c", "max_wal_senders=12", "-c", "max_prepared_transactions=10", "-c", "max_locks_per_transaction=128", "-c", "checkpoint_timeout=30s", "-c", "checkpoint_completion_target=0.1")
	for {
		if exec.CommandContext(ctx, docker, "exec", name, "sh", "-c", `[ "$(cat /proc/1/comm)" = postgres ] && pg_isready -U postgres`).Run() == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("fixture startup timed out")
		}
		time.Sleep(200 * time.Millisecond)
	}
	query := func(db, sql string) {
		command("exec", name, "psql", "-X", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	query("postgres", "CREATE ROLE rtk_postgres_backup LOGIN REPLICATION NOSUPERUSER")
	for _, db := range []string{"rtk_account_manager", "rtk_billing", "video_cloud"} {
		query("postgres", "CREATE DATABASE "+db)
	}
	query("rtk_account_manager", "CREATE TABLE organizations(id int); CREATE TABLE users(id int); CREATE TABLE organization_members(organization_id int,user_id int); INSERT INTO organizations VALUES(1); INSERT INTO users VALUES(2); INSERT INTO organization_members VALUES(1,2); CREATE TABLE device_operations(operation_id int); CREATE TABLE device_message_outbox(operation_id int,attempt_count int); INSERT INTO device_operations VALUES(3); INSERT INTO device_message_outbox VALUES(3,0)")
	query("rtk_billing", "CREATE TABLE commercial_accounts(id int,currency text); CREATE TABLE balance_ledger_entries(account_id int,currency text,amount_minor bigint); INSERT INTO commercial_accounts VALUES(1,'TWD'); INSERT INTO balance_ledger_entries VALUES(1,'TWD',100)")
	query("video_cloud", "CREATE TABLE devices(id int,info jsonb,config jsonb); INSERT INTO devices VALUES(1,'{}','{}'); CREATE TABLE device_presence_outbox(generation int,attempts int,status text); INSERT INTO device_presence_outbox VALUES(1,0,'online')")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	command("exec", "--user", hostOwner, name, "pg_basebackup", "-U", "postgres", "-D", filepath.Join(target, "pgdata"), "-Fp", "-X", "stream", "--checkpoint=spread")
	command("exec", "--user", hostOwner, name, "pg_verifybackup", filepath.Join(target, "pgdata"))
	run := func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
		user := "postgres"
		if args[0] == "env" {
			user = hostOwner
		}
		if args[0] == "pg_ctl" && args[len(args)-1] == "start" {
			command("exec", name, "chmod", "0711", root)
			command("exec", name, "chown", "-R", "postgres:postgres", target)
		}
		argv := append([]string{"exec", "-i", "-u", user, name}, args...)
		cmd := exec.CommandContext(ctx, docker, argv...)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w: %s", args[0], err, stderr.String())
		}
		return nil
	}
	drillErr := postgresBackupVerifyRunningClusterWithExecutor(ctx, target, run)
	// The helper stops the PostgreSQL-owned restored server before returning.
	// Linux bind mounts preserve that UID, unlike Docker Desktop's host mapping.
	command("exec", "--user", "root", name, "chown", "-R", hostOwner, target)
	if drillErr != nil {
		log, _ := os.ReadFile(filepath.Join(target, "postgres.log"))
		t.Fatalf("tuned source drill failed: %v\n%s", drillErr, log)
	}
	conf, err := os.ReadFile(filepath.Join(target, "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"max_connections = 800", "max_worker_processes = 16", "max_wal_senders = 12", "max_prepared_transactions = 10", "max_locks_per_transaction = 128"} {
		if !strings.Contains(string(conf), setting) {
			t.Fatalf("missing recovered setting %s", setting)
		}
	}
	query("video_cloud", "SELECT count(*) FROM devices")
}

package postgresbackup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

// Real PostgreSQL generates, archives and replays WAL in a disposable,
// network-isolated container. The encrypted immutable repository is a fixture;
// this test does not claim live provider or production qualification.
func TestContinuousWALNativePITRReplaysToExactTimeAndRemainsFenced(t *testing.T) {
	if os.Getenv("RTK_POSTGRES_BACKUP_INTEGRATION") != "1" {
		t.Skip("set RTK_POSTGRES_BACKUP_INTEGRATION=1")
	}
	docker, err := exec.LookPath("docker")
	if err != nil || exec.Command(docker, "image", "inspect", "postgres:16-alpine").Run() != nil {
		t.Fatal("native PITR fixture requires Docker and an already-installed PostgreSQL 16 Alpine image; never pulls")
	}
	c, _, identity := workerFixture(t)
	root := filepath.Dir(c.Directory)
	c.Source.Host, c.Source.SSLMode = "127.0.0.1", "disable"
	c.MaxPlaintextBytes, c.MaxArchiveBytes, c.TimeoutSeconds = 256<<20, 256<<20, 180
	hostOwner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	name := "rtk-pitr-" + recovery.NewID()
	command := `mkdir -p /tmp/rtk-wal-archive; chown postgres:postgres /tmp/rtk-wal-archive; chmod 0700 /tmp/rtk-wal-archive; exec docker-entrypoint.sh postgres -c archive_mode=on -c archive_timeout=60s -c 'archive_command=cp -- %p /tmp/rtk-wal-archive/%f'`
	if exec.CommandContext(ctx, docker, "run", "-d", "--network", "none", "--name", name, "--label", "rtk.postgres-backup.test=true", "--mount", "type=bind,source="+root+",target="+root, "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "-e", "PGDATA=/var/lib/postgresql/data/pgdata", "--entrypoint", "sh", "postgres:16-alpine", "-ceu", command).Run() != nil {
		t.Fatal("cannot start disposable native WAL fixture")
	}
	var restored string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if restored != "" {
			_ = exec.CommandContext(cleanup, docker, "exec", "--user", "postgres", name, "pg_ctl", "-D", restored, "-m", "fast", "-w", "-t", "5", "stop").Run()
		}
		if exec.CommandContext(cleanup, docker, "exec", "--user", "root", name, "chown", "-R", hostOwner, root).Run() != nil {
			t.Error("fixture ownership cleanup failed")
		}
		if exec.CommandContext(cleanup, docker, "rm", "-fv", name).Run() != nil {
			t.Error("fixture container cleanup failed")
		}
	})
	for exec.CommandContext(ctx, docker, "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() != nil {
		if ctx.Err() != nil {
			t.Fatal("native WAL fixture startup timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
	query := func(port, sql string) string {
		t.Helper()
		args := []string{"exec"}
		host := "127.0.0.1"
		if port == "55432" {
			host = "/tmp"
			args = append(args, "--user", "postgres")
		}
		args = append(args, name, "psql", "-X", "-U", "postgres", "-h", host, "-p", port, "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
		raw, err := exec.CommandContext(ctx, docker, args...).Output()
		if err != nil {
			t.Fatal("native PITR fixture SQL failed")
		}
		return strings.TrimSpace(string(raw))
	}
	c.Source.SystemIdentifier = query("5432", "SELECT system_identifier FROM pg_control_system()")
	query("5432", "CREATE ROLE backup REPLICATION LOGIN NOINHERIT PASSWORD 'fixture-password'; GRANT pg_monitor TO backup; CREATE TABLE pitr_records(id integer PRIMARY KEY,value text); INSERT INTO pitr_records VALUES(1,'base-backup')")
	engine := Engine{remote: newMemoryRepository(), Exec: func(ctx context.Context, argv, env []string, out io.Writer) error {
		args := []string{"exec", "--user", hostOwner}
		for _, setting := range env {
			key, _, _ := strings.Cut(setting, "=")
			if strings.HasPrefix(key, "PG") || key == "LC_ALL" || key == "LANG" {
				args = append(args, "-e", setting)
			}
		}
		args = append(args, name)
		args = append(args, argv...)
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Stdout, cmd.Stderr = out, io.Discard
		return cmd.Run()
	}}
	base, err := engine.Capture(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	query("5432", "INSERT INTO pitr_records VALUES(2,'replayed-from-encrypted-wal')")
	at, err := time.Parse(time.RFC3339Nano, query("5432", `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`))
	if err != nil {
		t.Fatal("native target timestamp unavailable")
	}
	time.Sleep(20 * time.Millisecond)
	query("5432", "INSERT INTO pitr_records VALUES(3,'after-target-must-be-excluded')")
	last := query("5432", "SELECT pg_walfile_name(pg_current_wal_lsn())")
	query("5432", "SELECT pg_switch_wal()")
	for query("5432", "SELECT COALESCE(last_archived_wal,'') FROM pg_stat_archiver") < last {
		if ctx.Err() != nil {
			t.Fatal("native archive did not complete")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Mirror actual pg_control and native archive files for the host encryptor.
	// Production archive-wal runs in the source container and needs no mirror.
	mirror := filepath.Join(root, "source-mirror")
	for _, directory := range []string{mirror, filepath.Join(mirror, "global"), filepath.Join(mirror, "pg_wal")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	copyOut := func(source, target string) {
		t.Helper()
		if exec.CommandContext(ctx, docker, "cp", name+":"+source, target).Run() != nil {
			t.Fatal("cannot copy actual native fixture evidence", source)
		}
	}
	copyOut("/var/lib/postgresql/data/pgdata/PG_VERSION", filepath.Join(mirror, "PG_VERSION"))
	copyOut("/var/lib/postgresql/data/pgdata/global/pg_control", filepath.Join(mirror, "global", "pg_control"))
	copyOut("/tmp/rtk-wal-archive/.", filepath.Join(mirror, "pg_wal"))
	entries, err := os.ReadDir(filepath.Join(mirror, "pg_wal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range entries {
		if !walName.MatchString(file.Name()) {
			t.Fatal("unexpected native archive file")
		}
		source := filepath.Join(mirror, "pg_wal", file.Name())
		if err := engine.VerifyWALSource(ctx, c, mirror, file.Name(), source); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.ArchiveWAL(ctx, c, file.Name(), source); err != nil {
			t.Fatal(err)
		}
	}
	if health, err := engine.CheckContinuousWAL(ctx, c); err != nil || health.SystemIdentifier != c.Source.SystemIdentifier || health.LastArchivedWAL == "" {
		t.Fatal("native continuous WAL health or remote read-back failed", err)
	}
	if _, err := engine.HeartbeatWAL(ctx, c); err != nil {
		t.Fatal("native idle WAL heartbeat failed for NOINHERIT monitor session", err)
	}
	before := query("5432", "SELECT pg_current_wal_lsn()")
	foreign := c
	foreign.Source.SystemIdentifier = "9999999999999999999"
	if _, err := engine.HeartbeatWAL(ctx, foreign); err == nil || query("5432", "SELECT pg_current_wal_lsn()") != before {
		t.Fatal("foreign source received WAL heartbeat")
	}
	started := time.Now()
	target := filepath.Join(root, "pitr-target")
	prepared, err := engine.PreparePITR(ctx, c, base.File, identity, target, PITRTarget{At: at, Timeline: 1, MaxWALBytes: 128 << 20})
	if err != nil || len(prepared.WAL) == 0 {
		t.Fatal("native PITR preparation failed", err)
	}
	pgdata := filepath.Join(target, "pgdata")
	for _, args := range [][]string{{"chmod", "0711", root, target}} {
		if exec.CommandContext(ctx, docker, append([]string{"exec", "--user", "root", name}, args...)...).Run() != nil {
			t.Fatal("cannot prepare isolated native restore ownership")
		}
	}
	restored = pgdata
	verifier := Engine{Exec: func(ctx context.Context, argv, env []string, out io.Writer) error {
		// Host preparation first verifies the immutable configuration fence.
		// The disposable container then transfers ownership to its postgres UID.
		if argv[0] == "pg_ctl" && argv[len(argv)-1] == "start" {
			if err := exec.CommandContext(ctx, docker, "exec", "--user", "root", name, "chown", "-R", "postgres:postgres", target).Run(); err != nil {
				return err
			}
		}
		args := append([]string{"exec", "--user", "postgres", name}, argv...)
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Stdout, cmd.Stderr = out, io.Discard
		return cmd.Run()
	}}
	observation, err := verifier.VerifyPreparedPITR(ctx, c, target, prepared, func(query RestoredQuery) error {
		value, err := query("postgres", "SELECT string_agg(id::text||':'||value,',' ORDER BY id) FROM pitr_records")
		if err != nil || value != "1:base-backup,2:replayed-from-encrypted-wal" {
			return fmt.Errorf("native WAL did not recover the selected timestamp")
		}
		return nil
	})
	if err != nil {
		if log, readErr := exec.CommandContext(ctx, docker, "exec", "--user", "postgres", name, "tail", "-n", "30", filepath.Join(target, "postgres.log")).Output(); readErr == nil {
			t.Log(strings.ReplaceAll(string(log), "fixture-password", "[redacted]"))
		}
		t.Fatal("native PITR verification failed", err)
	}
	if query("5432", "SELECT count(*) FROM pitr_records") != "3" {
		t.Fatal("PITR changed source data")
	}
	if time.Since(started) > 4*time.Hour || observation.ReplayTime.After(at) || observation.RTOms <= 0 {
		t.Fatal("isolated native target or measured restore objective failed")
	}
	t.Logf("isolated native PITR: target recovered, later transaction excluded, writes fenced and target stopped; RTO=%s; production RPO still requires an independent fault-time witness", time.Since(started).Round(time.Millisecond))
}

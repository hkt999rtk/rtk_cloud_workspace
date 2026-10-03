package postgresbackup

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

// This opt-in test uses an already-installed image and a network-isolated
// container. It never pulls images or connects to a deployed environment.
func TestPhysicalPostgres16DockerRoundTrip(t *testing.T) {
	if os.Getenv("RTK_POSTGRES_BACKUP_INTEGRATION") != "1" {
		t.Skip("set RTK_POSTGRES_BACKUP_INTEGRATION=1")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker unavailable")
	}
	if exec.Command(docker, "image", "inspect", "postgres:16-alpine").Run() != nil {
		t.Skip("local postgres:16-alpine image and Docker daemon required; test never pulls")
	}
	c, _, identity := workerFixture(t)
	root := filepath.Dir(c.Directory)
	c.Source.Host = "127.0.0.1"
	c.Source.SSLMode = "disable"
	c.MaxPlaintextBytes = 256 << 20
	c.MaxArchiveBytes = 256 << 20
	c.TimeoutSeconds = 120
	name := "rtk-pg-physical-" + recovery.NewID()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err = exec.CommandContext(ctx, docker, "run", "-d", "--network", "none", "--name", name, "--label", "rtk.postgres-backup.test=true", "--mount", "type=bind,source="+root+",target="+root, "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16-alpine", "postgres", "-c", "checkpoint_timeout=30s", "-c", "checkpoint_completion_target=0.1").Run(); err != nil {
		t.Fatal("cannot start isolated PostgreSQL fixture")
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-fv", name).Run() })
	for {
		if exec.CommandContext(ctx, docker, "exec", name, "sh", "-c", `[ "$(cat /proc/1/comm)" = postgres ] && pg_isready -U postgres`).Run() == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("isolated PostgreSQL fixture did not become ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	query := func(port, db, sql string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, docker, "exec", name, "psql", "-X", "-U", "postgres", "-h", "127.0.0.1", "-p", port, "-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql).Output()
		if err != nil {
			t.Fatal("fixture SQL failed")
		}
		return strings.TrimSpace(string(b))
	}
	c.Source.SystemIdentifier = query("5432", "postgres", "SELECT system_identifier FROM pg_control_system()")
	query("5432", "postgres", "CREATE ROLE backup REPLICATION LOGIN PASSWORD 'fixture-password'; CREATE ROLE app_reader;")
	query("5432", "postgres", "CREATE DATABASE accounts")
	query("5432", "accounts", "CREATE TABLE records(id integer PRIMARY KEY,value text); INSERT INTO records VALUES(1,'captured'); GRANT SELECT ON records TO app_reader")
	query("5432", "postgres", "CREATE DATABASE billing")
	query("5432", "billing", "CREATE TABLE receipts(id integer PRIMARY KEY); INSERT INTO receipts VALUES(42)")
	e := Engine{Exec: func(ctx context.Context, args, env []string, out io.Writer) error {
		argv := []string{"exec"}
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if strings.HasPrefix(key, "PG") || key == "LC_ALL" || key == "LANG" {
				argv = append(argv, "-e", entry)
			}
		}
		argv = append(argv, name)
		argv = append(argv, args...)
		cmd := exec.CommandContext(ctx, docker, argv...)
		cmd.Stdout = out
		cmd.Stderr = io.Discard
		return cmd.Run()
	}}
	r, err := e.Capture(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	query("5432", "accounts", "UPDATE records SET value='after-backup'; INSERT INTO records VALUES(2,'new'); REVOKE SELECT ON records FROM app_reader")
	target := filepath.Join(root, "restore")
	if _, err = e.Restore(ctx, c, r.File, identity, target); err != nil {
		t.Fatal(err)
	}
	pgdata := filepath.Join(target, "pgdata")
	badTarget := filepath.Join(root, "invalid-native")
	if _, err = Unpack(ctx, c, r.File, identity, badTarget); err != nil {
		t.Fatal(err)
	}
	badData := filepath.Join(badTarget, "pgdata")
	workerWrite(t, filepath.Join(badData, "backup_manifest"), []byte("invalid native manifest"))
	if err = e.verify(ctx, c, badData); err == nil {
		t.Fatal("real pg_verifybackup accepted a malformed native manifest")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(pgdata, "backup_manifest"))
	if err != nil {
		t.Fatal(err)
	}
	workerWrite(t, filepath.Join(badData, "backup_manifest"), manifestBytes)
	wals, err := os.ReadDir(filepath.Join(badData, "pg_wal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wal := range wals {
		if wal.Type().IsRegular() {
			if err = os.Remove(filepath.Join(badData, "pg_wal", wal.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = e.verify(ctx, c, badData); err == nil {
		t.Fatal("real pg_verifybackup accepted missing recovery WAL")
	}
	// Only the test's fresh directory is made accessible to the fixture's
	// PostgreSQL UID; the production target is never involved.
	if err = exec.CommandContext(ctx, docker, "exec", name, "chmod", "0711", root, target).Run(); err != nil {
		t.Fatal("fixture traversal permissions")
	}
	if err = exec.CommandContext(ctx, docker, "exec", name, "chown", "-R", "postgres:postgres", pgdata).Run(); err != nil {
		t.Fatal("fixture ownership")
	}
	if err = exec.CommandContext(ctx, docker, "exec", "-u", "postgres", name, "pg_ctl", "-D", pgdata, "-l", filepath.Join(pgdata, "restore.log"), "-o", "-p 55432 -h 127.0.0.1 -c archive_mode=off -c primary_conninfo=''", "-w", "start").Run(); err != nil {
		t.Fatal("restored PostgreSQL failed to start")
	}
	if got := query("55432", "accounts", "SELECT value FROM records WHERE id=1"); got != "captured" {
		t.Fatal("wrong restored row")
	}
	if got := query("55432", "accounts", "SELECT count(*) FROM records"); got != "1" {
		t.Fatal("post-capture row leaked into restore")
	}
	if got := query("55432", "accounts", "SELECT has_table_privilege('app_reader','records','SELECT')"); got != "t" {
		t.Fatal("role grant not restored")
	}
	if got := query("55432", "billing", "SELECT id FROM receipts"); got != "42" {
		t.Fatal("second database not restored")
	}
	if got := query("5432", "accounts", "SELECT value FROM records WHERE id=1"); got != "after-backup" {
		t.Fatal("source data changed during restore")
	}
}

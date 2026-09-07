package recovery

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresPhysicalEncryptedRoundTrip(t *testing.T) {
	if os.Getenv("RTK_PHYSICAL_BACKUP_INTEGRATION") != "1" {
		t.Skip("set RTK_PHYSICAL_BACKUP_INTEGRATION=1; cached postgres:16-alpine required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	if err = exec.Command(docker, "image", "inspect", "postgres:16-alpine").Run(); err != nil {
		t.Fatal("cached PostgreSQL 16 image required")
	}
	directory, err := os.MkdirTemp("/private/tmp", "rtk-physical-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	name := "rtk-physical-" + NewID()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, docker, "run", "--detach", "--pull", "never", "--network", "none", "--name", name, "--mount", "type=bind,source="+directory+",target="+directory, "--env", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16-alpine").CombinedOutput(); err != nil {
		t.Fatalf("start isolated primary: %s %v", out, err)
	}
	t.Cleanup(func() { exec.Command(docker, "rm", "-fv", name).Run() })
	for {
		if exec.CommandContext(ctx, docker, "exec", name, "sh", "-c", `[ "$(cat /proc/1/comm)" = postgres ] && pg_isready -U postgres`).Run() == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(200 * time.Millisecond)
	}
	query := func(port, sql string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, docker, "exec", name, "psql", "-X", "-U", "postgres", "-p", port, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	query("5432", "CREATE TABLE physical_fixture(id integer PRIMARY KEY, value text); INSERT INTO physical_fixture VALUES(1,'captured'); CREATE ROLE physical_reader;")
	systemID := query("5432", "SELECT system_identifier FROM pg_control_system()")
	wal, _, _, identity := walFixture(t)
	wal.Directory = filepath.Join(directory, "spool")
	wal.SystemIdentifier = systemID
	wal.SegmentBytes = 16 << 20
	serviceFile := filepath.Join(directory, "service.conf")
	if err = os.WriteFile(serviceFile, []byte("[physical]\nhost=/var/run/postgresql\nuser=postgres\ndbname=postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := BaseBackupConfig{WAL: wal, BinaryDirectory: "/usr/local/bin", ServiceFile: serviceFile, Service: "physical", TimeoutSeconds: 120, MaxArchiveBytes: 128 << 20}
	e := BaseBackupEngine{Config: cfg, Exec: func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
		return QuietExec(ctx, append([]string{docker, "exec", "--env", "PGSERVICEFILE=" + serviceFile, "--env", "LC_ALL=C", name}, argv...), in, out)
	}}
	path, err := e.Stage(ctx, "fixture")
	if err != nil {
		t.Fatal("physical capture", err)
	}
	before, err := DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A retry must not recapture newer database contents.
	query("5432", "INSERT INTO physical_fixture VALUES(2,'after-backup')")
	if _, err = e.Stage(ctx, "fixture"); err != nil {
		t.Fatal("durable retry", err)
	}
	after, _ := DigestFile(path)
	if before != after {
		t.Fatal("retry changed captured backup")
	}
	objects := &memoryObjects{objects: map[string][]byte{}, ambiguous: true}
	if err = upload(ctx, objects, cfg.remoteConfig(), "fixture", path); err != nil {
		t.Fatal(err)
	}
	downloadDir := filepath.Join(directory, "download")
	if err = PrivateDirectory(downloadDir); err != nil {
		t.Fatal(err)
	}
	downloaded, err := download(ctx, objects, cfg.remoteConfig(), "fixture", downloadDir)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "identity")
	if err = os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "restored")
	if err = e.restoreFile(ctx, "fixture", downloaded, destination, key); err != nil {
		t.Fatal("encrypted physical restore", err)
	}
	if err = e.restoreFile(ctx, "fixture", downloaded, destination, key); err == nil {
		t.Fatal("existing target accepted")
	}
	// Independently prove that native verification rejects missing required WAL.
	badPayload := filepath.Join(directory, "bad-payload")
	if _, err = unpackScoped(downloaded, key, badPayload, cfg.MaxArchiveBytes, physicalScope); err != nil {
		t.Fatal(err)
	}
	badData := filepath.Join(directory, "bad-data")
	if err = extractBaseTar(ctx, filepath.Join(badPayload, "base.tar"), badData, cfg.MaxArchiveBytes); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(badData, "pg_wal"))
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, entry := range entries {
		if walName.MatchString(entry.Name()) {
			if err = os.Remove(filepath.Join(badData, "pg_wal", entry.Name())); err != nil {
				t.Fatal(err)
			}
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("base backup did not include WAL")
	}
	if _, err = e.verify(ctx, badData); err == nil {
		t.Fatal("native verification accepted missing WAL")
	}
	wrong := e
	wrong.Config.WAL.SystemIdentifier = "1"
	if _, err = wrong.verify(ctx, filepath.Join(destination, "pgdata")); err == nil {
		t.Fatal("wrong cluster accepted")
	}
	wrong = e
	wrong.Config.WAL.SegmentBytes = 1 << 20
	if _, err = wrong.verify(ctx, filepath.Join(destination, "pgdata")); err == nil {
		t.Fatal("wrong archive segment size accepted")
	}
	if err = exec.CommandContext(ctx, docker, "exec", name, "chown", "-R", "postgres:postgres", destination).Run(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, docker, "exec", "--user", "postgres", name, "pg_ctl", "-D", filepath.Join(destination, "pgdata"), "-l", filepath.Join(destination, "server.log"), "-o", "-p 5433", "-w", "start").CombinedOutput(); err != nil {
		t.Fatalf("start restored cluster: %s %v", out, err)
	}
	if got := query("5433", "SELECT id || ':' || value FROM physical_fixture ORDER BY id"); got != "1:captured" {
		t.Fatal("restored data differs", got)
	}
	if got := query("5433", "SELECT count(*) FROM pg_roles WHERE rolname='physical_reader'"); got != "1" {
		t.Fatal("cluster role missing", got)
	}
	if got := query("5433", "SELECT pg_is_in_recovery()"); got != "f" {
		t.Fatal("backup consistency replay incomplete", got)
	}
	t.Log("native manifest/WAL validation, encrypted remote round trip and independent cluster startup passed")
}

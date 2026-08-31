package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedisDurableRoundTrip(t *testing.T) {
	if os.Getenv("RTK_RECOVERY_INTEGRATION") != "1" {
		t.Skip("set RTK_RECOVERY_INTEGRATION=1 for isolated Redis/Postgres drills")
	}
	server, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server unavailable")
	}
	cli, err := exec.LookPath("redis-cli")
	if err != nil {
		t.Skip("redis-cli unavailable")
	}
	// Keep Unix socket below platform path-length limits; never bind TCP.
	tmp, err := os.MkdirTemp("", "rtk-redis-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	socket := filepath.Join(tmp, "redis.sock")
	cmd := exec.Command(server, "--port", "0", "--unixsocket", socket, "--save", "", "--appendonly", "no", "--dir", tmp)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	redis := func(args ...string) []byte {
		t.Helper()
		b, err := exec.Command(cli, append([]string{"-s", socket, "-e", "--raw"}, args...)...).Output()
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(b)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Redis did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	redis("SET", "shadow:document", "original")
	redis("SADD", "shadow:index", "one", "two")
	redis("XADD", "shadow:outbox", "*", "event", "created")
	redis("SET", "cache:ephemeral", "not-backed-up", "EX", "3600")
	redis("SET", "shadow:publisher-lock", "lease", "EX", "3600")
	c := Component{ID: "shadow", Kind: "redis", Namespace: "platform", Pod: "redis-0", Prefixes: []string{"shadow:"}, ExcludeKeys: []string{"shadow:publisher-lock"}}
	e := Engine{Config: Config{TimeoutSeconds: 5, MaxArchiveBytes: 1 << 20}, Exec: func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
		for i, arg := range argv {
			if arg == "--" {
				return QuietExec(ctx, append([]string{cli, "-s", socket}, argv[i+2:]...), in, out)
			}
		}
		t.Fatal("unexpected command")
		return nil
	}}
	var b bytes.Buffer
	if err = e.captureRedis(context.Background(), c, &b); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(privateTemp(t), "redis.jsonl")
	writeTest(t, archive, b.Bytes())
	if bytes.Contains(b.Bytes(), []byte("cache:ephemeral")) {
		t.Fatal("cache included")
	}
	redis("DEL", "shadow:document", "shadow:index", "shadow:outbox")
	redis("SET", "shadow:stale", "delete-me")
	if err = e.restoreRedis(context.Background(), c, archive); err != nil {
		t.Fatal(err)
	}
	if string(redis("GET", "shadow:document")) != "original" || string(redis("SCARD", "shadow:index")) != "2" || string(redis("XLEN", "shadow:outbox")) != "1" {
		t.Fatal("durable Redis data not restored")
	}
	if string(redis("EXISTS", "shadow:stale")) != "0" || string(redis("GET", "cache:ephemeral")) != "not-backed-up" {
		t.Fatal("restore scope wrong")
	}
	redis("SET", "shadow:lease", "temporary", "EX", "100")
	if e.captureRedis(context.Background(), c, io.Discard) == nil {
		t.Fatal("expiring durable key accepted")
	}
}

func TestPostgresLogicalRoundTrip(t *testing.T) {
	if os.Getenv("RTK_RECOVERY_INTEGRATION") != "1" {
		t.Skip("set RTK_RECOVERY_INTEGRATION=1")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker unavailable")
	}
	name := "rtk-recovery-test-" + NewID()
	if err = exec.Command(docker, "image", "inspect", "postgres:16-alpine").Run(); err != nil {
		t.Skip("local postgres:16-alpine image required; test never pulls")
	}
	if b, err := exec.Command(docker, "run", "-d", "--network", "none", "--name", name, "--label", "rtk.recovery.test=true", "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16-alpine").CombinedOutput(); err != nil {
		t.Fatalf("isolated Postgres start: %s", b)
	}
	t.Cleanup(func() { exec.Command(docker, "rm", "-fv", name).Run() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		if exec.CommandContext(ctx, docker, "exec", name, "sh", "-c", `[ "$(cat /proc/1/comm)" = postgres ] && pg_isready -U postgres`).Run() == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("Postgres unavailable")
		}
		time.Sleep(200 * time.Millisecond)
	}
	query := func(db, sql string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, docker, "exec", name, "psql", "-X", "-U", "postgres", "-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	query("postgres", "CREATE ROLE app_reader LOGIN PASSWORD 'fixture-only';")
	query("postgres", "CREATE DATABASE accounts;")
	query("accounts", "CREATE TABLE records(id integer PRIMARY KEY, value text); INSERT INTO records VALUES (1,'original'); GRANT SELECT ON records TO app_reader;")
	components := []Component{{ID: "globals", Kind: "postgres", Pod: "postgres-0", Namespace: "platform", User: "postgres", Database: "@globals"}, {ID: "accounts", Kind: "postgres", Pod: "postgres-0", Namespace: "platform", User: "postgres", Database: "accounts"}}
	e := Engine{Config: Config{Components: components, TimeoutSeconds: 20, MaxArchiveBytes: 16 << 20}, Exec: func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
		for i, arg := range argv {
			if arg == "--" {
				return QuietExec(ctx, append([]string{docker, "exec", "-i", name}, argv[i+1:]...), in, out)
			}
		}
		t.Fatal("unexpected command")
		return nil
	}}
	if err = e.checkDatabases(ctx); err != nil {
		t.Fatal(err)
	}
	dir := privateTemp(t)
	if err = e.Capture(ctx, dir); err != nil {
		t.Fatal(err)
	}
	query("accounts", "UPDATE records SET value='destroyed'; INSERT INTO records VALUES (2,'extra'); REVOKE SELECT ON records FROM app_reader;")
	if err = e.Apply(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if query("accounts", "SELECT value FROM records WHERE id=1") != "original" || query("accounts", "SELECT count(*) FROM records") != "1" {
		t.Fatal("Postgres rows not recovered")
	}
	if query("accounts", "SELECT has_table_privilege('app_reader','records','SELECT')") != "t" {
		t.Fatal("role permissions not restored")
	}
	query("postgres", "CREATE DATABASE missing_inventory;")
	if e.checkDatabases(ctx) == nil {
		t.Fatal("uninventoried DB accepted")
	}
}

func TestArchiveManifestJSONIsNotExecutableConfig(t *testing.T) {
	c := fixture(t)
	m := Manifest{Environment: c.Environment, Stack: c.Stack, Components: c.Components, ConfigurationSHA256: InventoryFingerprint(c)}
	b, _ := json.Marshal(m)
	if bytes.Contains(b, []byte("argv")) {
		t.Fatal("operator commands should not be loaded from backup")
	}
}

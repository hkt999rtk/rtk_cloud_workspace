package postgresbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func workerPrivateTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func workerFixture(t *testing.T) (Config, *age.X25519Identity, string) {
	t.Helper()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := workerPrivateTemp(t)
	password := filepath.Join(dir, "password")
	workerWrite(t, password, []byte("fixture-password\n"))
	identity := filepath.Join(dir, "identity")
	workerWrite(t, identity, []byte(key.String()+"\n"))
	c := Config{Version: 1, Environment: "staging", Stack: "video-cloud-staging", ClusterID: "postgresql", Directory: filepath.Join(dir, "archives"), Source: Source{Host: "postgresql.internal", Port: 5432, User: "backup", PasswordFile: password, SSLMode: "require", PostgresImage: "postgres@sha256:" + strings.Repeat("a", 64), SystemIdentifier: "123456789"}, Recipients: []string{key.Recipient().String()}, Remote: recovery.Remote{Endpoint: "https://backup.example.invalid", Region: "test", Bucket: "rtk-cloud-staging-backup-test", Prefix: "staging/postgres-physical"}, TimeoutSeconds: 10, MaxArchiveBytes: 4 << 20, MaxPlaintextBytes: 4 << 20, RetentionDays: 14, MinimumBackups: 14}
	return c, key, identity
}

func workerWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func workerNative(t *testing.T, dir string) {
	t.Helper()
	for name, content := range map[string]string{"PG_VERSION": "16\n", "backup_manifest": "{\"fixture\":true}", "base/1/123": "rows", "global/pg_control": "fixture control", "pg_wal/000000010000000000000001": "fixture WAL"} {
		workerWrite(t, filepath.Join(dir, name), []byte(content))
	}
	if err := os.MkdirAll(filepath.Join(dir, "pg_tblspc"), 0700); err != nil {
		t.Fatal(err)
	}
}

func workerManifest(t *testing.T, c Config, dir string) Manifest {
	t.Helper()
	a, err := recovery.DigestFile(filepath.Join(dir, "backup_manifest"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return Manifest{Version: 1, Scope: Scope, ID: "backup-fixture", Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, PostgresMajor: 16, PostgresImage: c.Source.PostgresImage, SystemIdentifier: c.Source.SystemIdentifier, StartedAt: now.Add(-time.Minute), FinishedAt: now, NativeManifestSHA256: a.SHA256}
}

func workerExec(t *testing.T, c Config, fail string) Executor {
	t.Helper()
	return func(ctx context.Context, args, env []string, out io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if args[0] == fail {
			return errors.New("fixture-password must not escape")
		}
		switch args[0] {
		case "pg_basebackup":
			if len(args) == 2 && args[1] == "--version" {
				_, err := io.WriteString(out, "pg_basebackup (PostgreSQL) 16.10\n")
				return err
			}
			joined := strings.Join(args, " ")
			for _, s := range []string{"--wal-method=stream", "--checkpoint=spread", "--max-rate=10M", "--no-estimate-size"} {
				if !strings.Contains(joined, s) {
					t.Fatalf("missing safe backup flag %s", s)
				}
			}
			if strings.Contains(joined, "fixture-password") || strings.Contains(strings.Join(env, " "), "fixture-password") {
				t.Fatal("password exposed outside passfile")
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--pgdata=") {
					workerNative(t, strings.TrimPrefix(arg, "--pgdata="))
					return nil
				}
			}
			return errors.New("missing capture path")
		case "psql":
			if strings.Contains(strings.Join(args, " "), "server_version_num") {
				_, err := io.WriteString(out, "160010|0\n")
				return err
			}
			_, err := io.WriteString(out, c.Source.SystemIdentifier+"|1|0/10000|\n")
			return err
		case "pg_controldata":
			_, err := io.WriteString(out, "Database system identifier:            "+c.Source.SystemIdentifier+"\n")
			return err
		case "pg_verifybackup":
			return nil
		default:
			return errors.New("unexpected fixture command")
		}
	}
}

func TestPhysicalCaptureRestoreAndPlaintextCleanup(t *testing.T) {
	c, _, identity := workerFixture(t)
	e := Engine{Exec: workerExec(t, c, "")}
	r, err := e.Capture(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest.Scope != Scope || r.Manifest.PostgresMajor != 16 {
		t.Fatal("wrong manifest")
	}
	if _, err = ReadManifest(filepath.Join(c.Directory, r.Manifest.ID+".manifest.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(c.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatal("plaintext capture not removed")
	}
	for _, entry := range entries {
		i, _ := entry.Info()
		if i.Mode().Perm() != 0600 {
			t.Fatal("output permissions")
		}
	}
	target := filepath.Join(workerPrivateTemp(t), "restore")
	got, err := e.Restore(context.Background(), c, r.File, identity, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != r.Manifest.ID {
		t.Fatal("restored different identity")
	}
	data, err := os.ReadFile(filepath.Join(target, "pgdata/base/1/123"))
	if err != nil || string(data) != "rows" {
		t.Fatal("physical data changed")
	}
	if _, err = e.Restore(context.Background(), c, r.File, identity, target); err == nil {
		t.Fatal("nonempty restore target accepted")
	}
	data, _ = os.ReadFile(filepath.Join(target, "pgdata/base/1/123"))
	if string(data) != "rows" {
		t.Fatal("failed retry removed existing target")
	}
}

func TestPhysicalCaptureFailuresCleanPlaintextAndRedact(t *testing.T) {
	for _, fail := range []string{"psql", "pg_basebackup", "pg_verifybackup"} {
		t.Run(fail, func(t *testing.T) {
			c, _, _ := workerFixture(t)
			_, err := (Engine{Exec: workerExec(t, c, fail)}).Capture(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "fixture-password") {
				t.Fatal("failure missing or secret exposed")
			}
			entries, _ := os.ReadDir(c.Directory)
			if len(entries) != 0 {
				t.Fatal("capture plaintext survived failure")
			}
		})
	}
	c, _, _ := workerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Engine{Exec: workerExec(t, c, "")}).Capture(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	entries, _ := os.ReadDir(c.Directory)
	if len(entries) != 0 {
		t.Fatal("cancelled capture left plaintext")
	}
}

func TestPhysicalPreflightRejectsVersionAndTablespacesBeforeCopy(t *testing.T) {
	for _, catalog := range []string{"170001|0\n", "160010|1\n", "garbage"} {
		t.Run(strings.TrimSpace(catalog), func(t *testing.T) {
			c, _, _ := workerFixture(t)
			copyStarted := false
			base := workerExec(t, c, "")
			e := Engine{Exec: func(ctx context.Context, args, env []string, out io.Writer) error {
				if args[0] == "psql" && strings.Contains(strings.Join(args, " "), "server_version_num") {
					_, err := io.WriteString(out, catalog)
					return err
				}
				if args[0] == "pg_basebackup" && len(args) > 2 {
					copyStarted = true
				}
				return base(ctx, args, env, out)
			}}
			if _, err := e.Capture(context.Background(), c); err == nil || copyStarted {
				t.Fatal("unsafe source reached physical copy")
			}
		})
	}
}

func TestPhysicalCaptureCapacityGuardCancelsCopy(t *testing.T) {
	c, _, _ := workerFixture(t)
	c.MaxPlaintextBytes = 1 << 20
	base := workerExec(t, c, "")
	stopped := false
	e := Engine{Exec: func(ctx context.Context, args, env []string, out io.Writer) error {
		if args[0] == "pg_basebackup" && len(args) > 2 {
			for _, arg := range args {
				if strings.HasPrefix(arg, "--pgdata=") {
					dir := strings.TrimPrefix(arg, "--pgdata=")
					workerWrite(t, filepath.Join(dir, "oversized"), make([]byte, 2<<20))
				}
			}
			<-ctx.Done()
			stopped = true
			return ctx.Err()
		}
		return base(ctx, args, env, out)
	}}
	if _, err := e.Capture(context.Background(), c); err == nil || !stopped {
		t.Fatal("oversized running copy was not cancelled")
	}
	entries, _ := os.ReadDir(c.Directory)
	if len(entries) != 0 {
		t.Fatal("oversized capture left plaintext")
	}
	c.MaxPlaintextBytes = 1 << 60
	c.MaxArchiveBytes = 1 << 60
	called := false
	if _, err := (Engine{Exec: func(context.Context, []string, []string, io.Writer) error { called = true; return nil }}).Capture(context.Background(), c); err == nil || called {
		t.Fatal("insufficient scratch was not rejected before execution")
	}
}

func TestPhysicalConfigurationAndIdentity(t *testing.T) {
	c, _, _ := workerFixture(t)
	for _, change := range []func(*Config){func(c *Config) { c.Version = 2 }, func(c *Config) { c.RetentionDays = 13 }, func(c *Config) { c.MinimumBackups = 13 }, func(c *Config) { c.Source.SystemIdentifier = "" }, func(c *Config) { c.Source.PostgresImage = "postgres:16" }, func(c *Config) { c.Remote.Prefix = "prod/backups" }, func(c *Config) { c.MaxPlaintextBytes = 0 }} {
		copy := c
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	b, _ := json.Marshal(c)
	if _, err := Decode(bytes.NewReader(append(b, []byte("{}")...))); err == nil {
		t.Fatal("trailing config accepted")
	}
	data := filepath.Join(workerPrivateTemp(t), "pgdata")
	workerNative(t, data)
	m := workerManifest(t, c, data)
	for _, change := range []func(*Manifest){func(m *Manifest) { m.Scope = "core" }, func(m *Manifest) { m.PostgresMajor = 15 }, func(m *Manifest) { m.SystemIdentifier = "222" }, func(m *Manifest) { m.Environment = "prod" }, func(m *Manifest) { m.FinishedAt = m.StartedAt.Add(-time.Second) }} {
		copy := m
		change(&copy)
		if c.MatchManifest(copy) == nil {
			t.Fatal("mismatched manifest accepted")
		}
	}
}

func TestPhysicalArchiveLimitsTamperingAndWrongIdentity(t *testing.T) {
	c, _, identity := workerFixture(t)
	data := filepath.Join(workerPrivateTemp(t), "pgdata")
	workerNative(t, data)
	m := workerManifest(t, c, data)
	file := filepath.Join(workerPrivateTemp(t), "backup.age")
	if err := Pack(context.Background(), c, m, data, file); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	for _, damage := range []func([]byte) []byte{func(b []byte) []byte { return b[:len(b)-1] }, func(b []byte) []byte { b[len(b)-1] ^= 1; return b }} {
		broken := filepath.Join(workerPrivateTemp(t), "broken.age")
		workerWrite(t, broken, damage(append([]byte(nil), b...)))
		target := filepath.Join(workerPrivateTemp(t), "restore")
		if _, err := Unpack(context.Background(), c, broken, identity, target); err == nil {
			t.Fatal("unauthenticated ciphertext accepted")
		}
		entries, _ := os.ReadDir(target)
		if len(entries) != 0 {
			t.Fatal("failed decrypt left plaintext")
		}
	}
	_, _, wrong := workerFixture(t)
	if _, err := Unpack(context.Background(), c, file, wrong, filepath.Join(workerPrivateTemp(t), "restore")); err == nil {
		t.Fatal("wrong identity accepted")
	}
	short := c
	short.MaxArchiveBytes = 10
	if err := Pack(context.Background(), short, m, data, filepath.Join(workerPrivateTemp(t), "small.age")); err == nil {
		t.Fatal("encrypted size limit ignored")
	}
	short = c
	short.MaxPlaintextBytes = 1
	if _, err := Unpack(context.Background(), short, file, identity, filepath.Join(workerPrivateTemp(t), "restore")); err == nil {
		t.Fatal("plaintext size limit ignored")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), c, m, data, filepath.Join(workerPrivateTemp(t), "link.age")); err == nil {
		t.Fatal("source symlink accepted")
	}
}

type maliciousMember struct {
	header tar.Header
	data   string
}

func writePhysicalMembers(t *testing.T, c Config, m Manifest, members []maliciousMember) string {
	t.Helper()
	file := filepath.Join(workerPrivateTemp(t), "malicious.age")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := age.ParseX25519Recipient(c.Recipients[0])
	enc, err := age.Encrypt(f, r)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	b, _ := json.Marshal(m)
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Size: int64(len(b)), Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	tw.Write(b)
	for _, member := range members {
		if err = tw.WriteHeader(&member.header); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte(member.data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, close := range []func() error{tw.Close, gz.Close, enc.Close, f.Close} {
		if err = close(); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestPhysicalRejectsMaliciousArchiveMembers(t *testing.T) {
	c, _, identity := workerFixture(t)
	data := filepath.Join(workerPrivateTemp(t), "pgdata")
	workerNative(t, data)
	m := workerManifest(t, c, data)
	cases := map[string][]maliciousMember{
		"traversal":               {{header: tar.Header{Name: "pgdata/../outside", Typeflag: tar.TypeReg}}},
		"absolute":                {{header: tar.Header{Name: "/outside", Typeflag: tar.TypeReg}}},
		"symlink":                 {{header: tar.Header{Name: "pgdata/link", Typeflag: tar.TypeSymlink, Linkname: "/outside"}}},
		"hardlink":                {{header: tar.Header{Name: "pgdata/link", Typeflag: tar.TypeLink, Linkname: "/outside"}}},
		"duplicate":               {{header: tar.Header{Name: "pgdata/PG_VERSION", Typeflag: tar.TypeReg}}, {header: tar.Header{Name: "pgdata/PG_VERSION", Typeflag: tar.TypeReg}}},
		"tablespace":              {{header: tar.Header{Name: "pgdata/pg_tblspc/123", Typeflag: tar.TypeReg}}},
		"missing-native-manifest": {{header: tar.Header{Name: "pgdata/PG_VERSION", Typeflag: tar.TypeReg, Size: 2}, data: "16"}},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			file := writePhysicalMembers(t, c, m, members)
			target := filepath.Join(workerPrivateTemp(t), "restore")
			if _, err := Unpack(context.Background(), c, file, identity, target); err == nil {
				t.Fatal("malicious archive accepted")
			}
			entries, _ := os.ReadDir(target)
			if len(entries) != 0 {
				t.Fatal("malicious extraction left plaintext")
			}
		})
	}
}

func TestPhysicalRestoreRequiresNativeVerification(t *testing.T) {
	c, _, identity := workerFixture(t)
	data := filepath.Join(workerPrivateTemp(t), "pgdata")
	workerNative(t, data)
	m := workerManifest(t, c, data)
	file := filepath.Join(workerPrivateTemp(t), "backup.age")
	if err := Pack(context.Background(), c, m, data, file); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workerPrivateTemp(t), "restore")
	if _, err := (Engine{Exec: workerExec(t, c, "pg_verifybackup")}).Restore(context.Background(), c, file, identity, target); err == nil {
		t.Fatal("native verification failure ignored")
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Fatal("native verification failure left plaintext")
	}
	if err := os.RemoveAll(filepath.Join(data, "pg_wal")); err != nil {
		t.Fatal(err)
	}
	if err := (Engine{Exec: workerExec(t, c, "")}).verify(context.Background(), c, data); err == nil {
		t.Fatal("missing WAL accepted")
	}
}

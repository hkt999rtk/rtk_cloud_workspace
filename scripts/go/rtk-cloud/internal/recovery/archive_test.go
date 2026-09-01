package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func privateTemp(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func writeTest(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) Config {
	t.Helper()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	check := []Check{{ID: "operator-check", Argv: []string{"/operator/check"}}}
	return Config{Version: 1, Environment: "staging", Stack: "video-cloud-staging", Namespaces: []string{"platform"}, LockNamespace: "platform", Directory: privateTemp(t), Recipients: []string{key.Recipient().String()}, Remote: Remote{Endpoint: "https://backup.example.invalid", Bucket: "private-backups", Region: "test", Prefix: "staging/core"}, TimeoutSeconds: 2, MaxArchiveBytes: 16 << 20,
		Workloads:       []Workload{{Namespace: "platform", Kind: "deployment", Name: "api", Role: "application"}, {Namespace: "platform", Kind: "statefulset", Name: "postgres", Role: "data"}, {Namespace: "platform", Kind: "statefulset", Name: "openbao", Role: "offline"}},
		Components:      []Component{{ID: "postgres-globals", Kind: "postgres", Namespace: "platform", Pod: "postgres-0", User: "postgres", Database: "@globals"}, {ID: "postgres-data", Kind: "postgres", Namespace: "platform", Pod: "postgres-0", User: "postgres", Database: "accounts"}, {ID: "openbao", Kind: "volume", Namespace: "platform", PVC: "openbao-data", Image: "helper@sha256:" + strings.Repeat("a", 64), Purpose: "openbao-file"}, {ID: "runtime", Kind: "secretstore", Paths: []string{"runtime"}}},
		PreflightChecks: check, StartupChecks: check, QuiescenceChecks: check, RecoveryChecks: check, HealthChecks: check, ExternalDependencies: []string{"media/firmware payloads; audit; payments"}}
}

func TestConfigFailClosed(t *testing.T) {
	c := fixture(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"http", func(c *Config) { c.Remote.Endpoint = "http://backup.invalid" }},
		{"endpoint-password", func(c *Config) { c.Remote.Endpoint = "https://user:secret@backup.invalid" }},
		{"cross-environment-prefix", func(c *Config) { c.Remote.Prefix = "prod/core" }},
		{"no-escrow-recipient", func(c *Config) { c.Recipients = []string{"not-age"} }},
		{"oversized", func(c *Config) { c.MaxArchiveBytes = 5 << 30 }},
		{"root-directory", func(c *Config) { c.Directory = "/" }},
		{"no-checks", func(c *Config) { c.PreflightChecks = nil }},
		{"missing-globals", func(c *Config) { c.Components = c.Components[1:] }},
		{"no-openbao", func(c *Config) { c.Components = append(c.Components[:2], c.Components[3:]...) }},
		{"operator-secret", func(c *Config) { c.Components[3].Paths = []string{"operator"} }},
		{"unpinned-helper", func(c *Config) { c.Components[2].Image = "alpine:latest" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := fixture(t)
			tc.change(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	var decoded Config
	if Decode(strings.NewReader(`{"unknown":"secret"}`), &decoded) == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestArchiveRoundTripAndTampering(t *testing.T) {
	key, _ := age.GenerateX25519Identity()
	dir := privateTemp(t)
	capture := privateTemp(t)
	writeTest(t, filepath.Join(capture, "db.data"), []byte("sensitive snapshot\x00\xff"))
	identity := filepath.Join(dir, "identity")
	writeTest(t, identity, []byte(key.String()+"\n"))
	m := Manifest{Version: 1, Scope: "core", ID: "backup-1", Environment: "staging", Stack: "stack"}
	backup := filepath.Join(dir, "backup.age")
	if err := Pack(capture, backup, m, []string{key.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	got, err := Unpack(backup, identity, privateTemp(t), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Artifacts) != 1 || got.ID != m.ID {
		t.Fatal("manifest mismatch")
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("sensitive snapshot")) {
		t.Fatal("plaintext in encrypted archive")
	}
	for _, mode := range []string{"modified", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			copy := append([]byte(nil), b...)
			if mode == "modified" {
				copy[len(copy)-1] ^= 0x01
			} else {
				copy = copy[:len(copy)-1]
			}
			p := filepath.Join(dir, mode)
			writeTest(t, p, copy)
			if _, err := Unpack(p, identity, privateTemp(t), 1<<20); err == nil {
				t.Fatal("unauthenticated archive accepted")
			}
		})
	}
	if _, err := Unpack(backup, identity, privateTemp(t), 1); err == nil {
		t.Fatal("size limit bypassed")
	}
	os.Chmod(identity, 0644)
	if _, err := Unpack(backup, identity, privateTemp(t), 1<<20); err == nil {
		t.Fatal("public identity accepted")
	}
}

func TestVolumeRejectsUnsafeMembers(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../../escape", "a\\escape"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTemp(t), "archive")
			f, _ := os.Create(path)
			tw := tar.NewWriter(f)
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 1, Typeflag: tar.TypeReg})
			tw.Write([]byte("x"))
			tw.Close()
			f.Close()
			if WalkArchive(path, 100, nil) == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	path := filepath.Join(privateTemp(t), "link")
	f, _ := os.Create(path)
	tw := tar.NewWriter(f)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	f.Close()
	if WalkArchive(path, 100, nil) == nil {
		t.Fatal("link accepted")
	}
}

func TestSecretStoreReplacementAndSymlink(t *testing.T) {
	source := privateTemp(t)
	target := privateTemp(t)
	os.Mkdir(filepath.Join(source, "runtime"), 0700)
	os.Mkdir(filepath.Join(target, "runtime"), 0700)
	writeTest(t, filepath.Join(source, "runtime", "current"), []byte("saved"))
	writeTest(t, filepath.Join(target, "runtime", "stale"), []byte("stale"))
	writeTest(t, filepath.Join(target, "operator"), []byte("keep"))
	archive := filepath.Join(privateTemp(t), "secrets.tar")
	if err := PackPaths(source, []string{"runtime"}, archive); err != nil {
		t.Fatal(err)
	}
	c := Component{Paths: []string{"runtime"}}
	if err := ReplaceSecretPaths(archive, target, c, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "runtime", "stale")); !os.IsNotExist(err) {
		t.Fatal("stale credential survived")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "operator")); string(b) != "keep" {
		t.Fatal("operator state changed")
	}
	linkRoot := privateTemp(t)
	os.Symlink(source, filepath.Join(linkRoot, "linked"))
	if err := PackPaths(linkRoot, []string{"linked/runtime/current"}, filepath.Join(privateTemp(t), "bad.tar")); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
}

func TestSQLiteOfflineArchive(t *testing.T) {
	root := privateTemp(t)
	db, err := sql.Open("sqlite", filepath.Join(root, "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE leads (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO leads VALUES (1,'kept')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	archive := filepath.Join(privateTemp(t), "sqlite.tar")
	if err := PackPaths(root, []string{"leads.db"}, archive); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLite(archive, Component{SQLiteFiles: []string{"leads.db"}}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLite(archive, Component{SQLiteFiles: []string{"missing.db"}}, 1<<20); err == nil {
		t.Fatal("missing DB accepted")
	}
}

func TestLogicalIdentityDoesNotPermitEnvironmentCloning(t *testing.T) {
	c := fixture(t)
	m := Manifest{Environment: c.Environment, Stack: c.Stack, Components: c.Components, ConfigurationSHA256: InventoryFingerprint(c)}
	e := Engine{Config: c}
	if err := e.matchManifest(m); err != nil {
		t.Fatal(err)
	}
	c.Components[0].Pod = "new-postgres-0"
	e.Config = c
	if err := e.matchManifest(m); err != nil {
		t.Fatal("physical relocation rejected")
	}
	m.Environment = "prod"
	if e.matchManifest(m) == nil {
		t.Fatal("prod to staging clone accepted")
	}
}

func TestQuietExecDoesNotLeak(t *testing.T) {
	err := QuietExec(context.Background(), []string{"sh", "-c", "echo secret-value >&2; exit 1"}, nil, io.Discard)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("subprocess error leaks output")
	}
}

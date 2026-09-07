package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func baseConfigFixture(t *testing.T) BaseBackupConfig {
	t.Helper()
	wal, _, source, _ := walFixture(t)
	service := filepath.Join(filepath.Dir(source), "service.conf")
	if err := os.WriteFile(service, []byte("[test]\nhost=/var/run/postgresql\nuser=backup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return BaseBackupConfig{WAL: wal, BinaryDirectory: "/usr/local/bin", ServiceFile: service, Service: "test", TimeoutSeconds: 60, MaxArchiveBytes: 8 << 20}
}
func TestBaseBackupConfigurationAndPrivateService(t *testing.T) {
	c := baseConfigFixture(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BaseBackupConfig){func(c *BaseBackupConfig) { c.BinaryDirectory = "relative" }, func(c *BaseBackupConfig) { c.Service = "name sslmode=disable" }, func(c *BaseBackupConfig) { c.ServiceFile = "relative" }, func(c *BaseBackupConfig) { c.TimeoutSeconds = 0 }, func(c *BaseBackupConfig) { c.MaxArchiveBytes = 5 << 30 }} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid physical config accepted")
		}
	}
	if err := os.Chmod(c.ServiceFile, 0644); err != nil {
		t.Fatal(err)
	}
	e := BaseBackupEngine{Config: c, Exec: func(context.Context, []string, io.Reader, io.Writer) error {
		t.Fatal("tool ran with public service file")
		return nil
	}}
	if _, err := e.Stage(context.Background(), "test"); err == nil {
		t.Fatal("public service file accepted")
	}
}

func TestBaseBackupTarRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    byte
		content string
	}{
		{"../escape", tar.TypeReg, "data"}, {"linked", tar.TypeSymlink, ""}, {"linked", tar.TypeLink, ""}, {"device", tar.TypeChar, ""}, {"tablespace_map", tar.TypeReg, "123 /outside"}, {"pg_tblspc/123", tar.TypeDir, ""}, {"standby.signal", tar.TypeReg, ""}, {"postmaster.pid", tar.TypeReg, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := privateTemp(t)
			source := filepath.Join(dir, "base.tar")
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			h := &tar.Header{Name: test.name, Mode: 0600, Typeflag: test.kind, Size: int64(len(test.content))}
			if test.kind == tar.TypeLink || test.kind == tar.TypeSymlink {
				h.Linkname = "/outside"
			}
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(test.content))
			w.Close()
			if err := os.WriteFile(source, b.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractBaseTar(context.Background(), source, filepath.Join(dir, "data"), 1<<20); err == nil {
				t.Fatal("unsafe tar accepted")
			}
		})
	}
}
func TestPhysicalArchiveScopeIsolation(t *testing.T) {
	c, _, source, identity := walFixture(t)
	root := filepath.Dir(source)
	capture := filepath.Join(root, "capture")
	if err := PrivateDirectory(capture); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "base.tar"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: Version, Scope: physicalScope, ID: "scope-test", Environment: c.Environment, Stack: c.Stack}
	encrypted := filepath.Join(root, "physical.age")
	if err := packScoped(capture, encrypted, m, c.Recipients, physicalScope); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "key")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpack(encrypted, key, filepath.Join(root, "core"), 1<<20); err == nil {
		t.Fatal("core restore accepted physical scope")
	}
	if _, err := unpackScoped(encrypted, key, filepath.Join(root, "physical"), 1<<20, physicalScope); err != nil {
		t.Fatal(err)
	}
}
func TestBaseBackupToolVersionAndCancelledCapture(t *testing.T) {
	c := baseConfigFixture(t)
	for _, mode := range []string{"wrong-version", "cancelled", "capture-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			e := BaseBackupEngine{Config: c, Exec: func(ctx context.Context, args []string, _ io.Reader, out io.Writer) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if args[1] == "--version" {
					version := "16.1"
					if mode == "wrong-version" {
						version = "18.1"
					}
					_, err := io.WriteString(out, filepath.Base(args[0])+" (PostgreSQL) "+version+"\n")
					return err
				}
				return errors.New("fixture capture failure")
			}}
			if _, err := e.Stage(ctx, mode); err == nil {
				t.Fatal("failed capture accepted")
			}
			if _, err := os.Lstat(filepath.Join(c.WAL.Directory, "base-"+mode)); !os.IsNotExist(err) {
				t.Fatal("failed backup published")
			}
			leftovers, _ := filepath.Glob(filepath.Join(c.WAL.Directory, ".base-*"))
			if len(leftovers) != 0 {
				t.Fatal("plaintext capture retained")
			}
		})
	}
}

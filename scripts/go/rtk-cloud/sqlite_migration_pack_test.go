package main

import (
	"archive/tar"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

const sqliteMigrationTestUID = "fa1864f3-920f-4dc6-84a9-bf77e6bb2085"

func writeSQLiteMigrationDB(t *testing.T, dir, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE evidence (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO evidence(value) VALUES ('retained');"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMigrationPackEncryptsVerifiedFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	outputDir := filepath.Join(root, "archive")
	for _, dir := range []string{source, outputDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeSQLiteMigrationDB(t, source, "connectplus.db")
	writeSQLiteMigrationDB(t, source, "analytics.db")
	writeSQLiteMigrationDB(t, source, "search.db")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(outputDir, "frontend.age")
	if err := runSQLiteMigrationPack([]string{
		"--workload", "frontend", "--source-dir", source,
		"--source-pod-uid", sqliteMigrationTestUID,
		"--recipient", identity.Recipient().String(), "--output", archivePath,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private encrypted archive: info=%v err=%v", info, err)
	}
	ciphertext, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(ciphertext, identity)
	if err != nil {
		ciphertext.Close()
		t.Fatal(err)
	}
	reader := tar.NewReader(decrypted)
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[header.Name] = true
		if header.Name == "manifest.json" {
			var manifest sqliteMigrationManifest
			if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Version != 1 || manifest.Workload != "frontend" || manifest.SourcePodUID != sqliteMigrationTestUID || len(manifest.Files) != 3 {
				t.Fatalf("unexpected archive manifest: %#v", manifest)
			}
		}
	}
	ciphertext.Close()
	for _, name := range []string{"manifest.json", "connectplus.db", "analytics.db", "search.db"} {
		if !seen[name] {
			t.Fatalf("archive omitted %s", name)
		}
	}
}

func TestSQLiteMigrationPackRejectsUnsafeSources(t *testing.T) {
	cases := []struct {
		name string
		make func(*testing.T, string)
		want string
	}{
		{"missing database", func(t *testing.T, dir string) {}, "required SQLite file"},
		{"corrupt database", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "rtk-cloud-admin.db"), []byte("not sqlite"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "integrity check failed"},
		{"symlink", func(t *testing.T, dir string) {
			writeSQLiteMigrationDB(t, dir, "rtk-cloud-admin.db")
			if err := os.Symlink("rtk-cloud-admin.db", filepath.Join(dir, "other.db")); err != nil {
				t.Fatal(err)
			}
		}, "non-regular"},
		{"unknown file", func(t *testing.T, dir string) {
			writeSQLiteMigrationDB(t, dir, "rtk-cloud-admin.db")
			if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "unexpected"},
		{"public source directory", func(t *testing.T, dir string) {
			writeSQLiteMigrationDB(t, dir, "rtk-cloud-admin.db")
			if err := os.Chmod(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}, "mode 0700"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			outputDir := filepath.Join(root, "output")
			for _, dir := range []string{source, outputDir} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			tc.make(t, source)
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(outputDir, "admin.age")
			err = runSQLiteMigrationPack([]string{
				"--workload", "cloud-admin", "--source-dir", source,
				"--source-pod-uid", sqliteMigrationTestUID,
				"--recipient", identity.Recipient().String(), "--output", archive,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pack error=%v, want %q", err, tc.want)
			}
			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("unsafe source created archive: %v", err)
			}
		})
	}
}

func TestSQLiteMigrationPackRejectsUnsafeArgumentsAndLayouts(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, args []string, root, source, archive string) []string
		want string
	}{
		{"unexpected argument", func(t *testing.T, args []string, _, _, _ string) []string {
			return append(args, "extra")
		}, "invalid sqlite-migration-pack arguments"},
		{"unknown workload", func(t *testing.T, args []string, _, _, _ string) []string {
			args[1] = "unknown"
			return args
		}, "--workload"},
		{"short pod uid", func(t *testing.T, args []string, _, _, _ string) []string {
			args[5] = "short"
			return args
		}, "source-pod-uid"},
		{"bad pod uid separator", func(t *testing.T, args []string, _, _, _ string) []string {
			args[5] = "fa1864f3x920f-4dc6-84a9-bf77e6bb2085"
			return args
		}, "source-pod-uid"},
		{"bad pod uid hex", func(t *testing.T, args []string, _, _, _ string) []string {
			args[5] = "za1864f3-920f-4dc6-84a9-bf77e6bb2085"
			return args
		}, "source-pod-uid"},
		{"invalid recipient", func(t *testing.T, args []string, _, _, _ string) []string {
			args[7] = "invalid"
			return args
		}, "recipient"},
		{"missing archive path", func(t *testing.T, args []string, _, _, _ string) []string {
			args[9] = ""
			return args
		}, "--output required"},
		{"public archive parent", func(t *testing.T, args []string, root, _, _ string) []string {
			if err := os.Chmod(filepath.Join(root, "archive"), 0755); err != nil {
				t.Fatal(err)
			}
			return args
		}, "archive parent"},
		{"archive inside source", func(t *testing.T, args []string, _, source, _ string) []string {
			args[9] = filepath.Join(source, "copy.age")
			return args
		}, "outside source directory"},
		{"existing archive", func(t *testing.T, args []string, _, _, archive string) []string {
			if err := os.WriteFile(archive, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			return args
		}, "file exists"},
		{"empty database", func(t *testing.T, args []string, _, source, _ string) []string {
			if err := os.WriteFile(filepath.Join(source, "rtk-cloud-admin.db"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			return args
		}, "empty SQLite source file"},
		{"orphan sidecar", func(t *testing.T, args []string, _, source, _ string) []string {
			if err := os.WriteFile(filepath.Join(source, "orphan.db-wal"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			return args
		}, "has no database"},
		{"symlink source directory", func(t *testing.T, args []string, root, source, _ string) []string {
			alias := filepath.Join(root, "alias")
			if err := os.Symlink(source, alias); err != nil {
				t.Fatal(err)
			}
			args[3] = alias
			return args
		}, "symlink ancestors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			archiveDir := filepath.Join(root, "archive")
			for _, dir := range []string{source, archiveDir} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			writeSQLiteMigrationDB(t, source, "rtk-cloud-admin.db")
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(archiveDir, "admin.age")
			args := []string{"--workload", "cloud-admin", "--source-dir", source,
				"--source-pod-uid", sqliteMigrationTestUID,
				"--recipient", identity.Recipient().String(), "--output", archive}
			args = tc.edit(t, args, root, source, archive)
			err = runSQLiteMigrationPack(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pack error=%v, want %q", err, tc.want)
			}
			if tc.name == "existing archive" {
				content, err := os.ReadFile(archive)
				if err != nil || string(content) != "keep" {
					t.Fatalf("existing archive was changed: %q, %v", content, err)
				}
			}
		})
	}
}

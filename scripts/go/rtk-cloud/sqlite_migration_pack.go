package main

import (
	"archive/tar"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite"
)

var sqliteMigrationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.db(?:-(?:wal|shm|journal))?$`)

type sqliteMigrationFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type sqliteMigrationManifest struct {
	Version      int                   `json:"version"`
	Workload     string                `json:"workload"`
	SourcePodUID string                `json:"source_pod_uid"`
	CreatedAt    string                `json:"created_at"`
	Files        []sqliteMigrationFile `json:"files"`
}

func runSQLiteMigrationPack(args []string) error {
	fs := flag.NewFlagSet("sqlite-migration-pack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workload := fs.String("workload", "", "cloud-admin or frontend")
	sourceDir := fs.String("source-dir", "", "private directory containing the fenced SQLite copy")
	sourcePodUID := fs.String("source-pod-uid", "", "source Pod UID recorded at the copy boundary")
	recipient := fs.String("recipient", "", "age X25519 public recipient")
	output := fs.String("output", "", "new encrypted archive path")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("invalid sqlite-migration-pack arguments")
	}
	if *workload != "cloud-admin" && *workload != "frontend" {
		return errors.New("--workload must be cloud-admin or frontend")
	}
	if !validSQLitePodUID(*sourcePodUID) {
		return errors.New("valid --source-pod-uid required")
	}
	r, err := age.ParseX25519Recipient(*recipient)
	if err != nil {
		return errors.New("valid age X25519 --recipient required")
	}
	manifest, err := sqliteMigrationInspect(*sourceDir, *workload, *sourcePodUID)
	if err != nil {
		return err
	}
	if *output == "" {
		return errors.New("--output required")
	}
	outDir := filepath.Dir(*output)
	if err := sqliteMigrationPrivateDir(outDir); err != nil {
		return fmt.Errorf("archive parent: %w", err)
	}
	absSource, err := filepath.Abs(*sourceDir)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if absOutput == absSource || strings.HasPrefix(absOutput, absSource+string(os.PathSeparator)) {
		return errors.New("archive output must be outside source directory")
	}
	if err := sqliteMigrationVerify(*sourceDir, manifest); err != nil {
		return err
	}
	if err := sqliteMigrationWrite(*sourceDir, *output, manifest, r); err != nil {
		return err
	}
	hash, err := sqliteMigrationHash(*output)
	if err != nil {
		return err
	}
	fmt.Printf("Encrypted archive: %s\nArchive SHA-256: %s\nSource Pod UID: %s\n", *output, hash, *sourcePodUID)
	return nil
}

func validSQLitePodUID(uid string) bool {
	if len(uid) != 36 {
		return false
	}
	for i, c := range uid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func sqliteMigrationPrivateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := abs; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("directory path must have no symlink ancestors")
		}
		if current == abs && (!info.IsDir() || info.Mode().Perm()&0077 != 0) {
			return errors.New("directory must exist with mode 0700")
		}
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	return nil
}

func sqliteMigrationInspect(dir, workload, uid string) (sqliteMigrationManifest, error) {
	m := sqliteMigrationManifest{Version: 1, Workload: workload, SourcePodUID: uid, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := sqliteMigrationPrivateDir(dir); err != nil {
		return m, fmt.Errorf("source directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, err
	}
	bases := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !sqliteMigrationName.MatchString(name) || !entry.Type().IsRegular() {
			return m, fmt.Errorf("unexpected or non-regular source entry %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return m, err
		}
		if info.Size() == 0 {
			return m, fmt.Errorf("empty SQLite source file %q", name)
		}
		hash, err := sqliteMigrationHash(filepath.Join(dir, name))
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, sqliteMigrationFile{Name: name, Size: info.Size(), SHA256: hash})
		if strings.HasSuffix(name, ".db") {
			bases[name] = true
		}
	}
	needed := []string{"rtk-cloud-admin.db"}
	if workload == "frontend" {
		needed = []string{"connectplus.db", "analytics.db"}
	}
	for _, name := range needed {
		if !bases[name] {
			return m, fmt.Errorf("required SQLite file %s is missing", name)
		}
	}
	for _, file := range m.Files {
		if strings.HasSuffix(file.Name, ".db") {
			continue
		}
		base := file.Name[:strings.Index(file.Name, ".db")+3]
		if !bases[base] {
			return m, fmt.Errorf("SQLite sidecar %s has no database", file.Name)
		}
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	return m, nil
}

func sqliteMigrationVerify(dir string, m sqliteMigrationManifest) error {
	verifyDir, err := os.MkdirTemp(dir, ".sqlite-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(verifyDir)
	for _, file := range m.Files {
		source, err := os.Open(filepath.Join(dir, file.Name))
		if err != nil {
			return err
		}
		copyPath := filepath.Join(verifyDir, file.Name)
		copyFile, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			source.Close()
			return err
		}
		_, err = io.Copy(copyFile, source)
		source.Close()
		closeErr := copyFile.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if hash, err := sqliteMigrationHash(copyPath); err != nil || hash != file.SHA256 {
			return fmt.Errorf("SQLite copy changed while verifying %s", file.Name)
		}
	}
	for _, file := range m.Files {
		if !strings.HasSuffix(file.Name, ".db") {
			continue
		}
		uri := (&url.URL{Scheme: "file", Path: filepath.Join(verifyDir, file.Name), RawQuery: "mode=ro"}).String()
		db, err := sql.Open("sqlite", uri)
		if err != nil {
			return fmt.Errorf("open SQLite copy %s: %w", file.Name, err)
		}
		var result string
		err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
		closeErr := db.Close()
		if err != nil || closeErr != nil || result != "ok" {
			return fmt.Errorf("SQLite integrity check failed for %s", file.Name)
		}
	}
	for _, file := range m.Files {
		if hash, err := sqliteMigrationHash(filepath.Join(dir, file.Name)); err != nil || hash != file.SHA256 {
			return fmt.Errorf("SQLite source changed during verification: %s", file.Name)
		}
	}
	return nil
}

func sqliteMigrationWrite(dir, destination string, m sqliteMigrationManifest, recipient age.Recipient) error {
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		out.Close()
		if !complete {
			os.Remove(destination)
		}
	}()
	encrypted, err := age.Encrypt(out, recipient)
	if err != nil {
		return err
	}
	tarWriter := tar.NewWriter(encrypted)
	manifestBytes, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(manifestBytes))}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(manifestBytes); err != nil {
		return err
	}
	for _, file := range m.Files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: file.Name, Typeflag: tar.TypeReg, Mode: 0600, Size: file.Size}); err != nil {
			return err
		}
		source, err := os.Open(filepath.Join(dir, file.Name))
		if err != nil {
			return err
		}
		_, err = io.Copy(tarWriter, source)
		source.Close()
		if err != nil {
			return err
		}
		if hash, err := sqliteMigrationHash(filepath.Join(dir, file.Name)); err != nil || hash != file.SHA256 {
			return fmt.Errorf("SQLite source changed while packing: %s", file.Name)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	after, err := sqliteMigrationInspect(dir, m.Workload, m.SourcePodUID)
	if err != nil || !slices.Equal(m.Files, after.Files) {
		return errors.New("SQLite source file set changed while packing")
	}
	complete = true
	return nil
}

func sqliteMigrationHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

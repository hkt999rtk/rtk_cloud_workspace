package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const physicalScope = "postgres-physical"

type BaseBackupConfig struct {
	WAL             WALConfig `json:"wal"`
	BinaryDirectory string    `json:"binary_directory"`
	ServiceFile     string    `json:"service_file"`
	Service         string    `json:"service"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	MaxArchiveBytes int64     `json:"max_archive_bytes"`
}

func (c BaseBackupConfig) Validate() error {
	if err := c.WAL.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.BinaryDirectory) || filepath.Clean(c.BinaryDirectory) != c.BinaryDirectory || !filepath.IsAbs(c.ServiceFile) || !Name.MatchString(c.Service) || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 14400 || c.MaxArchiveBytes < 4<<20 || c.MaxArchiveBytes > 4<<30 {
		return errors.New("invalid physical backup tooling, service, deadline or size limit")
	}
	return nil
}
func (c BaseBackupConfig) remoteConfig() Config {
	r := c.WAL.Remote
	r.Prefix += "/base-v1/" + c.WAL.SystemIdentifier
	return Config{Environment: c.WAL.Environment, Stack: c.WAL.Stack, Remote: r, TimeoutSeconds: c.TimeoutSeconds, MaxArchiveBytes: c.MaxArchiveBytes}
}

type BaseBackupEngine struct {
	Config BaseBackupConfig
	Exec   Executor
}
type baseReceipt struct {
	Manifest  Manifest `json:"manifest"`
	Encrypted Artifact `json:"encrypted"`
}
type baseWALRange struct {
	Timeline uint32 `json:"Timeline"`
	Start    string `json:"Start-LSN"`
	End      string `json:"End-LSN"`
}

func (e BaseBackupEngine) run(ctx context.Context, tool string, out io.Writer, args ...string) error {
	argv := append([]string{filepath.Join(e.Config.BinaryDirectory, tool)}, args...)
	if e.Exec != nil {
		return e.Exec(ctx, argv, nil, out)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Connection secrets are supplied only through the reviewed service/pass files.
	// Ignore ambient PG* settings, startup files and locale-dependent tool output.
	cmd.Env = []string{"LC_ALL=C", "PATH=" + e.Config.BinaryDirectory + ":/usr/bin:/bin", "PGSERVICEFILE=" + e.Config.ServiceFile}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("PostgreSQL backup tool failed (output suppressed)")
	}
	return nil
}
func (e BaseBackupEngine) checkTools(ctx context.Context) error {
	for _, tool := range []string{"pg_basebackup", "pg_verifybackup", "pg_controldata"} {
		var b bytes.Buffer
		if err := e.run(ctx, tool, &boundedWriter{writer: &b, remaining: 4096}, "--version"); err != nil {
			return err
		}
		if !strings.HasPrefix(b.String(), tool+" (PostgreSQL) 16.") {
			return errors.New("PostgreSQL 16 backup tools required")
		}
	}
	return nil
}

// Create retains verified ciphertext for an immutable remote retry. No database
// writers are paused, and the captured tar includes the WAL needed for consistency.
func (e BaseBackupEngine) Create(ctx context.Context, id string) error {
	if err := e.Config.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	path, err := e.Stage(ctx, id)
	if err != nil {
		return err
	}
	return Upload(ctx, e.Config.remoteConfig(), id, path)
}
func (e BaseBackupEngine) Stage(ctx context.Context, id string) (string, error) {
	c := e.Config
	if err := c.Validate(); err != nil {
		return "", err
	}
	if !Name.MatchString(id) {
		return "", errors.New("invalid base backup id")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := PrivateDirectory(c.WAL.Directory); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(c.WAL.Directory, "base-"+id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return "", errors.New("base backup busy; retry")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	destination := filepath.Join(c.WAL.Directory, "base-"+id)
	encrypted := filepath.Join(destination, id+".age")
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", errors.New("invalid base backup retry directory")
		}
		f, err := os.Open(filepath.Join(destination, "receipt.json"))
		if err != nil {
			return "", err
		}
		defer f.Close()
		var receipt baseReceipt
		if err = Decode(io.LimitReader(f, 1<<20), &receipt); err != nil {
			return "", err
		}
		info, err = os.Lstat(encrypted)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > c.MaxArchiveBytes {
			return "", errors.New("invalid base backup retry ciphertext")
		}
		actual, err := DigestFile(encrypted)
		if err != nil || receipt.Manifest.Version != Version || receipt.Manifest.Environment != c.WAL.Environment || receipt.Manifest.Stack != c.WAL.Stack || receipt.Manifest.ID != id || receipt.Manifest.Scope != physicalScope || receipt.Manifest.ConfigurationSHA256 != Digest(c) || receipt.Encrypted.Path != id+".age" || actual.Size != receipt.Encrypted.Size || actual.SHA256 != receipt.Encrypted.SHA256 {
			return "", errors.New("base backup retry mismatch")
		}
		return encrypted, ctx.Err()
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// A service file can contain passwords; never put its contents into argv/errors.
	info, err := os.Lstat(c.ServiceFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return "", errors.New("private regular PostgreSQL service file required")
	}
	if err = e.checkTools(ctx); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(c.WAL.Directory, ".base-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	capture := filepath.Join(temp, "capture")
	if err = PrivateDirectory(capture); err != nil {
		return "", err
	}
	tarPath := filepath.Join(capture, "base.tar")
	output, err := os.OpenFile(tarPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	// stdout tar refuses additional tablespaces and never writes their source paths.
	err = e.run(ctx, "pg_basebackup", &boundedWriter{writer: output, remaining: c.MaxArchiveBytes - (1 << 20)}, "--dbname=service="+c.Service+" sslmode=verify-full", "--pgdata=-", "--format=tar", "--wal-method=fetch", "--manifest-checksums=SHA256", "--checkpoint=fast", "--no-password")
	closeErr := output.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	verified := filepath.Join(temp, "verified")
	if err = extractBaseTar(ctx, tarPath, verified, c.MaxArchiveBytes); err != nil {
		return "", err
	}
	evidence, err := e.verify(ctx, verified)
	if err != nil {
		return "", err
	}
	m := Manifest{Version: Version, Scope: physicalScope, ID: id, Environment: c.WAL.Environment, Stack: c.WAL.Stack, CreatedAt: time.Now().UTC(), ConfigurationSHA256: Digest(c), Evidence: evidence}
	encryptedTemp := filepath.Join(temp, id+".age")
	if err = packScoped(capture, encryptedTemp, m, c.WAL.Recipients, physicalScope); err != nil {
		return "", err
	}
	artifact, err := DigestFile(encryptedTemp)
	if err != nil {
		return "", err
	}
	artifact.Path = id + ".age"
	if artifact.Size > c.MaxArchiveBytes {
		return "", errors.New("encrypted base backup exceeds limit")
	}
	if err = WriteJSON(filepath.Join(temp, "receipt.json"), baseReceipt{m, artifact}); err != nil {
		return "", err
	}
	if err = os.RemoveAll(capture); err != nil {
		return "", err
	}
	if err = os.RemoveAll(verified); err != nil {
		return "", err
	}
	if err = syncWALDirectory(temp); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = os.Rename(temp, destination); err != nil {
		return "", err
	}
	if err = syncWALDirectory(c.WAL.Directory); err != nil {
		return "", err
	}
	return encrypted, nil
}

func extractBaseTar(ctx context.Context, source, destination string, limit int64) error {
	// Caller owns a fresh staging path. Reject an existing path even if empty.
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	return WalkArchive(source, limit, func(h *tar.Header, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if h.Name == "postmaster.pid" || h.Name == "standby.signal" || h.Name == "recovery.signal" || (h.Name == "tablespace_map" && h.Size != 0) || strings.HasPrefix(h.Name, "pg_tblspc/") {
			return errors.New("unsupported physical backup runtime state or tablespace")
		}
		path := filepath.Join(destination, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			return os.MkdirAll(path, 0700)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, &walContextReader{ctx, r})
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

var controlSegmentSize = regexp.MustCompile(`(?m)^Bytes per WAL segment:\s+([0-9]+)\s*$`)
var controlAlignment = regexp.MustCompile(`(?m)^Maximum data alignment:\s+8\s*$`)
var controlWALBlock = regexp.MustCompile(`(?m)^WAL block size:\s+8192\s*$`)
var controlIdentifier = regexp.MustCompile(`(?m)^Database system identifier:\s+([0-9]+)\s*$`)

func (e BaseBackupEngine) verify(ctx context.Context, directory string) (map[string]string, error) {
	if err := e.run(ctx, "pg_verifybackup", io.Discard, "--exit-on-error", directory); err != nil {
		return nil, err
	}
	version, err := os.ReadFile(filepath.Join(directory, "PG_VERSION"))
	if err != nil || string(version) != "16\n" {
		return nil, errors.New("PostgreSQL 16 base backup required")
	}
	var control bytes.Buffer
	if err = e.run(ctx, "pg_controldata", &boundedWriter{writer: &control, remaining: 64 << 10}, directory); err != nil {
		return nil, err
	}
	if strings.Contains(control.String(), "WARNING:") {
		return nil, errors.New("PostgreSQL control data is untrustworthy")
	}
	match := controlIdentifier.FindStringSubmatch(control.String())
	if len(match) != 2 || match[1] != e.Config.WAL.SystemIdentifier {
		return nil, errors.New("base backup cluster identifier mismatch")
	}
	segment := controlSegmentSize.FindStringSubmatch(control.String())
	if len(segment) != 2 || segment[1] != strconv.FormatInt(e.Config.WAL.SegmentBytes, 10) || !controlAlignment.MatchString(control.String()) || !controlWALBlock.MatchString(control.String()) {
		return nil, errors.New("base backup WAL layout differs from configured archive")
	}
	// Validate the manifest's WAL range shape; native pg_verifybackup checks its
	// checksum, data files and required WAL records using pg_waldump.
	f, err := os.Open(filepath.Join(directory, "backup_manifest"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var manifest struct {
		Version  int             `json:"PostgreSQL-Backup-Manifest-Version"`
		Ranges   []baseWALRange  `json:"WAL-Ranges"`
		Files    json.RawMessage `json:"Files"`
		Checksum string          `json:"Manifest-Checksum"`
	}
	if err = Decode(io.LimitReader(f, 64<<20), &manifest); err != nil {
		return nil, err
	}
	if manifest.Version != 1 || len(manifest.Ranges) == 0 || len(manifest.Ranges) > 128 {
		return nil, errors.New("unsupported base backup WAL ranges")
	}
	for _, r := range manifest.Ranges {
		start, ok1 := baseLSN(r.Start)
		end, ok2 := baseLSN(r.End)
		if !ok1 || !ok2 || r.Timeline == 0 || start == 0 || start >= end {
			return nil, errors.New("invalid base backup WAL coverage")
		}
	}
	digest, err := DigestFile(filepath.Join(directory, "backup_manifest"))
	if err != nil {
		return nil, err
	}
	ranges, _ := json.Marshal(manifest.Ranges)
	return map[string]string{"system_identifier": match[1], "postgres_major": "16", "manifest_sha256": digest.SHA256, "wal_ranges": string(ranges)}, nil
}
func baseLSN(s string) (uint64, bool) {
	if !historyLSN.MatchString(s) {
		return 0, false
	}
	parts := strings.Split(s, "/")
	hi, _ := strconv.ParseUint(parts[0], 16, 32)
	lo, _ := strconv.ParseUint(parts[1], 16, 32)
	return hi<<32 | lo, true
}

// Restore creates a fresh private directory and never starts a PostgreSQL server.
// The destination is unusable until successful return; failed attempts are removed.
func (e BaseBackupEngine) Restore(ctx context.Context, id, destination, identity string) error {
	c := e.Config
	if err := c.Validate(); err != nil {
		return err
	}
	if !Name.MatchString(id) {
		return errors.New("invalid base backup id")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := PrivateDirectory(c.WAL.Directory); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(c.WAL.Directory, ".base-download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	encrypted, err := Download(ctx, c.remoteConfig(), id, temp)
	if err != nil {
		return err
	}
	return e.restoreFile(ctx, id, encrypted, destination, identity)
}
func (e BaseBackupEngine) restoreFile(ctx context.Context, id, encrypted, destination, identity string) error {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || destination == "/" {
		return errors.New("absolute clean recovery destination required")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("recovery destination exists or is inaccessible")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(destination) {
		return errors.New("recovery destination parent must be canonical")
	}
	if err = e.checkTools(ctx); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".base-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	payload := filepath.Join(temp, "payload")
	m, err := unpackScoped(encrypted, identity, payload, e.Config.MaxArchiveBytes, physicalScope)
	if err != nil {
		return err
	}
	c := e.Config
	if m.ID != id || m.Environment != c.WAL.Environment || m.Stack != c.WAL.Stack || m.ConfigurationSHA256 != Digest(c) || len(m.Artifacts) != 1 || m.Artifacts[0].Path != "base.tar" {
		return errors.New("physical backup envelope scope/artifact mismatch")
	}
	// Reserve the requested destination exclusively. It must not be used by any
	// server until the command succeeds; cleanup owns only this new directory.
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(destination)
		}
	}()
	data := filepath.Join(destination, "pgdata")
	if err = extractBaseTar(ctx, filepath.Join(payload, "base.tar"), data, c.MaxArchiveBytes); err != nil {
		return err
	}
	evidence, err := e.verify(ctx, data)
	if err != nil {
		return err
	}
	if Digest(evidence) != Digest(m.Evidence) {
		return errors.New("physical backup verification evidence changed")
	}
	if err = WriteJSON(filepath.Join(destination, "verified.json"), m); err != nil {
		return err
	}
	if err = syncBaseDirectories(ctx, data); err != nil {
		return err
	}
	if err = syncWALDirectory(destination); err != nil {
		return err
	}
	if err = syncWALDirectory(parent); err != nil {
		return err
	}
	success = true
	return nil
}
func syncBaseDirectories(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return syncWALDirectory(path)
		}
		return nil
	})
}

package postgresbackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

// Executor receives argv and an explicit environment. Implementations must not
// expose subprocess output or arguments in errors: database utilities may print
// credentials. Stdout is only captured for fixed identity/version commands.
type Executor func(context.Context, []string, []string, io.Writer) error

type Engine struct {
	Exec   Executor
	remote objectStore
}

func quietExec(ctx context.Context, argv, env []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("PostgreSQL backup subprocess failed (output suppressed)")
	}
	return nil
}

func (e Engine) run(ctx context.Context, argv, env []string, out io.Writer) error {
	run := e.Exec
	if run == nil {
		run = quietExec
	}
	if err := run(ctx, argv, env, out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("PostgreSQL backup subprocess failed (output suppressed)")
	}
	return nil
}

func safeEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "TMPDIR", "SYSTEMROOT":
			env = append(env, entry)
		}
	}
	return append(env, "LC_ALL=C", "LANG=C")
}

func Capture(ctx context.Context, c Config) (CaptureResult, error) { return (Engine{}).Capture(ctx, c) }

func (e Engine) Capture(ctx context.Context, c Config) (result CaptureResult, err error) {
	if err = c.Validate(); err != nil {
		return result, err
	}
	if err = privateScratch(c.Directory); err != nil {
		return result, err
	}
	if err = scratchSpace(c.Directory, c.MaxPlaintextBytes+c.MaxArchiveBytes); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	tmp, err := os.MkdirTemp(c.Directory, ".capture-postgres-")
	if err != nil {
		return result, errors.New("cannot create private capture directory")
	}
	defer os.RemoveAll(tmp)
	password, err := readPrivateFile(c.Source.PasswordFile, 65536)
	if err != nil {
		return result, err
	}
	password = bytes.TrimSuffix(password, []byte("\n"))
	if len(password) == 0 || bytes.ContainsAny(password, "\r\n\x00") {
		return result, errors.New("invalid PostgreSQL password file")
	}
	passfile := filepath.Join(tmp, "pgpass")
	escape := func(s string) string { return strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(s) }
	line := ""
	for _, database := range []string{"replication", "postgres"} {
		line += escape(c.Source.Host) + ":" + strconv.Itoa(c.Source.Port) + ":" + database + ":" + escape(c.Source.User) + ":" + escape(string(password)) + "\n"
	}
	if err = os.WriteFile(passfile, []byte(line), 0600); err != nil {
		return result, errors.New("cannot prepare PostgreSQL credential file")
	}
	env := append(safeEnvironment(), "PGHOST="+c.Source.Host, "PGPORT="+strconv.Itoa(c.Source.Port), "PGUSER="+c.Source.User, "PGPASSFILE="+passfile, "PGSSLMODE="+c.Source.SSLMode, "PGCONNECT_TIMEOUT=15", "PGAPPNAME=rtk-postgres-backup")
	if c.Source.RootCertFile != "" {
		env = append(env, "PGSSLROOTCERT="+c.Source.RootCertFile)
	}
	var out cappedBuffer
	if err = e.run(ctx, []string{"pg_basebackup", "--version"}, env, &out); err != nil {
		return result, err
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "pg_basebackup (PostgreSQL) 16.") {
		return result, errors.New("PostgreSQL 16 backup tools required")
	}
	out.Reset()
	if err = e.run(ctx, []string{"psql", "--no-psqlrc", "--no-password", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", "--dbname=dbname=replication replication=true", "--command=IDENTIFY_SYSTEM"}, env, &out); err != nil {
		return result, err
	}
	fields := strings.Split(strings.TrimSpace(out.String()), "|")
	if len(fields) != 4 || fields[0] != c.Source.SystemIdentifier {
		return result, errors.New("source PostgreSQL system identifier differs from reviewed configuration")
	}
	out.Reset()
	if err = e.run(ctx, []string{"psql", "--no-psqlrc", "--no-password", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--command=SELECT current_setting('server_version_num'), (SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global'))"}, env, &out); err != nil {
		return result, err
	}
	fields = strings.Split(strings.TrimSpace(out.String()), "|")
	if len(fields) != 2 {
		return result, errors.New("source PostgreSQL catalog preflight failed")
	}
	serverVersion, versionErr := strconv.Atoi(fields[0])
	if versionErr != nil || serverVersion/10000 != PostgresMajor || fields[1] != "0" {
		return result, errors.New("source must be PostgreSQL 16 without custom tablespaces")
	}
	id := recovery.NewID()
	m := Manifest{Version: Version, Scope: Scope, ID: id, Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, PostgresMajor: PostgresMajor, PostgresImage: c.Source.PostgresImage, SystemIdentifier: c.Source.SystemIdentifier, StartedAt: time.Now().UTC()}
	pgdata := filepath.Join(tmp, "pgdata")
	maxRate := c.Source.MaxRate
	if maxRate == "" {
		maxRate = "10M"
	}
	args := []string{"pg_basebackup", "--pgdata=" + pgdata, "--format=plain", "--wal-method=stream", "--checkpoint=spread", "--max-rate=" + maxRate, "--no-estimate-size", "--no-password", "--label=" + id}
	if err = e.captureBounded(ctx, c, pgdata, args, env); err != nil {
		return result, err
	}
	// A restore point belongs to capture completion, never to upload completion.
	m.FinishedAt = time.Now().UTC()
	if err = e.verify(ctx, c, pgdata); err != nil {
		return result, err
	}
	a, err := recovery.DigestFile(filepath.Join(pgdata, "backup_manifest"))
	if err != nil {
		return result, errors.New("native backup manifest unavailable")
	}
	m.NativeManifestSHA256 = a.SHA256
	file := filepath.Join(c.Directory, id+".age")
	if err = Pack(ctx, c, m, pgdata, file); err != nil {
		return result, err
	}
	if err = recovery.WriteJSON(filepath.Join(c.Directory, id+".manifest.json"), m); err != nil {
		os.Remove(file)
		return result, errors.New("cannot save capture metadata")
	}
	return CaptureResult{Manifest: m, File: file}, nil
}

// Bound the copy while it is in progress. The scratch reservation also covers
// the later encrypted file; checking only after pg_basebackup exits can fill a
// filesystem before an oversized backup is rejected.
func (e Engine) captureBounded(ctx context.Context, c Config, pgdata string, args, env []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	failed := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := scratchSpace(c.Directory, 0)
				if err == nil {
					if _, statErr := os.Lstat(pgdata); statErr == nil {
						err = inspectTree(pgdata, c.MaxPlaintextBytes, true)
					} else if !os.IsNotExist(statErr) {
						err = errors.New("cannot monitor native backup capacity")
					}
				}
				if err != nil {
					failed <- err
					cancel()
					return
				}
			}
		}
	}()
	err := e.run(ctx, args, env, io.Discard)
	close(done)
	wg.Wait()
	select {
	case capacityErr := <-failed:
		return capacityErr
	default:
		return err
	}
}

func scratchSpace(dir string, required int64) error {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return errors.New("cannot inspect backup scratch capacity")
	}
	const reserve = int64(64 << 20)
	if required < 0 || uint64(required)+uint64(reserve) > uint64(s.Bavail)*uint64(s.Bsize) {
		return errors.New("insufficient scratch space for plaintext and encrypted backup with reserve")
	}
	return nil
}

func Restore(ctx context.Context, c Config, file, identity, targetDir string) (Manifest, error) {
	return (Engine{}).Restore(ctx, c, file, identity, targetDir)
}

func (e Engine) Restore(ctx context.Context, c Config, file, identity, targetDir string) (m Manifest, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	m, err = Unpack(ctx, c, file, identity, targetDir)
	if err != nil {
		return m, err
	}
	if err = e.verify(ctx, c, filepath.Join(targetDir, "pgdata")); err != nil {
		removeExtracted(targetDir)
		return m, err
	}
	return m, nil
}

func (e Engine) verify(ctx context.Context, c Config, pgdata string) error {
	if err := validateTree(pgdata, c.MaxPlaintextBytes); err != nil {
		return err
	}
	version, err := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
	if err != nil || strings.TrimSpace(string(version)) != "16" {
		return errors.New("native backup PostgreSQL major version mismatch")
	}
	entries, err := os.ReadDir(filepath.Join(pgdata, "pg_tblspc"))
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect tablespaces")
	}
	if len(entries) != 0 {
		return errors.New("custom PostgreSQL tablespaces are unsupported")
	}
	if b, err := os.ReadFile(filepath.Join(pgdata, "tablespace_map")); (err == nil && len(bytes.TrimSpace(b)) != 0) || (err != nil && !os.IsNotExist(err)) {
		return errors.New("custom PostgreSQL tablespaces are unsupported")
	}
	wals, err := os.ReadDir(filepath.Join(pgdata, "pg_wal"))
	if err != nil || len(wals) == 0 {
		return errors.New("native backup is missing required WAL")
	}
	var out cappedBuffer
	if err = e.run(ctx, []string{"pg_controldata", pgdata}, safeEnvironment(), &out); err != nil {
		return err
	}
	systemID := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "Database system identifier" {
			systemID = strings.TrimSpace(value)
		}
	}
	if systemID != c.Source.SystemIdentifier {
		return errors.New("native backup system identifier mismatch")
	}
	if err = e.run(ctx, []string{"pg_verifybackup", pgdata}, safeEnvironment(), io.Discard); err != nil {
		return errors.New("native PostgreSQL backup/WAL verification failed")
	}
	return nil
}

// VerifyNative is available to orchestration after transport, without starting
// a server or changing the captured files.
func VerifyNative(ctx context.Context, c Config, pgdata string) error {
	return (Engine{}).verify(ctx, c, pgdata)
}

func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	f, err := os.Open(path)
	if err != nil {
		return m, errors.New("backup metadata unavailable")
	}
	defer f.Close()
	if err = recovery.Decode(io.LimitReader(f, 65536), &m); err != nil {
		return m, errors.New("invalid backup metadata")
	}
	return m, m.Validate()
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("PostgreSQL metadata output exceeded limit")
	}
	return b.Buffer.Write(p)
}

func privateScratch(dir string) error {
	if err := recovery.PrivateDirectory(dir); err != nil {
		return err
	}
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return errors.New("backup scratch must be outside a Git checkout")
		} else if !os.IsNotExist(err) {
			return errors.New("cannot validate backup scratch")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PITRPlan identifies an explicit WAL target. Preparation is not evidence that
// PostgreSQL reached the target; the recovered server must be inspected while paused.
type PITRPlan struct {
	Version        int    `json:"version"`
	TargetLSN      string `json:"target_lsn"`
	TargetTimeline uint32 `json:"target_timeline"`
	Executable     string `json:"executable"`
	WALConfigFile  string `json:"wal_config_file"`
}

func (p PITRPlan) Validate() error {
	lsn, ok := baseLSN(p.TargetLSN)
	if p.Version != 1 || !ok || lsn == 0 || p.TargetTimeline == 0 {
		return errors.New("explicit nonzero PITR LSN and timeline required")
	}
	for _, path := range []string{p.Executable, p.WALConfigFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("absolute clean PITR tool/config paths required")
		}
	}
	return nil
}
func (p PITRPlan) checkInputs(c WALConfig, identity string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, path := range []string{p.Executable, p.WALConfigFile, identity} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("regular PITR tool/config/identity file required")
		}
		if path == p.Executable && info.Mode().Perm()&0111 == 0 {
			return errors.New("PITR restore tool is not executable")
		}
		if path == identity && (info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20) {
			return errors.New("private PITR age identity required")
		}
	}
	if !filepath.IsAbs(identity) || filepath.Clean(identity) != identity || strings.ContainsAny(identity, "\x00\r\n") {
		return errors.New("absolute clean PITR identity path required")
	}
	f, err := os.Open(p.WALConfigFile)
	if err != nil {
		return err
	}
	defer f.Close()
	var supplied WALConfig
	if err = Decode(io.LimitReader(f, 1<<20), &supplied); err != nil {
		return err
	}
	if Digest(supplied) != Digest(c) {
		return errors.New("PITR WAL configuration differs from physical backup scope")
	}
	return nil
}

func (e BaseBackupEngine) RestorePITR(ctx context.Context, id, destination, identity string, p PITRPlan) error {
	if err := e.Config.Validate(); err != nil {
		return err
	}
	if err := p.checkInputs(e.Config.WAL, identity); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := e.Restore(ctx, id, destination, identity); err != nil {
		return err
	}
	if err := e.preparePITR(ctx, id, destination, identity, p); err != nil {
		os.RemoveAll(destination)
		return err
	}
	return nil
}

func (e BaseBackupEngine) preparePITR(ctx context.Context, id, destination, identity string, p PITRPlan) error {
	if err := p.checkInputs(e.Config.WAL, identity); err != nil {
		return err
	}
	data := filepath.Join(destination, "pgdata")
	for _, name := range []string{"postmaster.pid", "recovery.signal", "standby.signal"} {
		if _, err := os.Lstat(filepath.Join(data, name)); !os.IsNotExist(err) {
			return errors.New("PITR requires a fresh stopped physical restore")
		}
	}
	f, err := os.Open(filepath.Join(destination, "verified.json"))
	if err != nil {
		return err
	}
	var m Manifest
	err = Decode(io.LimitReader(f, 1<<20), &m)
	f.Close()
	if err != nil {
		return err
	}
	if m.Version != Version || m.Environment != e.Config.WAL.Environment || m.Stack != e.Config.WAL.Stack || m.ID != id || m.Scope != physicalScope || m.ConfigurationSHA256 != Digest(e.Config) {
		return errors.New("PITR physical restore scope mismatch")
	}
	evidence, err := e.verify(ctx, data)
	if err != nil {
		return err
	}
	if Digest(evidence) != Digest(m.Evidence) {
		return errors.New("physical restore changed before PITR preparation")
	}
	var ranges []baseWALRange
	if err = Decode(strings.NewReader(evidence["wal_ranges"]), &ranges); err != nil || len(ranges) == 0 {
		return errors.New("PITR requires verified backup WAL ranges")
	}
	target, _ := baseLSN(p.TargetLSN)
	// Base backups cannot recover to a time before their consistency point. A
	// descendant timeline is resolved and checked by PostgreSQL using authenticated
	// archive history; never select an implicit latest timeline here.
	for _, r := range ranges {
		end, ok := baseLSN(r.End)
		if !ok || target < end || p.TargetTimeline < r.Timeline {
			return errors.New("PITR target precedes physical backup consistency")
		}
	}
	var control bytes.Buffer
	if err = e.run(ctx, "pg_controldata", &boundedWriter{writer: &control, remaining: 64 << 10}, data); err != nil {
		return err
	}
	config, err := renderPITRConfig(e.Config.WAL, p, destination, identity, control.String())
	if err != nil {
		return err
	}
	// These paths belong only to the newly restored copy. Preserve the source
	// settings outside PGDATA, then replace them; never include/execute them.
	for _, name := range []string{"postgresql.conf", "postgresql.auto.conf", "postmaster.opts"} {
		path := filepath.Join(data, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("invalid restored PostgreSQL configuration")
		}
		saved := filepath.Join(destination, "source-"+name)
		if _, err = os.Lstat(saved); !os.IsNotExist(err) {
			return errors.New("PITR source configuration already saved")
		}
		if err = os.Rename(path, saved); err != nil {
			return err
		}
	}
	if err = os.Mkdir(filepath.Join(destination, "socket"), 0700); err != nil {
		return err
	}
	for _, file := range []struct{ path, body string }{
		{filepath.Join(data, "postgresql.conf"), config},
		{filepath.Join(data, "postgresql.auto.conf"), "# Isolated recovery; no inherited ALTER SYSTEM settings.\n"},
		{filepath.Join(destination, "pitr_hba.conf"), "local all all peer\n"},
		{filepath.Join(destination, "pitr_ident.conf"), "# No user mappings.\n"},
	} {
		if err = writePITRFile(file.path, file.body); err != nil {
			return err
		}
	}
	if err = WriteJSON(filepath.Join(destination, "pitr.json"), pitrPreparation{"prepared-not-replayed", id, p}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Signal comes last, after all recovery settings are durable.
	if err = syncWALDirectory(data); err != nil {
		return err
	}
	if err = syncWALDirectory(destination); err != nil {
		return err
	}
	if err = writePITRFile(filepath.Join(data, "recovery.signal"), ""); err != nil {
		return err
	}
	return syncWALDirectory(data)
}
func writePITRFile(path, body string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.WriteString(body); err != nil {
		return err
	}
	return f.Sync()
}
func pgConfigString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "''") + "'"
}
func restoreShellArgument(s string) string {
	// PostgreSQL expands percent escapes after reading the config and before the
	// shell interprets it. Protect static percent characters and shell metacharacters.
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "%", "%%"), "'", "'\"'\"'") + "'"
}
func renderPITRConfig(c WALConfig, p PITRPlan, destination, identity, control string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(destination) || strings.ContainsAny(destination+identity, "\x00\r\n") {
		return "", errors.New("invalid PITR recovery paths")
	}
	if strings.Contains(control, "WARNING:") {
		return "", errors.New("untrusted PostgreSQL control data")
	}
	settings := [][2]string{{"max_connections", "max_connections"}, {"max_worker_processes", "max_worker_processes"}, {"max_wal_senders", "max_wal_senders"}, {"max_prepared_transactions", "max_prepared_xacts"}, {"max_locks_per_transaction", "max_locks_per_xact"}}
	var b strings.Builder
	b.WriteString("# Generated isolated PITR configuration. Source config is not included.\n")
	for _, setting := range settings {
		pattern := regexp.MustCompile(`(?m)^` + setting[1] + ` setting:\s+([0-9]+)\s*$`)
		m := pattern.FindStringSubmatch(control)
		if len(m) != 2 {
			return "", errors.New("missing PostgreSQL recovery resource settings")
		}
		if _, err := strconv.ParseUint(m[1], 10, 31); err != nil {
			return "", errors.New("invalid PostgreSQL recovery resource setting")
		}
		fmt.Fprintf(&b, "%s = %s\n", setting[0], m[1])
	}
	for _, setting := range [][2]string{
		{"data_directory", filepath.Join(destination, "pgdata")}, {"hba_file", filepath.Join(destination, "pitr_hba.conf")}, {"ident_file", filepath.Join(destination, "pitr_ident.conf")}, {"unix_socket_directories", filepath.Join(destination, "socket")},
	} {
		fmt.Fprintf(&b, "%s = %s\n", setting[0], pgConfigString(setting[1]))
	}
	b.WriteString("listen_addresses = ''\nport = 5432\nunix_socket_permissions = 0700\nssl = off\narchive_mode = off\nhot_standby = on\nlogging_collector = off\nshared_preload_libraries = ''\n")
	argv := []string{p.Executable, "wal-restore", "--config", p.WALConfigFile, "--confirm-environment", c.Environment, "--confirm-stack", c.Stack, "--identity", identity}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = restoreShellArgument(arg)
	}
	command := "exec " + strings.Join(quoted, " ") + " --name '%f' --destination '%p'"
	fmt.Fprintf(&b, "restore_command = %s\nrecovery_target_lsn = %s\nrecovery_target_timeline = '%d'\nrecovery_target_inclusive = on\nrecovery_target_action = 'pause'\n", pgConfigString(command), pgConfigString(p.TargetLSN), p.TargetTimeline)
	return b.String(), nil
}

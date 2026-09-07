package recovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PITRConnection is deliberately local-only. It cannot select a TCP endpoint or
// inherit the source backup service's connection settings.
type PITRConnection struct {
	SocketDirectory string
	Port            int
	User            string
	Database        string
}

type pitrPreparation struct {
	Status   string   `json:"status"`
	BackupID string   `json:"backup_id"`
	Plan     PITRPlan `json:"plan"`
}

type PITRObservation struct {
	Status           string    `json:"status"`
	ObservedAt       time.Time `json:"observed_at"`
	BackupID         string    `json:"backup_id"`
	Environment      string    `json:"environment"`
	Stack            string    `json:"stack"`
	SystemIdentifier string    `json:"system_identifier"`
	TargetLSN        string    `json:"target_lsn"`
	TargetTimeline   uint32    `json:"target_timeline"`
	ReplayLSN        string    `json:"replay_lsn"`
	DataDirectory    string    `json:"data_directory"`
}

type pitrRuntime struct {
	SystemIdentifier string  `json:"system_identifier"`
	DataDirectory    string  `json:"data_directory"`
	ServerVersion    int     `json:"server_version"`
	InRecovery       bool    `json:"in_recovery"`
	PauseState       string  `json:"pause_state"`
	ReplayLSN        string  `json:"replay_lsn"`
	TargetLSN        string  `json:"target_lsn"`
	TargetTimeline   string  `json:"target_timeline"`
	TargetAction     string  `json:"target_action"`
	TargetInclusive  string  `json:"target_inclusive"`
	ListenAddresses  *string `json:"listen_addresses"`
	ArchiveMode      string  `json:"archive_mode"`
	ReadOnly         string  `json:"read_only"`
}

const pitrObservationSQL = `BEGIN READ ONLY;
SET LOCAL statement_timeout = '10s';
SET LOCAL search_path = pg_catalog;
SELECT json_build_object(
 'system_identifier', (SELECT system_identifier::text FROM pg_control_system()),
 'data_directory', current_setting('data_directory'),
 'server_version', current_setting('server_version_num')::int,
 'in_recovery', pg_is_in_recovery(),
 'pause_state', pg_get_wal_replay_pause_state(),
 'replay_lsn', pg_last_wal_replay_lsn()::text,
 'target_lsn', current_setting('recovery_target_lsn'),
 'target_timeline', current_setting('recovery_target_timeline'),
 'target_action', current_setting('recovery_target_action'),
 'target_inclusive', current_setting('recovery_target_inclusive'),
 'listen_addresses', current_setting('listen_addresses'),
 'archive_mode', current_setting('archive_mode'),
 'read_only', current_setting('transaction_read_only'));
COMMIT;`

// ObservePITR reports a point-in-time runtime observation. It neither starts nor
// promotes a server, mutates its files, or asserts application/PKI reconciliation.
func (e BaseBackupEngine) ObservePITR(ctx context.Context, id, destination string, conn PITRConnection) (PITRObservation, error) {
	var result PITRObservation
	if err := e.Config.Validate(); err != nil {
		return result, err
	}
	if !Name.MatchString(id) || conn.Port < 1 || conn.Port > 65535 || !sqlIdentifier(conn.User) || !sqlIdentifier(conn.Database) {
		return result, errors.New("invalid PITR observation identity or connection")
	}
	for _, dir := range []string{destination, conn.SocketDirectory} {
		if err := existingPrivateDirectory(dir); err != nil {
			return result, err
		}
	}
	var manifest Manifest
	if err := readPrivatePITRJSON(filepath.Join(destination, "verified.json"), &manifest); err != nil {
		return result, err
	}
	if manifest.Version != Version || manifest.ID != id || manifest.Scope != physicalScope || manifest.Environment != e.Config.WAL.Environment || manifest.Stack != e.Config.WAL.Stack || manifest.ConfigurationSHA256 != Digest(e.Config) {
		return result, errors.New("PITR observation backup scope mismatch")
	}
	var prepared pitrPreparation
	if err := readPrivatePITRJSON(filepath.Join(destination, "pitr.json"), &prepared); err != nil {
		return result, err
	}
	if prepared.Status != "prepared-not-replayed" || prepared.BackupID != id || prepared.Plan.Validate() != nil {
		return result, errors.New("invalid PITR preparation record")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(min(e.Config.TimeoutSeconds, 30))*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := e.run(ctx, "psql", &boundedWriter{writer: &output, remaining: 16 << 10}, "--no-psqlrc", "--no-password", "--quiet", "--tuples-only", "--no-align", "--host="+conn.SocketDirectory, "--port="+strconv.Itoa(conn.Port), "--username="+conn.User, "--dbname="+conn.Database, "--set=ON_ERROR_STOP=1", "--command="+pitrObservationSQL); err != nil {
		return result, err
	}
	var runtime pitrRuntime
	if err := Decode(&output, &runtime); err != nil {
		return result, err
	}
	if err := validatePITRRuntime(e.Config, prepared.Plan, destination, runtime); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return PITRObservation{Status: "paused-target-observed", ObservedAt: time.Now().UTC(), BackupID: id, Environment: e.Config.WAL.Environment, Stack: e.Config.WAL.Stack, SystemIdentifier: runtime.SystemIdentifier, TargetLSN: prepared.Plan.TargetLSN, TargetTimeline: prepared.Plan.TargetTimeline, ReplayLSN: runtime.ReplayLSN, DataDirectory: runtime.DataDirectory}, nil
}

func validatePITRRuntime(c BaseBackupConfig, p PITRPlan, destination string, r pitrRuntime) error {
	target, _ := baseLSN(p.TargetLSN)
	actualTarget, ok := baseLSN(r.TargetLSN)
	replay, replayOK := baseLSN(r.ReplayLSN)
	if r.SystemIdentifier != c.WAL.SystemIdentifier || r.DataDirectory != filepath.Join(destination, "pgdata") || r.ServerVersion < 160000 || r.ServerVersion >= 170000 {
		return errors.New("observed PostgreSQL identity or version mismatch")
	}
	if !r.InRecovery || r.PauseState != "paused" || r.ReadOnly != "on" || !ok || actualTarget != target || !replayOK || replay < target || r.TargetTimeline != strconv.FormatUint(uint64(p.TargetTimeline), 10) || r.TargetAction != "pause" || r.TargetInclusive != "on" {
		return errors.New("PostgreSQL is not paused at the reviewed recovery target")
	}
	if r.ListenAddresses == nil || *r.ListenAddresses != "" || r.ArchiveMode != "off" {
		return errors.New("observed recovery server is not isolated")
	}
	return nil
}
func existingPrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("absolute clean private recovery directory required")
	}
	for p := path; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || (p == path && info.Mode().Perm() != 0700) {
			return errors.New("existing private recovery directory without symlink ancestors required")
		}
	}
	return nil
}
func readPrivatePITRJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return errors.New("private regular PITR metadata required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("PITR metadata changed during observation")
	}
	return Decode(io.LimitReader(f, 1<<20), target)
}

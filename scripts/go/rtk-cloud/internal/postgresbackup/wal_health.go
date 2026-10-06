package postgresbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
	"strings"
	"time"
)

// The dedicated NOINHERIT backup role switches only this session to its granted
// read-only pg_monitor role. This query reads the real archive process and physical source identity. It
// cannot fabricate freshness by switching WAL or writing a heartbeat at preflight.
const walHealthSQL = `SET ROLE pg_monitor; SELECT json_build_object('system_identifier',(SELECT system_identifier::text FROM pg_control_system()),'archive_mode',current_setting('archive_mode'),'archive_timeout_seconds',EXTRACT(epoch FROM current_setting('archive_timeout')::interval)::integer,'in_recovery',pg_is_in_recovery(),'last_archived_wal',last_archived_wal,'last_archived_at',to_char(last_archived_time AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'last_failed_at',to_char(last_failed_time AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'observed_at',to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) FROM pg_stat_archiver`

type WALObservation struct {
	SystemIdentifier      string     `json:"system_identifier"`
	ArchiveMode           string     `json:"archive_mode"`
	ArchiveTimeoutSeconds int        `json:"archive_timeout_seconds"`
	InRecovery            bool       `json:"in_recovery"`
	LastArchivedWAL       string     `json:"last_archived_wal"`
	LastArchivedAt        *time.Time `json:"last_archived_at"`
	LastFailedAt          *time.Time `json:"last_failed_at"`
	ObservedAt            time.Time  `json:"observed_at"`
}
type WALHealth struct {
	Environment            string    `json:"environment"`
	SystemIdentifier       string    `json:"system_identifier"`
	ObservedAt             time.Time `json:"observed_at"`
	LastArchivedAt         time.Time `json:"last_archived_at"`
	LastArchivedWAL        string    `json:"last_archived_wal"`
	RemoteArtifactSHA256   string    `json:"remote_artifact_sha256"`
	ArchiveAgeMilliseconds int64     `json:"archive_age_ms"`
}

func (e Engine) CheckContinuousWAL(ctx context.Context, c Config) (WALHealth, error) {
	var out cappedBuffer
	if err := e.walQuery(ctx, c, walHealthSQL, &out); err != nil {
		return WALHealth{}, err
	}
	var observation WALObservation
	if recovery.Decode(strings.NewReader(out.String()), &observation) != nil {
		return WALHealth{}, errors.New("native WAL archive observation unavailable")
	}
	return e.VerifyContinuousWAL(ctx, c, observation, time.Now().UTC())
}

// HeartbeatWAL is an explicit scheduled write, never called by preflight. One
// small native WAL message lets archive_timeout protect otherwise idle sources;
// the read-only health check still independently verifies the remote archive.
func (e Engine) HeartbeatWAL(ctx context.Context, c Config) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	sql := fmt.Sprintf(`SET ROLE pg_monitor; SELECT pg_logical_emit_message(false,'rtk-wal-heartbeat',clock_timestamp()::text) FROM pg_control_system() WHERE system_identifier::text='%s' AND NOT pg_is_in_recovery() AND current_setting('archive_mode')='on' AND current_setting('archive_timeout')::interval BETWEEN interval '1 second' AND interval '60 seconds'`, c.Source.SystemIdentifier)
	var out cappedBuffer
	if err := e.walQuery(ctx, c, sql, &out); err != nil {
		return "", err
	}
	lsn := strings.TrimSpace(out.String())
	if !nativeReplayLSN.MatchString(lsn) {
		return "", errors.New("WAL heartbeat requires the exact primary cluster and continuous archive configuration")
	}
	return lsn, nil
}

func (e Engine) walQuery(ctx context.Context, c Config, sql string, out io.Writer) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := privateScratch(c.Directory); err != nil {
		return err
	}
	directory, err := os.MkdirTemp(c.Directory, ".check-wal-")
	if err != nil {
		return errors.New("cannot prepare private WAL query credentials")
	}
	defer os.RemoveAll(directory)
	env, err := sourceEnvironment(c, directory)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	return e.run(ctx, []string{"psql", "--quiet", "--no-psqlrc", "--no-password", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--command=" + sql}, env, out)
}

// VerifyContinuousWAL couples source-side archiver status to a complete encrypted
// remote read-back for that exact segment. Base-backup or upload timestamps alone
// cannot establish continuous protection, measured RPO or successful restoration.
func (e Engine) VerifyContinuousWAL(ctx context.Context, c Config, o WALObservation, now time.Time) (WALHealth, error) {
	var result WALHealth
	if err := c.Validate(); err != nil {
		return result, err
	}
	if now.IsZero() || o.ObservedAt.IsZero() || o.ObservedAt.Before(now.Add(-30*time.Second)) || o.ObservedAt.After(now.Add(30*time.Second)) {
		return result, errors.New("WAL source clock or observation is stale; repair clock synchronization and repeat the live check")
	}
	if o.SystemIdentifier != c.Source.SystemIdentifier || o.InRecovery || o.ArchiveMode != "on" || o.ArchiveTimeoutSeconds < 1 || o.ArchiveTimeoutSeconds > 60 {
		return result, errors.New("continuous WAL requires the exact primary cluster with archiving enabled and archive_timeout at most 60 seconds")
	}
	if !walName.MatchString(o.LastArchivedWAL) || len(o.LastArchivedWAL) != 24 || o.LastArchivedAt == nil || o.LastArchivedAt.After(o.ObservedAt.Add(30*time.Second)) || o.LastArchivedAt.Before(o.ObservedAt.Add(-15*time.Minute)) {
		return result, errors.New("continuous WAL archive freshness exceeds 15 minutes or lacks an exact native segment; inspect the archiver, monitoring and maintained heartbeat")
	}
	if o.LastFailedAt != nil && o.LastFailedAt.After(*o.LastArchivedAt) {
		return result, errors.New("native WAL archiver has an unresolved failure after its last successful archive")
	}
	client, err := e.repository(c)
	if err != nil {
		return result, err
	}
	receipt, err := readWALCompletion(ctx, client, c, o.LastArchivedWAL)
	if err != nil {
		return result, errors.New("the exact native WAL archive has no valid remote completion")
	}
	if receipt.Manifest.ArchivedAt.Before(o.ObservedAt.Add(-15*time.Minute)) || receipt.Manifest.ArchivedAt.After(o.ObservedAt.Add(30*time.Second)) {
		return result, errors.New("remote WAL completion is outside the current freshness window")
	}
	if err := verifyWALRemote(ctx, client, c, receipt.Artifact); err != nil {
		return result, err
	}
	result = WALHealth{Environment: c.Environment, SystemIdentifier: c.Source.SystemIdentifier, ObservedAt: o.ObservedAt, LastArchivedAt: *o.LastArchivedAt, LastArchivedWAL: o.LastArchivedWAL, RemoteArtifactSHA256: receipt.Artifact.SHA256, ArchiveAgeMilliseconds: max(int64(0), o.ObservedAt.Sub(*o.LastArchivedAt).Milliseconds())}
	return result, nil
}

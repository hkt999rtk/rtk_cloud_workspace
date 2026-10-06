package postgresbackup

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PITRObservation records measurements from native recovery, not permission to
// reopen traffic. Independently retained security state and a fault-time
// witness are still required to qualify production and measure its RPO.
type PITRObservation struct {
	Target     PITRTarget `json:"target"`
	ReplayTime time.Time  `json:"replay_time"`
	ReplayLSN  string     `json:"replay_lsn"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	RTOms      int64      `json:"rto_ms"`
}

type RestoredQuery func(database, sql string) (string, error)

var nativeReplayLSN = regexp.MustCompile(`^[0-9A-F]+/[0-9A-F]+$`)

// VerifyPreparedPITR starts only the quarantined, authenticated target produced
// by PreparePITR. It always stops the server, including when a data check fails.
// The caller supplies checks from the reviewed release, never from the archive.
func (e Engine) VerifyPreparedPITR(ctx context.Context, c Config, directory string, prepared PreparedPITR, checkData func(RestoredQuery) error) (observation PITRObservation, resultErr error) {
	if c.MatchManifest(prepared.Manifest) != nil || prepared.Target.At.IsZero() || prepared.Target.Timeline == 0 || len(prepared.WAL) == 0 || checkData == nil {
		return observation, errors.New("authenticated PITR preparation and maintained data checks are required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	digest, err := pitrConfigurationDigest(directory)
	if err != nil || digest != prepared.ConfigurationSHA256 {
		return observation, errors.New("isolated PITR configuration changed after preparation")
	}
	observation.Target, observation.StartedAt = prepared.Target, time.Now().UTC()
	pgdata := filepath.Join(directory, "pgdata")
	if err := e.run(ctx, []string{"pg_ctl", "-D", pgdata, "-w", "-t", "120", "-l", filepath.Join(directory, "postgres.log"), "start"}, safeEnvironment(), io.Discard); err != nil {
		// pg_ctl can time out after the server has started. Stop only this exact
		// private target even when startup did not return an acknowledgment.
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		_ = e.run(stop, []string{"pg_ctl", "-D", pgdata, "-m", "fast", "-w", "-t", "30", "stop"}, safeEnvironment(), io.Discard)
		return observation, errors.New("isolated PITR startup failed")
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if err := e.run(stop, []string{"pg_ctl", "-D", pgdata, "-m", "fast", "-w", "-t", "30", "stop"}, safeEnvironment(), io.Discard); err != nil {
			resultErr = errors.New("isolated PITR shutdown failed; target remains quarantined")
		}
		observation.FinishedAt = time.Now().UTC()
		observation.RTOms = observation.FinishedAt.Sub(observation.StartedAt).Milliseconds()
	}()
	query := RestoredQuery(func(database, sql string) (string, error) {
		var out cappedBuffer
		err := e.run(ctx, []string{"psql", "-X", "-h", "/tmp", "-p", "6543", "-U", "postgres", "-d", database, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql}, safeEnvironment(), &out)
		if err != nil {
			return "", errors.New("isolated PITR query failed")
		}
		return strings.TrimSpace(out.String()), nil
	})
	for {
		state, err := query("postgres", "SELECT pg_is_in_recovery(), pg_is_wal_replay_paused(), current_setting('transaction_read_only'), current_setting('default_transaction_read_only')")
		if err == nil && state == "t|t|on|on" {
			break
		}
		// Promotion, a writable target or normal completion without a pause are
		// definitive failures, rather than reasons to retry until the deadline.
		if err == nil && state != "t|f|on|on" {
			return observation, errors.New("PITR target lost the recovery or read-only fence")
		}
		select {
		case <-ctx.Done():
			return observation, errors.New("PITR did not reach its native recovery pause before the deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
	timestamp, err := query("postgres", `SELECT to_char(pg_last_xact_replay_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`)
	if err != nil {
		return observation, err
	}
	observation.ReplayTime, err = time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || observation.ReplayTime.After(prepared.Target.At) {
		return observation, errors.New("PITR replay time is absent or exceeds the requested target")
	}
	observation.ReplayLSN, err = query("postgres", "SELECT pg_last_wal_replay_lsn()")
	if err != nil || !nativeReplayLSN.MatchString(observation.ReplayLSN) {
		return observation, errors.New("PITR replay position is absent or invalid")
	}
	if checkData(query) != nil {
		return observation, errors.New("restored PITR data checks failed")
	}
	return observation, nil
}

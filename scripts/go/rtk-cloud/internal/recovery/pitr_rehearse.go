package recovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type PITRRehearsal struct {
	Status         string          `json:"status"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     time.Time       `json:"finished_at"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	Observation    PITRObservation `json:"observation"`
}

// RehearsePITR owns a newly restored, isolated server process. It reports success
// only after observing the paused target and stopping that process. It never
// promotes, removes recovered data, or releases application traffic fences.
func (e BaseBackupEngine) RehearsePITR(ctx context.Context, id, destination, identity, user, database string, p PITRPlan) (PITRRehearsal, error) {
	var report PITRRehearsal
	if err := e.Config.Validate(); err != nil {
		return report, err
	}
	if !sqlIdentifier(user) || !sqlIdentifier(database) {
		return report, errors.New("explicit local recovery database user and database required")
	}
	if os.Geteuid() == 0 {
		return report, errors.New("run PostgreSQL rehearsal as its non-root recovery OS user")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := e.RestorePITR(ctx, id, destination, identity, p); err != nil {
		return report, err
	}
	observation, err := e.rehearsePrepared(ctx, id, destination, user, database)
	if err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	finished := time.Now()
	report = PITRRehearsal{Status: "postgres-replay-rehearsed", StartedAt: started.UTC(), FinishedAt: finished.UTC(), ElapsedSeconds: finished.Sub(started).Seconds(), Observation: observation}
	if err := WriteJSON(filepath.Join(destination, "rehearsal.json"), report); err != nil {
		return PITRRehearsal{}, err
	}
	if err := syncWALDirectory(destination); err != nil {
		return PITRRehearsal{}, err
	}
	return report, nil
}

func (e BaseBackupEngine) rehearsePrepared(ctx context.Context, id, destination, user, database string) (observation PITRObservation, resultErr error) {
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	log, err := os.OpenFile(filepath.Join(destination, "rehearsal-server.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return observation, err
	}
	defer log.Close()
	// Launch directly so the exact server process remains owned and waitable.
	// No pg_ctl daemonization, inherited PG* options or shell interpretation.
	cmd := exec.Command(filepath.Join(e.Config.BinaryDirectory, "postgres"), "-D", filepath.Join(destination, "pgdata"))
	cmd.Env = rehearsalEnvironment(e.Config.BinaryDirectory)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return observation, errors.New("unable to start isolated rehearsal server")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	defer func() {
		if !exited {
			if err := stopRehearsal(cmd, done); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
		if err := log.Sync(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	conn := PITRConnection{SocketDirectory: filepath.Join(destination, "socket"), Port: 5432, User: user, Database: database}
	var lastError error
	for {
		select {
		case <-done:
			exited = true
			return observation, errors.New("isolated rehearsal server exited before successful observation; inspect private server log")
		case <-ctx.Done():
			return observation, errors.Join(ctx.Err(), lastError)
		default:
		}
		observation, err = e.ObservePITR(ctx, id, destination, conn)
		if err == nil {
			return observation, nil
		}
		lastError = err
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return observation, errors.Join(ctx.Err(), lastError)
		case <-timer.C:
		}
	}
}

func rehearsalEnvironment(binaryDirectory string) []string {
	env := []string{"LC_ALL=C", "PATH=" + binaryDirectory + ":/usr/bin:/bin"}
	// Only the archive reader's dedicated credentials and TLS trust overrides.
	for _, name := range []string{"RTK_BACKUP_ACCESS_KEY_ID", "RTK_BACKUP_SECRET_ACCESS_KEY", "RTK_BACKUP_SESSION_TOKEN", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// Cleanup deliberately survives caller cancellation. PostgreSQL SIGINT requests
// fast shutdown; SIGQUIT is the bounded fallback. Forced cleanup is a failed drill.
func stopRehearsal(cmd *exec.Cmd, done <-chan error) error {
	select {
	case <-done:
		return errors.New("rehearsal server exited before controlled shutdown")
	default:
	}
	signalErr := cmd.Process.Signal(syscall.SIGINT)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil || signalErr != nil {
			return errors.New("rehearsal server shutdown failed; inspect private server log")
		}
		return nil
	case <-timer.C:
	}
	_ = cmd.Process.Signal(syscall.SIGQUIT)
	timer.Reset(10 * time.Second)
	select {
	case <-done:
		return errors.New("rehearsal required immediate shutdown; inspect private recovery data")
	case <-timer.C:
	}
	_ = cmd.Process.Kill()
	// Do not claim stopped if even a kill cannot be confirmed (e.g. stuck I/O).
	timer.Reset(5 * time.Second)
	select {
	case <-done:
		return errors.New("rehearsal required forced termination; inspect private recovery data")
	case <-timer.C:
		return errors.New("rehearsal process termination unconfirmed; operator cleanup required")
	}
}

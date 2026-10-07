package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func runPostgresBackupWorker(args []string) (resultErr error) {
	if len(args) == 0 {
		return errors.New("worker action required")
	}
	action := args[0]
	fs := flag.NewFlagSet("postgres-backup-worker", flag.ContinueOnError)
	config := fs.String("config", "", "mounted deployment config")
	id := fs.String("id", "", "backup id")
	identity := fs.String("identity", "", "mounted restore identity")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected worker arguments")
	}
	var c postgresBackupDeployment
	f, err := os.Open(*config)
	if err != nil {
		return errors.New("worker config unavailable")
	}
	err = recovery.Decode(f, &c)
	f.Close()
	if err != nil {
		return err
	}
	if err = c.validate(); err != nil {
		return err
	}
	_, runnerBase, baseOK := strings.Cut(os.Getenv("RTK_POSTGRES_RUNNER_SOURCE_IMAGE"), "@sha256:")
	_, sourceDigest, _ := strings.Cut(c.Worker.Source.PostgresImage, "@sha256:")
	if !baseOK || runnerBase != sourceDigest {
		return errors.New("runner was not built from the reviewed source PostgreSQL image digest")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Worker.TimeoutSeconds)*time.Second)
	defer cancel()
	if action == "drill" {
		return postgresBackupDrillWorker(ctx, c, *id, *identity)
	}
	if action != "run" && action != "retry-upload" && action != "prune" {
		return errors.New("unsupported worker action")
	}
	kubeconfig, cleanup, err := recovery.ServiceAccountConfig()
	if err != nil {
		return err
	}
	defer cleanup()
	m := postgresBackupManager{Config: c, Kubeconfig: kubeconfig}
	return m.runWorker(ctx, action, *id, postgresBackupWorkerOperations{
		AcquireLock: func(ctx context.Context) (func() error, error) {
			return recovery.AcquireClusterLock(ctx, lkeKubectl(), kubeconfig, c.Namespace, "postgres-backup-"+postgresBackupRandomID(), true)
		},
		Capture: postgresbackup.Capture, Upload: postgresbackup.Upload, Prune: postgresbackup.Prune, Status: postgresbackup.GetStatus,
	})
}

type postgresBackupWorkerOperations struct {
	AcquireLock func(context.Context) (func() error, error)
	Capture     func(context.Context, postgresbackup.Config) (postgresbackup.CaptureResult, error)
	Upload      func(context.Context, postgresbackup.Config, postgresbackup.Manifest, string) error
	Prune       func(context.Context, postgresbackup.Config, bool) (postgresbackup.PrunePlan, error)
	Status      func(context.Context, postgresbackup.Config) (postgresbackup.Status, error)
}

func (m *postgresBackupManager) runWorker(ctx context.Context, action, id string, operations postgresBackupWorkerOperations) (resultErr error) {
	c := m.Config
	release, err := operations.AcquireLock(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "PostgreSQL backup skipped: another recovery operation owns the cluster lock; shared status unchanged")
		return err
	}
	var status postgresbackup.Status
	var attempt *postgresbackup.Attempt
	defer func() {
		if attempt != nil {
			attempt.FinishedAt = time.Now().UTC()
			if resultErr != nil && attempt.Status == "running" {
				attempt.Status = "failed"
			}
			if err := m.writeStatus(context.WithoutCancel(ctx), status); err != nil && resultErr == nil {
				resultErr = err
			}
		}
		if err := release(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	status, err = postgresBackupReadStatus(ctx, m)
	if err != nil {
		return err
	}
	status.Version = 1
	status.Environment = c.Environment
	status.Stack = c.Stack
	status.ClusterID = c.Worker.ClusterID
	attempt = &postgresbackup.Attempt{StartedAt: time.Now().UTC(), Status: "running"}
	status.LastAttempt = attempt
	if err = m.writeStatus(ctx, status); err != nil {
		return err
	}
	switch action {
	case "run":
		if err := m.validateSourcePod(ctx); err != nil {
			return err
		}
		if err := postgresBackupScratchReady(c.Worker.Directory); err != nil {
			return err
		}
		capture, err := operations.Capture(ctx, c.Worker)
		if err != nil {
			return err
		}
		if err = operations.Upload(ctx, c.Worker, capture.Manifest, capture.File); err != nil {
			return err
		}
		if err = postgresBackupPublishedStatus(&status, c.Worker, capture.Manifest.ID); err != nil {
			return err
		}
		// Delete only the successfully published encrypted artifact and its metadata.
		if err = postgresBackupRemovePublished(c.Worker, capture.Manifest.ID); err != nil {
			return err
		}
		if _, err = operations.Prune(ctx, c.Worker, false); err != nil {
			return err
		}
	case "retry-upload":
		if !recovery.Name.MatchString(id) {
			return errors.New("invalid retry backup id")
		}
		manifest, err := postgresbackup.ReadManifest(filepath.Join(c.Worker.Directory, id+".manifest.json"))
		if err != nil {
			return err
		}
		if err = c.Worker.MatchManifest(manifest); err != nil {
			return err
		}
		if err = operations.Upload(ctx, c.Worker, manifest, filepath.Join(c.Worker.Directory, id+".age")); err != nil {
			return err
		}
		if err = postgresBackupPublishedStatus(&status, c.Worker, id); err != nil {
			return err
		}
		if err = postgresBackupRemovePublished(c.Worker, id); err != nil {
			return err
		}
	case "prune":
		if _, err = operations.Prune(ctx, c.Worker, false); err != nil {
			return err
		}
	}
	remote, err := operations.Status(ctx, c.Worker)
	if err != nil {
		return err
	}
	status = remote
	status.LastAttempt = attempt
	attempt.Status = "succeeded"
	return nil
}

func postgresBackupPublishedStatus(status *postgresbackup.Status, c postgresbackup.Config, id string) error {
	b, err := os.ReadFile(filepath.Join(c.Directory, id+".complete.json"))
	if err != nil {
		return errors.New("published backup metadata unavailable")
	}
	var completion postgresbackup.Completion
	if json.Unmarshal(b, &completion) != nil || c.MatchManifest(completion.Manifest) != nil {
		return errors.New("published backup metadata invalid")
	}
	if status.Latest == nil || completion.Manifest.FinishedAt.After(status.Latest.Manifest.FinishedAt) {
		status.Latest = &completion
	}
	if status.CompletedCount == 0 {
		status.CompletedCount = 1
	}
	return nil
}

func postgresBackupScratchReady(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("backup scratch unavailable")
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".age") || strings.HasSuffix(entry.Name(), ".manifest.json") {
			return errors.New("unpublished backup remains on scratch; retry-upload that backup before starting another capture")
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".capture-postgres-") {
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return errors.New("stale capture scratch cleanup failed")
			}
		}
	}
	return nil
}

func postgresBackupRemovePublished(c postgresbackup.Config, id string) error {
	if !recovery.Name.MatchString(id) {
		return errors.New("invalid published backup id")
	}
	for _, suffix := range []string{".age", ".manifest.json"} {
		if err := os.Remove(filepath.Join(c.Directory, id+suffix)); err != nil && !os.IsNotExist(err) {
			return errors.New("published backup scratch cleanup failed")
		}
	}
	return nil
}

func (m *postgresBackupManager) writeStatus(ctx context.Context, status postgresbackup.Status) error {
	status.GeneratedAt = time.Now().UTC()
	b, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return m.apply(ctx, map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": postgresBackupMetadata(m.Config, postgresBackupStatusName), "data": map[string]string{"status.json": string(b)}})
}

func postgresBackupDrillWorker(ctx context.Context, c postgresBackupDeployment, id, identity string) error {
	if !recovery.Name.MatchString(id) || identity != "/run/postgres-restore/identity.agekey" {
		return errors.New("drill requires an explicit backup id and mounted private identity")
	}
	file, err := postgresbackup.Download(ctx, c.Worker, id, c.Worker.Directory)
	if err != nil {
		return err
	}
	target := filepath.Join(c.Worker.Directory, "restore-"+id)
	manifest, err := postgresbackup.Restore(ctx, c.Worker, file, identity, target)
	if err != nil {
		return err
	}
	if err := postgresBackupVerifyDrillManifest(c.Worker, id, manifest); err != nil {
		return err
	}
	if err := postgresBackupVerifyRunningCluster(ctx, target); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(postgresbackup.Drill{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, BackupID: id, SystemIdentifier: manifest.SystemIdentifier, PostgresImage: manifest.PostgresImage, FinishedAt: time.Now().UTC(), Success: true})
}

func postgresBackupVerifyDrillManifest(c postgresbackup.Config, id string, manifest postgresbackup.Manifest) error {
	f, err := os.Open(filepath.Join(c.Directory, id+".complete.json"))
	if err != nil {
		return errors.New("download completion evidence unavailable")
	}
	defer f.Close()
	var completion postgresbackup.Completion
	if recovery.Decode(io.LimitReader(f, 1<<20), &completion) != nil || manifest.ID != id || manifest != completion.Manifest || c.MatchManifest(manifest) != nil {
		return errors.New("authenticated inner backup manifest differs from requested download completion evidence")
	}
	return nil
}

func postgresBackupVerifyRunningCluster(ctx context.Context, target string) error {
	return postgresBackupVerifyRunningClusterWithExecutor(ctx, target, recovery.QuietExec)
}

func postgresBackupVerifyRunningClusterWithExecutor(ctx context.Context, target string, run recovery.Executor) (resultErr error) {
	pgdata := filepath.Join(target, "pgdata")
	var control bytes.Buffer
	if err := run(ctx, []string{"env", "LC_ALL=C", "pg_controldata", pgdata}, nil, &control); err != nil {
		return errors.New("cannot read verified PostgreSQL recovery parameters")
	}
	recoverySettings, err := postgresBackupRecoverySettings(control.String())
	if err != nil {
		return err
	}
	socket := filepath.Join(target, "socket")
	if err := os.Mkdir(socket, 0700); err != nil {
		return errors.New("cannot create isolated PostgreSQL socket")
	}
	hba := filepath.Join(target, "pg_hba.conf")
	if err := os.WriteFile(hba, []byte("local all postgres trust\n"), 0600); err != nil {
		return err
	}
	// A clean, operator-owned config prevents restored preload libraries,
	// subscriptions, archive commands and network listeners from contacting services.
	conf := filepath.Join(target, "postgresql.conf")
	settings := "listen_addresses = ''\nport = 6543\narchive_mode = off\nprimary_conninfo = ''\nrestore_command = ''\nshared_preload_libraries = ''\nmax_logical_replication_workers = 0\ndefault_transaction_read_only = on\nssl = off\n" + recoverySettings
	settings += "unix_socket_directories = '" + strings.ReplaceAll(socket, "'", "''") + "'\nhba_file = '" + strings.ReplaceAll(hba, "'", "''") + "'\n"
	if err := os.WriteFile(conf, []byte(settings), 0600); err != nil {
		return err
	}
	// postgresql.auto.conf overrides config_file; exclude source runtime overrides
	// after native verification and before the first isolated start.
	if err := os.WriteFile(filepath.Join(pgdata, "postgresql.auto.conf"), nil, 0600); err != nil {
		return err
	}
	argv := []string{"pg_ctl", "-D", pgdata, "-w", "-t", "120", "-l", filepath.Join(target, "postgres.log"), "-o", "-c config_file=" + conf, "start"}
	if err := run(ctx, argv, nil, io.Discard); err != nil {
		return errors.New("isolated PostgreSQL startup failed")
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if err := run(stop, []string{"pg_ctl", "-D", pgdata, "-m", "fast", "-w", "stop"}, nil, io.Discard); err != nil && resultErr == nil {
			resultErr = errors.New("isolated PostgreSQL shutdown failed")
		}
	}()
	query := func(db, sql string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, []string{"psql", "-X", "-h", socket, "-p", "6543", "-U", "postgres", "-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql}, nil, &out)
		return strings.TrimSpace(out.String()), err
	}
	if v, err := query("postgres", "SELECT pg_is_in_recovery();"); err != nil || v != "f" {
		return errors.New("restored cluster did not finish recovery")
	}
	for _, db := range []string{"rtk_account_manager", "rtk_billing", "video_cloud"} {
		v, err := query(db, "SELECT count(*) FROM pg_catalog.pg_class WHERE relkind IN ('r','p') AND relnamespace IN (SELECT oid FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema');")
		if err != nil || v == "0" || v == "" {
			return fmt.Errorf("restored %s schema validation failed", db)
		}
	}
	return postgresBackupCheckRestoredData(query)
}

func postgresBackupRecoverySettings(control string) (string, error) {
	if strings.Contains(control, "WARNING:") {
		return "", errors.New("PostgreSQL control data reported a warning")
	}
	keys := []struct{ field, parameter string }{{"max_connections", "max_connections"}, {"max_worker_processes", "max_worker_processes"}, {"max_wal_senders", "max_wal_senders"}, {"max_prepared_xacts", "max_prepared_transactions"}, {"max_locks_per_xact", "max_locks_per_transaction"}}
	values := map[string]string{}
	for _, line := range strings.Split(control, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	var settings strings.Builder
	for _, key := range keys {
		value, err := strconv.Atoi(values[key.field+" setting"])
		if err != nil || value < 0 || value > 262143 {
			return "", errors.New("invalid or missing PostgreSQL recovery parameter")
		}
		fmt.Fprintf(&settings, "%s = %d\n", key.parameter, value)
	}
	return settings.String(), nil
}

type postgresBackupDataCheck struct{ Name, Database, SQL, Expected string }

// Checks belong to the reviewed workspace release, never to an archive. Missing
// roles, relations or columns fail the drill instead of being interpreted as zero.
func postgresBackupDataChecks() []postgresBackupDataCheck {
	return []postgresBackupDataCheck{
		{"postgres-owner", "postgres", "SELECT rolcanlogin, rolsuper FROM pg_roles WHERE rolname = 'postgres'", "t|t"},
		{"backup-role", "postgres", "SELECT rolcanlogin, rolreplication, rolsuper FROM pg_roles WHERE rolname = 'rtk_postgres_backup'", "t|t|f"},
		{"account-membership", "rtk_account_manager", "SELECT count(*) FROM public.organization_members m LEFT JOIN public.organizations o ON o.id = m.organization_id LEFT JOIN public.users u ON u.id = m.user_id WHERE o.id IS NULL OR u.id IS NULL", "0"},
		{"billing-ledger", "rtk_billing", "SELECT count(*) FROM public.balance_ledger_entries l LEFT JOIN public.commercial_accounts a ON a.id = l.account_id WHERE a.id IS NULL OR l.currency <> a.currency OR l.amount_minor <= 0", "0"},
		{"video-device-data", "video_cloud", "SELECT count(*) FROM public.devices WHERE id IS NULL OR info IS NULL OR config IS NULL", "0"},
		{"account-outbox", "rtk_account_manager", "SELECT count(*) FROM public.device_message_outbox o LEFT JOIN public.device_operations d ON d.operation_id = o.operation_id WHERE d.operation_id IS NULL OR o.attempt_count < 0", "0"},
		{"video-presence-outbox", "video_cloud", "SELECT count(*) FROM public.device_presence_outbox WHERE generation <= 0 OR attempts < 0 OR status NOT IN ('online','offline')", "0"},
	}
}

func postgresBackupCheckRestoredData(query func(string, string) (string, error)) error {
	for _, check := range postgresBackupDataChecks() {
		value, err := query(check.Database, check.SQL)
		if err != nil || value != check.Expected {
			return fmt.Errorf("restored PostgreSQL check %s failed", check.Name)
		}
	}
	return nil
}

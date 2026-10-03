package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

const postgresBackupName = "rtk-postgres-backup"
const postgresBackupStatusName = "rtk-postgres-backup-status"

type postgresBackupDeployment struct {
	Version         int                   `json:"version"`
	Environment     string                `json:"environment"`
	Stack           string                `json:"stack"`
	Namespace       string                `json:"namespace"`
	RunnerImage     string                `json:"runner_image"`
	SourcePod       string                `json:"source_pod"`
	SourceContainer string                `json:"source_container"`
	SourcePVC       string                `json:"source_pvc"`
	ScratchStorage  string                `json:"scratch_storage"`
	StorageClass    string                `json:"storage_class,omitempty"`
	ImagePullSecret string                `json:"image_pull_secret,omitempty"`
	Schedule        string                `json:"schedule,omitempty"`
	TimeZone        string                `json:"time_zone,omitempty"`
	Worker          postgresbackup.Config `json:"worker"`
}

var postgresBackupDNSName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var postgresBackupDigest = regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`)

func readPostgresBackupDeployment(path, environment string) (postgresBackupDeployment, error) {
	var c postgresBackupDeployment
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("PostgreSQL backup config unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid PostgreSQL backup config: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("backup config must contain exactly one JSON object")
	}
	if c.Environment != environment {
		return c, errors.New("backup config does not match selected environment")
	}
	return c, c.validate()
}

func (c postgresBackupDeployment) validate() error {
	if c.Version != 1 {
		return errors.New("unsupported PostgreSQL backup deployment version")
	}
	for _, name := range []string{c.Environment, c.Stack, c.Namespace, c.SourcePod, c.SourceContainer, c.SourcePVC} {
		if !postgresBackupDNSName.MatchString(name) || len(name) > 63 {
			return errors.New("invalid PostgreSQL backup resource identity")
		}
	}
	if c.Namespace != c.Stack+"-platform" || c.SourcePod != "postgresql-0" || c.SourceContainer != "postgres" || c.SourcePVC != "data-postgresql-0" {
		return errors.New("daily backup supports only the reviewed platform PostgreSQL StatefulSet")
	}
	if !postgresBackupDigest.MatchString(c.RunnerImage) || !postgresBackupDigest.MatchString(c.Worker.Source.PostgresImage) {
		return errors.New("runner and source PostgreSQL images must be digest pinned")
	}
	if c.Worker.Environment != c.Environment || c.Worker.Stack != c.Stack {
		return errors.New("worker identity differs from deployment")
	}
	if c.Worker.Directory != "/backup" || c.Worker.Source.PasswordFile != "/run/postgres-backup/source-password" {
		return errors.New("worker must use the dedicated mounted scratch and password paths")
	}
	if c.Worker.Source.Host != "postgresql."+c.Namespace+".svc.cluster.local" || c.Worker.Source.User != "rtk_postgres_backup" || c.Worker.Source.Port != 5432 {
		return errors.New("worker must target the selected platform PostgreSQL service with its dedicated role")
	}
	if c.Worker.TimeoutSeconds != 14400 || c.Worker.RetentionDays != 14 || c.Worker.MinimumBackups < 14 {
		return errors.New("daily backup requires a four-hour timeout, fourteen-day retention and at least fourteen retained backups")
	}
	scratch, err := postgresBackupStorageBytes(c.ScratchStorage)
	if err != nil {
		return err
	}
	if c.Worker.MaxPlaintextBytes > scratch-(64<<20) || c.Worker.MaxArchiveBytes > scratch-(64<<20)-c.Worker.MaxPlaintextBytes {
		return errors.New("scratch storage must exceed plaintext plus encrypted archive limits and a 64MiB reserve")
	}
	if c.StorageClass != "" && !postgresBackupDNSName.MatchString(c.StorageClass) {
		return errors.New("invalid scratch storage class")
	}
	if c.ImagePullSecret != "" && !postgresBackupDNSName.MatchString(c.ImagePullSecret) {
		return errors.New("invalid image pull Secret reference")
	}
	if c.Worker.Source.SSLMode != "disable" || c.Worker.Source.RootCertFile != "" {
		return errors.New("this platform adapter uses the existing private PostgreSQL connection; TLS sources require a reviewed certificate-mount adapter")
	}
	fields := strings.Fields(c.schedule())
	if len(fields) != 5 || strings.Join(fields[2:], " ") != "* * *" {
		return errors.New("backup schedule must specify one daily minute and hour")
	}
	minute, me := strconv.Atoi(fields[0])
	hour, he := strconv.Atoi(fields[1])
	if me != nil || he != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		return errors.New("invalid daily backup schedule")
	}
	if _, err := time.LoadLocation(c.timeZone()); err != nil || c.timeZone() == "Local" {
		return errors.New("backup time_zone must be an explicit IANA time zone")
	}
	return c.Worker.Validate()
}

func (c postgresBackupDeployment) schedule() string {
	if c.Schedule != "" {
		return c.Schedule
	}
	return "0 3 * * *"
}
func (c postgresBackupDeployment) timeZone() string {
	if c.TimeZone != "" {
		return c.TimeZone
	}
	return "Asia/Shanghai"
}

func (c postgresBackupDeployment) imagePullSecret() string {
	if c.ImagePullSecret != "" {
		return c.ImagePullSecret
	}
	return "ghcr-pull"
}

func postgresBackupStorageBytes(value string) (int64, error) {
	if !strings.HasSuffix(value, "Gi") {
		return 0, errors.New("scratch_storage must be a whole number of GiB")
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(value, "Gi"), 10, 64)
	if err != nil || n < 60 || n > 16384 {
		return 0, errors.New("scratch_storage must be between 60Gi and 16384Gi")
	}
	return n << 30, nil
}

type postgresBackupManager struct {
	Config           postgresBackupDeployment
	Store            secretStore
	Kubeconfig       string
	Exec             recovery.Executor
	RecordDrill      func(context.Context, postgresbackup.Config, postgresbackup.Drill) error
	ReadRemoteStatus func(context.Context, postgresbackup.Config) (postgresbackup.Status, error)
}

func (m *postgresBackupManager) kube(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	run := m.Exec
	if run == nil {
		run = recovery.QuietExec
	}
	argv := []string{lkeKubectl(), "--kubeconfig", m.Kubeconfig, "--request-timeout=45s"}
	var out bytes.Buffer
	if err := run(ctx, append(argv, args...), input, &out); err != nil {
		return nil, errors.New("PostgreSQL backup Kubernetes operation failed; sensitive subprocess output suppressed")
	}
	return out.Bytes(), nil
}

func (m *postgresBackupManager) apply(ctx context.Context, objects ...map[string]any) error {
	for _, obj := range objects {
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		if _, err = m.kube(ctx, bytes.NewReader(b), "apply", "-f", "-"); err != nil {
			return err
		}
	}
	return nil
}

func runPostgresBackup(args []string) error  { return runPostgresBackupCLI(false, args) }
func runPostgresRestore(args []string) error { return runPostgresBackupCLI(true, args) }

func runPostgresBackupCLI(restore bool, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("postgres-backup: plan | configure | run | status | retry-upload | prune --dry-run")
		fmt.Println("postgres-restore: drill | cleanup (isolated same-environment namespace only)")
		fmt.Println("Required: --environment ENV --config FILE. Mutations require --confirm-environment ENV --confirm-stack STACK.")
		fmt.Println("configure defaults to a suspended schedule; --enable-schedule enables daily 03:00 Asia/Shanghai backups.")
		return nil
	}
	action := args[0]
	allowed := map[string]bool{"plan": true, "configure": true, "run": true, "status": true, "retry-upload": true, "prune": true}
	if restore {
		allowed = map[string]bool{"drill": true, "cleanup": true}
	}
	if !allowed[action] {
		return errors.New("unsupported PostgreSQL backup/restore action")
	}
	fs := flag.NewFlagSet("postgres-backup "+action, flag.ContinueOnError)
	environment := fs.String("environment", "", "logical environment")
	configPath := fs.String("config", "", "reviewed PostgreSQL backup JSON")
	configRoot := fs.String("config-root", "", "SecretStore config root")
	kubeconfig := fs.String("kubeconfig", "", "environment kubeconfig")
	confirmEnv := fs.String("confirm-environment", "", "explicit mutation target")
	confirmStack := fs.String("confirm-stack", "", "explicit mutation target")
	enable := fs.Bool("enable-schedule", false, "enable the reviewed daily schedule")
	dryRun := fs.Bool("dry-run", false, "show retention candidates without deletion")
	id := fs.String("id", "", "backup id for retry or restore")
	drillID := fs.String("drill-id", "", "isolated drill resource suffix")
	identity := fs.String("identity", "", "private age identity file for isolated drill")
	qualification := fs.String("qualification", "", "reviewed staging performance evidence required to enable schedule")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" || *configPath == "" {
		return errors.New("explicit --environment and --config required; unexpected positional arguments rejected")
	}
	cfg, err := readPostgresBackupDeployment(*configPath, *environment)
	if err != nil {
		return err
	}
	if action == "plan" {
		b, _ := json.MarshalIndent(postgresBackupPlan(cfg), "", "  ")
		fmt.Println(string(b))
		return nil
	}
	store, err := newSecretStore(*configRoot, *environment)
	if err != nil {
		return err
	}
	if *kubeconfig == "" {
		*kubeconfig = store.KubeconfigPath()
	}
	m := postgresBackupManager{Config: cfg, Store: store, Kubeconfig: *kubeconfig}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if action == "status" {
		status, err := postgresBackupReadStatus(ctx, &m)
		if err != nil {
			return err
		}
		if status.Version == 0 {
			return errors.New("PostgreSQL backup status is not configured")
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	if action == "prune" && *dryRun {
		restoreEnv, err := postgresBackupRemoteEnvironment(store, false)
		if err != nil {
			return err
		}
		defer restoreEnv()
		plan, err := postgresbackup.Prune(ctx, cfg.Worker, true)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	if *confirmEnv != cfg.Environment || *confirmStack != cfg.Stack {
		return errors.New("confirm-environment and confirm-stack must exactly match the reviewed config")
	}
	switch action {
	case "configure":
		return m.configure(ctx, *enable, *qualification)
	case "run", "retry-upload", "prune":
		if action == "retry-upload" && !postgresBackupDNSName.MatchString(*id) {
			return errors.New("retry-upload requires a valid --id")
		}
		return m.createJob(ctx, action, *id)
	case "drill":
		return m.drill(ctx, *id, *drillID, *identity)
	case "cleanup":
		return m.cleanupDrill(ctx, *drillID)
	}
	return errors.New("unsupported action")
}

func postgresBackupPlan(c postgresBackupDeployment) map[string]any {
	return map[string]any{"version": 1, "environment": c.Environment, "stack": c.Stack, "source_host": c.Worker.Source.Host, "source_pvc": c.SourcePVC, "scratch_pvc": postgresBackupName + "-scratch", "scratch_storage": c.ScratchStorage, "schedule": c.schedule(), "time_zone": c.timeZone(), "schedule_initially_suspended": true, "worker_image": c.RunnerImage, "cpu_limit": "500m", "memory_limit": "512Mi", "timeout_seconds": 14400, "retention_days": 14, "replication_role": c.Worker.Source.User, "restart_required": false, "scope": "whole-postgresql-cluster; isolated restore drill; no application cutover"}
}

func postgresBackupRemoteEnvironment(store secretStore, readonly bool) (func(), error) {
	prefix := "RTK_POSTGRES_BACKUP_"
	if readonly {
		prefix = "RTK_POSTGRES_RESTORE_"
	}
	values := map[string]string{}
	for _, name := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		v, err := store.read(filepath.Join("operator", "env", prefix+name))
		if err != nil || v == "" {
			return nil, fmt.Errorf("dedicated %s%s credential unavailable", prefix, name)
		}
		values["RTK_BACKUP_"+name] = v
	}
	old := map[string]*string{}
	for key, value := range values {
		if previous, ok := os.LookupEnv(key); ok {
			v := previous
			old[key] = &v
		}
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	return func() {
		for key := range values {
			if previous := old[key]; previous != nil {
				_ = os.Setenv(key, *previous)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}, nil
}

func postgresBackupRandomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func postgresBackupLabels(c postgresBackupDeployment) map[string]string {
	return map[string]string{"app.kubernetes.io/name": postgresBackupName, "rtk.realtek.com/stack": c.Stack, "rtk.realtek.com/environment": c.Environment}
}
func postgresBackupMetadata(c postgresBackupDeployment, name string) map[string]any {
	return map[string]any{"name": name, "namespace": c.Namespace, "labels": postgresBackupLabels(c)}
}

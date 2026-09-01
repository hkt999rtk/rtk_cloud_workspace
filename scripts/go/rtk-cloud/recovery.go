package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func runBackup(args []string) error  { return runRecovery("backup", args) }
func runRestore(args []string) error { return runRecovery("restore", args) }

// Local cooperating CLI guard. External controllers/direct kubectl are fenced
// by operator RBAC/network controls; a local file is not a cluster-wide policy.
func recoveryMutationGuard(args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "backup", "restore", "docs-check", "contracts-check", "status-all", "test-catalog", "test-inventory", "test-spec-inventory", "test-spec-impact", "test-coverage", "test-coverage-aggregate", "test-feature-coverage":
		return nil
	}
	if len(args) > 1 && args[0] == "secrets" && (args[1] == "verify" || args[1] == "inventory" || args[1] == "plan") {
		return nil
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return nil
		}
	}
	root, err := defaultRTKCloudConfigRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		for _, name := range []string{"recovery-state.json", "recovery-command.lock"} {
			if _, err := os.Lstat(filepath.Join(root, entry.Name(), name)); err == nil {
				return errors.New("maintenance is active in local SecretStore; use backup/restore status, verify, resume or abort before other mutations")
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func runRecovery(command string, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("backup: plan | preflight | create | upload | fetch | status | verify | resume | abort")
		fmt.Println("restore: inspect | apply | retry | status | verify | resume")
		fmt.Println("Required: --environment ENV --config FILE. Mutations also require --confirm-environment ENV --confirm-stack STACK.")
		fmt.Println("Data options: --file FILE.age --identity PRIVATE_AGE_KEY_FILE --id BACKUP_ID --reconciliation FILE.json")
		fmt.Println("See docs/backup-restore.md. No live action is performed by plan or inspect.")
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet(command+" "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	environment := fs.String("environment", "", "logical environment")
	configPath := fs.String("config", "", "reviewed recovery JSON inventory")
	configRoot := fs.String("config-root", "", "SecretStore config root")
	kubeconfig := fs.String("kubeconfig", "", "target kubeconfig (default: environment SecretStore)")
	confirmEnv := fs.String("confirm-environment", "", "explicit mutation target")
	confirmStack := fs.String("confirm-stack", "", "explicit mutation target")
	file := fs.String("file", "", "encrypted backup file")
	identity := fs.String("identity", "", "private age identity file")
	id := fs.String("id", "", "backup id for upload/fetch")
	evidence := fs.String("reconciliation", "", "approved restore reconciliation evidence")
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New("invalid recovery arguments; use backup --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected recovery arguments")
	}
	allowed := map[string]map[string]bool{
		"backup":  {"plan": true, "preflight": true, "create": true, "upload": true, "fetch": true, "status": true, "verify": true, "resume": true, "abort": true},
		"restore": {"inspect": true, "apply": true, "retry": true, "status": true, "verify": true, "resume": true},
	}
	if !allowed[command][action] {
		return errors.New("unsupported recovery action")
	}
	if *environment == "" || *configPath == "" {
		return errors.New("explicit --environment and --config required")
	}
	f, err := os.Open(*configPath)
	if err != nil {
		return errors.New("recovery config unavailable")
	}
	var cfg recovery.Config
	err = recovery.Decode(io.LimitReader(f, 1<<20), &cfg)
	f.Close()
	if err != nil {
		return err
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	if cfg.Environment != *environment {
		return errors.New("config does not belong to requested environment")
	}
	store, err := newSecretStore(*configRoot, *environment)
	if err != nil {
		return err
	}
	if *kubeconfig == "" {
		*kubeconfig = store.KubeconfigPath()
	}
	engine := recovery.Engine{Config: cfg, SecretRoot: store.Root, Kubeconfig: *kubeconfig, Kubectl: lkeKubectl()}
	if err := engine.ValidateLayout(); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch action {
	case "plan":
		fmt.Printf("Core recovery plan: environment=%s stack=%s components=%d workloads=%d\n", cfg.Environment, cfg.Stack, len(cfg.Components), len(cfg.Workloads))
		fmt.Println("Maintenance required. Core data only; media/firmware payloads, external audit and external side effects are excluded.")
		for _, c := range cfg.Components {
			fmt.Printf("  %s: %s\n", c.ID, c.Kind)
		}
		return nil
	case "inspect":
		m, err := engine.Inspect(ctx, *file, *identity)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ID          string `json:"backup_id"`
			Environment string `json:"environment"`
			Stack       string `json:"stack"`
			Scope       string `json:"scope"`
			Artifacts   int    `json:"artifacts"`
		}{m.ID, m.Environment, m.Stack, m.Scope, len(m.Artifacts)})
	case "status":
		j, err := engine.Load(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(j)
	}
	if *confirmEnv != cfg.Environment || *confirmStack != cfg.Stack {
		return errors.New("confirm-environment and confirm-stack must exactly match reviewed config")
	}
	// Preflight executes operator-owned checks; require confirmation even though
	// built-in inventory queries themselves are read-only.
	if action == "preflight" {
		_, err := engine.Preflight(ctx)
		return err
	}
	if err = recovery.PrivateDirectory(cfg.Directory); err != nil {
		return err
	}
	if action == "fetch" {
		path, err := recovery.Download(ctx, cfg, *id, cfg.Directory)
		if err == nil {
			fmt.Println(path)
		}
		return err
	}
	if action == "upload" {
		if !recovery.Name.MatchString(*id) {
			return errors.New("valid --id required")
		}
		expected := filepath.Join(cfg.Directory, *id+".age")
		if *file != "" && *file != expected {
			return errors.New("upload only accepts the named backup in configured directory")
		}
		return recovery.Upload(ctx, cfg, *id, expected)
	}
	release, err := engine.InvocationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	switch action {
	case "create":
		id, err := engine.Create(ctx)
		if id != "" {
			fmt.Println("Backup operation:", id)
		}
		return err
	case "apply":
		id, err := engine.Restore(ctx, *file, *identity)
		if id != "" {
			fmt.Println("Restore operation:", id)
		}
		return err
	case "retry":
		return engine.Reapply(ctx, *file, *identity)
	case "verify":
		return engine.Verify(ctx)
	case "resume":
		return engine.Resume(ctx, *evidence)
	case "abort":
		return engine.Abort(ctx)
	}
	return errors.New("unsupported recovery action")
}

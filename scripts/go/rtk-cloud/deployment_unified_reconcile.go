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
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentReconciliationAPI interface {
	environmentExecutionAPI
	deploymentplan.Backend
}

func runEnvironmentExecutionReconcile(args []string) error {
	fs := flag.NewFlagSet("deployment reconcile-execution", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "selected maintained environment")
	workspace := fs.String("workspace", "", "workspace root")
	environmentRoot := fs.String("environment-root", "", "maintained environment root")
	sha := fs.String("plan-sha256", "", "original reviewed execution plan SHA-256")
	confirm := fs.String("confirm", "", "exact environment stack confirmation")
	timeout := fs.Duration("timeout", 10*time.Minute, "outcome verification deadline")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return exitCode(2)
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || len(*sha) != 64 || strings.Trim(*sha, "0123456789abcdef") != "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "error: valid --environment, original --plan-sha256 and positive --timeout are required")
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil || *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		fmt.Fprintln(os.Stderr, "error: maintained configuration and exact --confirm stack are required")
		return exitCode(2)
	}
	store, err := newSecretStore("", *environment)
	if err != nil || verifySecretStorePermissionsOnly(store) != nil {
		fmt.Fprintln(os.Stderr, "error: original private environment SecretStore is unavailable")
		return exitCode(1)
	}
	raw, err := readSensitiveFile(store.KubeconfigPath(), "selected Kubernetes identity")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: selected private Kubernetes identity is unavailable")
		return exitCode(1)
	}
	backend, err := newEnvironmentKubernetesBackend(raw, *environment)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: selected Kubernetes identity cannot be frozen")
		return exitCode(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	outcome, err := reconcileEnvironmentExecution(ctx, backend, store.Root, *environment, cfg.Values["CLOUD_STACK_NAME"], *sha)
	if err != nil {
		fmt.Fprintln(os.Stdout, "NO-GO deployment.execution.reconciliation")
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			fmt.Fprintf(os.Stdout, "  cause: %s\n  next: %s\n", blocker.Cause, blocker.Next)
		} else {
			fmt.Fprintln(os.Stdout, "  cause: original execution ownership or outcome cannot be verified\n  next: inspect the original private journal; preserve uncertain writes and concurrent owners, and qualify separate recovery")
		}
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "RECONCILED environment=%s plan-sha256=%s outcome=%s\n", *environment, *sha, outcome)
	fmt.Fprintln(os.Stdout, "No resource rollback was performed. Repeat complete preflight for the selected release; reconciliation is not deployment GO.")
	return nil
}

func readExecutionJSON(path string, value any) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("original private execution evidence unavailable")
	}
	f := os.NewFile(uintptr(fd), "execution-evidence")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<20 {
		return errors.New("bounded private regular execution evidence required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 {
		return errors.New("execution evidence exceeds the private bound")
	}
	return decodeEnvironmentJSON(raw, value)
}

func reconcileEnvironmentExecution(ctx context.Context, api environmentReconciliationAPI, root, environment, stack, sha string) (string, error) {
	if !filepath.IsAbs(root) || !secretEnvironmentPattern.MatchString(environment) || stack != "video-cloud-"+environment || len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return "", errors.New("exact environment and reviewed plan required")
	}
	active := filepath.Join(root, "deployment", ".execution-active")
	directory := filepath.Join(root, "deployment", "rollback", sha)
	for _, path := range []string{root, filepath.Join(root, "deployment"), filepath.Join(root, "deployment", "rollback"), directory, active} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("original private execution layout required")
		}
	}
	running, err := deploymentplan.LockExecutionOwner(active, false)
	if err != nil {
		return "", &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "original execution process is running or its lock cannot be verified", Next: "stop the original controller and reconcile using its original SecretStore; never copy the fence or steal a live owner"}
	}
	defer running.Close()
	var owner deploymentplan.ExecutionOwner
	if err := readExecutionJSON(filepath.Join(active, "owner.json"), &owner); err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	leaseID := "Lease/" + stack + "-video-cloud/rtk-deployment-execution"
	if err != nil || owner.Hostname == "" || owner.Hostname != hostname || owner.PlanSHA256 != sha || owner.Environment != environment || owner.Stack != stack || owner.Lease.ResourceID != leaseID || owner.Lease.UID == "" || !strings.HasPrefix(owner.Lease.Holder, sha+":") {
		return "", errors.New("original controller, environment and exact lease receipt required")
	}
	var plan deploymentplan.Plan
	if err := readExecutionJSON(filepath.Join(directory, "plan.json"), &plan); err != nil {
		return "", err
	}
	if plan.SHA256() != sha || plan.Environment != environment || plan.Stack != stack {
		return "", errors.New("original reviewed plan does not match execution owner")
	}
	var journal deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(directory, "journal.json"), &journal); err != nil {
		return "", err
	}
	journalLock := filepath.Join(directory, ".active")
	if info, err := os.Lstat(journalLock); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("original private journal lock required")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("original private journal lock cannot be inspected")
	}
	if entries, err := os.ReadDir(journalLock); err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		return "", errors.New("unexpected private journal lock content; preserve evidence")
	}
	checkLease := func() (bool, error) {
		current, err := api.Read(ctx, leaseID)
		if err != nil {
			return false, errors.New("original cluster lease cannot be inspected")
		}
		if current == nil {
			return false, nil
		}
		if deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != owner.Lease.UID || deploymentplan.Map(current["spec"])["holderIdentity"] != owner.Lease.Holder || deploymentplan.Map(deploymentplan.Map(current["metadata"])["annotations"])["rtk.realtek.com/plan-sha256"] != sha {
			return false, &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "cluster execution belongs to another owner", Next: "preserve the concurrent lease; reconcile its own original journal separately"}
		}
		return true, nil
	}
	if _, err := checkLease(); err != nil {
		return "", err
	}
	outcome, err := plan.ReconcileOutcome(ctx, api, journal)
	if err != nil {
		return "", err
	}
	// Preserve every attempt rather than overwriting an earlier verification or
	// deleting the stopped controller's private owner evidence.
	attempt, err := os.MkdirTemp(directory, "reconciled-")
	if err != nil {
		return "", errors.New("private reconciliation receipt cannot be prepared")
	}
	receipt := struct {
		PlanSHA256, Outcome, JournalSHA256 string
		Lease                              deploymentplan.LeaseOwner
		VerifiedAt                         time.Time
	}{sha, outcome, deploymentplan.Digest(journal), owner.Lease, time.Now().UTC()}
	encoded, err := json.Marshal(receipt)
	if err != nil || writeExclusiveEnvironmentRelease(filepath.Join(attempt, "verification.json"), encoded) != nil {
		return "", errors.New("private reconciliation verification cannot be persisted")
	}
	// Repeat the complete outcome immediately before the conditional lease
	// deletion. No resource write or automatic rollback occurs in this command.
	if repeated, err := plan.ReconcileOutcome(ctx, api, journal); err != nil || repeated != outcome {
		return "", errors.New("execution outcome changed during reconciliation")
	}
	occupied, err := checkLease()
	if err != nil {
		return "", err
	}
	if occupied {
		lease := &environmentExecutionLease{api, leaseID, owner.Lease.UID, owner.Lease.Holder}
		if err := lease.Release(ctx); err != nil {
			return "", err
		}
	}
	if occupied, err := checkLease(); err != nil || occupied {
		return "", errors.New("cluster lease release cannot be confirmed")
	}
	if err := os.Remove(journalLock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("private journal lock changed; preserve the local fence")
	}
	if err := os.Rename(active, filepath.Join(attempt, "owner")); err != nil {
		return "", errors.New("original local fence could not be archived")
	}
	if err := writeExclusiveEnvironmentRelease(filepath.Join(attempt, "released.json"), encoded); err != nil {
		return "", &deploymentplan.Blocker{ID: "deployment.execution.reconciliation", Cause: "fences released but completion receipt could not be persisted", Next: "inspect the retained private reconciliation verification; repeat complete preflight before any new execution"}
	}
	return outcome, nil
}

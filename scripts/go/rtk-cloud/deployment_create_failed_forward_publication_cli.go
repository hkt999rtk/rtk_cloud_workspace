package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func parseFailedForwardPublicationArgs(args []string) (string, int64, error) {
	environment, pull, _, err := parseFailedForwardPublicationSelection(args)
	return environment, pull, err
}

func parseFailedForwardPublicationSelection(args []string) (string, int64, string, error) {
	fs := flag.NewFlagSet("record-create-repair-publication", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	environment := fs.String("environment", "", "selected environment")
	pull := fs.Int64("pull-request", 0, "merged repair PR number")
	purpose := fs.String("purpose", "", "fixed repair tool purpose")
	if err := fs.Parse(args); err != nil {
		return "", 0, "", err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || *pull <= 0 {
		return "", 0, "", createProviderRecoveryBlock("select an environment and the merged repair PR number")
	}
	if _, err := failedForwardPublicationFile(*purpose); err != nil {
		return "", 0, "", err
	}
	return *environment, *pull, *purpose, nil
}

// openCreateExecutionForPublication acquires only the original generation's
// existing process lock. It never creates, releases or replaces its fence.
func openCreateExecutionForPublication(ctx context.Context, store secretStore, checkpoint createRecoveryCheckpoint) (*environmentCreateExecution, error) {
	bad := func() (*environmentCreateExecution, error) {
		return nil, createProviderRecoveryBlock("original selected create owner cannot be held and verified")
	}
	fence := filepath.Join(store.Root, "deployment", ".execution-active")
	if validateEnvironmentPreflightDirectories(store, fence) != nil {
		return bad()
	}
	lock, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return bad()
	}
	e := &environmentCreateExecution{versionState: &createOperatorVersionState{}, Store: store, running: lock, owner: checkpoint.Owner, Journal: checkpoint.Journal}
	ok := false
	defer func() {
		if !ok {
			_ = e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return bad()
	}
	e.runningInfo, err = lock.Stat()
	if err != nil {
		return bad()
	}
	e.JournalPath, err = privateDeploymentStatePath(store.Environment, e.owner.Journal)
	if err != nil {
		return bad()
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if e.Check(ctx) != nil {
		return bad()
	}
	ok = true
	return e, nil
}

// The PR/CI automation invokes this after a merge; only the PR number is
// passed. The original parent and exact successful Release are discovered and
// independently verified by the command, then written to the private journal.
func runRecordCreateRepairPublication(args []string) error {
	environment, pull, purpose, err := parseFailedForwardPublicationSelection(args)
	if err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, environment, "")
	if err != nil {
		return err
	}
	store, _, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, err := openCreateExecutionForPublication(ctx, store, checkpoint)
	if err != nil {
		return err
	}
	defer e.Close()
	evidence, err := publishFailedForwardToolFromPRForPurpose(ctx, store, e, pull, environmentCIImageCommand(cfg), time.Now().UTC(), purpose)
	if err != nil {
		return err
	}
	label := "public-crl"
	if purpose != "" {
		label = purpose
	}
	fmt.Fprintf(os.Stdout, "PASS environment=%s repair=%s publication-run=%d parent=%s\n", environment, label, evidence.RunID, evidence.Parent)
	return nil
}

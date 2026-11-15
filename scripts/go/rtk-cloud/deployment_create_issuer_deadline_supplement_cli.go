package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The explicit operation only records an extension of the original execution
// window. It does not mutate the environment, credentials, or workloads.
func runCreateIssuerDeadlineSupplement(args []string) error {
	fs := flag.NewFlagSet("extend-create-issuer-deadline", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	review := fs.String("review-sha256", "", "original issuer continuation review")
	total := fs.Duration("total-timeout", 0, "total duration from the original create continuation start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || !environmentPreflightDigest.MatchString(*review) || *total <= 0 {
		return createProviderRecoveryBlock("select an environment, original issuer review, and positive total timeout")
	}
	root, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(root, *env, "")
	if err != nil {
		return err
	}
	store, _, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil || !environmentPreflightDigest.MatchString(checkpoint.Owner.PlanSHA256) {
		return createProviderRecoveryBlock("original create generation is unavailable")
	}
	e, _, err := loadCreateRecoveryExecutionOwner(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(checkpoint.Journal) {
		return createProviderRecoveryBlock("original create generation changed while acquiring its execution owner")
	}
	original, ok, err := initialSuccessorContinuationEntry(store, e)
	if err != nil || !ok {
		return createProviderRecoveryBlock("original issuer continuation entry is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	deadline, err := recordIssuerDeadlineSupplement(ctx, store, e, *review, original, *total, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("Extended original create continuation: environment=%s started=%s deadline=%s total=%s\n", *env, original.StartedAt.UTC().Format(time.RFC3339), deadline.UTC().Format(time.RFC3339), *total)
	return nil
}

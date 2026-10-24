package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Capture is local preparation only. It acquires the original parent owner but
// performs no Kubernetes request, stage execution or new admission.
func runCreateNormalTailRuntimeSourceCapture(args []string) error {
	fs := flag.NewFlagSet("deployment capture-runtime-configuration-source", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected original environment")
	workspace := fs.String("workspace", "", "workspace root")
	environmentRoot := fs.String("environment-root", "", "maintained environment root")
	parent := fs.String("parent-plan-sha256", "", "original create parent")
	confirm := fs.String("confirm", "", "exact original stack")
	readOnly := fs.Bool("read-only", false, "qualify the original snapshot without publishing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !environmentPreflightDigest.MatchString(*parent) {
		return createNormalTailRuntimeBlock("capture arguments")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil {
		return err
	}
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return createNormalTailRuntimeBlock("capture stack confirmation")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e, err := loadCreateNormalTailExecution(ctx, cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	admission, err := readCreateNormalTailProgramAdmission(store, e)
	if err != nil || !time.Now().Before(admission.Deadline) {
		return createNormalTailRuntimeBlock("same current execution window")
	}
	var source *createNormalTailRuntimeSource
	if *readOnly {
		source, err = qualifyCreateNormalTailRuntimeSource(ctx, cfg, store, e)
	} else {
		source, err = captureCreateNormalTailRuntimeSource(ctx, cfg, store, e)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"source_sha256": deploymentplan.Digest(source), "source_path": createNormalTailRuntimeSourcePath(e), "previous_plan_sha256": source.PreviousPlanSHA256, "previous_journal_sha256": source.PreviousJournalSHA256, "deadline": admission.Deadline, "native_requests": 0, "captured_original_inputs": true})
}

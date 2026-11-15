package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This command initializes public signed caches only. It opens the existing
// execution and original native DB/custody authority even if CertIssuer is down.
func runCreateOpenBaoPublicCRLRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-openbao-public-crl", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	action := fs.String("action", "preflight", "preflight or create")
	review := fs.String("plan-sha256", "", "exact reviewed producer qualification")
	confirm := fs.String("confirm", "", "selected stack for execution")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || *action != "preflight" && *action != "create" || *action == "create" && (!environmentPreflightDigest.MatchString(*review) || *confirm != "video-cloud-"+*environment) || *action == "preflight" && *review != "" {
		return openBaoPublicCRLBlock("official-command-scope")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *environment, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	e, artifact, err := loadCreateRecoveryExecutionOwner(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return err
	}
	defer e.Close()
	parent, err := authenticatedCreateOperatorPlan(store, e)
	if err != nil || parent.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
		return openBaoPublicCRLBlock("original-configuration")
	}
	if _, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, parent); err != nil {
		return err
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != artifact.Plan.KubeconfigSHA256 {
		return openBaoPublicCRLBlock("original-kubernetes-identity")
	}
	native, err := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
	if err != nil {
		return err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native}
	return runEnvironmentProgress(ctx, os.Stdout, "deployment.create.openbao-public-crl."+*action, func(ctx context.Context) error {
		var q *createOpenBaoPublicCRLQualification
		var selected string
		if *action == "preflight" {
			q, selected, err = prepareOpenBaoPublicCRLRecoveryQualification(ctx, cfg, store, parent, runtime)
			if err != nil {
				return err
			}
		} else {
			q, selected, err = readSelectedOpenBaoPublicCRL(store, e)
			if err != nil || q == nil || selected != *review {
				return openBaoPublicCRLBlock("selected-frozen-producer")
			}
		}
		if *action == "create" {
			if err := executeOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, selected, os.Stdout); err != nil {
				return err
			}
		} else if err := verifyOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, selected); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "PASS environment=%s scope=openbao-public-crl action=%s plan=%s source=%s helper=%s runtime-images-changed=false\n", cfg.Environment, *action, selected, q.SourcePlanSHA, q.Tool.Image.Reference)
		return nil
	})
}

// Completed publication remains observable after the original core child has
// legitimately advanced. Incomplete repair still requires that exact failed
// source; no source reconstruction or new qualification occurs on this branch.
func prepareOpenBaoPublicCRLRecoveryQualification(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime) (*createOpenBaoPublicCRLQualification, string, error) {
	e := runtime.Execution
	q, review, err := readSelectedOpenBaoPublicCRL(store, e)
	if err != nil {
		return nil, "", err
	}
	completed := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+openBaoPublicCRLStage)
	if q != nil && completed != nil && completed.State == "completed" {
		return q, review, nil // The caller authenticates through the common verifier.
	}
	source, err := readEnvironmentCreateStageInput(store, e, "managed-complete-issuer")
	if err != nil || source == nil {
		return nil, "", openBaoPublicCRLBlock("original-failed-core-input")
	}
	journal, exists, err := source.ReadExecution(store.Root)
	if err != nil || !exists || journal.Accepted || journal.Failed == "" || journal.InFlight != "" {
		return nil, "", openBaoPublicCRLBlock("original-stopped-core-journal")
	}
	var core []deploymentplan.Definition
	for _, r := range source.Resources {
		core = append(core, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
	}
	head, _, err := sessionLineageHead(store, e)
	if err != nil || head == nil {
		return nil, "", openBaoPublicCRLBlock("original-session-lineage")
	}
	service := environmentCreateServiceCeremony{BootstrapSessionID: head.Renewal.RenewedIntent.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + head.Renewal.RenewedIntent.SessionID}
	return prepareOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, service, core)
}

// Fresh creation uses the same frozen helper/plan. Missing public cache records
// are planned native outputs, never future installation acknowledgments.
func qualifyEnvironmentCreateOpenBaoPublicCRLs(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, core []deploymentplan.Definition) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	q, review, err := prepareOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, service, core)
	if err != nil {
		return err
	}
	return verifyOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, review)
}

func ensureEnvironmentCreateOpenBaoPublicCRLs(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, core []deploymentplan.Definition) error {
	return ensureEnvironmentCreateOpenBaoPublicCRLsWithObservations(ctx, cfg, store, parent, runtime, service, core, nil)
}

func ensureEnvironmentCreateOpenBaoPublicCRLsWithObservations(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, core []deploymentplan.Definition, observations []openBaoPublicCRLObservations) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	q, review, err := readSelectedOpenBaoPublicCRL(store, runtime.Execution)
	if err != nil {
		return err
	}
	if q == nil && openBaoPublicCRLFreshParent(parent, runtime.Execution) {
		q, review, err = prepareOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, service, core, observations...)
		if err != nil {
			return err
		}
	}
	if q == nil {
		return openBaoPublicCRLBlock("preflight-required-before-managed-consumers")
	}
	purposes, err := originalOpenBaoPublicCRLPurposes(cfg.Environment, parent.Stack, core)
	if err != nil || deploymentplan.Digest(purposes) != deploymentplan.Digest(q.Purposes) {
		return openBaoPublicCRLBlock("remaining-original-public-purposes")
	}
	return executeOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, review, os.Stdout, observations...)
}

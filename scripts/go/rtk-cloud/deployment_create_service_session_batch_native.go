package main

import (
	"context"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// nativeServiceSessionBatchExecution connects the admitted batch to the
// original native renewal and registry reads. The caller supplies the
// separately qualified original OpenBao host readiness proof.
func nativeServiceSessionBatchExecution(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, parent *environmentCreatePlan, native environmentKubernetesBackend, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, q secondSessionAdmission, review string, readyOpenBao func(context.Context) error, out io.Writer) (serviceSessionBatchExecution, error) {
	bad := func() (serviceSessionBatchExecution, error) {
		return serviceSessionBatchExecution{}, createProviderRecoveryBlock("native Service session batch requires its original execution and OpenBao owner")
	}
	if ctx == nil || e == nil || parent == nil || out == nil || readyOpenBao == nil || runtime.Execution != e || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || parent.Environment != cfg.Environment || parent.Stack != e.Journal.Stack || deploymentplan.Digest(q) != review || !validSecondSessionAdmission(store, e, q) || !validServiceSessionBatches(q) {
		return bad()
	}
	recovery, err := restoreSecondSessionRuntime(ctx, store, e, q, review, service, openBaoRoot, *parent)
	if err != nil {
		return bad()
	}
	runtime.ServiceSessionRecovery = recovery
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
		// The runtime stage name differs from the qualification binding. Keep
		// the admitted Job plan rather than compiling a different child digest.
		if stage == "service-session-original-key-renewal-"+review {
			candidate := recovery.RenewalCandidate
			if candidate == nil || len(candidate.Resources) != 1 || len(definitions) != 1 || definitions[0].Phase != candidate.Resources[0].Phase || deploymentplan.ID(definitions[0].Object) != candidate.Resources[0].ID || deploymentplan.Digest(deploymentplan.Projection(definitions[0].Object)) != deploymentplan.Digest(deploymentplan.Projection(candidate.Resources[0].Desired)) {
				return createProviderRecoveryBlock("native Service session renewal differs from its qualified original Job")
			}
			return executeCompiledEnvironmentCreateStage(ctx, cfg, store, parent, e, stage, candidate, native, out)
		}
		return applyEnvironmentCreateStage(ctx, cfg, store, parent, e, stage, definitions, out)
	}
	return serviceSessionBatchExecution{
		Backend: backend,
		Ready:   readyOpenBao,
		VerifyHistory: func(ctx context.Context) error {
			if _, err := verifyServiceSessionHistoricalJobs(ctx, store, e, backend, q); err != nil {
				return err
			}
			return verifyServiceSessionReferenceSource(store, e, q)
		},
		Mint: func(ctx context.Context) error {
			_, err := executeManagedSessionRecovery(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, parent.Bootstrap, store, runtime, apply)
			if err != nil {
				return err
			}
			return verifyServiceSessionMintCustodyCompleted(ctx, store, e, backend, q, review)
		},
		Pending: func(ctx context.Context, batch serviceSessionBatch) error {
			for _, item := range batch.Pending {
				if item.Reason == "not-written" {
					continue
				}
				if item.Reason != "non-deployment" || item.ID != "StatefulSet/"+parent.Stack+"-secrets/openbao" {
					return createProviderRecoveryBlock("Service session pending owner requires its own original native handoff")
				}
				if err := readyOpenBao(ctx); err != nil {
					return err
				}
				if err := executeServiceSessionReference(ctx, store, e, backend, q, out); err != nil {
					return err
				}
			}
			return nil
		},
		Accept: func(ctx context.Context, p *deploymentplan.Plan) error {
			objects, err := native.TargetWorkloadInventory(ctx, p)
			if err != nil {
				return err
			}
			return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
				return environmentRuntimeImageDigests(ctx, store, image)
			})
		},
		VerifyACKs: func(ctx context.Context) (managedSessionState, error) {
			var observed managedSessionState
			err := runEnvironmentProgress(ctx, out, "deployment.recovery.service-session-acks", func(ctx context.Context) error {
				if err := observeSecondSessionSuccessor(ctx, cfg, store, e, native, *parent, service, openBaoRoot, review, time.Now().UTC()); err != nil {
					return err
				}
				if native.RolloutTimeout > 0 {
					bounded, cancel := context.WithTimeout(ctx, native.RolloutTimeout)
					defer cancel()
					ctx = bounded
				}
				var err error
				observed, err = waitServiceSessionExistingConsumersACK(ctx, cfg, runtime, q)
				return err
			})
			return observed, err
		},
	}, nil
}

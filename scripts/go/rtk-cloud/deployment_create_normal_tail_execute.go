package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The same ordered suffix is used by production and interruption tests. Each
// stage owns its existing receipt; cancellation cannot advance the next stage
// or release the original parent after an incomplete acceptance or capture.
func executeCreateNormalTailSteps(ctx context.Context, e *environmentCreateExecution, workload, external, dns, turn, accept, capture func(context.Context) error) error {
	return executeCreateNormalTailSequence(ctx, e, []func(context.Context) error{workload, external, dns, turn, accept, capture})
}

func executeCreateNormalTailProgramSteps(ctx context.Context, e *environmentCreateExecution, publicTLS, products, workload, finalize, steady, external, dns, turn, accept, capture func(context.Context) error) error {
	return executeCreateNormalTailSequence(ctx, e, []func(context.Context) error{publicTLS, products, workload, finalize, steady, external, dns, turn, accept, capture})
}

func executeCreateNormalTailSequence(ctx context.Context, e *environmentCreateExecution, steps []func(context.Context) error) error {
	if e == nil {
		return createProviderRecoveryBlock("original normal continuation owner unavailable")
	}
	for _, step := range steps {
		if step == nil {
			return createProviderRecoveryBlock("complete normal continuation stage is unavailable")
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := step(ctx); err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
	}
	measured := beginDeploymentMeasurement(ctx, "deployment.create.remaining.original-closure")
	err := e.CompleteAndReleaseContext(ctx, time.Now().UTC())
	measured(err)
	return err
}

// Use the qualified original target, not a newly compiled retain variant after
// a partial write. Child execution owns every applied resource independently of
// whether its readiness or whole-stage acceptance has finished.
func executeCreateNormalTailWorkload(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, compiled *compiledEnvironmentDeployment, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer) error {
	if h == nil || h.Execution == nil || h.OriginalPlan == nil || compiled == nil || compiled.Plan == nil || compiled.Store != store || h.Execution.Store != store || h.Execution.Check(ctx) != nil || qualify == nil || fence == nil || accept == nil {
		return createProviderRecoveryBlock("qualified normal target and original held execution are required")
	}
	backend := createNormalTailWorkloadBackend(cfg, store, h, compiled)
	return executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, "workload", compiled.Plan, backend, qualify, fence, accept, out, func(ctx context.Context) error {
		return captureEnvironmentCreateVolumeOwners(ctx, cfg, compiled.Backend, h.Execution)
	}, compiled.FenceInventory)
}

func executeCreateNormalTailWorkloadBackend(ctx context.Context, store secretStore, h *createNormalTailHandoff, p *deploymentplan.Plan, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error) error {
	return executeCreateNormalTailStageBackend(ctx, store, h, "workload", p, backend, qualify, fence, accept, out, capture)
}

func executeCreateNormalTailStageBackend(ctx context.Context, store secretStore, h *createNormalTailHandoff, stage string, p *deploymentplan.Plan, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error) error {
	return executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, stage, p, backend, qualify, fence, accept, out, capture)
}

func executeCreateNormalTailStageBackendWithPhaseFence(ctx context.Context, store secretStore, h *createNormalTailHandoff, stage string, p *deploymentplan.Plan, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error, phaseFences ...deploymentplan.PhaseFence) error {
	if h == nil || h.Execution == nil || p == nil || backend == nil || qualify == nil || fence == nil || accept == nil || capture == nil || h.Execution.Store != store {
		return createProviderRecoveryBlock("complete original workload adapters are required")
	}
	e := h.Execution
	if p.CreateParentSHA256 != e.Journal.PlanSHA256 || p.CreateHandoffSHA256 != deploymentplan.Digest(h.Pins) || p.Environment != e.Journal.Environment || p.Stack != e.Journal.Stack {
		return createProviderRecoveryBlock("workload plan differs from its authenticated handoff")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	ctx = withCreateNormalTailHandoff(ctx, h)
	key := "kubernetes/" + stage
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		return err
	}
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if err := e.BeginEntry(key, stage, intent, time.Now().UTC()); err != nil {
			return err
		}
	} else if entry.Phase != stage || entry.IntentSHA256 != intentSHA {
		return createProviderRecoveryBlock("normal workload differs from its original recorded plan")
	}
	checkParent := func(ctx context.Context, p *deploymentplan.Plan) error {
		parent, ok := backend.(deploymentplan.ParentExecutionBackend)
		if !ok {
			return createProviderRecoveryBlock("workload backend has no original parent fence")
		}
		if err := parent.CheckParentExecution(ctx, p, store.Root); err != nil {
			return err
		}
		return nil
	}
	checkedFence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := checkParent(ctx, p); err != nil {
			return err
		}
		return fence(ctx, p)
	}
	if len(phaseFences) == 1 && phaseFences[0] != nil {
		phaseFence := phaseFences[0]
		phaseFences = []deploymentplan.PhaseFence{func(ctx context.Context, p *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
			if err := checkParent(ctx, p); err != nil {
				return nil, err
			}
			return phaseFence(ctx, p)
		}}
	}
	if err := runEnvironmentProgress(ctx, out, "deployment.create.kubernetes."+stage, func(ctx context.Context) error {
		return p.ExecuteResumable(ctx, backend, p.SHA256(), store.Root, qualify, checkedFence, accept, phaseFences...)
	}); err != nil {
		return err
	}
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || !j.Accepted {
		return createProviderRecoveryBlock("normal workload lacks genuine accepted child execution")
	}
	receipt, err := json.Marshal(deploymentplan.Object{"plan_sha256": p.SHA256(), "owners": j.Owners})
	if err != nil {
		return err
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return createProviderRecoveryBlock("normal workload parent intent disappeared")
	}
	if entry.State == "completed" {
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(receipt)) {
			return createProviderRecoveryBlock("normal workload completion receipt changed")
		}
		return e.Check(ctx)
	}
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	if err := capture(ctx); err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

// Product and other explicitly qualified preparation stages use the same
// original Plan/journal executor as the final workload. This does not compile
// a partially written create as a new retain plan.
func applyCreateNormalTailStage(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, stage string, definitions []deploymentplan.Definition, out io.Writer) error {
	return applyCreateNormalTailStageWithCapture(ctx, cfg, store, parent, e, stage, definitions, out, true)
}

func applyCreateNormalTailStageWithCapture(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, stage string, definitions []deploymentplan.Definition, out io.Writer, captureAllocations bool) error {
	if !captureAllocations {
		for _, d := range definitions {
			if !strings.HasPrefix(stage, createNormalTailPostCleanupStage+"-") || !keySet("Deployment", "Ingress", "NetworkPolicy", "ConfigMap", "Job")[deploymentplan.String(d.Object["kind"])] {
				return postCleanupCorrectionBlock("nonallocating stage changed an undeclared resource kind")
			}
		}
	}
	h, ok := createNormalTailHandoffFromContext(ctx)
	if !ok || h.Execution != e || e.Store != store || parent == nil || parent.Environment != cfg.Environment || parent.Stack != e.Journal.Stack || len(definitions) == 0 {
		return createProviderRecoveryBlock("qualified original preparation stage unavailable")
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return err
	}
	native, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && runtime.Execution == e && runtime.ExecutionSHA256 == e.Journal.PlanSHA256 {
		native.OpenBaoReplacementReady = func(ctx context.Context, controller, pod deploymentplan.Object) error {
			if err := validateOpenBaoPodOwner(controller, pod); err != nil {
				return err
			}
			return unsealEnvironmentCreateOpenBaoRestart(ctx, cfg, store, runtime, pod)
		}
	}
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	configurationSHA, operatorSHA := deploymentplan.Digest(cfg), deploymentplan.Digest(operator)
	p, err := readCreateNormalTailPreparationPlan(store, e, stage)
	if err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry != nil && p == nil {
		return createProviderRecoveryBlock("started preparation lost its original compiled plan")
	}
	if p == nil {
		live, err := native.Inventory(ctx, parent.Stack)
		if err != nil {
			return err
		}
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: configurationSHA, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, definitions, live)
		if err != nil {
			return err
		}
		if entry != nil { // only a request that never acquired a child lease is unstarted
			for _, lease := range e.Journal.KubernetesLeases {
				if strings.HasPrefix(lease.Holder, p.SHA256()+":") {
					return createProviderRecoveryBlock("preparation lease has no original child journal")
				}
			}
		}
	}
	if p.ConfigurationSHA256 != configurationSHA || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) || p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, definitions) {
		return createProviderRecoveryBlock("preparation scope differs from its frozen original plan")
	}
	if entry == nil {
		if _, exists, err := p.ReadExecution(store.Root); err != nil {
			return err
		} else if exists {
			return createProviderRecoveryBlock("preparation child has no original parent request")
		}
	}
	if !captureAllocations {
		if err := validatePostCleanupStageBeforeExecution(cfg, store, e, stage, p); err != nil {
			return err
		}
	}
	if err := saveCreateNormalTailStagePlan(ctx, store, e, stage, p); err != nil {
		return err
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store, stagePlanSHA256: p.SHA256()}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
		_, op, opErr := readEnvironmentCreateOperator(cfg)
		if err != nil || opErr != nil || deploymentplan.Digest(fresh) != configurationSHA || deploymentplan.Digest(op) != operatorSHA {
			return createProviderRecoveryBlock("preparation inputs changed")
		}
		return nil
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error {
		objects, err := native.Inventory(ctx, p.Stack)
		if err != nil {
			return err
		}
		return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		})
	}
	capture := func(ctx context.Context) error { return captureEnvironmentCreateVolumeOwners(ctx, cfg, native, e) }
	if !captureAllocations {
		capture = e.Check
	}
	return executeCreateNormalTailStageBackend(ctx, store, h, stage, p, backend, fence, fence, accept, out, capture)
}

// A recorded response can be reconciled by exact fresh readback. A pending
// request without a response remains uncertain and is never replayed.
func createNormalTailRecordedStage(ctx context.Context, e *environmentCreateExecution, key, phase string, intent, receipt []byte, verify func(context.Context) error) (bool, error) {
	if err := e.Check(ctx); err != nil {
		return false, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return false, nil
	}
	sha, err := environmentCreatePlanSHA256(intent)
	want, receiptErr := canonicalEnvironmentCreateJSON(receipt)
	if err != nil || receiptErr != nil || entry.Phase != phase || entry.IntentSHA256 != sha || entry.State != "allocated" && entry.State != "completed" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(want)) {
		return true, createProviderRecoveryBlock("normal stage has an uncertain request or changed original outcome: " + key)
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	if err := e.Check(ctx); err != nil {
		return true, err
	}
	if entry.State == "allocated" {
		return true, e.CompleteEntry(key, time.Now().UTC())
	}
	return true, nil
}

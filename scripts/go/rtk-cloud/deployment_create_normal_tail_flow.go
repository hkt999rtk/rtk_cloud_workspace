package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the original create execution's target, not a second execution
// authority. The ordinary preflight proof authorizes it; the original parent
// journal and the workload child journal record its actual progress.
type createNormalTailTarget struct {
	Schema, ParentSHA256, OwnerSHA256 string
	Pins                              map[string]string
	Plan                              *deploymentplan.Plan
	Release                           environmentRelease
	Bindings                          environmentIdentityBindings
}

const createNormalTailTargetSchema = "rtk-cloud.create-normal-tail-target/v1"

func readCreateNormalTailFrozenTarget(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, error) {
	var target createNormalTailTarget
	if e == nil || e.Store != store || readEnvironmentPreflightJSON(filepath.Join(e.Directory, "normal-tail-target.json"), &target) != nil || target.Schema != createNormalTailTargetSchema || target.ParentSHA256 != e.Journal.PlanSHA256 || target.OwnerSHA256 != deploymentplan.Digest(e.owner) || target.Plan == nil || target.Plan.Environment != store.Environment || target.Plan.Stack != e.Journal.Stack || target.Plan.Operation != "create" || len(target.Plan.Resources) == 0 || len(target.Pins) == 0 {
		return nil, environmentCreateBlocker("deployment.create.target", "the original execution's frozen complete target cannot be verified")
	}
	return target.Plan, nil
}

// Select by a durable completed lifecycle, not the number of journal entries.
// Malformed completion is an error, never a fallback to replaying bootstrap.
func tryUnifiedCreateNormalTail(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) (bool, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return false, nil
	}
	var owner environmentCreateOwner
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), &owner) != nil || !environmentPreflightDigest.MatchString(owner.PlanSHA256) {
		return false, nil
	}
	completed := filepath.Join(store.Root, managedLateRedisDurableBase(owner.PlanSHA256), "completed.json")
	if _, err := os.Lstat(completed); errors.Is(err, os.ErrNotExist) {
		// Final capture and the durable completion marker precede the owner
		// archive rename. Reopening that exact local gap runs no bootstrap.
		path, pathErr := privateDeploymentStatePath(cfg.Environment, owner.Journal)
		var journal environmentCreateJournal
		if pathErr != nil || readEnvironmentPreflightJSON(path, &journal) != nil || journal.PlanSHA256 != owner.PlanSHA256 || journal.CompletedAt.IsZero() {
			return false, nil
		}
	}
	fail := func(err error) (bool, error) {
		report.Scope, report.Decision = "original-create-normal-tail", "NO-GO"
		report.Gates = []deploymentplan.GateResult{safeQualifiedExecutionFailure(err)}
		return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if o.ParentPlanSHA256 != "" && o.ParentPlanSHA256 != owner.PlanSHA256 {
		return fail(environmentCreateBlocker("deployment.create.target", "selected parent differs from the original create execution"))
	}
	e, err := loadCreateNormalTailExecution(ctx, cfg, store, owner.PlanSHA256)
	if err != nil {
		return fail(err)
	}
	defer e.Close()
	if !e.Journal.CompletedAt.IsZero() {
		if err := resumeCreateCompletedOwnerArchive(ctx, e, o); err != nil {
			return fail(err)
		}
		report.Scope, report.Decision, report.Completed = "original-create-completion", "GO", o.Action == "create"
		return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	h, err := readCreateNormalTailHandoff(ctx, cfg, store, e)
	if err != nil {
		return fail(err)
	}
	ctx = newEnvironmentReleaseProofCapture(withCreateNormalTailHandoff(ctx, h))
	report.Scope = "original-create-normal-tail"
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		return fail(err)
	}
	needsProducer := false
	for _, product := range products {
		needsProducer = needsProducer || h.Bindings.Owners[product.ID] == ""
	}
	if _, err := os.Lstat(createNormalTailProgramPath(e)); err == nil || !errors.Is(err, os.ErrNotExist) || needsProducer {
		return true, runCreateNormalTailProgram(ctx, cfg, o, h, report, out, diagnostics)
	}
	deps := createNormalTailDependencies(cfg, store, h, out)
	return true, runUnifiedCompiledEnvironmentDeployment(ctx, cfg, o, report, out, diagnostics, deps)
}

func createNormalTailDependencies(cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, out io.Writer) unifiedDeploymentDependencies {
	deps := defaultUnifiedDeploymentDependencies()
	deps.ContinueCreate, deps.FreshCreate, deps.RecoveryPreflight, deps.PrepareStorage = nil, nil, nil, nil
	deps.CheckExecutionAvailable = func(ctx context.Context, _ deploymentConfig, _ string) error { return h.Execution.Check(ctx) }
	deps.Compile = func(ctx context.Context, cfg deploymentConfig, _ string) (*compiledEnvironmentDeployment, error) {
		if createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/workload") != nil {
			return hydrateCreateNormalTailTarget(ctx, cfg, store, h, nil)
		}
		// The handoff has already authenticated the effective release. Do not
		// recursively reconstruct historical recovery at the compiler boundary.
		c, err := compileEnvironmentDeploymentWithImagesTarget(ctx, cfg, "create", h.Release, localEnvironmentReleaseSourceCommand)
		if err != nil {
			return nil, err
		}
		c.Plan.CreateParentSHA256 = h.Execution.Journal.PlanSHA256
		c.Plan.CreateHandoffSHA256 = deploymentplan.Digest(h.Pins)
		return c, nil
	}
	deps.LoadQualified = func(ctx context.Context, cfg deploymentConfig, operation, sha string) (*compiledEnvironmentDeployment, error) {
		artifact, err := loadEnvironmentPreflightProof(cfg, store, operation, sha, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		return hydrateCreateNormalTailTarget(ctx, cfg, store, h, artifact)
	}
	publish := deps.PublishProof
	deps.PublishProof = func(ctx context.Context, c *compiledEnvironmentDeployment, q deploymentplan.Qualification) error {
		c.ReleaseProofs = snapshotVerifiedReleaseProofs(ctx, c.Release)
		if err := saveCreateNormalTailTarget(c, h); err != nil {
			return err
		}
		return publish(ctx, c, q)
	}
	checks, liveChecks := deps.Checks, deps.LiveChecks
	deps.Checks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
		return createNormalTailChecks(c, h, checks(c), false)
	}
	deps.LiveChecks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
		return createNormalTailChecks(c, h, liveChecks(c), true)
	}
	deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, qualify func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
		if c.Plan.SHA256() != sha || c.Plan.CreateHandoffSHA256 != deploymentplan.Digest(h.Pins) {
			return preflightExecutionProofBlocker("qualified target differs from the authenticated original create handoff")
		}
		operator, err := store.readOperator()
		if err != nil {
			return err
		}
		return executeCreateNormalTailSteps(ctx, h.Execution,
			func(ctx context.Context) error {
				return executeCreateNormalTailWorkload(ctx, cfg, store, h, c, qualify, c.Fence, func(ctx context.Context, p *deploymentplan.Plan) error { return c.Backend.CheckTargetWorkloads(ctx, p) }, out)
			},
			func(ctx context.Context) error {
				return configureEnvironmentCreateExternalDataPlanes(ctx, cfg, h.OriginalPlan, h.Receipts, store, operator, h.Execution)
			},
			func(ctx context.Context) error {
				return executeEnvironmentCreateDNS(ctx, cfg, h.OriginalPlan.DNS, h.Receipts, operator, h.Execution)
			},
			func(ctx context.Context) error {
				return activateEnvironmentCreateTURNRegistrars(ctx, cfg, h.OriginalPlan, h.Receipts, store, operator, h.Execution)
			},
			func(ctx context.Context) error { return accept(ctx, c) },
			func(ctx context.Context) error {
				return captureEnvironmentCreateResources(ctx, cfg, c, h.Receipts, h.Execution)
			})
	}
	return deps
}

func saveCreateNormalTailTarget(c *compiledEnvironmentDeployment, h *createNormalTailHandoff) error {
	if c == nil || c.Plan == nil || h == nil || h.Execution == nil || c.Plan.CreateParentSHA256 != h.Execution.Journal.PlanSHA256 || c.Plan.CreateHandoffSHA256 != deploymentplan.Digest(h.Pins) {
		return preflightExecutionProofBlocker("complete target must bind the original create execution and completed lifecycle")
	}
	target := createNormalTailTarget{Schema: createNormalTailTargetSchema, ParentSHA256: h.Execution.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(h.Execution.owner), Pins: h.Pins, Plan: c.Plan, Release: c.Release, Bindings: c.Bindings}
	if createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/workload") != nil {
		original, err := readCreateNormalTailFrozenTarget(c.Store, h.Execution)
		if err != nil || original.SHA256() != c.Plan.SHA256() {
			return preflightExecutionProofBlocker("a started workload must keep its original frozen plan")
		}
		return nil
	}
	path := filepath.Join(h.Execution.Directory, "normal-tail-target.json")
	relative, err := filepath.Rel(c.Store.Root, path)
	if err != nil {
		return err
	}
	return c.Store.write(relative, mustProviderRecoveryJSON(target), true)
}

// Hydration deliberately does not recompile changed resources as retain. The
// original child journal identifies approved progress during resumed admission.
func hydrateCreateNormalTailTarget(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, artifact *environmentPreflightArtifact) (*compiledEnvironmentDeployment, error) {
	return hydrateCreateNormalTailTargetWithInputLoader(ctx, cfg, store, h, artifact, func() (string, environmentKubernetesBackend, error) {
		snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
		if err != nil {
			return "", environmentKubernetesBackend{}, preflightExecutionProofBlocker("canonical target inputs changed since this original workload plan was frozen")
		}
		backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, cfg.Environment)
		if err != nil {
			return "", backend, err
		}
		return environmentDeploymentSnapshotDigest(ctx, cfg, snapshot), backend, nil
	})
}

func hydrateCreateNormalTailTargetWithInputLoader(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, artifact *environmentPreflightArtifact, load func() (string, environmentKubernetesBackend, error)) (*compiledEnvironmentDeployment, error) {
	var target createNormalTailTarget
	plan, err := readCreateNormalTailFrozenTarget(store, h.Execution)
	if err != nil || readEnvironmentPreflightJSON(filepath.Join(h.Execution.Directory, "normal-tail-target.json"), &target) != nil || plan.CreateParentSHA256 != h.Execution.Journal.PlanSHA256 || plan.CreateHandoffSHA256 != deploymentplan.Digest(h.Pins) || deploymentplan.Digest(target.Pins) != plan.CreateHandoffSHA256 || deploymentplan.Digest(target.Release) != plan.ReleaseSHA256 || deploymentplan.Digest(target.Bindings) != plan.BindingsSHA256 {
		return nil, preflightExecutionProofBlocker("original frozen complete target differs from its create handoff")
	}
	configurationSHA, backend, err := load()
	if err != nil {
		return nil, err
	}
	backend.SchemaWorkspace = cfg.Workspace
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return nil, err
	}
	plan, err = selectCreateNormalTailWorkloadTarget(store, h.Execution, plan)
	if err != nil {
		return nil, err
	}
	if configurationSHA != plan.ConfigurationSHA256 {
		return nil, preflightExecutionProofBlocker("canonical target inputs changed since this original workload plan was frozen")
	}
	c := &compiledEnvironmentDeployment{Plan: plan, Release: target.Release, Bindings: target.Bindings, Store: store, Backend: backend, Configuration: cfg, SourceCommand: localEnvironmentReleaseSourceCommand}
	if artifact != nil {
		cluster, identity, err := environmentPreflightKubernetesIdentity(backend.KubeconfigContent)
		if err != nil || artifact.Plan.SHA256() != plan.SHA256() || artifact.Proof.ClusterSHA256 != cluster || artifact.Proof.KubernetesIdentitySHA256 != identity {
			return nil, preflightExecutionProofBlocker("qualified target or current Kubernetes identity changed")
		}
		c.Proof, c.PreflightGeneration = &artifact.Proof, artifact.Proof.Generation
	}
	return c, ctx.Err()
}

func createNormalTailChecks(c *compiledEnvironmentDeployment, h *createNormalTailHandoff, checks map[string]deploymentplan.GateCheck, live bool) map[string]deploymentplan.GateCheck {
	if live {
		check := checks["deployment.execution"]
		checks["deployment.execution"] = func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if err := verifyCreateNormalTailRedis(ctx, c.Configuration, c.Store, h, c.Backend); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			return check(ctx, p)
		}
	}
	checks["deployment.network"] = func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
		operator, err := c.Store.readOperator()
		if err == nil {
			_, err = environmentCreateDataPlaneNodeUpstreams(ctx, c.Configuration, c.Backend)
		}
		if err == nil {
			err = qualifyCreateNormalTailDNS(ctx, c.Configuration, h, operator)
		}
		return deploymentplan.GateEvidence{SHA256: p.SHA256()}, err
	}
	if !live {
		checks["deployment.recovery"] = func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if err := managedLateRedisVerifyOriginalVolumes(ctx, c.Backend, h.OriginalVolumes); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			if err := verifyCreateNormalTailRedis(ctx, c.Configuration, c.Store, h, c.Backend); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest(h.Pins)}, h.Execution.Check(ctx)
		}
	}
	return checks
}

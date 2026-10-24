package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Once admitted, stage recovery consumes the original program and clock. Failure
// guidance must not send operators back through qualification for every retry.
func createNormalTailProgramFailure(err error, report *unifiedDeploymentReport, now time.Time) deploymentplan.GateResult {
	row := safeQualifiedExecutionFailure(err)
	if report == nil || !report.ReusedPreflight || report.ExecutionAdmittedAt == nil || report.ExecutionDeadline == nil || !report.ExecutionDeadline.After(*report.ExecutionAdmittedAt) {
		return row
	}
	if now.Before(*report.ExecutionDeadline) {
		row.Next = "repair and reconcile the reported stage against its retained native receipts, then resume the same admitted plan before " + report.ExecutionDeadline.UTC().Format(time.RFC3339Nano) + "; retain completed stages and the original clock; another full preflight is not required for this recovery"
		row.Recheck = "verify the original program, execution owner, completed outputs and remaining stage during admitted recovery"
	} else {
		row.Next = "preserve the original program, identities, data and completed receipts; the original execution deadline has expired, so qualify a bounded remaining execution window before further writes"
		row.Recheck = "verify the remaining work and execution window without replaying completed producers"
	}
	return row
}

func runCreateNormalTailProgram(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, h *createNormalTailHandoff, report *unifiedDeploymentReport, out, diagnostics io.Writer) error {
	store, e := h.Execution.Store, h.Execution
	fail := func(err error) error {
		report.Decision = "NO-GO"
		report.Gates = append(report.Gates, createNormalTailProgramFailure(err, report, time.Now().UTC()))
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	var a createNormalTailProgramArtifact
	if o.Action == "create" {
		if _, err := readCreateNormalTailProgram(store, e); err != nil {
			return fail(err)
		}
		if readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &a) != nil || deploymentplan.Digest(a.Program) != o.PlanSHA256 {
			return fail(preflightExecutionProofBlocker("selected program differs from the original complete preflight"))
		}
		setCreateNormalTailProgramReport(report, a)
		return executeCreateNormalTailProgram(ctx, cfg, o, h, a, report, out, diagnostics)
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program") != nil {
		// A retry consumes the same qualified program and original clock. Do
		// not replace its producer source with the already modified runtime.
		if readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &a) != nil || verifyCreateNormalTailProgramAdmission(store, e) != nil {
			return fail(preflightExecutionProofBlocker("the admitted remaining program cannot be verified"))
		}
		admission, err := readCreateNormalTailProgramAdmission(store, e)
		if err != nil || !time.Now().Before(admission.Deadline) {
			return fail(environmentCreateBlocker("deployment.execution.deadline", "the admitted remaining-program window has expired; its historical GO cannot authorize another write"))
		}
		if err := createNormalTailProgramFence(ctx, cfg, h, &a.Program); err != nil {
			return fail(err)
		}
		setCreateNormalTailProgramReport(report, a)
		report.ReusedPreflight = true
		report.ExecutionAdmittedAt, report.ExecutionDeadline = &admission.AdmittedAt, &admission.Deadline
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	generation := ""
	if o.Action == "preflight" {
		var err error
		generation, err = invalidateEnvironmentPreflightProofWithToken(cfg)
		if err != nil {
			return fail(err)
		}
	}
	var rendered *renderedEnvironmentTarget
	var program *createNormalTailProgram
	err := runEnvironmentProgress(ctx, out, "deployment.compile.complete-program", func(ctx context.Context) error {
		// Completed bootstrap images are historical evidence. Select the release
		// for the remaining consumers once, before freezing the new program;
		// an admitted retry always consumes that frozen selection above.
		release, err := loadEnvironmentCIImages(ctx, cfg, environmentCIImageCommand(cfg))
		if err != nil {
			return err
		}
		rendered, err = renderEnvironmentDeploymentTarget(ctx, cfg, "create", release)
		if err != nil {
			return err
		}
		program, err = buildCreateNormalTailProgram(ctx, cfg, h, rendered)
		return err
	})
	if err != nil {
		return fail(err)
	}
	a.Program = *program
	setCreateNormalTailProgramReport(report, a)
	if o.Action == "plan" {
		report.Decision = "PLANNED"
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	checks, err := createNormalTailProgramChecks(ctx, cfg, h, program, rendered)
	if err != nil {
		return fail(err)
	}
	a.Qualification, err = qualifyCreateNormalTailProgram(ctx, program, checks, out)
	report.Gates, report.Decision = a.Qualification.Gates, a.Qualification.Decision
	if err != nil {
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if err := createNormalTailProgramFence(ctx, cfg, h, program); err != nil {
		return fail(err)
	}
	validity, err := environmentPreflightValidity(cfg.Values)
	if err != nil {
		return fail(err)
	}
	a.CheckerSHA256, err = environmentPreflightCheckerSHA256()
	if err != nil {
		return fail(err)
	}
	a.Generation, a.QualifiedAt = generation, time.Now().UTC()
	a.ValidUntil = a.QualifiedAt.Add(time.Duration(validity) * time.Second)
	a.ReleaseProofs = snapshotVerifiedReleaseProofs(ctx, program.Release)
	if err := validateEnvironmentPreflightReleaseProofs(environmentPreflightProof{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], QualifiedAt: a.QualifiedAt, ReleaseProofs: a.ReleaseProofs}, program.Release); err != nil {
		return fail(err)
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Generation != generation || active.Decision != "NO-GO" {
		return fail(preflightExecutionProofBlocker("another qualification superseded this complete program"))
	}
	if err := saveCreateNormalTailProgramArtifact(store, e, a); err != nil {
		return fail(err)
	}
	// The shared pointer names the immutable qualified artifact. The original
	// execution keeps the same program for recovery and historical provenance.
	name := fmt.Sprintf("%s/qualification-%d", deploymentplan.Digest(program), a.QualifiedAt.UnixNano())
	if err := store.write(filepath.Join("deployment/preflight", name, "program.json"), mustProviderRecoveryJSON(a), false); err != nil {
		return fail(err)
	}
	active = environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: deploymentplan.Digest(program), Artifact: name, ProofSHA256: deploymentplan.Digest(a)}
	if err := writeEnvironmentPreflightActive(store, active); err != nil {
		return fail(err)
	}
	setCreateNormalTailProgramReport(report, a)
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

func setCreateNormalTailProgramReport(report *unifiedDeploymentReport, a createNormalTailProgramArtifact) {
	p := a.Program
	report.Scope, report.Decision = "original-create-normal-tail", a.Qualification.Decision
	report.Release = &p.Release
	summary := deploymentplan.Summary{SchemaVersion: deploymentplan.Version, Environment: p.Bindings.Environment, Operation: "create", PlanSHA256: deploymentplan.Digest(p)}
	for _, d := range p.Definitions {
		summary.Resources = append(summary.Resources, deploymentplan.SummaryResource{ID: deploymentplan.ID(d.Object), Phase: d.Phase, Action: "resolve"})
	}
	if p.Products != nil {
		for _, output := range p.Products.Outputs {
			summary.Resources = append(summary.Resources, deploymentplan.SummaryResource{ID: output.ID, Phase: "bootstrap", Action: "native-enrollment"})
		}
	}
	if outputs, err := environmentPublicTLSOutputContract(p.PublicTLS); err == nil {
		for _, id := range sortedMapKeys(outputs) {
			summary.Resources = append(summary.Resources, deploymentplan.SummaryResource{ID: id, Phase: "public-tls", Action: "certificate-publication"})
		}
	}
	report.Plan, report.Gates = &summary, a.Qualification.Gates
	if !a.QualifiedAt.IsZero() {
		report.QualifiedAt, report.EvidenceValidUntil = &a.QualifiedAt, &a.ValidUntil
		report.QualificationSHA256 = deploymentplan.Digest(a)
	}
}

func compileCreateNormalTailProductTarget(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, p *createNormalTailProgram, backend environmentKubernetesBackend, steady bool) (*compiledEnvironmentDeployment, error) {
	effective, err := readEffectiveProductInstalled(h.Execution.Store, h.Execution, p.Products)
	if err != nil {
		return nil, err
	}
	installed := *effective
	resolver := resolveCreateNormalTailProductDefinitions
	if steady {
		resolver = resolveCreateNormalTailProductFinalDefinitions
	}
	definitions, err := resolver(cfg, p.Products, &installed)
	if err != nil {
		return nil, err
	}
	if steady {
		if err := productPreparationsRetired(ctx, cfg, h.Execution.Store, h.Execution, p.Products, &installed, backend); err != nil {
			return nil, err
		}
	} else {
		preparations, err := productPreparationDefinitions(cfg, h.Execution.Store, h.Execution, p.Products, &installed, false)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, preparations...)
	}
	definitions, err = applyCreateNormalTailRuntimeDefinitions(cfg, h.Execution.Store, h.Execution, definitions)
	if err != nil {
		return nil, err
	}
	definitions, err = retainCreateNormalTailPostgresAccess(h.Execution.Store, h.Execution, definitions)
	if err != nil {
		return nil, err
	}
	definitions, err = resolveEnvironmentPublicTLSDefinitions(h.Execution.Store, p.PublicTLS, definitions)
	if err != nil {
		return nil, err
	}
	definitions, err = applyCreateNormalTailPostCleanupDefinitions(cfg, h.Execution.Store, h.Execution, definitions)
	if err != nil {
		return nil, err
	}
	live, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, err
	}
	bindings, err := observeCreateNormalTailPostCleanupBindings(ctx, cfg, h.Execution, p.Release, live)
	if err != nil {
		return nil, err
	}
	for id, uid := range p.Bindings.Owners {
		if bindings.Owners[id] != uid {
			return nil, environmentCreateBlocker("deployment.ownership.drift", "an original program owner changed during native production: "+id)
		}
	}
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, h.Execution.Store)
	if err != nil {
		return nil, err
	}
	rendered := &renderedEnvironmentTarget{Snapshot: snapshot, Definitions: definitions, Live: live, Bindings: bindings, Backend: backend, Store: h.Execution.Store, Release: p.Release}
	c, err := compileRenderedEnvironmentDeploymentTarget(ctx, cfg, "create", rendered, localEnvironmentReleaseSourceCommand)
	if err != nil {
		return nil, err
	}
	c.Plan.CreateParentSHA256, c.Plan.CreateHandoffSHA256 = p.ParentSHA256, p.HandoffSHA256
	return c, nil
}

func executeCreateNormalTailProgram(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, h *createNormalTailHandoff, a createNormalTailProgramArtifact, report *unifiedDeploymentReport, out, diagnostics io.Writer) error {
	fail := func(err error) error {
		report.Decision = "NO-GO"
		report.Gates = append(report.Gates, createNormalTailProgramFailure(err, report, time.Now().UTC()))
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	store := h.Execution.Store
	p := &a.Program
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		return fail(err)
	}
	backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, cfg.Environment)
	if err != nil {
		return fail(err)
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return fail(err)
	}
	cluster, identity, err := environmentPreflightKubernetesIdentity(backend.KubeconfigContent)
	if err != nil || cluster != p.ClusterSHA256 || identity != p.IdentitySHA256 {
		return fail(preflightExecutionProofBlocker("selected Kubernetes target or identity changed"))
	}
	if err := verifyCreateNormalTailRedis(ctx, cfg, store, h, backend); err != nil {
		return fail(err)
	}
	admission, err := admitCreateNormalTailProgram(ctx, cfg, h, a, o.Timeout, time.Now().UTC(), o.TotalTimeout)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithDeadline(ctx, admission.Deadline)
	defer cancel()
	report.ExecutionAdmittedAt, report.ExecutionDeadline, report.ReusedPreflight = &admission.AdmittedAt, &admission.Deadline, true
	if err := runEnvironmentProgress(ctx, out, "deployment.create.remaining.runtime-dependency-access", func(ctx context.Context) error {
		return ensureCreateNormalTailPostgresAccess(ctx, cfg, h, p, backend, out)
	}); err != nil {
		return fail(err)
	}
	c := &compiledEnvironmentDeployment{Store: store, Backend: backend, Configuration: cfg, Release: p.Release, Bindings: p.Bindings}
	if err := runEnvironmentProgress(ctx, out, "deployment.create.remaining.account-registration-crls", func(ctx context.Context) error {
		return ensureCreateNormalTailAccountRegistrationCRLs(ctx, cfg, h, c, out)
	}); err != nil {
		return fail(err)
	}
	operator, err := store.readOperator()
	if err != nil {
		return fail(err)
	}
	programFence := func(ctx context.Context, _ *deploymentplan.Plan) error {
		return createNormalTailProgramFence(ctx, cfg, h, p)
	}
	accept := defaultUnifiedDeploymentDependencies().Accept
	step := func(name string, run func(context.Context) error) func(context.Context) error {
		return func(ctx context.Context) error {
			return runEnvironmentProgress(ctx, out, "deployment.create.remaining."+name, run)
		}
	}
	err = executeCreateNormalTailProgramSteps(ctx, h.Execution,
		step("public-tls", func(ctx context.Context) error {
			return executeEnvironmentPublicTLS(ctx, cfg, store, h.Execution, p.PublicTLS, func(ctx context.Context, definition deploymentplan.Definition) (string, error) {
				parent := *h.OriginalPlan
				parent.Release = p.Release
				if err := applyCreateNormalTailStage(ctx, cfg, store, &parent, h.Execution, "public-tls", []deploymentplan.Definition{definition}, out); err != nil {
					return "", err
				}
				object, err := backend.Read(ctx, deploymentplan.ID(definition.Object))
				if err != nil {
					return "", err
				}
				uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
				if uid == "" {
					return "", environmentCreateBlocker("deployment.public-tls", "published public TLS Secret has no native UID")
				}
				return uid, nil
			})
		}),
		step("product-identities", func(ctx context.Context) error {
			if err := hydrateCreateNormalTailProducts(ctx, cfg, h, backend, p.Products); err != nil {
				return err
			}
			if err := executeCreateNormalTailProducts(ctx, cfg, h, c, p.Products); err != nil {
				return err
			}
			return nil
		}),
		step("workloads", func(ctx context.Context) error {
			if err := runEnvironmentProgress(ctx, out, "deployment.create.remaining.runtime-configuration-target", func(ctx context.Context) error {
				var err error
				c, err = prepareCreateNormalTailRuntimeForward(ctx, cfg, h, c)
				return err
			}); err != nil {
				return err
			}
			if createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/workload") != nil {
				c, err = hydrateCreateNormalTailTarget(ctx, cfg, store, h, nil)
			} else {
				c, err = compileCreateNormalTailProductTarget(ctx, cfg, h, p, backend, false)
				if err == nil {
					err = saveCreateNormalTailTarget(c, h)
				}
			}
			if err != nil {
				return err
			}
			c, err = prepareCreateNormalTailWorkloadForward(ctx, cfg, h, c)
			if err != nil {
				return err
			}
			entry := createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/workload")
			if entry == nil || entry.State != "completed" {
				if err := executeCreateNormalTailForwardWorkload(ctx, cfg, store, h, c, programFence, c.Fence, func(ctx context.Context, plan *deploymentplan.Plan) error {
					return c.Backend.CheckTargetWorkloads(ctx, plan)
				}, out); err != nil {
					return err
				}
			} else {
				// Finalization supersedes transient ACK settings. Retain the
				// accepted initial child as history and resume its steady child.
				frozen, err := readCreateNormalTailFrozenTarget(store, h.Execution)
				if err != nil {
					return err
				}
				_, child, err := readCreateNormalTailWorkload(store, h.Execution, frozen)
				if err != nil || !child.Accepted {
					return environmentCreateBlocker("deployment.create.program", "initial workload has no accepted original child to finalize")
				}
			}
			return nil
		}),
		step("finalization-and-cleanup", func(ctx context.Context) error { return completeCreateNormalTailProducts(ctx, cfg, h, c, p.Products) }),
		step("verify-steady-target", func(ctx context.Context) error {
			steady, err := createNormalTailProductsSteady(ctx, cfg, h, c, p.Products)
			if err != nil || !steady {
				return environmentCreateBlocker("deployment.create.program", "native product finalization and its steady target are not complete")
			}
			return nil
		}),
		step("external-routing", func(ctx context.Context) error {
			return configureEnvironmentCreateExternalDataPlanes(ctx, cfg, h.OriginalPlan, h.Receipts, store, operator, h.Execution)
		}),
		step("dns", func(ctx context.Context) error {
			return executeEnvironmentCreateDNS(ctx, cfg, h.OriginalPlan.DNS, h.Receipts, operator, h.Execution)
		}),
		step("turn", func(ctx context.Context) error {
			return activateEnvironmentCreateTURNRegistrars(ctx, cfg, h.OriginalPlan, h.Receipts, store, operator, h.Execution)
		}),
		step("full-acceptance", func(ctx context.Context) error {
			if err := runEnvironmentProgress(ctx, out, "deployment.create.remaining.post-cleanup-correction", func(ctx context.Context) error {
				if err := ensureCreateNormalTailPostCleanupCorrection(ctx, cfg, h, c, o.PostCleanupHelperImage, o.PostCleanupControllerImage, out); err != nil {
					return err
				}
				c, err = compileCreateNormalTailProductTarget(ctx, cfg, h, p, backend, true)
				return err
			}); err != nil {
				return err
			}
			return accept(ctx, c)
		}),
		step("resource-ledger", func(ctx context.Context) error {
			return captureEnvironmentCreateResources(ctx, cfg, c, h.Receipts, h.Execution)
		}))
	if err != nil {
		return fail(err)
	}
	report.Decision, report.Completed = "GO", true
	report.ResolvedTargetSHA256 = c.Plan.SHA256()
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

package main

import (
	"context"
	"io"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// dispatchFailedApplicationForward qualifies or executes only a fully written
// original application stage; all other create stages retain their normal flow.
type failedForwardDispatchAdapters struct {
	backend    deploymentplan.Backend
	command    environmentReleaseCommand
	selectTool func(context.Context) (failedForwardTool, error)
	project    func(context.Context, []deploymentplan.Definition) ([]deploymentplan.Definition, error)
	inventory  func(context.Context) ([]deploymentplan.Object, error)
	execute    func(context.Context, string, *deploymentplan.Plan) error
	accept     func(context.Context, *deploymentplan.Plan) error
	clock      func() time.Time
	mountAlias func(context.Context, secretStore, *environmentCreateExecution, deploymentplan.Backend, createInitializerMountForwardProof, createInitializerForwardProof, createFailedForwardProof, issuerEnrollmentContinuationProof, string, func(context.Context, *deploymentplan.Plan) error, time.Time) error
}

func dispatchFailedApplicationForward(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, admitted issuerEnrollmentContinuationProof, admissionSHA string, action, selected string, out io.Writer, now, executionDeadline time.Time, injected ...failedForwardDispatchAdapters) (*createFailedForwardProof, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-application-owners")
	if entry == nil {
		return nil, nil
	}
	original, err := readEnvironmentCreateStageInput(store, e, "managed-application-owners")
	if err != nil || original == nil {
		return nil, createProviderRecoveryBlock("original application stage input is unavailable")
	}
	journal, exists, err := original.ReadExecution(store.Root)
	if err != nil {
		return nil, err
	}
	if !exists || journal.Failed == "" {
		return nil, nil
	}
	r := runtime.ServiceSessionRecovery
	var issuers []environmentCreateRegisteredIssuer
	if r != nil && r.ManagedTransportRecovery != nil && r.MQTTRoot != nil && r.MQTTIntermediate != nil {
		d := r.ManagedTransportRecovery
		issuers = []environmentCreateRegisteredIssuer{r.Ceremony.Root, r.Ceremony.Intermediate, *r.MQTTRoot, *r.MQTTIntermediate, d.Device, d.AppRoot, d.AppIntermediate}
		runtime.ServiceCeremony = &r.Ceremony
	}
	project := func(ctx context.Context, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
		return projectEnvironmentCreateCustodyStage(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, store, parent, e, defs, nil)
	}
	inventory := func(ctx context.Context) ([]deploymentplan.Object, error) { return native.Inventory(ctx, parent.Stack) }
	var backend deploymentplan.Backend = environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	command := environmentCIImageCommand(cfg)
	clock := time.Now
	mountAlias := completeInitializerMountForwardAlias
	selectTool := func(ctx context.Context) (failedForwardTool, error) {
		return selectFailedForwardTool(ctx, store, e, command)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error {
		objects, err := native.TargetWorkloadInventory(ctx, p)
		if err != nil {
			return err
		}
		return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		})
	}
	execute := func(ctx context.Context, stage string, p *deploymentplan.Plan) error {
		return executeCompiledEnvironmentCreateStage(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, store, parent, e, stage, p, native, out)
	}
	if len(injected) > 1 {
		return nil, createProviderRecoveryBlock("one exact failed-stage execution adapter is required")
	}
	if len(injected) == 1 {
		a := injected[0]
		if a.backend == nil || a.command == nil || a.selectTool == nil || a.project == nil || a.inventory == nil || a.execute == nil || a.accept == nil {
			return nil, createProviderRecoveryBlock("complete failed-stage execution adapters are required")
		}
		backend, command, selectTool, project, inventory, execute, accept = a.backend, a.command, a.selectTool, a.project, a.inventory, a.execute, a.accept
		if a.clock != nil {
			clock = a.clock
		}
		if a.mountAlias != nil {
			mountAlias = a.mountAlias
		}
	}
	review := ""
	for _, item := range e.Journal.Entries {
		const prefix = "failed-stage-forward-"
		if strings.HasPrefix(item.Key, prefix) && item.Phase == "failed-stage-forward" {
			found := strings.TrimPrefix(item.Key, prefix)
			if review != "" || !environmentPreflightDigest.MatchString(found) {
				return nil, createProviderRecoveryBlock("original application repair admission is ambiguous")
			}
			review = found
		}
	}
	if action == "create" && review == "" {
		review = selected
	}
	var q createFailedForwardProof
	var repair, retained *deploymentplan.Plan
	if review != "" {
		q, repair, retained, err = loadFailedForwardBundle(store, e, review)
		if err != nil {
			return nil, err
		}
	} else {
		if action != "preflight" {
			return nil, createProviderRecoveryBlock("full preflight for the original failed application stage is required")
		}
		if r == nil || len(issuers) == 0 {
			return nil, createProviderRecoveryBlock("original application CRL repair issuer evidence is unavailable")
		}
		tool, toolErr := selectTool(ctx)
		if toolErr != nil {
			return nil, toolErr
		}
		q, repair, retained, err = prepareFailedForwardCandidates(ctx, cfg, store, e, backend, parent, runtime, r.Ceremony, issuers, admitted, admissionSHA, tool, project, inventory, clock)
		if err != nil {
			return nil, err
		}
		if err = saveFailedForwardBundle(store, e, q, repair, retained); err != nil {
			return nil, err
		}
		now = q.QualifiedAt
	}
	// A failed retained owner needs its own narrowly scoped successor; never
	// rewrite the frozen 59-resource source or its failed retain journal.
	_, oldRetainStage := failedForwardStageNames(q)
	oldRetain, oldReadErr := readEnvironmentCreateStageInput(store, e, oldRetainStage)
	if oldReadErr != nil {
		return nil, oldReadErr
	}
	if oldRetain != nil {
		oldJournal, exists, readErr := oldRetain.ReadExecution(store.Root)
		if readErr != nil {
			return nil, readErr
		}
		if exists && oldJournal.Failed != "" && !oldJournal.Accepted {
			deadline := executionDeadline
			if deadline.IsZero() {
				return nil, createProviderRecoveryBlock("initializer recovery requires the original admitted execution deadline")
			}
			var next createInitializerForwardProof
			var update, nextRetain *deploymentplan.Plan
			initializerReview := ""
			for _, item := range e.Journal.Entries {
				const prefix = "initializer-forward-"
				if item.Phase == "initializer-forward" && strings.HasPrefix(item.Key, prefix) {
					if initializerReview != "" {
						return nil, createProviderRecoveryBlock("initializer recovery admission is ambiguous")
					}
					initializerReview = strings.TrimPrefix(item.Key, prefix)
				}
			}
			if action == "create" && initializerReview == "" {
				initializerReview = selected
			}
			if initializerReview != "" {
				next, update, nextRetain, err = loadInitializerForwardBundle(store, e, initializerReview)
				if err != nil {
					return nil, err
				}
				if err = validateInitializerForward(ctx, store, e, next, q, admitted, admissionSHA, next.ExecutionDeadline, update, nextRetain); err != nil {
					return nil, err
				}

			} else {
				if action != "preflight" {
					return nil, createProviderRecoveryBlock("complete initializer recovery preflight is required")
				}
				objects, inventoryErr := inventory(ctx)
				if inventoryErr != nil {
					return nil, inventoryErr
				}
				next, update, nextRetain, err = prepareInitializerForward(ctx, store, e, backend, q, admitted, admissionSHA, deadline, objects, clock)
				if err != nil {
					return nil, err
				}
			}
			// An already-written q1 update can itself reveal a private-state
			// mount incompatibility. Qualify its one same-owner successor.
			previous, previousErr := readEnvironmentCreateStageInput(store, e, next.UpdateStage)
			if previousErr != nil {
				return nil, previousErr
			}
			previousFailed := false
			if previous != nil {
				journal, exists, readErr := previous.ReadExecution(store.Root)
				if readErr != nil {
					return nil, readErr
				}
				previousFailed = exists && journal.Failed != "" && !journal.Accepted
			}
			if previousFailed {
				mountReview := ""
				for _, item := range e.Journal.Entries {
					const prefix = "initializer-mount-forward-"
					if item.Phase == "initializer-mount-forward" && strings.HasPrefix(item.Key, prefix) {
						if mountReview != "" {
							return nil, createProviderRecoveryBlock("private state recovery admission is ambiguous")
						}
						mountReview = strings.TrimPrefix(item.Key, prefix)
					}
				}
				if action == "create" {
					mountReview = selected
				}
				var mount createInitializerMountForwardProof
				var mountUpdate, mountRetain *deploymentplan.Plan
				if mountReview != "" {
					mount, mountUpdate, mountRetain, err = loadInitializerMountForwardBundle(store, e, mountReview)
					if err != nil {
						return nil, err
					}
					if err = validateInitializerMountForward(ctx, store, e, mount, next, q, admitted, admissionSHA, update, nextRetain, mountUpdate, mountRetain, mount.ExecutionDeadline); err != nil {
						return nil, err
					}
					if err = verifyInitializerMountForwardOwners(ctx, store, e, backend, mount, update, mountUpdate); err != nil {
						return nil, err
					}
				} else {
					if action != "preflight" {
						return nil, createProviderRecoveryBlock("complete private state recovery preflight is required")
					}
					objects, inventoryErr := inventory(ctx)
					if inventoryErr != nil {
						return nil, inventoryErr
					}
					mount, mountUpdate, mountRetain, err = prepareInitializerMountForward(ctx, store, e, backend, next, q, admitted, admissionSHA, update, nextRetain, deadline, objects, clock)
					if err != nil {
						return nil, err
					}
				}
				if _, _, err := qualifyInitializerSessionRetain(ctx, store, e, backend, mount, q, mountRetain); err != nil {
					return nil, err
				}
				observedAt := clock().UTC()
				if observedAt.Before(mount.QualifiedAt) || !observedAt.Before(deadline) {
					return nil, createProviderRecoveryBlock("private state qualification is outside the original execution deadline")
				}
				if action == "create" {
					if err = executeInitializerMountForward(ctx, store, e, backend, mount, next, q, admitted, admissionSHA, update, nextRetain, mountUpdate, mountRetain, deadline, execute, accept, mountAlias, clock); err != nil {
						return nil, err
					}
				}
				next.mount = &mount
				q.initializer = &next
				return &q, nil
			}
			if r == nil || len(issuers) == 0 {
				return nil, createProviderRecoveryBlock("scoped private state repair requires its previously failed initializer")
			}
			if action == "create" && selected != initializerReview {
				return nil, createProviderRecoveryBlock("selected initializer qualification differs from the admitted repair")
			}
			if initializerReview != "" {
				if err = verifyInitializerForwardOwners(ctx, store, e, backend, next, update); err != nil {
					return nil, err
				}
			}
			observedAt := clock().UTC()
			if observedAt.Before(next.QualifiedAt) || !observedAt.Before(deadline) {
				return nil, createProviderRecoveryBlock("initializer qualification is outside the original execution deadline")
			}
			if action == "create" {
				if err = executeInitializerForward(ctx, store, e, backend, next, q, admitted, admissionSHA, update, nextRetain, deadline, execute, accept, clock); err != nil {
					return nil, err
				}
			}
			q.initializer = &next
			return &q, nil
		}
	}
	if action == "create" && selected != review {
		return nil, createProviderRecoveryBlock("the selected failed-stage qualification differs from the admitted original repair")
	}
	if r == nil || len(issuers) == 0 {
		return nil, createProviderRecoveryBlock("original application CRL repair issuer evidence is unavailable")
	}
	render := func(ctx context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		source, readErr := readEnvironmentCreateStageInput(store, e, q.FailedStage.Stage)
		if readErr != nil || source == nil || source.SHA256() != q.FailedStage.Child.PlanSHA256 {
			return nil, nil, createProviderRecoveryBlock("original application stage changed")
		}
		j, ok, readErr := source.ReadExecution(store.Root)
		if readErr != nil || !ok || deploymentplan.Digest(j) != q.FailedStage.Child.JournalSHA256 {
			return nil, nil, createProviderRecoveryBlock("original failed application journal changed")
		}
		trust, readErr := readLatestAcceptedCreateResource(ctx, store, e, backend, "ConfigMap/"+parent.Stack+"-video-cloud/pki-create-service-trust")
		if readErr != nil {
			return nil, nil, readErr
		}
		defs, readErr := renderCreateFailedForwardJobs(ctx, cfg, parent.Bootstrap, runtime, r.Ceremony, source, q.FailedStage.Child.PlanSHA256, trust, issuers, q.ToolImage)
		if readErr != nil {
			return nil, nil, readErr
		}
		defs, readErr = project(ctx, defs)
		if readErr != nil {
			return nil, nil, readErr
		}
		if readErr = verifyFailedForwardDefinitions(repair, defs); readErr != nil {
			return nil, nil, readErr
		}
		captured := make([]deploymentplan.Object, 0, len(retained.Resources))
		for _, resource := range retained.Resources {
			body := deploymentplan.Clone(resource.Baseline)
			if body == nil || resource.UID == "" {
				return nil, nil, createProviderRecoveryBlock("original retained owner baseline is missing")
			}
			deploymentplan.Map(body["metadata"])["uid"] = resource.UID
			captured = append(captured, body)
		}
		_, retainStage := failedForwardStageNames(q)
		again, readErr := compileFailedStageRetainForward(source, j, q.FailedStage, captured, retainStage)
		if readErr != nil || again.SHA256() != retained.SHA256() {
			return nil, nil, createProviderRecoveryBlock("original retained owners differ from qualified plan")
		}
		return repair, retained, nil
	}
	if action == "preflight" {
		proofTime := now
		if review != "" {
			proofTime = q.QualifiedAt
		}
		if _, err = verifyCreateFailedForwardLiveEvidence(ctx, q, admitted, admissionSHA, q.FailedStage, proofTime, store.Root, render, backend, command); err != nil {
			return nil, err
		}
		return &q, nil
	}
	if err = executeCreateFailedForward(ctx, store, e, backend, q, admitted, admissionSHA, render, command, execute, accept, now); err != nil {
		return nil, err
	}
	return &q, nil
}

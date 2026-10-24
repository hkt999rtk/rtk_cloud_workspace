package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Creation qualifies immutable stage intent before an API/UID exists. Each
// Kubernetes stage subsequently compiles and admits its actual exact objects
// through deploymentplan; successful preflight never invents future owners.
type environmentCreatePlan struct {
	Schema              string                         `json:"schema"`
	Environment         string                         `json:"environment"`
	Stack               string                         `json:"stack"`
	ConfigurationSHA256 string                         `json:"configuration_sha256"`
	OperatorSHA256      string                         `json:"operator_sha256"`
	Release             environmentRelease             `json:"release"`
	Provider            environmentCreateProviderPlan  `json:"provider"`
	Bootstrap           environmentCreateBootstrapPlan `json:"bootstrap"`
	DNS                 genericDNSPlan                 `json:"dns"`
	// New plans allocate foundation Redis durably from its first start. Omitted
	// historical plans retain their exact original foundation recipe.
	DurableFoundation bool                      `json:"durable_foundation,omitempty"`
	PublicTLS         *environmentPublicTLSPlan `json:"public_tls,omitempty"`
}

type environmentCreateDependencies struct {
	Release func(context.Context, deploymentConfig) (environmentRelease, []environmentReleasePublicationProof, error)
	Build   func(context.Context, deploymentConfig, environmentRelease, secretStore, map[string]string) (*environmentCreatePlan, error)
	Qualify func(context.Context, deploymentConfig, *environmentCreatePlan, secretStore, map[string]string, io.Writer) (deploymentplan.Qualification, error)
	Execute func(context.Context, deploymentConfig, *environmentCreatePlan, *environmentCreateExecution, secretStore, map[string]string, io.Writer) error
}

func defaultEnvironmentCreateDependencies() environmentCreateDependencies {
	return environmentCreateDependencies{
		Release: func(ctx context.Context, cfg deploymentConfig) (environmentRelease, []environmentReleasePublicationProof, error) {
			ctx = newEnvironmentReleaseProofCapture(ctx)
			release, err := loadEnvironmentCIImages(ctx, cfg, environmentCIImageCommand(cfg))
			return release, snapshotVerifiedReleaseProofs(ctx, release), err
		},
		Build:   buildEnvironmentCreatePlan,
		Qualify: qualifyEnvironmentCreatePlan,
		Execute: executeEnvironmentCreatePlan,
	}
}

func environmentCreateBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: id, Cause: cause, Next: "repair the selected fresh-environment prerequisite, repeat complete create preflight and review its exact plan SHA-256; retain original identities and partial-execution evidence"}
}

// Read only the selected canonical credentials. All directory ancestors and
// files are checked independently of future runtime credentials or kubeconfig.
func readEnvironmentCreateOperator(cfg deploymentConfig) (secretStore, map[string]string, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return store, nil, environmentCreateBlocker("deployment.create.inputs", "selected private environment store is unavailable")
	}
	directory := filepath.Join(store.Root, "operator", "env")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return store, nil, environmentCreateBlocker("deployment.create.inputs", "canonical operator credentials cannot be inspected")
	}
	operator := map[string]string{}
	for _, entry := range entries {
		if !operatorKeyPattern.MatchString(entry.Name()) || entry.IsDir() {
			return store, nil, environmentCreateBlocker("deployment.create.inputs", "operator credential catalog contains an invalid entry")
		}
		path, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("operator", "env", entry.Name()))
		var raw []byte
		if err == nil {
			raw, err = readBoundedEnvironmentInput(path, true, true)
		}
		if err != nil {
			blocker := environmentCreateBlocker("deployment.create.inputs", "canonical operator credentials require real 0700 directories and bounded regular 0600 files")
			blocker.(*deploymentplan.Blocker).Resource = filepath.Join("operator", "env", entry.Name())
			return store, nil, blocker
		}
		operator[entry.Name()] = strings.TrimSpace(string(raw))
	}
	publicPath, pathErr := privateDeploymentStatePath(cfg.Environment, filepath.Join("operator", "ssh", "id_ed25519.pub"))
	if pathErr != nil {
		return store, nil, environmentCreateBlocker("deployment.create.inputs", "canonical deployment SSH identity permissions cannot be verified")
	}
	if raw, readErr := readBoundedEnvironmentFile(publicPath, true); readErr == nil {
		public := strings.TrimSpace(string(raw))
		if existing := operator["DEPLOYMENT_SSH_PUBLIC_KEY"]; existing != "" && existing != public {
			return store, nil, environmentCreateBlocker("deployment.create.inputs", "configured SSH public key differs from the selected canonical key pair")
		}
		operator["DEPLOYMENT_SSH_PUBLIC_KEY"] = public
	}
	return store, operator, nil
}

func buildEnvironmentCreatePlan(ctx context.Context, cfg deploymentConfig, release environmentRelease, store secretStore, operator map[string]string) (*environmentCreatePlan, error) {
	provider, err := buildEnvironmentCreateProviderPlan(ctx, cfg, operator)
	if err != nil {
		return nil, err
	}
	bootstrap, err := buildEnvironmentCreateBootstrapPlan(ctx, cfg, release, store, operator)
	if err != nil {
		return nil, err
	}
	publicTLS, err := prepareEnvironmentCreatePublicTLS(ctx, cfg, store)
	if err != nil {
		return nil, err
	}
	allocations, err := qualifyEnvironmentCreateDependencyContract(ctx, cfg, release, store, operator, bootstrap)
	if err != nil {
		return nil, err
	}
	plan := &environmentCreatePlan{Schema: "rtk-cloud.create-plan/v1", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: deploymentplan.Digest(operator), Release: release, Provider: provider, Bootstrap: bootstrap, DNS: buildGenericDNSPlan(cfg), DurableFoundation: true, PublicTLS: publicTLS}
	registrarSHA, err := qualifyEnvironmentCreateDataPlaneArtifact(ctx, cfg, plan, store)
	if err != nil {
		return nil, err
	}
	plan.Provider.TurnRegistrarImage = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	plan.Provider.TurnRegistrarSHA256 = registrarSHA
	plan.Provider.WorkloadAllocations = allocations
	return plan, nil
}

func qualifyEnvironmentCreatePlan(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, out io.Writer) (deploymentplan.Qualification, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return deploymentplan.Qualification{}, err
	}
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return deploymentplan.Qualification{}, err
	}
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "NO-GO"}
	checks := []struct {
		id  string
		run func(context.Context) error
	}{
		{"deployment.create.target", func(ctx context.Context) error { return checkEnvironmentCreateAbsence(ctx, cfg, store) }},
		{"deployment.create.release", func(ctx context.Context) error { return verifyEnvironmentRelease(ctx, cfg, plan.Release) }},
		{"deployment.create.provider", func(ctx context.Context) error {
			return qualifyEnvironmentCreateProviders(ctx, cfg, plan.Provider, operator)
		}},
		{"deployment.create.bootstrap", func(ctx context.Context) error {
			if err := qualifyEnvironmentCreateBootstrap(ctx, cfg, plan.Bootstrap, store, operator); err != nil {
				return err
			}
			if plan.PublicTLS == nil {
				return nil // Omitted historical plans keep their original wire/recipe.
			}
			current, err := prepareEnvironmentCreatePublicTLS(ctx, cfg, store)
			if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(plan.PublicTLS) {
				return environmentCreateBlocker("deployment.create.public-tls", "public certificate intent changed before qualification")
			}
			return checkEnvironmentPublicTLSPrerequisites(ctx, cfg, store, plan.PublicTLS)
		}},
		{"deployment.create.network", func(ctx context.Context) error { return qualifyEnvironmentCreateDNS(ctx, cfg, plan.DNS, operator) }},
		{"deployment.create.inputs", func(ctx context.Context) error {
			fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
			_, current, readErr := readEnvironmentCreateOperator(cfg)
			if err != nil || readErr != nil || deploymentplan.Digest(fresh) != plan.ConfigurationSHA256 || deploymentplan.Digest(current) != plan.OperatorSHA256 {
				return environmentCreateBlocker("deployment.create.inputs", "maintained inputs or canonical operator credentials changed during qualification")
			}
			return ctx.Err()
		}},
	}
	passed := true
	for _, check := range checks {
		err := runEnvironmentProgress(ctx, out, check.id, check.run)
		row := deploymentplan.GateResult{ID: check.id, Status: "PASS", Required: true, EvidenceSHA256: sha}
		if err != nil {
			passed = false
			row.Status, row.EvidenceSHA256 = "BLOCKED", ""
			row.Cause, row.Next = "required fresh-environment evidence could not be verified", "repair this prerequisite and repeat complete create preflight"
			var blocker *deploymentplan.Blocker
			if errors.As(err, &blocker) {
				row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
			}
			row.Impact, row.Recheck = "fresh deployment is not authorized", "repeat complete create qualification for the exact inputs and release"
		}
		q.Gates = append(q.Gates, row)
	}
	if passed {
		q.Decision = "GO"
		return q, nil
	}
	return q, errors.New("fresh environment qualification is NO-GO")
}

// Fresh stage qualification proves native absence and retained-generation
// cleanup without requiring the kubeconfig that deployment will create.
func checkEnvironmentCreateAbsence(ctx context.Context, cfg deploymentConfig, store secretStore) error {
	return qualifyEnvironmentCreateTargetAbsence(ctx, cfg, store)
}

func runUnifiedFreshEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer, deps environmentCreateDependencies) error {
	report.Scope = "environment-bootstrap"
	generation := ""
	if o.Action == "preflight" {
		var invalidateErr error
		generation, invalidateErr = invalidateEnvironmentPreflightProofWithToken(cfg)
		if invalidateErr != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, invalidateErr)
		}
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	if o.Action == "create" {
		if deps.Execute == nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, environmentCreateBlocker("deployment.create.execution", "complete create execution adapter is unavailable"))
		}
		var artifact *environmentCreateArtifact
		err := runEnvironmentProgress(ctx, out, "deployment.preflight.evidence", func(context.Context) error {
			var loadErr error
			artifact, loadErr = loadEnvironmentCreateArtifact(cfg, deploymentplan.Digest(operator), o.PlanSHA256, time.Now().UTC())
			return loadErr
		})
		if err != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
		}
		var plan environmentCreatePlan
		if decodeEnvironmentJSON(artifact.Plan, &plan) != nil || validateEnvironmentCreatePlan(cfg, &plan, operator) != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, environmentCreateBlocker("deployment.preflight.proof", "qualified create stage plan is invalid or no longer matches selected inputs"))
		}
		report.Plan = environmentCreateSummary(&plan, artifact.PlanSHA256)
		report.ReusedPreflight = true
		report.QualifiedAt, report.EvidenceValidUntil = &artifact.QualifiedAt, &artifact.ValidUntil
		if err := runEnvironmentProgress(ctx, out, "deployment.create.target", func(ctx context.Context) error {
			return checkEnvironmentCreateAbsence(ctx, cfg, store)
		}); err != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
		}
		admitted := time.Now().UTC()
		execution, err := startEnvironmentCreateExecution(ctx, cfg, artifact, deploymentplan.Digest(operator), admitted)
		if err != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
		}
		defer execution.Close()
		report.ExecutionAdmittedAt = &admitted
		if deadline, ok := ctx.Deadline(); ok {
			report.ExecutionDeadline = &deadline
		}
		if deps.Execute == nil {
			err = environmentCreateBlocker("deployment.create.execution", "complete create execution adapter is unavailable")
		} else {
			err = deps.Execute(ctx, cfg, &plan, execution, store, operator, out)
		}
		if err == nil {
			err = execution.CompleteAndReleaseContext(ctx, time.Now().UTC())
		}
		if err != nil {
			return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
		}
		report.Decision, report.Completed = "GO", true
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	if err := checkEnvironmentCreateAbsence(ctx, cfg, store); err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	if deps.Release == nil || deps.Build == nil || deps.Qualify == nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, environmentCreateBlocker("deployment.create.adapter", "complete create qualification adapters are unavailable"))
	}
	var release environmentRelease
	var releaseProofs []environmentReleasePublicationProof
	err = runEnvironmentProgress(ctx, out, "deployment.release.resolve", func(ctx context.Context) error {
		var resolveErr error
		release, releaseProofs, resolveErr = deps.Release(ctx, cfg)
		return resolveErr
	})
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	plan, err := deps.Build(ctx, cfg, release, store, operator)
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	report.Plan = environmentCreateSummary(plan, sha)
	if o.Action == "plan" {
		report.Decision = "PLANNED"
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	qualification, err := deps.Qualify(ctx, cfg, plan, store, operator, out)
	report.Gates = qualification.Gates
	if err != nil || qualification.Decision != "GO" {
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	artifact, err := publishEnvironmentCreateArtifact(ctx, cfg, generation, raw, deploymentplan.Digest(operator), qualification, releaseProofs, time.Now().UTC())
	if err != nil {
		return emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	report.Decision, report.QualificationSHA256 = "GO", deploymentplan.Digest(artifact)
	report.QualifiedAt, report.EvidenceValidUntil = &artifact.QualifiedAt, &artifact.ValidUntil
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

func validateEnvironmentCreatePlan(cfg deploymentConfig, plan *environmentCreatePlan, operator map[string]string) error {
	if plan == nil || plan.Schema != "rtk-cloud.create-plan/v1" || plan.Environment != cfg.Environment || plan.Stack != cfg.Values["CLOUD_STACK_NAME"] || plan.ConfigurationSHA256 != deploymentplan.Digest(cfg) || plan.OperatorSHA256 != deploymentplan.Digest(operator) || plan.Release.Environment != cfg.Environment || plan.Release.Stack != plan.Stack {
		return environmentCreateBlocker("deployment.create.inputs", "create plan is not bound to the exact selected environment, configuration, credentials and formal release")
	}
	return validateEnvironmentCreateProviderPlan(cfg, plan.Provider)
}

func environmentCreateSummary(plan *environmentCreatePlan, sha string) *deploymentplan.Summary {
	summary := &deploymentplan.Summary{SchemaVersion: 1, Environment: plan.Environment, Operation: "create", PlanSHA256: sha}
	for _, resource := range plan.Provider.Resources {
		summary.Resources = append(summary.Resources, deploymentplan.SummaryResource{ID: resource.Kind + "/" + resource.Name, Phase: "provider", Action: "create"})
	}
	for _, phase := range []string{"bootstrap", "workload", "routing", "acceptance"} {
		summary.Resources = append(summary.Resources, deploymentplan.SummaryResource{ID: "Stage/" + phase, Phase: phase, Action: "create"})
	}
	return summary
}

func emitEnvironmentCreateFailure(out, diagnostics io.Writer, o unifiedDeploymentOptions, report *unifiedDeploymentReport, err error) error {
	row := deploymentplan.GateResult{ID: "deployment.create", Status: "BLOCKED", Required: true, Cause: "complete create stage failed or lacks verified evidence", Next: "inspect the selected create report and original private journal, reconcile actual outcomes and repeat exact qualification", Impact: "fresh deployment is incomplete", Recheck: "verify actual provider, Kubernetes, PKI and required public endpoints"}
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) {
		row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
	}
	if errors.Is(err, context.DeadlineExceeded) {
		row.Cause = "deployment execution timeout exceeded; original stage evidence and ownership are retained"
	}
	if errors.Is(err, context.Canceled) {
		row.Cause = "deployment execution cancelled; original stage evidence and ownership are retained"
	}
	report.Gates = append(report.Gates, row)
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
}

func executeEnvironmentCreatePlan(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, execution *environmentCreateExecution, store secretStore, operator map[string]string, out io.Writer) error {
	// Live fences are narrower than full qualification. Never resolve CI images
	// again, extend the saved TTL or silently compile another approved plan.
	if err := validateEnvironmentCreatePlan(cfg, plan, operator); err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateBootstrapLocalState(ctx, cfg, store, operator); err != nil {
		return err
	}
	if execution == nil || execution.Store.Root != store.Root || execution.Journal.Environment != cfg.Environment || execution.Journal.Stack != plan.Stack {
		return environmentCreateBlocker("deployment.create.execution", "original admitted selected create execution is required before provider allocation")
	}
	if err := execution.Check(ctx); err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateProviders(ctx, cfg, plan.Provider, operator); err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateDNS(ctx, cfg, plan.DNS, operator); err != nil {
		return err
	}
	if err := verifyEnvironmentImageSources(ctx, cfg, plan.Release, environmentCIImageCommand(cfg)); err != nil {
		return err
	}
	receipts := map[string]environmentCreateProviderReceipt{}
	for _, resource := range plan.Provider.Resources {
		resource := resource
		if err := runEnvironmentProgress(ctx, out, "deployment.create.provider."+resource.Key, func(ctx context.Context) error {
			material, err := prepareEnvironmentCreateProviderMaterial(ctx, cfg, execution.Journal.PlanSHA256, resource, store)
			if err != nil {
				return err
			}
			intent, err := json.Marshal(struct {
				Resource       environmentCreateProviderResource `json:"resource"`
				MaterialSHA256 string                            `json:"material_sha256"`
			}{resource, deploymentplan.Digest(material)})
			if err != nil {
				return err
			}
			if err := execution.BeginEntry(resource.Key, "provider", intent, time.Now().UTC()); err != nil {
				return err
			}
			receipt, allocateErr := executeEnvironmentCreateProviderResource(ctx, cfg, resource, operator, material)
			if receipt.NativeID != "" {
				body, err := json.Marshal(receipt)
				if err != nil {
					return err
				}
				if err := execution.RecordReceipt(resource.Key, body); err != nil {
					return err
				}
				if err := recordEnvironmentCreateProviderOwner(ctx, execution, receipt); err != nil {
					return err
				}
			}
			if allocateErr != nil {
				return allocateErr
			}
			for {
				updated, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, receipt, operator)
				if err != nil {
					return err
				}
				receipt = updated
				if ready {
					break
				}
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			receipts[resource.Key] = receipt
			if err := recordEnvironmentCreateProviderOwner(ctx, execution, receipt); err != nil {
				return err
			}
			return execution.CompleteEntry(resource.Key, time.Now().UTC())
		}); err != nil {
			return err
		}
	}
	if err := installEnvironmentCreateKubeconfig(ctx, cfg, store, receipts["lke"], operator, execution); err != nil {
		return err
	}
	return executeEnvironmentCreateAfterProvider(ctx, cfg, plan, receipts, store, operator, execution, out)
}

// Allocated-provider recovery enters here only after independently qualifying
// every original allocation and the unchanged canonical Kubernetes identity.
// It never calls an allocation endpoint or fetches replacement cluster access.
func executeEnvironmentCreateAfterProvider(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution, out io.Writer) error {
	if err := activateEnvironmentCreateStorage(ctx, cfg, plan, receipts, store, operator, execution); err != nil {
		return err
	}
	if err := initializeEnvironmentCreateWorkerNames(ctx, cfg, store, operator, receipts["lke"], execution); err != nil {
		return err
	}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		return applyEnvironmentCreateStage(ctx, cfg, store, plan, execution, stage, definitions, out)
	}
	runtime, closeRuntime, err := prepareEnvironmentCreateBootstrap(ctx, cfg, plan, store, operator, execution, apply)
	if err != nil {
		return err
	}
	defer closeRuntime()
	return executeEnvironmentCreateWithRuntime(ctx, cfg, plan, receipts, store, operator, execution, out, runtime, apply)
}

func executeEnvironmentCreateWithRuntime(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution, out io.Writer, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	bootstrapCtx := withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	bindings, err := executeEnvironmentCreateBootstrap(bootstrapCtx, cfg, plan.Bootstrap, plan.Release, store, operator, apply)
	if err != nil {
		return err
	}
	ctx, err = managedLateContextAfterFinalization(ctx, runtime)
	if err != nil {
		return err
	}
	bootstrapCtx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	if plan.PublicTLS != nil {
		if err := executeEnvironmentCreatePublicTLS(bootstrapCtx, cfg, plan, store, execution, apply); err != nil {
			return err
		}
		live, err := runtime.Backend.Inventory(bootstrapCtx, cfg.Values["CLOUD_STACK_NAME"])
		if err != nil {
			return err
		}
		bindings, err = observeEnvironmentTargetBindings(bootstrapCtx, cfg, plan.Release, "create", live)
		if err != nil {
			return err
		}
		bootstrapCtx = context.WithValue(bootstrapCtx, environmentCreatePublicTLSKey{}, plan.PublicTLS)
	}
	compiled, err := compileCreatedEnvironmentTarget(bootstrapCtx, cfg, plan, store, bindings, execution.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	workload := createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/workload")
	var originalWorkload *deploymentplan.Plan
	if workload != nil {
		originalWorkload, err = readEnvironmentCreateStageInput(store, execution, "workload")
		if err != nil {
			return err
		}
		if originalWorkload == nil {
			originalWorkload, _, err = originalCreateChildAtIntent(store, execution, *workload)
			if err != nil {
				return err
			}
		}
		journal, exists, readErr := readEnvironmentCreateStageProgress(store, execution, "workload", originalWorkload)
		if readErr != nil {
			return readErr
		}
		if workload.State == "completed" && (!exists || !journal.Accepted) {
			return environmentCreateBootstrapBlocker("original fresh workload has no accepted child")
		}
		// Finalization deliberately supersedes the initial ACK environment.
		// Keep that original child as history; finish the declared steady stage.
		compiled.Plan = originalWorkload
	}
	if workload == nil || workload.State != "completed" {
		if err := apply(bootstrapCtx, "workload", environmentCreateDefinitions(compiled.Plan)); err != nil {
			return err
		}
	}
	compiled, err = finishEnvironmentCreateFreshProducts(bootstrapCtx, cfg, plan, store, operator, execution, compiled, apply)
	if err != nil {
		return err
	}
	if err := configureEnvironmentCreateExternalDataPlanes(bootstrapCtx, cfg, plan, receipts, store, operator, execution); err != nil {
		return err
	}
	if err := executeEnvironmentCreateDNS(bootstrapCtx, cfg, plan.DNS, receipts, operator, execution); err != nil {
		return err
	}
	if err := activateEnvironmentCreateTURNRegistrars(bootstrapCtx, cfg, plan, receipts, store, operator, execution); err != nil {
		return err
	}
	if err := acceptEnvironmentRollout(bootstrapCtx, compiled, defaultDeploymentCheckDependencies(), out); err != nil {
		return err
	}
	return captureEnvironmentCreateResources(bootstrapCtx, cfg, compiled, receipts, execution)
}

func environmentCreateDefinitions(plan *deploymentplan.Plan) []deploymentplan.Definition {
	var definitions []deploymentplan.Definition
	for _, resource := range plan.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if resource.UID != "" {
			metadata := deploymentplan.Map(object["metadata"])
			if metadata == nil {
				metadata = deploymentplan.Object{}
				object["metadata"] = metadata
			}
			metadata["uid"] = resource.UID
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: resource.Phase, Dependencies: resource.Dependencies, Retain: resource.Action == "retain", Retire: resource.Action == "retire"})
	}
	return definitions
}

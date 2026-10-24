package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The remaining create program freezes the complete consumer catalog together
// with its native producers. A future issuer ID, key or Secret is an output;
// it is never represented by a fictitious executable Kubernetes resource.
type createNormalTailProgram struct {
	Schema, ParentSHA256, OwnerSHA256, HandoffSHA256 string
	ConfigurationSHA256, MaintainedConfigSHA256      string
	ClusterSHA256, IdentitySHA256                    string
	Release                                          environmentRelease
	Bindings                                         environmentIdentityBindings
	Products                                         *createNormalTailProductLifecycle
	PublicTLS                                        *environmentPublicTLSPlan `json:"PublicTLS,omitempty"`
	Definitions                                      []deploymentplan.Definition
}

type createNormalTailProgramArtifact struct {
	Program                   createNormalTailProgram
	Generation, CheckerSHA256 string
	Qualification             deploymentplan.Qualification
	QualifiedAt, ValidUntil   time.Time
	ReleaseProofs             []environmentReleasePublicationProof
}

type createNormalTailProgramAdmission struct {
	ProgramSHA256, QualificationSHA256 string
	AdmittedAt, Deadline               time.Time
}

func verifyCreateNormalTailProgramAdmission(store secretStore, e *environmentCreateExecution) error {
	_, err := readCreateNormalTailProgramAdmission(store, e)
	return err
}

// Validate historical admission without applying the current clock. Fresh
// native writes separately require now < Deadline; retries retain this start.
func readCreateNormalTailProgramOriginalAdmission(store secretStore, e *environmentCreateExecution) (createNormalTailProgramAdmission, error) {
	var receipt createNormalTailProgramAdmission
	var a createNormalTailProgramArtifact
	if _, err := readCreateNormalTailProgram(store, e); err != nil {
		return receipt, err
	}
	if readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &a) != nil {
		return receipt, environmentCreateBlocker("deployment.create.program", "original program qualification is unavailable")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program")
	intent := deploymentplan.Object{"program_sha256": deploymentplan.Digest(a.Program), "qualification_sha256": deploymentplan.Digest(a)}
	if entry == nil || entry.Phase != "workload" || entry.State != "completed" || entry.IntentSHA256 != deploymentplan.Digest(intent) || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.ProgramSHA256 != intent["program_sha256"] || receipt.QualificationSHA256 != intent["qualification_sha256"] || receipt.AdmittedAt.Before(a.QualifiedAt) || !receipt.AdmittedAt.Before(a.ValidUntil) || !receipt.Deadline.After(receipt.AdmittedAt) || !entry.StartedAt.Equal(receipt.AdmittedAt) || entry.CompletedAt.Before(entry.StartedAt) || !entry.CompletedAt.Before(receipt.Deadline) {
		return receipt, environmentCreateBlocker("deployment.create.program", "original program admission lacks a genuine fixed-window receipt")
	}
	return receipt, nil
}

func admitCreateNormalTailProgram(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, a createNormalTailProgramArtifact, timeout time.Duration, now time.Time, total ...time.Duration) (createNormalTailProgramAdmission, error) {
	return admitCreateNormalTailProgramWithFence(ctx, cfg, h, a, timeout, now, func(ctx context.Context) error { return createNormalTailProgramFence(ctx, cfg, h, &a.Program) }, total...)
}

func admitCreateNormalTailProgramWithFence(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, a createNormalTailProgramArtifact, timeout time.Duration, now time.Time, fence func(context.Context) error, total ...time.Duration) (createNormalTailProgramAdmission, error) {
	var receipt createNormalTailProgramAdmission
	if h == nil || h.Execution == nil || fence == nil {
		return receipt, environmentCreateBlocker("deployment.create.program", "original program admission adapters are unavailable")
	}
	e := h.Execution
	if err := validateCreateNormalTailProgramArtifact(a, e); err != nil {
		return receipt, err
	}
	if err := fence(ctx); err != nil {
		return receipt, err
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program") != nil {
		original, err := readCreateNormalTailProgramOriginalAdmission(e.Store, e)
		if err != nil || original.ProgramSHA256 != deploymentplan.Digest(a.Program) {
			return receipt, environmentCreateBlocker("deployment.execution.deadline", "the original remaining-program admission changed")
		}
		return admitCreateNormalTailProgramWindow(ctx, e, original, now, total...)
	}
	if len(total) > 1 || len(total) == 1 && total[0] != 0 {
		return receipt, environmentCreateBlocker("deployment.create.program", "a total window can only supplement an existing program admission")
	}
	active, err := readEnvironmentPreflightActive(e.Store)
	checker, checkerErr := environmentPreflightCheckerSHA256()
	if err != nil || checkerErr != nil || checker != a.CheckerSHA256 || active.Generation != a.Generation || active.PlanSHA256 != deploymentplan.Digest(a.Program) || active.ProofSHA256 != deploymentplan.Digest(a) || active.Decision != "GO" || now.Before(a.QualifiedAt) || !now.Before(a.ValidUntil) || timeout <= 0 {
		return receipt, environmentCreateBlocker("deployment.preflight.proof", "the exact current complete-program GO is not available for first admission")
	}
	receipt = createNormalTailProgramAdmission{ProgramSHA256: deploymentplan.Digest(a.Program), QualificationSHA256: deploymentplan.Digest(a), AdmittedAt: now, Deadline: now.Add(timeout)}
	intent := deploymentplan.Object{"program_sha256": receipt.ProgramSHA256, "qualification_sha256": receipt.QualificationSHA256}
	if err := e.Check(ctx); err != nil {
		return receipt, err
	}
	// Admission is a single local journal fact, not a native operation. Commit
	// it atomically before any external effect instead of creating an artificial
	// pending native request that would require reconciliation after a crash.
	before := e.Journal.Entries
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "normal-tail/program", Phase: "workload", State: "completed", IntentSHA256: deploymentplan.Digest(intent), Receipt: mustProviderRecoveryJSON(receipt), StartedAt: now, CompletedAt: now})
	if err := e.save(); err != nil {
		e.Journal.Entries = before
		return receipt, err
	}
	return receipt, nil
}

const createNormalTailProgramSchema = "rtk-cloud.create-remaining-program/v1"

func createNormalTailProgramPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-program.json")
}

// Historical readers authenticate this one original program. Current-time
// admission is checked separately so a completed producer does not expire.
func readCreateNormalTailProgram(store secretStore, e *environmentCreateExecution) (*createNormalTailProgram, error) {
	var a createNormalTailProgramArtifact
	if e == nil || e.Store != store || readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &a) != nil {
		return nil, environmentCreateBlocker("deployment.create.program", "the original complete remaining program cannot be read")
	}
	if err := validateCreateNormalTailProgramArtifact(a, e); err != nil {
		return nil, err
	}
	return &a.Program, nil
}

func validateCreateNormalTailProgramArtifact(a createNormalTailProgramArtifact, e *environmentCreateExecution) error {
	p := a.Program
	if e == nil || p.Schema != createNormalTailProgramSchema || p.ParentSHA256 != e.Journal.PlanSHA256 || p.OwnerSHA256 != deploymentplan.Digest(e.owner) || !environmentPreflightDigest.MatchString(p.HandoffSHA256) || !environmentPreflightDigest.MatchString(p.ConfigurationSHA256) || !environmentPreflightDigest.MatchString(p.MaintainedConfigSHA256) || !environmentPreflightDigest.MatchString(a.CheckerSHA256) || !environmentPreflightDigest.MatchString(p.ClusterSHA256) || !environmentPreflightDigest.MatchString(p.IdentitySHA256) || !environmentPreflightDigest.MatchString(a.Generation) || len(p.Definitions) == 0 || p.Products == nil || p.Products.ParentSHA256 != p.ParentSHA256 || p.Products.OwnerSHA256 != p.OwnerSHA256 || len(p.Products.Outputs) == 0 || deploymentplan.Digest(p.Products.SourceDefinitions) != deploymentplan.Digest(p.Definitions) {
		return environmentCreateBlocker("deployment.create.program", "remaining program does not bind its complete source, original owner and native producers")
	}
	if _, err := environmentPublicTLSOutputContract(p.PublicTLS); err != nil {
		return err
	}
	ids := deploymentplan.RequiredGateIDs(false)
	sha := deploymentplan.Digest(p)
	if a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != sha || len(a.Qualification.Gates) != len(ids) || a.QualifiedAt.IsZero() || !a.ValidUntil.After(a.QualifiedAt) {
		return environmentCreateBlocker("deployment.create.program", "complete remaining program qualification is unavailable")
	}
	for i, row := range a.Qualification.Gates {
		if row.ID != ids[i] || !row.Required || row.Status != "PASS" || row.EvidenceSHA256 != sha {
			return environmentCreateBlocker("deployment.create.program", "remaining program has incomplete qualification evidence")
		}
	}
	return nil
}

func createNormalTailProductOutputContracts(cfg deploymentConfig, p *createNormalTailProductLifecycle) (map[string]string, error) {
	expected, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || p == nil || len(expected) != len(p.Outputs) || p.Root.ID == "" || !environmentPreflightDigest.MatchString(p.Root.RootFingerprint) {
		return nil, environmentCreateBootstrapBlocker("complete enabled product output contracts and original Service Root are required")
	}
	outputs := map[string]string{}
	for i, output := range p.Outputs {
		base := output
		base.RootID, base.RootSHA256, base.IssuerID, base.PolicySHA256 = "", "", "", ""
		if deploymentplan.Digest(base) != deploymentplan.Digest(expected[i]) || output.RootID != p.Root.ID || output.RootSHA256 != p.Root.RootFingerprint || output.PolicySHA256 != deploymentplan.Digest(p.Policy) || !slices.Contains(p.Policy.ServiceClientIDs, output.Subject) || outputs[output.ID] != "" || p.RequiresSuccessor && output.IssuerID != "" || !p.RequiresSuccessor && output.IssuerID != p.Intermediate.ID {
			return nil, environmentCreateBootstrapBlocker("product output differs from its exact enabled subject, original Root or native issuer policy")
		}
		outputs[output.ID] = deploymentplan.Digest(output)
	}
	return outputs, nil
}

func buildCreateNormalTailProgram(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, rendered *renderedEnvironmentTarget) (*createNormalTailProgram, error) {
	if h == nil || h.Execution == nil || rendered == nil || rendered.Store != h.Execution.Store {
		return nil, environmentCreateBlocker("deployment.create.program", "original complete target and held execution are required")
	}
	products, err := prepareCreateNormalTailProducts(ctx, cfg, h, rendered.Backend)
	if err != nil {
		return nil, err
	}
	if err := bindCreateNormalTailProductRelease(cfg, products, rendered.Release); err != nil {
		return nil, err
	}
	outputs, err := createNormalTailProductOutputContracts(cfg, products)
	if err != nil {
		return nil, err
	}
	publicTLS, err := prepareEnvironmentPublicTLS(ctx, cfg, rendered.Store, rendered.Definitions, rendered.Live, rendered.Bindings)
	if err != nil {
		return nil, err
	}
	publicOutputs, err := environmentPublicTLSOutputContract(publicTLS)
	if err != nil {
		return nil, err
	}
	for id, contract := range publicOutputs {
		if outputs[id] != "" {
			return nil, environmentCreateBlocker("deployment.create.program", "multiple producers claim the same public TLS output: "+id)
		}
		outputs[id] = contract
	}
	definitions, err := bindEnvironmentRuntimeDependencyOwners(rendered.Definitions, rendered.RuntimeDependencies, rendered.Live, rendered.Bindings.Owners)
	if err != nil {
		return nil, err
	}
	if err := bindOpenBaoAllocations(definitions, rendered.Live); err != nil {
		return nil, err
	}
	if err := validateEnvironmentPKITransition(definitions, rendered.Live); err != nil {
		return nil, err
	}
	if err := validateEnvironmentDurableStorageTransition(definitions, rendered.Live); err != nil {
		return nil, err
	}
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "create", ConfigurationSHA256: environmentDeploymentSnapshotDigest(ctx, cfg, rendered.Snapshot), ReleaseSHA256: deploymentplan.Digest(rendered.Release), BindingsSHA256: deploymentplan.Digest(rendered.Bindings), CreateParentSHA256: h.Execution.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}
	definitions, err = validateEnvironmentDeploymentDependencyContract(input, definitions, rendered.Live, rendered.Bindings, outputs)
	if err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		if err := validateEnvironmentIssuerStartup(definition.Object); err != nil {
			return nil, err
		}
	}
	cluster, identity, err := environmentPreflightKubernetesIdentity(rendered.Backend.KubeconfigContent)
	if err != nil {
		return nil, err
	}
	for id, uid := range h.Bindings.Owners {
		if rendered.Bindings.Owners[id] != uid {
			return nil, environmentCreateBlocker("deployment.ownership.drift", "the completed handoff's original owner changed before program qualification: "+id)
		}
	}
	products.SourceOwners = rendered.Bindings.Owners
	if err := bindCreateNormalTailProductSourceDefinitions(products, definitions, rendered.Live, rendered.Bindings); err != nil {
		return nil, err
	}
	p := &createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: input.CreateParentSHA256, OwnerSHA256: deploymentplan.Digest(h.Execution.owner), HandoffSHA256: input.CreateHandoffSHA256, ConfigurationSHA256: input.ConfigurationSHA256, MaintainedConfigSHA256: deploymentplan.Digest(cfg), ClusterSHA256: cluster, IdentitySHA256: identity, Release: rendered.Release, Bindings: rendered.Bindings, Products: products, PublicTLS: publicTLS, Definitions: definitions}
	return p, h.Execution.Check(ctx)
}

// This temporary projection exists only for static ordering, capacity, access
// and server dry-run checks. It never becomes a qualified executable target.
// The original definitions and producer edges remain in the program artifact.
func createNormalTailProgramAdmissionPreview(cfg deploymentConfig, p *createNormalTailProgram, live []deploymentplan.Object) (*deploymentplan.Plan, error) {
	outputs, err := createNormalTailProductOutputContracts(cfg, p.Products)
	if err != nil {
		return nil, err
	}
	publicOutputs, err := environmentPublicTLSOutputContract(p.PublicTLS)
	if err != nil {
		return nil, err
	}
	for id, contract := range publicOutputs {
		outputs[id] = contract
	}
	known := map[string]bool{}
	for _, d := range p.Definitions {
		known[deploymentplan.ID(d.Object)] = true
	}
	var defs []deploymentplan.Definition
	for _, d := range p.Definitions {
		copy := d
		copy.Dependencies = nil
		for _, id := range d.Dependencies {
			if outputs[id] == "" || known[id] {
				copy.Dependencies = append(copy.Dependencies, id)
			}
		}
		defs = append(defs, copy)
	}
	claims, err := createNormalTailProductClaimDefinitions(cfg, p.Products)
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if !known[deploymentplan.ID(claim.Object)] {
			defs = append(defs, claim)
		}
	}
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "create", ConfigurationSHA256: p.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: deploymentplan.Digest(p.Bindings), CreateParentSHA256: p.ParentSHA256, CreateHandoffSHA256: p.HandoffSHA256}
	plan, err := deploymentplan.Compile(input, defs, live, p.Bindings.Owners)
	if err != nil {
		return nil, err
	}
	if err := qualifyEnvironmentOnDeleteOpenBaoPlan(plan, defs, live); err != nil {
		return nil, err
	}
	return plan, nil
}

func createNormalTailProgramFence(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, p *createNormalTailProgram) error {
	if p == nil || h == nil || h.Execution == nil || p.ParentSHA256 != h.Execution.Journal.PlanSHA256 || p.OwnerSHA256 != deploymentplan.Digest(h.Execution.owner) || p.HandoffSHA256 != deploymentplan.Digest(h.Pins) {
		return environmentCreateBlocker("deployment.create.program", "remaining program changed its original execution")
	}
	fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
	if err != nil || deploymentplan.Digest(fresh) != p.MaintainedConfigSHA256 {
		return environmentCreateBlocker("deployment.inputs.changed", "maintained full-program inputs changed")
	}
	digest, err := environmentDeploymentInputDigestContext(ctx, fresh, h.Execution.Store)
	if err != nil {
		return err
	}
	if digest != p.ConfigurationSHA256 {
		if err := checkCreateNormalTailRuntimeInputCorrection(ctx, fresh, h, p, digest); err != nil {
			return err
		}
	}
	if err := verifyEnvironmentImageSources(ctx, fresh, p.Release, localEnvironmentReleaseSourceCommand); err != nil {
		return err
	}
	return h.Execution.Check(ctx)
}

func saveCreateNormalTailProgramArtifact(store secretStore, e *environmentCreateExecution, a createNormalTailProgramArtifact) error {
	if err := validateCreateNormalTailProgramArtifact(a, e); err != nil {
		return err
	}
	path, err := filepath.Rel(store.Root, createNormalTailProgramPath(e))
	if err != nil || filepath.IsAbs(path) {
		return errors.New("remaining program is outside its original private execution")
	}
	return store.write(path, mustProviderRecoveryJSON(a), true)
}

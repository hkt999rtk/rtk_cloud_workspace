package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The captured original snapshot authorizes only the finite selected Billing
// inputs. All other canonical inputs and the maintained configuration stay exact.
func checkCreateNormalTailRuntimeInputCorrection(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, p *createNormalTailProgram, digest string) error {
	source, err := readCreateNormalTailRuntimeSource(h.Execution.Store, h.Execution)
	if err != nil {
		return err
	}
	if source == nil || source.ProgramSHA256 != deploymentplan.Digest(p) {
		return createNormalTailRuntimeBlock("original input source required")
	}
	current, err := readEnvironmentDeploymentSnapshot(cfg, h.Execution.Store)
	if err != nil {
		return err
	}
	current.DNSStateSHA256 = ""
	if current.Digest(cfg) != digest {
		return createNormalTailRuntimeBlock("current input observation changed")
	}
	inputs, err := qualifyCreateNormalTailRuntimeInputs(*source, cfg, current)
	if err != nil {
		return err
	}
	previous, _, err := readCreateNormalTailRuntimePrevious(h.Execution.Store, h.Execution)
	if err != nil {
		return err
	}
	if _, err = createNormalTailRuntimeCorrectionDefinitions(h.Execution.Store, *source, inputs, previous.Target.Plan, environmentBillingRuntimeKeyProfile); err != nil {
		return err
	}
	d, err := readCreateNormalTailRuntimeForward(h.Execution.Store, h.Execution)
	if err != nil {
		return err
	}
	if d != nil && deploymentplan.Digest(d.Inputs) != deploymentplan.Digest(inputs) {
		chain, readErr := readCreateNormalTailRuntimeChain(h.Execution.Store, h.Execution)
		if readErr != nil || len(chain) == 0 || !createNormalTailRuntimePartial(chain[len(chain)-1].Child, chain[len(chain)-1].Journal) {
			return createNormalTailRuntimeBlock("published selected inputs changed")
		}
	}
	return h.Execution.Check(ctx)
}

func prepareCreateNormalTailRuntimeForward(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment) (*compiledEnvironmentDeployment, error) {
	return prepareCreateNormalTailRuntimeForwardWithInventory(ctx, cfg, h, c, c.Backend.Inventory)
}
func prepareCreateNormalTailRuntimeForwardWithInventory(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, inventory func(context.Context, string) ([]deploymentplan.Object, error), profiles ...string) (*compiledEnvironmentDeployment, error) {
	e, store := h.Execution, h.Execution.Store
	originalSource, err := readCreateNormalTailRuntimeSource(store, e)
	if err != nil {
		return nil, err
	}
	if originalSource == nil {
		return c, nil
	}
	selected, err := readCreateNormalTailRuntimeForward(store, e)
	if err != nil {
		return nil, err
	}
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return nil, err
	}
	current, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		return nil, err
	}
	inputs, err := qualifyCreateNormalTailRuntimeInputs(*originalSource, cfg, current)
	if err != nil {
		return nil, err
	}
	profile := environmentBillingRuntimeKeyProfile
	if _, err := os.Lstat(createNormalTailAccountRegistrationCRLPath(e)); err == nil {
		profile = createNormalTailAccountRegistrationCRLProfile
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	normalize := func(s string) string {
		if s == "" {
			return environmentBillingRuntimeLegacyProfile
		}
		return s
	}
	if selected != nil && normalize(selected.Profile) == normalize(profile) && deploymentplan.Digest(selected.Inputs) == deploymentplan.Digest(inputs) {
		next := *c
		next.Plan = selected.Target.Plan
		next.Release = selected.Target.Release
		next.Bindings = selected.Target.Bindings
		next.SourceCommand = localEnvironmentReleaseSourceCommand
		return &next, e.Check(ctx)
	}
	source, sequence := originalSource, 2
	previous, j, err := readCreateNormalTailRuntimePrevious(store, e)
	if err != nil {
		return nil, err
	}
	previousPlan, previousJournal, previousDigest, target := previous.Target.Plan, j, deploymentplan.Digest(previous), previous.Target
	if selected != nil {
		if len(chain) == 0 {
			return nil, createNormalTailRuntimeBlock("numbered selected source")
		}
		last := chain[len(chain)-1]
		source, err = captureCreateNormalTailRuntimeNextSource(ctx, store, e, chain, profile)
		if err != nil {
			return nil, err
		}
		sequence = source.Sequence
		previousPlan, previousJournal, previousDigest, target = last.Descriptor.Target.Plan, last.Journal, deploymentplan.Digest(last.Descriptor), last.Descriptor.Target
	}
	if _, err := createNormalTailRuntimeCorrectionDefinitions(store, *source, inputs, previousPlan, profile); err != nil {
		return nil, err
	}
	stage := createNormalTailRuntimeStage(sequence)
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage) != nil {
		return nil, createNormalTailRuntimeBlock("orphan numbered request")
	}
	live, err := inventory(ctx, previousPlan.Stack)
	if err != nil {
		return nil, err
	}
	observed := map[string]deploymentplan.Object{}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if observed[id] != nil {
			return nil, createNormalTailRuntimeBlock("duplicate native identity: " + id)
		}
		observed[id] = o
	}
	for _, r := range previousPlan.Resources {
		if observed[r.ID] == nil {
			observed[r.ID], err = c.Backend.Read(ctx, r.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	for _, r := range source.SessionRenewalResources {
		if observed[r.ID] == nil {
			observed[r.ID], err = c.Backend.Read(ctx, r.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	plan, err := buildCreateNormalTailRuntimeForward(store, *source, inputs, previousPlan, previousJournal, observed, profile)
	if err != nil {
		return nil, err
	}
	if plan.SHA256() == previousPlan.SHA256() {
		return nil, createNormalTailRuntimeBlock("numbered correction has no new target")
	}
	if _, exists, err := plan.ReadExecution(store.Root); err != nil || exists {
		return nil, createNormalTailRuntimeBlock("orphan numbered native execution")
	}
	for _, lease := range e.Journal.KubernetesLeases {
		if strings.HasPrefix(lease.Holder, plan.SHA256()+":") {
			return nil, createNormalTailRuntimeBlock("orphan numbered native lease")
		}
	}
	target.Plan = plan
	d := &createNormalTailRuntimeForward{Schema: createNormalTailRuntimeForwardSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: source.ProgramSHA256, SourceSHA256: deploymentplan.Digest(source), PreviousDescriptorSHA256: previousDigest, PreviousPlanSHA256: previousPlan.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(previousJournal), ForwardStage: stage, Inputs: inputs, Target: target}
	if sequence > 2 {
		d.Sequence = sequence
		d.Profile = profile
	} else if normalize(profile) != environmentBillingRuntimeLegacyProfile {
		d.Profile = profile
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	if source.SessionRenewal != nil {
		d.SessionRenewalSHA256 = deploymentplan.Digest(source.SessionRenewal)
	}
	if source.RegistrationCRLs != nil {
		d.RegistrationCRLSHA256 = deploymentplan.Digest(source.RegistrationCRLs)
	}
	rel, err := filepath.Rel(store.Root, createNormalTailRuntimeDescriptorPath(e, sequence))
	if err != nil {
		return nil, err
	}
	if err := store.write(rel, mustProviderRecoveryJSON(d), false); err != nil {
		return nil, err
	}
	selected, err = readCreateNormalTailRuntimeForward(store, e)
	if err != nil {
		return nil, err
	}
	next := *c
	next.Plan = selected.Target.Plan
	next.Release = selected.Target.Release
	next.Bindings = selected.Target.Bindings
	next.SourceCommand = localEnvironmentReleaseSourceCommand
	return &next, e.Check(ctx)
}

// Initial and final target resolution use the same finite projection, so steady
// compilation cannot restore the frozen, diagnosed provider configuration.
func applyCreateNormalTailRuntimeDefinitions(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if _, err := os.Lstat(createNormalTailRuntimeForwardPath(e)); os.IsNotExist(err) {
		return definitions, nil
	} else if err != nil {
		return nil, err
	}
	d, err := readCreateNormalTailRuntimeForward(store, e)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return definitions, nil
	}
	source, err := readCreateNormalTailRuntimeForwardSource(store, e, d)
	if err != nil || source == nil {
		return nil, createNormalTailRuntimeBlock("steady source")
	}
	snapshot, err := createNormalTailRuntimeEffectiveSnapshot(*source, d.Inputs)
	if err != nil {
		return nil, err
	}
	billingProfile := d.Profile
	if billingProfile == createNormalTailAccountRegistrationCRLProfile {
		billingProfile = environmentBillingRuntimeKeyProfile
	}
	corrections, err := environmentBillingRuntimeCorrectionDefinitionsForProfile(cfg, store, source.Release, snapshot, definitions, billingProfile)
	if err != nil {
		return nil, err
	}
	if source.RegistrationCRLs != nil {
		for _, r := range d.Target.Plan.Resources {
			if r.ID == createNormalTailAccountRegistrationCRLID(d.Target.Plan.Stack) {
				corrections = append(corrections, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: r.Dependencies, Retain: true})
			}
		}
	}
	byID := map[string]deploymentplan.Definition{}
	for _, d := range corrections {
		byID[deploymentplan.ID(d.Object)] = d
	}
	for i, d := range definitions {
		if corrected, ok := byID[deploymentplan.ID(d.Object)]; ok {
			definitions[i] = corrected
		}
	}
	return definitions, nil
}

func executeCreateNormalTailRuntimeForwardBackend(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, d *createNormalTailRuntimeForward, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error, phaseFences ...deploymentplan.PhaseFence) error {
	selected, err := readCreateNormalTailRuntimeForward(store, h.Execution)
	if err != nil {
		return err
	}
	chain, err := readCreateNormalTailRuntimeChain(store, h.Execution)
	if err != nil {
		return err
	}
	if len(chain) == 0 || selected == nil || d == nil || deploymentplan.Digest(d) != deploymentplan.Digest(selected) || c.Plan == nil || c.Plan.SHA256() != d.Target.Plan.SHA256() {
		return createNormalTailRuntimeBlock("selected numbered target")
	}
	last := chain[len(chain)-1]
	if err := validateCreateNormalTailRuntimeForward(store, *last.Source, d.Inputs, last.PreviousPlan, last.PreviousJournal, c.Plan, d.Profile); err != nil {
		return err
	}
	if err := executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, d.ForwardStage, c.Plan, backend, qualify, fence, accept, out, capture, phaseFences...); err != nil {
		return err
	}
	j, exists, err := c.Plan.ReadExecution(store.Root)
	if err != nil || !exists || !j.Accepted {
		return createNormalTailRuntimeBlock("genuine accepted numbered child")
	}
	previous, _, err := readCreateNormalTailRuntimePrevious(store, h.Execution)
	if err != nil {
		return err
	}
	for i := len(chain) - 1; i >= 0; i-- {
		step := chain[i]
		stage := previous.ForwardStage
		if i > 0 {
			stage = chain[i-1].Descriptor.ForwardStage
		}
		receipt := createNormalTailRuntimeForwardReceipt{Schema: createNormalTailRuntimeForwardSchema, PreviousPlanSHA256: step.Descriptor.PreviousPlanSHA256, PreviousJournalSHA256: step.Descriptor.PreviousJournalSHA256, SourceSHA256: step.Descriptor.SourceSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
		if err := completeCreateNormalTailSupersededWorkload(ctx, h.Execution, "kubernetes/"+stage, receipt); err != nil {
			return err
		}
	}
	renewed := createNormalTailProductSessionForwardReceipt{Schema: createNormalTailProductSessionForwardSchema, PreviousPlanSHA256: previous.PreviousPlanSHA256, PreviousJournalSHA256: previous.PreviousJournalSHA256, RenewalSHA256: previous.RenewalSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	if err := completeCreateNormalTailSupersededWorkload(ctx, h.Execution, "kubernetes/"+createNormalTailWorkloadForwardStage, renewed); err != nil {
		return err
	}
	logical := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: previous.OriginalPlanSHA256, OriginalJournalSHA256: previous.OriginalJournalSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	return completeCreateNormalTailSupersededWorkload(ctx, h.Execution, "kubernetes/workload", logical)
}

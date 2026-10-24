package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailRuntimeSourceSchema = "rtk-cloud.create-runtime-configuration-source/v1"
const createNormalTailRuntimeForwardSchema = "rtk-cloud.create-runtime-configuration-forward/v1"
const createNormalTailRuntimeForwardStage = "workload-forward-0002"

// The private original snapshot makes a later selected-input correction
// reproducible without accepting changes to any other canonical input.
type createNormalTailRuntimeSource struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256                    string
	PreviousDescriptorSHA256, PreviousPlanSHA256, PreviousJournalSHA256 string
	PreviousIntentSHA256                                                string
	SessionRenewal                                                      *productSessionRenewal                   `json:",omitempty"`
	SessionRenewalResources                                             []deploymentplan.Resource                `json:",omitempty"`
	RegistrationCRLs                                                    *createNormalTailAccountRegistrationCRLs `json:",omitempty"`
	Sequence                                                            int                                      `json:",omitempty"`
	Profile                                                             string                                   `json:",omitempty"`
	Configuration                                                       deploymentConfig
	Release                                                             environmentRelease
	Snapshot                                                            environmentDeploymentSnapshot
	CapturedAt                                                          time.Time
}

type createNormalTailRuntimeValue struct {
	Present bool
	Value   string
}

type createNormalTailRuntimeInputs struct {
	Operator, Secrets map[string]createNormalTailRuntimeValue
}

type createNormalTailRuntimeForward struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256, SourceSHA256      string
	PreviousDescriptorSHA256, PreviousPlanSHA256, PreviousJournalSHA256 string
	ForwardStage                                                        string
	SessionRenewalSHA256                                                string `json:",omitempty"`
	RegistrationCRLSHA256                                               string `json:",omitempty"`
	Sequence                                                            int    `json:",omitempty"`
	Profile                                                             string `json:",omitempty"`
	Inputs                                                              createNormalTailRuntimeInputs
	Target                                                              createNormalTailTarget
}

type createNormalTailRuntimeForwardReceipt struct {
	Schema                string `json:"schema"`
	PreviousPlanSHA256    string `json:"previous_plan_sha256"`
	PreviousJournalSHA256 string `json:"previous_journal_sha256"`
	SourceSHA256          string `json:"source_sha256"`
	ForwardStage          string `json:"forward_stage"`
	ForwardPlanSHA256     string `json:"forward_plan_sha256"`
	ForwardJournalSHA256  string `json:"forward_journal_sha256"`
}

func createNormalTailRuntimeSourcePath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-runtime-configuration-source.json")
}
func createNormalTailRuntimeForwardPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-workload-forward-0002.json")
}
func createNormalTailRuntimeBlock(stage string) error {
	return createProviderRecoveryBlock("recorded Billing runtime correction requires its original partial workload and exact selected inputs: stage=" + stage)
}

func createNormalTailRuntimeInputKeys() ([]string, []string) {
	return []string{"PAYPAL_ENABLED", "PAYMENT_SIMULATOR_ENABLED", "NEWEBPAY_ENABLED"}, []string{lkeSecretFileName("paypal-client-id"), lkeSecretFileName("paypal-client-secret"), lkeSecretFileName("paypal-webhook-id")}
}

func createNormalTailRuntimeSelectedInputs(snapshot environmentDeploymentSnapshot) createNormalTailRuntimeInputs {
	out := createNormalTailRuntimeInputs{Operator: map[string]createNormalTailRuntimeValue{}, Secrets: map[string]createNormalTailRuntimeValue{}}
	operator, secrets := createNormalTailRuntimeInputKeys()
	for _, key := range operator {
		value, present := snapshot.Operator[key]
		out.Operator[key] = createNormalTailRuntimeValue{present, value}
	}
	for _, key := range secrets {
		value, present := snapshot.Secrets[key]
		out.Secrets[key] = createNormalTailRuntimeValue{present, value}
	}
	return out
}

func createNormalTailRuntimeEffectiveSnapshot(source createNormalTailRuntimeSource, inputs createNormalTailRuntimeInputs) (environmentDeploymentSnapshot, error) {
	var snapshot environmentDeploymentSnapshot
	if json.Unmarshal(mustProviderRecoveryJSON(source.Snapshot), &snapshot) != nil {
		return snapshot, createNormalTailRuntimeBlock("snapshot encoding")
	}
	if snapshot.Operator == nil || snapshot.Secrets == nil {
		return snapshot, createNormalTailRuntimeBlock("original selected input maps")
	}
	operator, secrets := createNormalTailRuntimeInputKeys()
	if len(inputs.Operator) != len(operator) || len(inputs.Secrets) != len(secrets) {
		return snapshot, createNormalTailRuntimeBlock("finite selected input set")
	}
	apply := func(keys []string, values map[string]createNormalTailRuntimeValue, target map[string]string) error {
		for _, key := range keys {
			value, ok := values[key]
			if !ok || !value.Present && value.Value != "" {
				return createNormalTailRuntimeBlock("selected input presence")
			}
			if value.Present {
				target[key] = value.Value
			} else {
				delete(target, key)
			}
		}
		return nil
	}
	if err := apply(operator, inputs.Operator, snapshot.Operator); err != nil {
		return snapshot, err
	}
	if err := apply(secrets, inputs.Secrets, snapshot.Secrets); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func qualifyCreateNormalTailRuntimeInputs(source createNormalTailRuntimeSource, cfg deploymentConfig, current environmentDeploymentSnapshot) (createNormalTailRuntimeInputs, error) {
	inputs := createNormalTailRuntimeSelectedInputs(current)
	current.DNSStateSHA256 = ""
	if deploymentplan.Digest(cfg) != deploymentplan.Digest(source.Configuration) {
		return inputs, createNormalTailRuntimeBlock("maintained configuration must remain original")
	}
	for _, v := range inputs.Operator {
		if v.Present && v.Value != "true" && v.Value != "false" {
			return inputs, createNormalTailRuntimeBlock("explicit selected payment boolean")
		}
	}
	expected, err := createNormalTailRuntimeEffectiveSnapshot(source, inputs)
	if err != nil {
		return inputs, err
	}
	if deploymentplan.Digest(expected) != deploymentplan.Digest(current) {
		return inputs, createNormalTailRuntimeBlock("unrelated original input changed")
	}
	return inputs, nil
}

// This is the literal previous execution, including its failed known-response
// resource. It is never made Accepted and its journal is never rewritten.
func readCreateNormalTailRuntimePrevious(store secretStore, e *environmentCreateExecution) (*createNormalTailProductSessionForward, deploymentplan.Journal, error) {
	var journal deploymentplan.Journal
	if e == nil || e.Store != store {
		return nil, journal, createNormalTailRuntimeBlock("previous descriptor")
	}
	p, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, journal, err
	}
	original, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return nil, journal, err
	}
	original, originalJournal, err := readCreateNormalTailWorkloadOriginal(store, e, original)
	if err != nil || original == nil {
		return nil, journal, createNormalTailRuntimeBlock("original workload source")
	}
	first, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, original, originalJournal)
	if err != nil || first == nil {
		return nil, journal, createNormalTailRuntimeBlock("first forward source")
	}
	descriptor, err := readCreateNormalTailProductSessionForwardSource(store, e, original, originalJournal, first)
	if err != nil || descriptor == nil {
		return nil, journal, createNormalTailRuntimeBlock("renewed forward source")
	}
	if descriptor.Schema != createNormalTailProductSessionForwardSchema || descriptor.ParentSHA256 != e.Journal.PlanSHA256 || descriptor.OwnerSHA256 != deploymentplan.Digest(e.owner) || descriptor.ProgramSHA256 != deploymentplan.Digest(p) || descriptor.ForwardStage != createNormalTailProductSessionForwardStage {
		return nil, journal, createNormalTailRuntimeBlock("previous lineage")
	}
	var exists bool
	journal, exists, err = descriptor.Target.Plan.ReadExecution(store.Root)
	if err != nil || !exists || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || len(journal.Completed) >= len(descriptor.Target.Plan.Resources) || len(journal.Owners) == 0 {
		return nil, journal, createNormalTailRuntimeBlock("known previous partial response")
	}
	billing := "Deployment/" + descriptor.Target.Plan.Stack + "-billing/billing"
	if journal.Failed != billing || descriptor.Target.Plan.Resources[len(journal.Completed)].ID != billing || journal.Owners[billing] == "" || len(journal.Owners) != len(journal.Completed)+1 {
		return nil, journal, createNormalTailRuntimeBlock("known Billing readiness failure")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+descriptor.ForwardStage)
	if entry == nil || entry.Phase != descriptor.ForwardStage || entry.IntentSHA256 == "" {
		return nil, journal, createNormalTailRuntimeBlock("original previous request")
	}
	child, rawJournal, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil || child == nil || child.SHA256() != descriptor.Target.Plan.SHA256() || deploymentplan.Digest(rawJournal) != deploymentplan.Digest(journal) {
		return nil, journal, createNormalTailRuntimeBlock("exact previous intent and lease")
	}
	return descriptor, journal, nil
}

func readCreateNormalTailRuntimeSource(store secretStore, e *environmentCreateExecution) (*createNormalTailRuntimeSource, error) {
	if e == nil || e.Store != store {
		return nil, createNormalTailRuntimeBlock("source owner")
	}
	path := createNormalTailRuntimeSourcePath(e)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var source createNormalTailRuntimeSource
	if readEnvironmentPreflightJSON(path, &source) != nil {
		return nil, createNormalTailRuntimeBlock("source encoding")
	}
	p, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	previous, j, err := readCreateNormalTailRuntimePrevious(store, e)
	if err != nil {
		return nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.ForwardStage)
	if source.Schema != createNormalTailRuntimeSourceSchema || source.ParentSHA256 != e.Journal.PlanSHA256 || source.OwnerSHA256 != deploymentplan.Digest(e.owner) || source.ProgramSHA256 != deploymentplan.Digest(p) || source.PreviousDescriptorSHA256 != deploymentplan.Digest(previous) || source.PreviousPlanSHA256 != previous.Target.Plan.SHA256() || source.PreviousJournalSHA256 != deploymentplan.Digest(j) || source.PreviousIntentSHA256 != entry.IntentSHA256 || source.CapturedAt.IsZero() || source.CapturedAt.After(time.Now().UTC()) || deploymentplan.Digest(source.Configuration) != p.MaintainedConfigSHA256 || deploymentplan.Digest(source.Release) != deploymentplan.Digest(p.Release) || source.Snapshot.DNSStateSHA256 != "" || source.Snapshot.Digest(source.Configuration) != previous.Target.Plan.ConfigurationSHA256 {
		return nil, createNormalTailRuntimeBlock("original snapshot and partial lineage")
	}
	return &source, nil
}

func captureCreateNormalTailRuntimeSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createNormalTailRuntimeSource, error) {
	if existing, err := readCreateNormalTailRuntimeSource(store, e); err != nil || existing != nil {
		return existing, err
	}
	source, err := qualifyCreateNormalTailRuntimeSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(store.Root, createNormalTailRuntimeSourcePath(e))
	if err != nil {
		return nil, err
	}
	if err = e.Check(ctx); err != nil {
		return nil, err
	}
	if err = store.write(rel, mustProviderRecoveryJSON(source), false); err != nil {
		return nil, err
	}
	return readCreateNormalTailRuntimeSource(store, e)
}

func qualifyCreateNormalTailRuntimeSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createNormalTailRuntimeSource, error) {
	if e == nil || e.Check(ctx) != nil {
		return nil, createNormalTailRuntimeBlock("held source owner")
	}
	p, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	previous, j, err := readCreateNormalTailRuntimePrevious(store, e)
	if err != nil {
		return nil, err
	}
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		return nil, err
	}
	snapshot.DNSStateSHA256 = ""
	if deploymentplan.Digest(cfg) != p.MaintainedConfigSHA256 || snapshot.Digest(cfg) != previous.Target.Plan.ConfigurationSHA256 || previous.Target.Plan.ConfigurationSHA256 != p.ConfigurationSHA256 {
		return nil, createNormalTailRuntimeBlock("capture before selected input change")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.ForwardStage)
	source := createNormalTailRuntimeSource{Schema: createNormalTailRuntimeSourceSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: deploymentplan.Digest(p), PreviousDescriptorSHA256: deploymentplan.Digest(previous), PreviousPlanSHA256: previous.Target.Plan.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(j), PreviousIntentSHA256: entry.IntentSHA256, Configuration: cfg, Release: p.Release, Snapshot: snapshot, CapturedAt: time.Now().UTC()}
	return &source, e.Check(ctx)
}

func createNormalTailRuntimeCorrectionDefinitions(store secretStore, source createNormalTailRuntimeSource, inputs createNormalTailRuntimeInputs, previous *deploymentplan.Plan, profiles ...string) ([]deploymentplan.Definition, error) {
	snapshot, err := createNormalTailRuntimeEffectiveSnapshot(source, inputs)
	if err != nil {
		return nil, err
	}
	definitions := make([]deploymentplan.Definition, 0, len(previous.Resources))
	for _, r := range previous.Resources {
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: r.Dependencies})
	}
	profile := source.Profile
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	billingProfile := profile
	if profile == createNormalTailAccountRegistrationCRLProfile {
		billingProfile = environmentBillingRuntimeKeyProfile
	}
	changes, err := environmentBillingRuntimeCorrectionDefinitionsForProfile(source.Configuration, store, source.Release, snapshot, definitions, billingProfile)
	if err != nil {
		return nil, err
	}
	if profile == createNormalTailAccountRegistrationCRLProfile {
		if source.RegistrationCRLs == nil {
			return nil, accountRegistrationCRLBlock("numbered source lacks accepted CRLs")
		}
		desired, err := accountRegistrationCRLDesired(previous, source.RegistrationCRLs)
		if err != nil {
			return nil, err
		}
		changes = append(changes, deploymentplan.Definition{Object: desired, Phase: "configuration"})
	} else if source.RegistrationCRLs != nil {
		return nil, accountRegistrationCRLBlock("CRL source profile mismatch")
	}
	return runtimeRenewalCorrections(source, previous, changes)
}

func validateCreateNormalTailRuntimeForward(store secretStore, source createNormalTailRuntimeSource, inputs createNormalTailRuntimeInputs, previous *deploymentplan.Plan, j deploymentplan.Journal, forward *deploymentplan.Plan, profiles ...string) error {
	if previous == nil || forward == nil || previous.SHA256() != source.PreviousPlanSHA256 || j.PlanSHA256 != previous.SHA256() || deploymentplan.Digest(j) != source.PreviousJournalSHA256 || j.Accepted || j.InFlight != "" || len(j.Completed) >= len(previous.Resources) || len(previous.Resources)+len(runtimeRenewalAdditional(source, previous)) != len(forward.Resources) || forward.Version != previous.Version {
		return createNormalTailRuntimeBlock("literal partial source")
	}
	snapshot, err := createNormalTailRuntimeEffectiveSnapshot(source, inputs)
	if err != nil {
		return err
	}
	expectedInputs := previous.Inputs
	expectedInputs.ConfigurationSHA256 = snapshot.Digest(source.Configuration)
	if deploymentplan.Digest(expectedInputs) != deploymentplan.Digest(forward.Inputs) {
		return createNormalTailRuntimeBlock("exact corrected snapshot inputs")
	}
	changes, err := createNormalTailRuntimeCorrectionDefinitions(store, source, inputs, previous, profiles...)
	if err != nil {
		return err
	}
	corrected := map[string]deploymentplan.Object{}
	for _, d := range changes {
		corrected[deploymentplan.ID(d.Object)] = d.Object
	}
	for i, old := range previous.Resources {
		got := forward.Resources[i]
		desired := old.Desired
		if replacement := corrected[old.ID]; replacement != nil {
			desired = replacement
		}
		changed := deploymentplan.Digest(desired) != deploymentplan.Digest(old.Desired)
		if got.ID != old.ID || got.Phase != old.Phase || deploymentplan.Digest(got.Dependencies) != deploymentplan.Digest(old.Dependencies) || deploymentplan.Digest(got.Desired) != deploymentplan.Digest(desired) {
			return createNormalTailRuntimeBlock("finite desired correction: " + old.ID)
		}
		if dep := runtimeRenewalDependency(source, old.ID); dep != nil {
			if err := validateRuntimeRenewalRetained(*dep, desired, got); err != nil {
				return err
			}
			continue
		}
		if source.RegistrationCRLs != nil && old.ID == createNormalTailAccountRegistrationCRLID(previous.Stack) {
			if got.Action != "retain" || got.UID != source.RegistrationCRLs.SecretUID || got.Baseline == nil || deploymentplan.Map(got.Baseline["metadata"])["uid"] != got.UID || deploymentplan.Map(got.Baseline["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(desired, got.Baseline) || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(deploymentplan.Projection(got.Baseline)) {
				return accountRegistrationCRLBlock("accepted CRL child retained baseline")
			}
			continue
		}
		if uid := j.Owners[old.ID]; uid != "" {
			action := "retain"
			if changed {
				action = "update"
			}
			if got.Action != action || got.UID != uid || got.Baseline == nil || deploymentplan.Map(got.Baseline["metadata"])["uid"] != uid || !deploymentplan.Matches(old.Desired, got.Baseline) || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(deploymentplan.Projection(got.Baseline)) || old.Action == "retain" && deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(old.Baseline) {
				return createNormalTailRuntimeBlock("recorded prefix owner: " + old.ID)
			}
		} else if old.Action == "create" {
			if got.Action != "create" || got.UID != "" || got.Baseline != nil {
				return createNormalTailRuntimeBlock("unrecorded create may not be adopted: " + old.ID)
			}
		} else {
			action := old.Action
			if changed {
				action = "update"
			}
			if got.Action != action || got.UID != old.UID || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(old.Baseline) {
				return createNormalTailRuntimeBlock("unwritten original precondition: " + old.ID)
			}
		}
	}
	additional := runtimeRenewalAdditional(source, previous)
	for i, dep := range additional {
		got := forward.Resources[len(previous.Resources)+i]
		desired := corrected[dep.ID]
		if got.ID != dep.ID || got.Phase != dep.Phase || len(got.Dependencies) != 0 || deploymentplan.Digest(got.Desired) != deploymentplan.Digest(desired) {
			return productSessionRenewalBlock("runtime additional dependency shape")
		}
		if err := validateRuntimeRenewalRetained(dep, desired, got); err != nil {
			return err
		}
	}
	return nil
}

func buildCreateNormalTailRuntimeForward(store secretStore, source createNormalTailRuntimeSource, inputs createNormalTailRuntimeInputs, previous *deploymentplan.Plan, j deploymentplan.Journal, observed map[string]deploymentplan.Object, profiles ...string) (*deploymentplan.Plan, error) {
	var next deploymentplan.Plan
	if previous == nil || json.Unmarshal(mustProviderRecoveryJSON(previous), &next) != nil {
		return nil, createNormalTailRuntimeBlock("previous plan encoding")
	}
	changes, err := createNormalTailRuntimeCorrectionDefinitions(store, source, inputs, previous, profiles...)
	if err != nil {
		return nil, err
	}
	corrected := map[string]deploymentplan.Object{}
	for _, d := range changes {
		corrected[deploymentplan.ID(d.Object)] = d.Object
	}
	snapshot, err := createNormalTailRuntimeEffectiveSnapshot(source, inputs)
	if err != nil {
		return nil, err
	}
	next.ConfigurationSHA256 = snapshot.Digest(source.Configuration)
	for i, old := range previous.Resources {
		r := &next.Resources[i]
		if replacement := corrected[old.ID]; replacement != nil {
			r.Desired = deploymentplan.Clone(replacement)
		}
		changed := deploymentplan.Digest(old.Desired) != deploymentplan.Digest(r.Desired)
		current := observed[old.ID]
		if dep := runtimeRenewalDependency(source, old.ID); dep != nil {
			if current == nil || deploymentplan.Map(current["metadata"])["uid"] != dep.UID || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(dep.Desired, current) {
				return nil, productSessionRenewalBlock("runtime dependency native owner")
			}
			r.Action = "retain"
			if !deploymentplan.Matches(r.Desired, current) {
				r.Action = "update"
			}
			r.UID = dep.UID
			r.Baseline = deploymentplan.Projection(current)
			continue
		}
		if source.RegistrationCRLs != nil && old.ID == createNormalTailAccountRegistrationCRLID(previous.Stack) {
			if current == nil || deploymentplan.Map(current["metadata"])["uid"] != source.RegistrationCRLs.SecretUID || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(r.Desired, current) {
				return nil, accountRegistrationCRLBlock("accepted CRL child current body")
			}
			r.Action = "retain"
			r.UID = source.RegistrationCRLs.SecretUID
			r.Baseline = deploymentplan.Projection(current)
			continue
		}
		if uid := j.Owners[old.ID]; uid != "" {
			if old.Action == "retain" {
				if _, err := old.Body(current); err != nil {
					return nil, createNormalTailRuntimeBlock("original retained body changed: " + old.ID)
				}
			}
			if current == nil || deploymentplan.Map(current["metadata"])["uid"] != uid || !deploymentplan.Matches(old.Desired, current) {
				return nil, createNormalTailRuntimeBlock("current recorded owner: " + old.ID)
			}
			r.Action = "retain"
			if changed {
				r.Action = "update"
			}
			r.UID = uid
			r.Baseline = deploymentplan.Projection(current)
		} else if old.Action == "create" {
			if current != nil {
				return nil, createNormalTailRuntimeBlock("unknown create exists: " + old.ID)
			}
		} else {
			if _, err := old.Body(current); err != nil {
				return nil, createNormalTailRuntimeBlock("original unwritten owner: " + old.ID)
			}
			if changed {
				r.Action = "update"
			}
		}
	}
	for _, dep := range runtimeRenewalAdditional(source, previous) {
		current := observed[dep.ID]
		if current == nil || deploymentplan.Map(current["metadata"])["uid"] != dep.UID || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(dep.Desired, current) {
			return nil, productSessionRenewalBlock("runtime additional dependency native owner")
		}
		dep.Action = "retain"
		dep.Dependencies = nil
		dep.Desired = deploymentplan.Clone(corrected[dep.ID])
		dep.Baseline = deploymentplan.Projection(current)
		next.Resources = append(next.Resources, dep)
	}
	if err := validateCreateNormalTailRuntimeForward(store, source, inputs, previous, j, &next, profiles...); err != nil {
		return nil, err
	}
	return &next, nil
}

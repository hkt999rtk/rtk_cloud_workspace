package main

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original failed core and its native ownership receipts remain immutable.
// This one bounded forward records actual Controller completion and the next
// written CertIssuer failure; it never manufactures a failed or accepted state.
type createManagedCoreInterruptedForward struct {
	Review, SourceSHA, ArtifactSHA string
	Plan                           *deploymentplan.Plan
	Journal                        deploymentplan.Journal
}

func managedCoreInterruptedForwardReview(j environmentCreateJournal) string {
	if len(j.Entries) < 18 {
		return ""
	}
	admission, forward := j.Entries[len(j.Entries)-2], j.Entries[len(j.Entries)-1]
	prefix := "kubernetes/managed-complete-issuer-forward-"
	review := strings.TrimPrefix(forward.Key, prefix)
	if !environmentPreflightDigest.MatchString(review) || forward.Key != prefix+review || forward.Phase != strings.TrimPrefix(forward.Key, "kubernetes/") || forward.State != "pending" || forward.StartedAt.IsZero() || !forward.CompletedAt.IsZero() || len(forward.Receipt) != 0 || !environmentPreflightDigest.MatchString(forward.IntentSHA256) || admission.Key != "openbao-recovery-admission-"+review || admission.Phase != "recovery" || admission.State != "completed" || admission.StartedAt.IsZero() || admission.CompletedAt.Before(admission.StartedAt) || len(admission.Receipt) == 0 {
		return ""
	}
	before := j
	before.Entries = slices.Clone(j.Entries[:len(j.Entries)-2])
	prior := &before.Entries[len(before.Entries)-1]
	oldReview := strings.TrimPrefix(prior.Key, "openbao-recovery-admission-")
	var receipt map[string]string
	if !environmentPreflightDigest.MatchString(oldReview) || prior.Key != "openbao-recovery-admission-"+oldReview || prior.Phase != "recovery" || prior.State != "completed" || prior.StartedAt.IsZero() || prior.CompletedAt.Before(prior.StartedAt) || decodeEnvironmentJSON(prior.Receipt, &receipt) != nil || len(receipt) != 2 || receipt["qualified_recovery_sha256"] != oldReview || !environmentPreflightDigest.MatchString(receipt["audit_sha256"]) {
		return ""
	}
	prior.State, prior.CompletedAt, prior.Receipt = "pending", time.Time{}, nil
	if managedCorePendingAdmissionReview(before) == "" {
		return ""
	}
	return review
}

// CurrentPlan is progress authority only after the bounded source loader has
// authenticated the admission and original candidate. It retains the original
// Plan/Journal for custody and all historical intent/material bindings.
func managedCorePartialCurrentPlan(core *createManagedCorePartialRecovery) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if core == nil || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil {
		return nil, deploymentplan.Journal{}, managedCorePartialBlock("original-core-authority")
	}
	if core.PriorForward == nil {
		return core.Plan, core.Journal, nil
	}
	if err := validateManagedCoreInterruptedJournal(core); err != nil {
		return nil, deploymentplan.Journal{}, err
	}
	return core.PriorForward.Plan, core.PriorForward.Journal, nil
}

func validateManagedCoreInterruptedJournal(core *createManagedCorePartialRecovery) error {
	if core == nil || core.PriorForward == nil || core.PriorAdmission == nil || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil {
		return managedCorePartialBlock("interrupted-original-authority")
	}
	prior := core.PriorForward
	if !environmentPreflightDigest.MatchString(prior.Review) || !environmentPreflightDigest.MatchString(prior.SourceSHA) || !environmentPreflightDigest.MatchString(prior.ArtifactSHA) || prior.Review == core.OriginalRepairReview || prior.Review == core.PriorAdmission.Review || prior.SourceSHA != core.PriorAdmission.SourceSHA || prior.Plan == nil {
		return managedCorePartialBlock("interrupted-forward-pins")
	}
	original := *core
	original.PriorForward = nil
	if validateManagedCoreInitialCandidate(&original, prior.Plan) != nil {
		return managedCorePartialBlock("interrupted-original-candidate")
	}
	plan, j := prior.Plan, prior.Journal
	if j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != plan.Resources[14].ID || len(j.Completed) != 14 || len(j.Owners) != 15 {
		return managedCorePartialBlock("interrupted-fourteen-fifteen")
	}
	for index, resource := range plan.Resources {
		if index < 14 && (j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID) || index == 14 && j.Owners[resource.ID] != resource.UID || index > 14 && j.Owners[resource.ID] != "" {
			return managedCorePartialBlock("interrupted-progress-" + resource.ID)
		}
	}
	if mqttFoundationEnv(mqttFoundationPrimary(plan.Resources[13].Desired), "PKI_FIXED_DEVICE_ROOT_TRUST") != "true" {
		return managedCorePartialBlock("completed-fixed-root-controller")
	}
	return nil
}

// Pure source authentication. It permits exactly the previously reviewed
// old-admission closure, one new completed admission and one genuine failed
// child lease. Neither stopped journal is rewritten or reported as accepted.
func loadManagedCoreInterruptedSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePartialRecovery, error) {
	var empty createFoundationRecoveryArtifact
	bad := func(stage string) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePartialRecovery, error) {
		return nil, empty, nil, managedCorePartialBlock("interrupted-" + stage)
	}
	if e == nil || store.Root != e.Store.Root {
		return bad("original-owner")
	}
	review := managedCoreInterruptedForwardReview(e.Journal)
	if review == "" {
		return bad("literal-checkpoint")
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != managedCorePartialSchema || managedCorePendingAdmissionReview(source) == "" || len(e.Journal.Entries) != len(source.Entries)+2 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 {
		return bad("six-go-and-source")
	}
	admission := e.Journal.Entries[len(source.Entries)]
	pending := admission
	pending.State, pending.CompletedAt, pending.Receipt = "pending", time.Time{}, nil
	if !managedCorePendingAdmissionArtifactValid(a, pending, source, review) {
		return bad("original-window-and-intent")
	}
	pure := *e
	pure.Journal = source
	historical, original, core, err := loadManagedCorePartialSource(store, &pure)
	if err != nil || core.PriorAdmission == nil || core.PriorForward != nil || core.Action != managedCoreRestoreOnly {
		return bad("bounded-original-source")
	}
	if !managedCoreInterruptedSourceMembership(source, e.Journal, core.PriorAdmission) {
		return bad("actual-old-admission-closure")
	}
	var receipt map[string]string
	if decodeEnvironmentJSON(admission.Receipt, &receipt) != nil || len(receipt) != 2 || receipt["qualified_recovery_sha256"] != review || receipt["audit_sha256"] != core.PriorAdmission.AuditSHA {
		return bad("genuine-native-admission-receipt")
	}
	for key, value := range map[string]string{"original-review": core.OriginalRepairReview, "source-plan": core.Plan.SHA256(), "source-journal": deploymentplan.Digest(core.Journal), "source": deploymentplan.Digest(core), "action": deploymentplan.Digest(managedCoreRestoreOnly), "original-admission": core.ArtifactSHA, "prior-admission": deploymentplan.Digest(core.PriorAdmission)} {
		if a.Plan.Materials["accepted-service-core-complete-issuer-partial-"+key] != value {
			return bad("material-" + key)
		}
	}
	if a.Plan.ParentJournalSHA != core.SourceSHA || a.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || a.Plan.Plan.Environment != source.Environment || a.Plan.Plan.Stack != source.Stack || a.Plan.Plan.ConfigurationSHA256 != core.Plan.ConfigurationSHA256 || deploymentplan.Digest(a.Plan.Plan.Release) != core.Plan.ReleaseSHA256 {
		return bad("action-and-original-inputs")
	}
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	var owner environmentCreateOwner
	if readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != source.Environment || owner.Stack != source.Stack || owner.PlanSHA256 != source.PlanSHA256 || owner.OwnerToken != source.OwnerToken || deploymentplan.Digest(owner) != a.Plan.ParentOwnerSHA || e.owner.Schema != "" && deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) {
		return bad("original-owner-binding")
	}
	var preview deploymentplan.Plan
	if readExecutionJSON(filepath.Join(base, managedCorePartialForwardPlanFilename), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] || validateManagedCoreInitialCandidate(core, &preview) != nil {
		return bad("original-fourteen-retain-two-preview")
	}
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	sha := strings.SplitN(lease.Holder, ":", 2)[0]
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	if lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(lease.UID) || !environmentPreflightDigest.MatchString(sha) || !strings.HasPrefix(lease.Holder, sha+":") || strings.TrimPrefix(lease.Holder, sha+":") == "" || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || !managedCoreSameQualifiedPlan(&plan, &preview) {
		return bad("actual-reviewed-child-and-lease")
	}
	defs, err := managedCoreInitialDefinitions(core)
	if err != nil || plan.BindingsSHA256 != environmentCreateStageBindingsSHA("managed-complete-issuer-forward-"+review, defs) {
		return bad("actual-forward-stage-binding")
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil || e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) {
		return bad("actual-forward-intent")
	}
	core.PriorForward = &createManagedCoreInterruptedForward{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &plan, Journal: journal}
	core.SourceSHA = deploymentplan.Digest(e.Journal)
	if err := validateManagedCoreInterruptedJournal(core); err != nil {
		return nil, empty, nil, err
	}
	return historical, original, core, nil
}

func managedCoreInterruptedSourceMembership(source, current environmentCreateJournal, prior *createManagedCorePendingAdmission) bool {
	if prior == nil || len(current.Entries) != len(source.Entries)+2 || len(current.KubernetesLeases) != len(source.KubernetesLeases)+1 {
		return false
	}
	prefix := current
	prefix.Entries = current.Entries[:len(source.Entries)]
	prefix.KubernetesLeases = current.KubernetesLeases[:len(source.KubernetesLeases)]
	return managedCorePendingAdmissionPrefixMatches(source, prefix, prior)
}

func managedCorePartialPrefixMatches(source, current environmentCreateJournal, core *createManagedCorePartialRecovery) bool {
	if core == nil || core.SourceSHA != deploymentplan.Digest(source) {
		return false
	}
	if core.PriorForward != nil {
		return deploymentplan.Digest(source) == deploymentplan.Digest(current)
	}
	return managedCorePendingAdmissionPrefixMatches(source, current, core.PriorAdmission)
}

func managedCorePartialAliasPrefixMatches(source, current environmentCreateJournal, core *createManagedCorePartialRecovery, stage, actualPlanSHA string) bool {
	if core == nil || len(source.Entries) != len(current.Entries) || !environmentPreflightDigest.MatchString(actualPlanSHA) {
		return false
	}
	normalized := current
	normalized.Entries = slices.Clone(current.Entries)
	expected := map[string]struct{ Plan, Journal string }{"kubernetes/managed-complete-issuer": {core.Plan.SHA256(), deploymentplan.Digest(core.Journal)}}
	if core.PriorForward != nil {
		expected["kubernetes/managed-complete-issuer-forward-"+core.PriorForward.Review] = struct{ Plan, Journal string }{core.PriorForward.Plan.SHA256(), deploymentplan.Digest(core.PriorForward.Journal)}
	}
	found := 0
	for index, old := range source.Entries {
		original, ok := expected[old.Key]
		if !ok {
			continue
		}
		entry := current.Entries[index]
		var receipt struct {
			Stage    string `json:"actual_qualified_forward_stage"`
			Original string `json:"original_plan_sha256"`
			Journal  string `json:"original_failed_journal_sha256"`
			Actual   string `json:"actual_qualified_forward_plan_sha256"`
			Review   string `json:"original_repair_review_sha256"`
		}
		if old.State != "pending" || len(old.Receipt) != 0 || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(old.StartedAt) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Stage != stage || receipt.Original != original.Plan || receipt.Journal != original.Journal || receipt.Actual != actualPlanSHA || receipt.Review != core.OriginalRepairReview {
			return false
		}
		entry.State, entry.CompletedAt, entry.Receipt = old.State, old.CompletedAt, old.Receipt
		normalized.Entries[index] = entry
		found++
	}
	return found == len(expected) && managedCorePartialPrefixMatches(source, normalized, core)
}

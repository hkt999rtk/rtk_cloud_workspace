package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedLateNormalTailApproval struct {
	ProposalSHA, ApprovalSHA, ParentSHA, SourceSHA, SourceRawSHA, OwnerSHA string
	OriginalAuthoritySHA, RawAuthoritySHA, CompletedProofSHA               string
	PendingSHA, IdentityPlanSHA, FailureSHA                                string
	FirstCallerLimit                                                       time.Duration
	ApprovedAt                                                             time.Time
}

type managedLateNormalTailApprovalRecord struct {
	Schema             string    `json:"schema"`
	Environment        string    `json:"environment"`
	Parent             string    `json:"parent_plan_sha256"`
	ProposalSHA        string    `json:"proposal_sha256"`
	SourceRawSHA       string    `json:"source_journal_sha256"`
	Approved           bool      `json:"approved"`
	FirstCallerMinutes int       `json:"first_caller_overall_minutes"`
	ExecutionAuthority *bool     `json:"execution_authority"`
	RecordedAt         time.Time `json:"recorded_at"`
	HumanMessage       string    `json:"human_message"`
}

type managedLateNormalTailProposal struct {
	Schema                 string    `json:"schema"`
	Environment            string    `json:"environment"`
	Parent                 string    `json:"parent_plan_sha256"`
	SourcePath             string    `json:"source_journal_path"`
	SourceRawSHA           string    `json:"source_journal_sha256"`
	OwnerSHA               string    `json:"source_owner_sha256"`
	SourceEntries          int       `json:"source_entries"`
	SourceLeases           int       `json:"source_leases"`
	Source428RawSHA        string    `json:"source428_sha256"`
	PriorAuthorityRawSHA   string    `json:"prior_authority_sha256"`
	PriorCleanupDeadline   time.Time `json:"prior_cleanup_deadline"`
	PriorOverallDeadline   time.Time `json:"prior_overall_deadline"`
	CompletedProofSHA      string    `json:"terminal_completion_sha256"`
	FailurePath            string    `json:"failure_path"`
	FailureSHA             string    `json:"failure_sha256"`
	FailedCheckerSHA       string    `json:"failed_checker_sha256"`
	FailedReviewSHA        string    `json:"failed_review_sha256"`
	FailedQualificationSHA string    `json:"failed_qualification_sha256"`
	PendingKey             string    `json:"pending_key"`
	PendingIntentSHA       string    `json:"pending_intent_sha256"`
	PendingStartedAt       time.Time `json:"pending_started_at"`
	MaterialPath           string    `json:"ingress_material_path"`
	MaterialSHA            string    `json:"ingress_material_sha256"`
	SecretID               string    `json:"ingress_secret_id"`
	SecretUID              string    `json:"ingress_secret_uid"`
	IdentityPlanSHA        string    `json:"ingress_identity_plan_sha256"`
	RegistrationPlanSHA    string    `json:"account_registration_plan_sha256"`
	CustodyRetirementSHA   string    `json:"custody_retirement_plan_sha256"`
	FirstCallerMinutes     int       `json:"first_caller_overall_minutes"`
	CredentialReserve      int       `json:"credential_reserve_minutes"`
	FirstAdmissionReserve  int       `json:"required_reserve_at_first_admission_minutes"`
	Allowed                []string  `json:"allowed_steps"`
	Forbidden              []string  `json:"forbidden_steps"`
	Completion             []string  `json:"completion_requirements"`
	RetryReusesClock       *bool     `json:"retry_reuses_original_new_phase_clock"`
	ExecutionAuthority     *bool     `json:"execution_authority"`
	Status                 string    `json:"status"`
	RecordedAt             time.Time `json:"recorded_at"`
}

const managedLateNormalTailProposalPath = "deployment/repairs/managed-late-remaining-normal-tail-proposal-90m.json"
const managedLateNormalTailApprovalPath = "deployment/repairs/managed-late-remaining-normal-tail-operator-approval.json"
const managedLateNormalTailSourcePath = "deployment/repairs/managed-late-remaining-normal-tail-source-journal.json"

func managedLateNormalTailApprovalRecordValid(a managedLateNormalTailApprovalRecord, environment, parent string, now time.Time) bool {
	return environment == "dev" && a.Schema == "rtk-cloud.remaining-normal-tail-operator-approval/v1" && a.Environment == environment && a.Parent == parent && a.Approved && a.FirstCallerMinutes == 90 && a.ExecutionAuthority != nil && !*a.ExecutionAuthority && strings.TrimSpace(a.HumanMessage) != "" && !a.RecordedAt.IsZero() && !a.RecordedAt.After(now) && environmentPreflightDigest.MatchString(a.ProposalSHA) && environmentPreflightDigest.MatchString(a.SourceRawSHA)
}

func managedLateNormalTailProposalValid(p managedLateNormalTailProposal, a managedLateNormalTailApprovalRecord, environment, parent string, now time.Time) bool {
	if environment != "dev" || p.Schema != "rtk-cloud.remaining-normal-tail-proposal/v1" || p.Environment != environment || p.Parent != parent || p.SourcePath != managedLateNormalTailSourcePath || p.SourceRawSHA != a.SourceRawSHA || p.SourceEntries != 433 || p.SourceLeases < 5 || p.FirstCallerMinutes != 90 || p.CredentialReserve != 5 || p.FirstAdmissionReserve != 15 || p.RetryReusesClock == nil || !*p.RetryReusesClock || p.ExecutionAuthority == nil || *p.ExecutionAuthority || p.Status != "proposal-awaiting-explicit-approval" || p.RecordedAt.IsZero() || p.RecordedAt.After(a.RecordedAt) || p.RecordedAt.After(now) || p.PendingKey != environmentCreateIngressAdmissionEntry || p.PendingStartedAt.IsZero() || p.PendingStartedAt.After(p.RecordedAt) || p.MaterialPath != environmentCreateIngressAdmissionMaterialPath(parent) || p.SecretID != "Secret/video-cloud-dev-ingress/"+environmentCreateIngressAdmissionSecret || !environmentBootstrapOwnerUUID.MatchString(p.SecretUID) || p.PriorCleanupDeadline.IsZero() || !p.PriorOverallDeadline.After(p.PriorCleanupDeadline) || !strings.HasPrefix(p.FailurePath, "deployment/reports/") || filepath.Clean(p.FailurePath) != p.FailurePath || !strings.HasPrefix(filepath.Base(p.FailurePath), "dev-ingress-dependency-order-failure-") || !strings.HasSuffix(p.FailurePath, ".json") {
		return false
	}
	for _, sha := range []string{parent, p.SourceRawSHA, p.OwnerSHA, p.Source428RawSHA, p.PriorAuthorityRawSHA, p.CompletedProofSHA, p.FailureSHA, p.FailedCheckerSHA, p.FailedReviewSHA, p.FailedQualificationSHA, p.PendingIntentSHA, p.MaterialSHA, p.IdentityPlanSHA, p.RegistrationPlanSHA, p.CustodyRetirementSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return slices.Equal(p.Allowed, []string{"resume-original-pending-ingress-admission", "ingress-controller-and-webhook", "maintained-workloads", "original-external-planes", "environment-dns", "turn-acceptance", "full-rollout-acceptance", "final-volume-ledger", "original-CompleteAndRelease"}) && slices.Equal(p.Forbidden, []string{"bootstrap-session-or-identity-generation", "native-finalizer-POST", "signing-or-revocation-replay", "residual-copy-cleanup", "completed-account-registration-replay", "completed-custody-or-export-helper-recreation", "ingress-identity-regeneration-or-secret-replacement", "old-authority-or-receipt-rewrite", "cross-environment-or-worktree-fallback"}) && slices.Equal(p.Completion, []string{"FULL-rollout-acceptance", "original-parent-completed-and-fence-archived", "independent-standard-FULL-post-health-started-after-parent-completion", "protected19-unchanged", "original18-volumes-retained"})
}

func managedLateNormalTailHistoricalWindowValid(source environmentCreateJournal, overall, recorded time.Time) bool {
	if len(source.Entries) != 433 || overall.IsZero() || recorded.IsZero() {
		return false
	}
	first := source.Entries[428].StartedAt
	if first.IsZero() {
		return false
	}
	for i, entry := range source.Entries[428:] {
		if entry.StartedAt.IsZero() || entry.StartedAt.Before(first) || entry.StartedAt.After(overall) || entry.StartedAt.After(recorded) {
			return false
		}
		if i != 3 && (entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(overall) || entry.CompletedAt.After(recorded)) {
			return false
		}
	}
	return true
}

func managedLateNormalTailHistoricalQualificationValid(p managedLateNormalTailProposal, source environmentCreateJournal, prefix *environmentCreateExecution, raw []byte) bool {
	var failed createFoundationRecoveryArtifact
	return len(source.Entries) == 433 && environmentPreflightBytesSHA256(raw) == p.FailedQualificationSHA && decodeEnvironmentJSON(raw, &failed) == nil && failed.Plan.CheckerSHA == p.FailedCheckerSHA && managedLateQualifiedArtifactValid(failed, prefix, failed.Plan, p.FailedReviewSHA, source.Entries[428].StartedAt, managedLateExpiredRenewalSchema)
}

// An operator decision selects immutable historical evidence. Only a new fresh
// six-gate qualification and once-only phase admission can authorize execution.
func loadManagedLateNormalTailApproval(store secretStore, parent string) (*createManagedLateNormalTailApproval, error) {
	bad := func() (*createManagedLateNormalTailApproval, error) {
		return nil, createOpenBaoACKBlock("normal-tail-operator-approval")
	}
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	read := func(relative, sha string) ([]byte, error) {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || sha != "" && !environmentPreflightDigest.MatchString(sha) {
			return nil, errors.New("invalid normal-tail evidence path")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || sha != "" && environmentPreflightBytesSHA256(raw) != sha {
			return nil, errors.New("normal-tail evidence differs")
		}
		return raw, nil
	}
	approvalPath, err := privateDeploymentStatePath(store.Environment, managedLateNormalTailApprovalPath)
	if err != nil || approvalPath != filepath.Join(store.Root, managedLateNormalTailApprovalPath) {
		return bad()
	}
	if _, err := os.Lstat(approvalPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	araw, err := read(managedLateNormalTailApprovalPath, "")
	var a managedLateNormalTailApprovalRecord
	if err != nil || decodeEnvironmentJSON(araw, &a) != nil || !managedLateNormalTailApprovalRecordValid(a, store.Environment, parent, time.Now().UTC()) {
		return bad()
	}
	praw, err := read(managedLateNormalTailProposalPath, a.ProposalSHA)
	var p managedLateNormalTailProposal
	if err != nil || decodeEnvironmentJSON(praw, &p) != nil || !managedLateNormalTailProposalValid(p, a, store.Environment, parent, time.Now().UTC()) {
		return bad()
	}
	jraw, err := read(p.SourcePath, p.SourceRawSHA)
	var source environmentCreateJournal
	if err != nil || decodeEnvironmentJSON(jraw, &source) != nil || !managedLateNormalTailSelected(source) || source.Environment != store.Environment || source.PlanSHA256 != parent || len(source.Entries) != p.SourceEntries || len(source.KubernetesLeases) != p.SourceLeases {
		return bad()
	}
	pair, err := readManagedLateResidualRemainingPair(store, parent)
	var phase createManagedLateResidualCleanupAuthority
	if err != nil || pair == nil || environmentPreflightBytesSHA256(pair) != p.PriorAuthorityRawSHA || decodeEnvironmentJSON(pair, &phase) != nil || phase.ParentSHA != parent || !phase.Deadline.Equal(p.PriorCleanupDeadline) || !phase.OverallDeadline.Equal(p.PriorOverallDeadline) || !managedLateNormalTailHistoricalWindowValid(source, phase.OverallDeadline, p.RecordedAt) {
		return bad()
	}
	var r createManagedLateRemaining
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", phase.Review, managedLateRemainingFilename), &r) != nil || r.ParentSHA != parent || r.Review != phase.Review || r.ResidualRemaining == nil {
		return bad()
	}
	if r.Authority, err = readManagedLateRemainingAuthority(store, &r); err != nil {
		return bad()
	}
	if r.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, &r); err != nil {
		return bad()
	}
	if r.FinalizationAuthority, err = readManagedLateFinalizationAuthority(store, &r); err != nil {
		return bad()
	}
	if r.TerminalAuthority, err = readManagedLateTerminalFinalizerAuthority(store, &r); err != nil {
		return bad()
	}
	if r.PostNativeAuthority, err = readManagedLatePostNativeCleanupAuthority(store, &r); err != nil {
		return bad()
	}
	if r.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, &r); err != nil {
		return bad()
	}
	if r.ResidualRemainingAuthority, err = readManagedLateResidualRemainingAuthority(store, &r); err != nil || r.ResidualRemainingAuthority == nil || deploymentplan.Digest(r.ResidualRemainingAuthority) != deploymentplan.Digest(phase) {
		return bad()
	}
	ownerRaw, err := read(filepath.Join("deployment/create", parent, "bootstrap/service/managed-late-residual-copy-cleanup-source-owner.json"), "")
	var owner environmentCreateOwner
	if err != nil || decodeEnvironmentJSON(ownerRaw, &owner) != nil || deploymentplan.Digest(owner) != p.OwnerSHA || r.OwnerSHA != p.OwnerSHA || owner.PlanSHA256 != parent || owner.Environment != store.Environment || owner.Stack != source.Stack || owner.OwnerToken != source.OwnerToken {
		return bad()
	}
	e := &environmentCreateExecution{Store: store, Journal: source, owner: owner}
	proofRaw, err := read(managedLateTerminalFinalizerCompletedPath(parent), p.CompletedProofSHA)
	if err != nil || len(proofRaw) == 0 || validateManagedLateTerminalFinalizerCompletion(store, e, &createManagedLateSessionRecovery{Review: r.OriginalReview}) != nil || validateManagedLateNormalTailStages(store, e, &r) != nil {
		return bad()
	}
	prefix := *e
	prefix.Journal = e.Journal
	prefix.Journal.Entries = prefix.Journal.Entries[:428]
	base := r.FinalizationAuthority.SourceLeases
	if base < 0 || len(prefix.Journal.KubernetesLeases) < base+2 {
		return bad()
	}
	prefix.Journal.KubernetesLeases = prefix.Journal.KubernetesLeases[:base+2]
	if environmentPreflightBytesSHA256(mustProviderRecoveryJSON(prefix.Journal)) != p.Source428RawSHA || validateManagedLateCompletedNormalStages(store, &prefix, &r) != nil {
		return bad()
	}
	pending := source.Entries[431]
	if pending.Key != p.PendingKey || pending.IntentSHA256 != p.PendingIntentSHA || !pending.StartedAt.Equal(p.PendingStartedAt) {
		return bad()
	}
	for key, expected := range map[string]string{"kubernetes/account-manager-registration": p.RegistrationPlanSHA, "kubernetes/retire-" + r.Ceremony.CustodyReader: p.CustodyRetirementSHA, "kubernetes/ingress-admission-identity": p.IdentityPlanSHA} {
		plan, _, err := managedTransportPostAcceptedStage(store, e, key)
		if err != nil || plan.SHA256() != expected {
			return bad()
		}
	}
	identity, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/ingress-admission-identity")
	if err != nil || len(identity.Resources) != 1 || identity.Resources[0].ID != p.SecretID || journal.Owners[p.SecretID] != p.SecretUID {
		return bad()
	}
	if _, err := read(p.MaterialPath, p.MaterialSHA); err != nil {
		return bad()
	}
	failureRaw, err := read(p.FailurePath, p.FailureSHA)
	var failure deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(failureRaw, &failure) != nil || failure["schema"] != "rtk-cloud.create-openbao-recovery-result/v1" || failure["environment"] != store.Environment || failure["parent_plan_sha256"] != parent || failure["decision"] != "NO-GO" || failure["completed"] != false {
		return bad()
	}
	qualificationRaw, err := read(filepath.Join("deployment/create/openbao-recovery", p.FailedReviewSHA, "qualification.json"), p.FailedQualificationSHA)
	if err != nil || !managedLateNormalTailHistoricalQualificationValid(p, source, &prefix, qualificationRaw) {
		return bad()
	}
	return &createManagedLateNormalTailApproval{ProposalSHA: a.ProposalSHA, ApprovalSHA: environmentPreflightBytesSHA256(araw), ParentSHA: parent, SourceSHA: deploymentplan.Digest(source), SourceRawSHA: p.SourceRawSHA, OwnerSHA: p.OwnerSHA, OriginalAuthoritySHA: deploymentplan.Digest(phase), RawAuthoritySHA: p.PriorAuthorityRawSHA, CompletedProofSHA: p.CompletedProofSHA, PendingSHA: deploymentplan.Digest(pending), IdentityPlanSHA: p.IdentityPlanSHA, FailureSHA: p.FailureSHA, FirstCallerLimit: 90 * time.Minute, ApprovedAt: a.RecordedAt}, nil
}

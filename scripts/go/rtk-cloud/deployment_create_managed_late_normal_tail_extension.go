package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

const managedLateNormalTailExtensionSchema = "rtk-cloud.managed-late-normal-tail-timeout-extension/v1"
const managedLateNormalTailExtensionLimit = 360 * time.Minute
const managedLateNormalTailExtensionMaterial = "accepted-service-core-late-normal-tail-extension-"
const managedLateNormalTailExtensionFilename = "managed-late-normal-tail-timeout-extension.json"
const managedLateNormalTailExtensionProposalPath = "deployment/repairs/managed-late-normal-tail-timeout-extension-proposal-total360m.json"
const managedLateNormalTailExtensionApprovalPath = "deployment/repairs/managed-late-normal-tail-timeout-extension-operator-approval.json"
const managedLateNormalTailExtensionSourcePath = "deployment/repairs/managed-late-normal-tail-timeout-extension-source-journal.json"
const managedLateNormalTailExtensionOwnerPath = "deployment/repairs/managed-late-normal-tail-timeout-extension-source-owner.json"

type managedLateNormalTailExtensionProposal struct {
	Schema                   string    `json:"schema"`
	Environment              string    `json:"environment"`
	Parent                   string    `json:"parent_plan_sha256"`
	SourcePath               string    `json:"source_journal_path"`
	SourceRawSHA             string    `json:"source_journal_sha256"`
	OwnerPath                string    `json:"source_owner_path"`
	OwnerSHA                 string    `json:"source_owner_sha256"`
	SourceEntries            int       `json:"source_entries"`
	SourceLeases             int       `json:"source_leases"`
	OriginalAuthorityRawSHA  string    `json:"original_authority_raw_sha256"`
	BaseAuthoritySHA         string    `json:"base_supplement_authority_sha256"`
	BaseAuthorityRawSHA      string    `json:"base_supplement_authority_raw_sha256"`
	BaseReview               string    `json:"base_supplement_review_sha256"`
	BaseQualifiedBytesSHA    string    `json:"base_supplement_qualified_bytes_sha256"`
	CompletedProofSHA        string    `json:"terminal_completion_sha256"`
	PreparedCheckerSHA       string    `json:"prepared_checker_sha256"`
	OriginalStartedAt        time.Time `json:"original_started_at"`
	BaseDeadline             time.Time `json:"base_supplement_deadline"`
	ExtendedDeadline         time.Time `json:"extended_deadline"`
	TotalMinutes             int       `json:"total_authorized_minutes"`
	FirstReserveMinutes      int       `json:"required_reserve_at_first_admission_minutes"`
	CredentialReserveMinutes int       `json:"credential_reserve_minutes"`
	ExecutionAuthority       *bool     `json:"execution_authority"`
	Status                   string    `json:"status"`
	RecordedAt               time.Time `json:"recorded_at"`
	Allowed                  []string  `json:"allowed_steps"`
	Forbidden                []string  `json:"forbidden_steps"`
	Completion               []string  `json:"completion_requirements"`
}
type managedLateNormalTailExtensionApprovalRecord struct {
	Schema                  string    `json:"schema"`
	Environment             string    `json:"environment"`
	Parent                  string    `json:"parent_plan_sha256"`
	ProposalSHA             string    `json:"proposal_sha256"`
	SourceRawSHA            string    `json:"source_journal_sha256"`
	OriginalAuthorityRawSHA string    `json:"original_authority_raw_sha256"`
	BaseAuthorityRawSHA     string    `json:"base_supplement_authority_raw_sha256"`
	TotalMinutes            int       `json:"total_authorized_minutes"`
	Approved                bool      `json:"approved"`
	ExecutionAuthority      *bool     `json:"execution_authority"`
	RecordedAt              time.Time `json:"recorded_at"`
	HumanMessage            string    `json:"human_message"`
}
type createManagedLateNormalTailExtensionApproval struct {
	ProposalSHA, ApprovalSHA, ParentSHA, SourceSHA, SourceRawSHA, OwnerSHA                                                                                         string
	OriginalAuthoritySHA, OriginalAuthorityRawSHA, BaseAuthoritySHA, BaseAuthorityRawSHA, BaseReview, BaseQualifiedBytesSHA, CompletedProofSHA, PreparedCheckerSHA string
	OriginalStartedAt, BaseDeadline, ExtendedDeadline, ApprovedAt                                                                                                  time.Time
}
type createManagedLateNormalTailExtension struct {
	Approval          *createManagedLateNormalTailExtensionApproval
	CredentialSHA     string
	CredentialExpires time.Time
	Authority         *createManagedLateNormalTailExtensionAuthority `json:"-"`
}
type createManagedLateNormalTailExtensionAuthority struct {
	Schema, ParentSHA, SourceSHA, SourceRawSHA, OwnerSHA, ProposalSHA, ApprovalSHA                                          string
	OriginalAuthoritySHA, OriginalAuthorityRawSHA, BaseAuthoritySHA, BaseAuthorityRawSHA, BaseReview, BaseQualifiedBytesSHA string
	IntentSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA, DescriptorSHA, CredentialSHA                             string
	OriginalStartedAt, BaseDeadline, ExtendedDeadline, FirstCallerDeadline, AdmittedAt, Deadline, CredentialExpires         time.Time
	TotalLimit                                                                                                              time.Duration
}

func managedLateNormalTailExtensionPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateNormalTailExtensionSchema, "anchor": "immutable-original-normal-authority.StartedAt", "total": managedLateNormalTailExtensionLimit.String(), "scope": "same-parent-existing-normal-tail-and-qualified-Redis-cutover-only", "admission": "paired-exclusive-witness-first", "historical": "original90-and-actual180-and-all-PKI-receipts-unchanged", "credential-reserve": managedLateExpiryReserve.String()})
}
func managedLateNormalTailExtensionIntent(r *createServiceSessionRecovery) string {
	if r == nil || r.normalSupplement == nil || r.normalSupplement.Extension == nil {
		return ""
	}
	c := *r.normalSupplement.Extension
	c.Authority = nil
	return deploymentplan.Digest(c)
}
func managedLateNormalTailExtensionAuthoritySHA(r *createServiceSessionRecovery) string {
	if r == nil || r.normalSupplement == nil || r.normalSupplement.Extension == nil || r.normalSupplement.Extension.Authority == nil {
		return ""
	}
	return deploymentplan.Digest(r.normalSupplement.Extension.Authority)
}
func managedLateNormalTailExtensionDeadline(original, caller, credential time.Time) time.Time {
	end := original.Add(managedLateNormalTailExtensionLimit)
	if caller.Before(end) {
		end = caller
	}
	if !credential.IsZero() && credential.Add(-managedLateExpiryReserve).Before(end) {
		end = credential.Add(-managedLateExpiryReserve)
	}
	return end
}
func managedLateNormalTailExtensionPauseMatches(a *createManagedLateNormalTailExtensionAuthority, deadline time.Time) bool {
	return managedLateNormalTailExtensionAuthorityValid(a) && a.Deadline.Equal(deadline)
}
func managedLateNormalTailExtensionAuthorityValid(a *createManagedLateNormalTailExtensionAuthority) bool {
	if a == nil || a.Schema != managedLateNormalTailExtensionSchema || a.TotalLimit != managedLateNormalTailExtensionLimit || a.OriginalStartedAt.IsZero() || a.BaseDeadline.IsZero() || !a.BaseDeadline.After(a.OriginalStartedAt) || a.BaseDeadline.After(a.OriginalStartedAt.Add(managedLateNormalTailSupplementLimit)) || !a.ExtendedDeadline.Equal(a.OriginalStartedAt.Add(managedLateNormalTailExtensionLimit)) || a.FirstCallerDeadline.IsZero() || a.AdmittedAt.Before(a.OriginalStartedAt) || !a.AdmittedAt.Before(a.Deadline) || a.Deadline.Sub(a.AdmittedAt) < managedLateFinalizerLimit || !a.Deadline.Equal(managedLateNormalTailExtensionDeadline(a.OriginalStartedAt, a.FirstCallerDeadline, a.CredentialExpires)) {
		return false
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.SourceRawSHA, a.OwnerSHA, a.ProposalSHA, a.ApprovalSHA, a.OriginalAuthoritySHA, a.OriginalAuthorityRawSHA, a.BaseAuthoritySHA, a.BaseAuthorityRawSHA, a.BaseReview, a.BaseQualifiedBytesSHA, a.IntentSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.CredentialSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return true
}
func managedLateNormalTailExtensionPath(parent string, witness bool) string {
	name := "managed-late-normal-tail-timeout-extension-authority.json"
	if witness {
		name = "managed-late-normal-tail-timeout-extension-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}
func readManagedLateNormalTailExtensionPair(store secretStore, parent string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("normal-tail-extension-authority-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLateNormalTailExtensionPath(parent, false), managedLateNormalTailExtensionPath(parent, true)}
	absent := 0
	for i, rel := range paths {
		p, err := privateDeploymentStatePath(store.Environment, rel)
		if err != nil || p != filepath.Join(store.Root, rel) || validateEnvironmentPreflightDirectories(store, filepath.Dir(p)) != nil {
			return bad()
		}
		paths[i] = p
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			absent++
		} else if err != nil {
			return bad()
		}
	}
	if absent == 2 {
		return nil, nil
	}
	if absent != 0 {
		return bad()
	}
	a, ae := readBoundedEnvironmentFile(paths[0], true)
	b, be := readBoundedEnvironmentFile(paths[1], true)
	if ae != nil || be != nil || len(a) == 0 || !bytes.Equal(a, b) {
		return bad()
	}
	return a, nil
}
func writeManagedLateNormalTailExtensionPair(store secretStore, parent string, raw []byte) error {
	old, err := readManagedLateNormalTailExtensionPair(store, parent)
	if err != nil || old != nil || len(raw) == 0 {
		return createOpenBaoACKBlock("normal-tail-extension-authority-exists")
	}
	for _, w := range []bool{true, false} {
		p, err := privateDeploymentStatePath(store.Environment, managedLateNormalTailExtensionPath(parent, w))
		if err != nil || p != filepath.Join(store.Root, managedLateNormalTailExtensionPath(parent, w)) || writeExclusivePrivateDeploymentJSON(p, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(p)) != nil {
			return createOpenBaoACKBlock("normal-tail-extension-exclusive-authority")
		}
	}
	got, err := readManagedLateNormalTailExtensionPair(store, parent)
	if err != nil || !bytes.Equal(got, raw) {
		return createOpenBaoACKBlock("normal-tail-extension-authority-readback")
	}
	return nil
}

func loadManagedLateNormalTailExtensionApproval(store secretStore, parent string) (*createManagedLateNormalTailExtensionApproval, error) {
	bad := func() (*createManagedLateNormalTailExtensionApproval, error) {
		return nil, createOpenBaoACKBlock("normal-tail-extension-operator-approval")
	}
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	read := func(relative string) ([]byte, error) {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return nil, createOpenBaoACKBlock("normal-tail-extension-private-source")
		}
		return readBoundedEnvironmentFile(path, true)
	}
	praw, err := read(managedLateNormalTailExtensionProposalPath)
	if err != nil {
		return bad()
	}
	araw, err := read(managedLateNormalTailExtensionApprovalPath)
	if err != nil {
		return bad()
	}
	var p managedLateNormalTailExtensionProposal
	var a managedLateNormalTailExtensionApprovalRecord
	now := time.Now().UTC()
	if decodeEnvironmentJSON(praw, &p) != nil || decodeEnvironmentJSON(araw, &a) != nil || p.Schema != "rtk-cloud.remaining-normal-tail-timeout-extension-proposal/v1" || a.Schema != "rtk-cloud.remaining-normal-tail-timeout-extension-operator-approval/v1" || p.Environment != store.Environment || a.Environment != store.Environment || p.Parent != parent || a.Parent != parent || a.ProposalSHA != environmentPreflightBytesSHA256(praw) || a.SourceRawSHA != p.SourceRawSHA || a.OriginalAuthorityRawSHA != p.OriginalAuthorityRawSHA || a.BaseAuthorityRawSHA != p.BaseAuthorityRawSHA || !a.Approved || a.TotalMinutes != 360 || p.TotalMinutes != 360 || p.FirstReserveMinutes != 15 || p.CredentialReserveMinutes != 5 || a.ExecutionAuthority == nil || *a.ExecutionAuthority || p.ExecutionAuthority == nil || *p.ExecutionAuthority || strings.TrimSpace(a.HumanMessage) == "" || a.RecordedAt.IsZero() || a.RecordedAt.After(now) || p.RecordedAt.IsZero() || p.RecordedAt.After(a.RecordedAt) || p.Status != "proposal-awaiting-explicit-approval" || p.SourcePath != managedLateNormalTailExtensionSourcePath || p.OwnerPath != managedLateNormalTailExtensionOwnerPath || p.SourceEntries != 434 || p.SourceLeases != 149 || !slices.Equal(p.Allowed, managedLateNormalTailSupplementAllowed()) || !slices.Equal(p.Forbidden, managedLateNormalTailSupplementForbidden()) || !slices.Equal(p.Completion, managedLateNormalTailSupplementCompletion()) || !p.ExtendedDeadline.Equal(p.OriginalStartedAt.Add(managedLateNormalTailExtensionLimit)) {
		return bad()
	}
	for _, sha := range []string{p.SourceRawSHA, p.OwnerSHA, p.OriginalAuthorityRawSHA, p.BaseAuthoritySHA, p.BaseAuthorityRawSHA, p.BaseReview, p.BaseQualifiedBytesSHA, p.CompletedProofSHA, p.PreparedCheckerSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return bad()
		}
	}
	original, err := loadManagedLateNormalTailSupplementApproval(store, parent)
	if err != nil {
		return bad()
	}
	base, err := readManagedLateNormalTailSupplementAuthority(store, parent)
	if err != nil || base == nil {
		return bad()
	}
	baseRaw, err := readManagedLateNormalTailSupplementPair(store, parent)
	if err != nil || deploymentplan.Digest(base) != p.BaseAuthoritySHA || environmentPreflightBytesSHA256(baseRaw) != p.BaseAuthorityRawSHA || base.OriginalAuthoritySHA != original.OriginalAuthoritySHA || base.OriginalAuthorityRawSHA != p.OriginalAuthorityRawSHA || base.ParentSHA != parent || base.OwnerSHA != p.OwnerSHA || base.SourceRawSHA != p.SourceRawSHA || base.Review != p.BaseReview || base.QualifiedBytesSHA != p.BaseQualifiedBytesSHA || !base.OriginalStartedAt.Equal(p.OriginalStartedAt) || !base.Deadline.Equal(p.BaseDeadline) || original.CompletedProofSHA != p.CompletedProofSHA {
		return bad()
	}
	sraw, err := read(p.SourcePath)
	if err != nil || environmentPreflightBytesSHA256(sraw) != p.SourceRawSHA {
		return bad()
	}
	oraw, err := read(p.OwnerPath)
	if err != nil {
		return bad()
	}
	var source environmentCreateJournal
	var owner environmentCreateOwner
	if decodeEnvironmentJSON(sraw, &source) != nil || decodeEnvironmentJSON(oraw, &owner) != nil || deploymentplan.Digest(owner) != p.OwnerSHA || source.PlanSHA256 != parent || source.OwnerToken != owner.OwnerToken || owner.PlanSHA256 != parent || source.Environment != store.Environment || !managedLateNormalTailControllerSelected(source) || source.Entries[431].State != "completed" || len(source.KubernetesLeases) != 149 || deploymentplan.Digest(source) != base.SourceSHA || source.Entries[433].CompletedAt.After(p.RecordedAt) {
		return bad()
	}
	return &createManagedLateNormalTailExtensionApproval{ProposalSHA: a.ProposalSHA, ApprovalSHA: environmentPreflightBytesSHA256(araw), ParentSHA: parent, SourceSHA: deploymentplan.Digest(source), SourceRawSHA: p.SourceRawSHA, OwnerSHA: p.OwnerSHA, OriginalAuthoritySHA: base.OriginalAuthoritySHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, BaseAuthoritySHA: p.BaseAuthoritySHA, BaseAuthorityRawSHA: p.BaseAuthorityRawSHA, BaseReview: p.BaseReview, BaseQualifiedBytesSHA: p.BaseQualifiedBytesSHA, CompletedProofSHA: p.CompletedProofSHA, PreparedCheckerSHA: p.PreparedCheckerSHA, OriginalStartedAt: p.OriginalStartedAt, BaseDeadline: p.BaseDeadline, ExtendedDeadline: p.ExtendedDeadline, ApprovedAt: a.RecordedAt}, nil
}
func prepareManagedLateNormalTailExtension(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r == nil || r.normalSupplement == nil {
		return nil
	}
	if e == nil || e.Check(ctx) != nil || store != e.Store {
		return createOpenBaoACKBlock("normal-tail-extension-source")
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateNormalTailExtensionApprovalPath)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		raw, pairErr := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256)
		if pairErr != nil || raw != nil {
			return createOpenBaoACKBlock("normal-tail-extension-missing-approval")
		}
		return nil
	} else if err != nil {
		return err
	}
	p, err := loadManagedLateNormalTailExtensionApproval(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	historical, sourceErr := managedLateNormalTailHistoricalSource(store, e)
	if sourceErr != nil {
		return sourceErr
	}
	if cfg.Environment != store.Environment || !managedLateNormalTailControllerSelected(historical.Journal) || deploymentplan.Digest(historical.Journal) != p.SourceSHA || deploymentplan.Digest(e.owner) != p.OwnerSHA || r.normalSupplement.Authority == nil || deploymentplan.Digest(r.normalSupplement.Authority) != p.BaseAuthoritySHA {
		return createOpenBaoACKBlock("normal-tail-extension-exact434")
	}
	x := &createManagedLateNormalTailExtension{Approval: p, CredentialSHA: managedLateNormalTailCredentialSHA(r.normalTail), CredentialExpires: r.normalTail.CredentialExpires}
	r.normalSupplement.Extension = x
	x.Authority, err = readManagedLateNormalTailExtensionAuthority(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	if managedLateRedisPrePauseSelected(e.Journal) && x.Authority == nil {
		return createOpenBaoACKBlock("normal-tail-pre-pause-existing360-required")
	}
	return e.Check(ctx)
}
func newManagedLateNormalTailExtensionAuthority(r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, rawSHA string, caller, now time.Time) (*createManagedLateNormalTailExtensionAuthority, error) {
	bad := func() (*createManagedLateNormalTailExtensionAuthority, error) {
		return nil, createOpenBaoACKBlock("normal-tail-extension-fresh-six-go")
	}
	if r == nil || r.normalTail == nil || r.normalSupplement == nil || r.normalSupplement.Extension == nil || r.normalSupplement.Extension.Approval == nil {
		return bad()
	}
	n, s := r.normalTail, r.normalSupplement
	x := s.Extension
	p := x.Approval
	base := s.Authority
	if n.CurrentSHA != p.SourceSHA || n.Authority == nil || base == nil || deploymentplan.Digest(base) != p.BaseAuthoritySHA || base.OriginalAuthoritySHA != p.OriginalAuthoritySHA || base.OriginalAuthorityRawSHA != p.OriginalAuthorityRawSHA || deploymentplan.Digest(n.Authority) != p.OriginalAuthoritySHA || base.ParentSHA != p.ParentSHA || base.OwnerSHA != p.OwnerSHA || base.SourceSHA != p.SourceSHA || base.SourceRawSHA != p.SourceRawSHA || base.Review != p.BaseReview || base.QualifiedBytesSHA != p.BaseQualifiedBytesSHA || !base.OriginalStartedAt.Equal(p.OriginalStartedAt) || !base.Deadline.Equal(p.BaseDeadline) || now.Before(p.ApprovedAt) || a.Plan.Schema != managedLateNormalTailSchema || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.ParentSHA != p.ParentSHA || a.Plan.ParentOwnerSHA != p.OwnerSHA || a.Plan.ParentJournalSHA != p.SourceSHA || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || a.Plan.KubeconfigSHA != n.KubeconfigSHA || a.Plan.Materials[managedLateNormalTailMaterial+"source"] != managedLateNormalTailDigest(n) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"intent"] != managedLateNormalTailExtensionIntent(r) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"policy"] != managedLateNormalTailExtensionPolicySHA() || !managedLateResidualCleanupEvidenceValid(n.NativeEvidence) || !managedLateNormalTailSupplementCredentialValid(n) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != deploymentplan.Digest(a.Plan) || len(a.Qualification.Gates) != 6 || x.CredentialSHA != managedLateNormalTailCredentialSHA(n) || x.CredentialSHA != n.NativeEvidence["current-credential"] || !x.CredentialExpires.Equal(n.CredentialExpires) {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != a.Qualification.PlanSHA256 {
			return bad()
		}
	}
	out := &createManagedLateNormalTailExtensionAuthority{Schema: managedLateNormalTailExtensionSchema, ParentSHA: p.ParentSHA, SourceSHA: p.SourceSHA, SourceRawSHA: p.SourceRawSHA, OwnerSHA: p.OwnerSHA, ProposalSHA: p.ProposalSHA, ApprovalSHA: p.ApprovalSHA, OriginalAuthoritySHA: p.OriginalAuthoritySHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, BaseAuthoritySHA: p.BaseAuthoritySHA, BaseAuthorityRawSHA: p.BaseAuthorityRawSHA, BaseReview: p.BaseReview, BaseQualifiedBytesSHA: p.BaseQualifiedBytesSHA, IntentSHA: managedLateNormalTailExtensionIntent(r), Review: a.Qualification.PlanSHA256, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: rawSHA, DescriptorSHA: managedLateNormalTailDigest(n), CredentialSHA: x.CredentialSHA, OriginalStartedAt: p.OriginalStartedAt, BaseDeadline: p.BaseDeadline, ExtendedDeadline: p.ExtendedDeadline, FirstCallerDeadline: caller, AdmittedAt: now, Deadline: managedLateNormalTailExtensionDeadline(p.OriginalStartedAt, caller, x.CredentialExpires), CredentialExpires: x.CredentialExpires, TotalLimit: managedLateNormalTailExtensionLimit}
	if !managedLateNormalTailExtensionAuthorityValid(out) {
		return bad()
	}
	return out, nil
}
func readManagedLateNormalTailExtensionAuthority(store secretStore, parent string) (*createManagedLateNormalTailExtensionAuthority, error) {
	raw, err := readManagedLateNormalTailExtensionPair(store, parent)
	if err != nil || raw == nil {
		return nil, err
	}
	bad := func() (*createManagedLateNormalTailExtensionAuthority, error) {
		return nil, createOpenBaoACKBlock("normal-tail-extension-immutable-authority")
	}
	var header createManagedLateNormalTailExtensionAuthority
	if decodeEnvironmentJSON(raw, &header) != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(header)) || !managedLateNormalTailExtensionAuthorityValid(&header) {
		return bad()
	}
	approval, err := loadManagedLateNormalTailExtensionApproval(store, parent)
	if err != nil {
		return bad()
	}
	var n createManagedLateNormalTail
	var s createManagedLateNormalTailSupplement
	var x createManagedLateNormalTailExtension
	base := filepath.Join("deployment/create/openbao-recovery", header.Review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateNormalTailFilename), &n) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateNormalTailSupplementFilename), &s) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateNormalTailExtensionFilename), &x) != nil || deploymentplan.Digest(x.Approval) != deploymentplan.Digest(approval) {
		return bad()
	}
	n.Authority, err = readManagedLateNormalTailCurrentAuthority(store, &n)
	if err != nil {
		return bad()
	}
	s.Authority, err = readManagedLateNormalTailSupplementAuthority(store, parent)
	if err != nil || s.Authority == nil {
		return bad()
	}
	s.Extension = &x
	r := &createServiceSessionRecovery{normalTail: &n, normalSupplement: &s}
	qraw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "qualification.json"), true)
	var a createFoundationRecoveryArtifact
	if err != nil || environmentPreflightBytesSHA256(qraw) != header.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != header.ArtifactSHA {
		return bad()
	}
	expected, err := newManagedLateNormalTailExtensionAuthority(r, a, header.QualifiedBytesSHA, header.FirstCallerDeadline, header.AdmittedAt)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(header) {
		return bad()
	}
	return &header, nil
}
func verifyManagedLateNormalTailExtensionRuntime(store secretStore, r *createServiceSessionRecovery) error {
	if r == nil || r.normalSupplement == nil || r.normalSupplement.Extension == nil {
		return nil
	}
	x := r.normalSupplement.Extension
	if x.Authority == nil {
		return createOpenBaoACKBlock("normal-tail-extension-admission-required")
	}
	a, err := readManagedLateNormalTailExtensionAuthority(store, r.normalTail.Approval.ParentSHA)
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(x.Authority) || !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("normal-tail-extension-fixed-deadline")
	}
	return nil
}
func admitManagedLateNormalTailExtension(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, rawSHA string) (context.Context, context.CancelFunc, error) {
	bad := func() (context.Context, context.CancelFunc, error) {
		return nil, nil, createOpenBaoACKBlock("normal-tail-extension-current-admission")
	}
	if e == nil || r == nil || r.normalTail == nil || r.normalSupplement == nil || r.normalSupplement.Extension == nil || e.Check(ctx) != nil || r.normalSupplement.Extension.Approval == nil {
		return bad()
	}
	resume := managedLateRedisPrePauseSelected(e.Journal)
	var resumeProof *managedLateRedisPrePause
	if resume {
		var sourceErr error
		resumeProof, _, sourceErr = readManagedLateRedisPrePauseSource(store, e)
		if sourceErr != nil {
			return nil, nil, sourceErr
		}
		if resumeProof.SourceSHA != r.normalSupplement.Extension.Approval.SourceSHA {
			return bad()
		}
	} else if r.normalSupplement.Extension.Approval.SourceSHA != deploymentplan.Digest(e.Journal) {
		return bad()
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || checker != a.Plan.CheckerSHA || a.QualifiedAt.After(time.Now().UTC()) || !time.Now().Before(a.ValidUntil) || a.Qualification.PlanSHA256 != deploymentplan.Digest(a.Plan) || a.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"intent"] != managedLateNormalTailExtensionIntent(r) {
		return bad()
	}
	existing, err := readManagedLateNormalTailExtensionAuthority(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, nil, err
	}
	if resume {
		if existing == nil || r.normalSupplement.Extension.Authority == nil || deploymentplan.Digest(existing) != deploymentplan.Digest(r.normalSupplement.Extension.Authority) || a.Plan.Materials[managedLateRedisDurableMaterial+"resume"] != deploymentplan.Digest(resumeProof) || !managedLateNormalTailPrePauseQualification(r, e, a, rawSHA, time.Now().UTC()) {
			return bad()
		}
		freshRaw, readErr := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", a.Qualification.PlanSHA256, "qualification.json"), true)
		if readErr != nil || environmentPreflightBytesSHA256(freshRaw) != rawSHA || environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)) != rawSHA || e.Check(ctx) != nil {
			return bad()
		}
	}
	if existing == nil {
		caller, ok := ctx.Deadline()
		if !ok {
			return bad()
		}
		phase, err := newManagedLateNormalTailExtensionAuthority(r, a, rawSHA, caller, time.Now().UTC())
		if err != nil {
			return nil, nil, err
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", phase.Review, "qualification.json"), true)
		approval, ae := loadManagedLateNormalTailExtensionApproval(store, e.Journal.PlanSHA256)
		if err != nil || environmentPreflightBytesSHA256(raw) != rawSHA || ae != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(r.normalSupplement.Extension.Approval) || e.Check(ctx) != nil {
			return bad()
		}
		if managedLateSourceReuseActive(e) {
			if err := checkManagedLateSourceReuse(e); err != nil {
				return nil, nil, err
			}
		}
		invalidateManagedLateSourceReuse(e)
		if err := writeManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
			return nil, nil, err
		}
		existing = phase
	}
	if !time.Now().Before(existing.Deadline) || existing.IntentSHA != managedLateNormalTailExtensionIntent(r) {
		return bad()
	}
	r.normalSupplement.Extension.Authority = existing
	r.normalTail.Supplement = r.normalSupplement
	bounded, cancel := context.WithDeadline(ctx, existing.Deadline)
	if err := e.Check(bounded); err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}

// Fresh437 qualification authorizes only readback/acceptance of the recorded
// helper and the remaining original migration under the already existing pair.
func managedLateNormalTailPrePauseQualification(r *createServiceSessionRecovery, e *environmentCreateExecution, a createFoundationRecoveryArtifact, rawSHA string, now time.Time) bool {
	if r == nil || r.normalTail == nil || e == nil || !managedLateRedisPrePauseSelected(e.Journal) || !environmentPreflightDigest.MatchString(rawSHA) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateNormalTailSchema || a.Plan.ParentSHA != e.Journal.PlanSHA256 || a.Plan.ParentOwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) || a.Plan.KubeconfigSHA != r.normalTail.KubeconfigSHA || a.Plan.Materials[managedLateNormalTailMaterial+"source"] != managedLateNormalTailDigest(r.normalTail) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"intent"] != managedLateNormalTailExtensionIntent(r) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"policy"] != managedLateNormalTailExtensionPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != deploymentplan.Digest(a.Plan) || len(a.Qualification.Gates) != 6 {
		return false
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != a.Qualification.PlanSHA256 {
			return false
		}
	}
	return true
}

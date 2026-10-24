package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateNormalTailSupplementSchema = "rtk-cloud.managed-late-normal-tail-timeout-supplement/v1"
const managedLateNormalTailSupplementLimit = 180 * time.Minute
const managedLateNormalTailSupplementMaterial = "accepted-service-core-late-normal-tail-supplement-"
const managedLateNormalTailSupplementFilename = "managed-late-normal-tail-timeout-supplement.json"
const managedLateNormalTailSupplementProposalPath = "deployment/repairs/managed-late-normal-tail-timeout-supplement-proposal-total180m.json"
const managedLateNormalTailSupplementApprovalPath = "deployment/repairs/managed-late-normal-tail-timeout-supplement-operator-approval.json"
const managedLateNormalTailSupplementSourcePath = "deployment/repairs/managed-late-normal-tail-timeout-supplement-source-journal.json"
const managedLateNormalTailSupplementOwnerPath = "deployment/repairs/managed-late-normal-tail-timeout-supplement-source-owner.json"

type managedLateNormalTailSupplementProposal struct {
	Schema                    string    `json:"schema"`
	Environment               string    `json:"environment"`
	Parent                    string    `json:"parent_plan_sha256"`
	SourcePath                string    `json:"source_journal_path"`
	SourceRawSHA              string    `json:"source_journal_sha256"`
	OwnerPath                 string    `json:"source_owner_path"`
	OwnerSHA                  string    `json:"source_owner_sha256"`
	SourceEntries             int       `json:"source_entries"`
	SourceLeases              int       `json:"source_leases"`
	OriginalAuthoritySHA      string    `json:"original_authority_sha256"`
	OriginalAuthorityRawSHA   string    `json:"original_authority_raw_sha256"`
	OriginalReview            string    `json:"original_review_sha256"`
	OriginalQualifiedBytesSHA string    `json:"original_qualified_bytes_sha256"`
	CompletedProofSHA         string    `json:"terminal_completion_sha256"`
	PreparedCheckerSHA        string    `json:"prepared_checker_sha256"`
	OriginalStartedAt         time.Time `json:"original_started_at"`
	OriginalDeadline          time.Time `json:"original_deadline"`
	ExtendedDeadline          time.Time `json:"extended_deadline"`
	TotalMinutes              int       `json:"total_authorized_minutes"`
	FirstReserveMinutes       int       `json:"required_reserve_at_first_admission_minutes"`
	CredentialReserveMinutes  int       `json:"credential_reserve_minutes"`
	ExecutionAuthority        *bool     `json:"execution_authority"`
	Status                    string    `json:"status"`
	RecordedAt                time.Time `json:"recorded_at"`
	Allowed                   []string  `json:"allowed_steps"`
	Forbidden                 []string  `json:"forbidden_steps"`
	Completion                []string  `json:"completion_requirements"`
}
type managedLateNormalTailSupplementApprovalRecord struct {
	Schema                  string    `json:"schema"`
	Environment             string    `json:"environment"`
	Parent                  string    `json:"parent_plan_sha256"`
	ProposalSHA             string    `json:"proposal_sha256"`
	SourceRawSHA            string    `json:"source_journal_sha256"`
	OriginalAuthorityRawSHA string    `json:"original_authority_raw_sha256"`
	TotalMinutes            int       `json:"total_authorized_minutes"`
	Approved                bool      `json:"approved"`
	ExecutionAuthority      *bool     `json:"execution_authority"`
	RecordedAt              time.Time `json:"recorded_at"`
	HumanMessage            string    `json:"human_message"`
}
type createManagedLateNormalTailSupplementApproval struct {
	ProposalSHA, ApprovalSHA, ParentSHA, SourceSHA, SourceRawSHA, OwnerSHA                                                          string
	OriginalAuthoritySHA, OriginalAuthorityRawSHA, OriginalReview, OriginalQualifiedBytesSHA, CompletedProofSHA, PreparedCheckerSHA string
	OriginalStartedAt, OriginalDeadline, ExtendedDeadline, ApprovedAt                                                               time.Time
}
type createManagedLateNormalTailSupplement struct {
	Extension         *createManagedLateNormalTailExtension `json:"-"`
	Approval          *createManagedLateNormalTailSupplementApproval
	CredentialSHA     string
	CredentialExpires time.Time
	Authority         *createManagedLateNormalTailSupplementAuthority `json:"-"`
}
type createManagedLateNormalTailSupplementAuthority struct {
	Schema, ParentSHA, SourceSHA, SourceRawSHA, OwnerSHA, ProposalSHA, ApprovalSHA                                      string
	OriginalAuthoritySHA, OriginalAuthorityRawSHA, OriginalReview, OriginalQualifiedBytesSHA                            string
	IntentSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA, DescriptorSHA, CredentialSHA                         string
	OriginalStartedAt, OriginalDeadline, ExtendedDeadline, FirstCallerDeadline, AdmittedAt, Deadline, CredentialExpires time.Time
	TotalLimit                                                                                                          time.Duration
}

func managedLateNormalTailSupplementPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateNormalTailSupplementSchema, "anchor": "immutable-original-normal-authority.StartedAt", "total": managedLateNormalTailSupplementLimit.String(), "scope": "same-parent-existing-normal-tail-and-qualified-Redis-cutover-only", "admission": "paired-exclusive-witness-first", "historical": "original90-and-all-PKI-receipts-unchanged", "credential-reserve": managedLateExpiryReserve.String()})
}
func managedLateNormalTailSupplementIntent(r *createServiceSessionRecovery) string {
	if r == nil || r.normalSupplement == nil {
		return ""
	}
	s := *r.normalSupplement
	s.Authority = nil
	return deploymentplan.Digest(s)
}
func managedLateNormalTailSupplementAuthoritySHA(r *createServiceSessionRecovery) string {
	if r == nil || r.normalSupplement == nil || r.normalSupplement.Authority == nil {
		return ""
	}
	return deploymentplan.Digest(r.normalSupplement.Authority)
}
func managedLateNormalTailEffectiveDeadline(r *createServiceSessionRecovery) time.Time {
	if r == nil || r.normalTail == nil || r.normalTail.Authority == nil {
		return time.Time{}
	}
	if r.normalSupplement != nil {
		if x := r.normalSupplement.Extension; x != nil {
			if x.Authority != nil {
				return x.Authority.Deadline
			}
			if x.Approval != nil {
				return managedLateNormalTailExtensionDeadline(x.Approval.OriginalStartedAt, x.Approval.ExtendedDeadline, x.CredentialExpires)
			}
		}
		if r.normalSupplement.Authority != nil {
			return r.normalSupplement.Authority.Deadline
		}
		if r.normalSupplement.Approval != nil {
			end := r.normalSupplement.Approval.ExtendedDeadline
			if expiry := r.normalSupplement.CredentialExpires; !expiry.IsZero() && expiry.Add(-managedLateExpiryReserve).Before(end) {
				end = expiry.Add(-managedLateExpiryReserve)
			}
			return end
		}
	}
	return r.normalTail.Authority.Deadline
}
func managedLateNormalTailSupplementDeadline(original, caller, credential time.Time) time.Time {
	end := original.Add(managedLateNormalTailSupplementLimit)
	if caller.Before(end) {
		end = caller
	}
	if !credential.IsZero() && credential.Add(-managedLateExpiryReserve).Before(end) {
		end = credential.Add(-managedLateExpiryReserve)
	}
	return end
}
func managedLateNormalTailSupplementPauseMatches(a *createManagedLateNormalTailSupplementAuthority, deadline time.Time) bool {
	return managedLateNormalTailSupplementAuthorityValid(a) && a.Deadline.Equal(deadline)
}
func managedLateNormalTailSupplementAuthorityValid(a *createManagedLateNormalTailSupplementAuthority) bool {
	if a == nil || a.Schema != managedLateNormalTailSupplementSchema || a.TotalLimit != managedLateNormalTailSupplementLimit || a.OriginalStartedAt.IsZero() || !a.OriginalDeadline.Equal(a.OriginalStartedAt.Add(managedLateOverallLimit)) || !a.ExtendedDeadline.Equal(a.OriginalStartedAt.Add(managedLateNormalTailSupplementLimit)) || a.FirstCallerDeadline.IsZero() || a.AdmittedAt.IsZero() || a.AdmittedAt.Before(a.OriginalStartedAt) || !a.Deadline.Equal(managedLateNormalTailSupplementDeadline(a.OriginalStartedAt, a.FirstCallerDeadline, a.CredentialExpires)) || !a.AdmittedAt.Before(a.Deadline) || a.Deadline.Sub(a.AdmittedAt) < managedLateFinalizerLimit {
		return false
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.SourceRawSHA, a.OwnerSHA, a.ProposalSHA, a.ApprovalSHA, a.OriginalAuthoritySHA, a.OriginalAuthorityRawSHA, a.OriginalReview, a.OriginalQualifiedBytesSHA, a.IntentSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.CredentialSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return true
}
func loadManagedLateNormalTailSupplementApproval(store secretStore, parent string) (*createManagedLateNormalTailSupplementApproval, error) {
	bad := func() (*createManagedLateNormalTailSupplementApproval, error) {
		return nil, createOpenBaoACKBlock("normal-tail-supplement-operator-approval")
	}
	if store.Environment != "dev" || !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	read := func(relative string) ([]byte, error) {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return nil, createOpenBaoACKBlock("normal-tail-supplement-private-source")
		}
		return readBoundedEnvironmentFile(path, true)
	}
	praw, err := read(managedLateNormalTailSupplementProposalPath)
	if err != nil {
		return bad()
	}
	araw, err := read(managedLateNormalTailSupplementApprovalPath)
	if err != nil {
		return bad()
	}
	var p managedLateNormalTailSupplementProposal
	var a managedLateNormalTailSupplementApprovalRecord
	now := time.Now().UTC()
	if decodeEnvironmentJSON(praw, &p) != nil || decodeEnvironmentJSON(araw, &a) != nil || p.Schema != "rtk-cloud.remaining-normal-tail-timeout-supplement-proposal/v1" || a.Schema != "rtk-cloud.remaining-normal-tail-timeout-supplement-operator-approval/v1" || p.Environment != store.Environment || a.Environment != store.Environment || p.Parent != parent || a.Parent != parent || a.ProposalSHA != environmentPreflightBytesSHA256(praw) || a.SourceRawSHA != p.SourceRawSHA || a.OriginalAuthorityRawSHA != p.OriginalAuthorityRawSHA || !a.Approved || a.TotalMinutes != 180 || p.TotalMinutes != 180 || p.FirstReserveMinutes != 15 || p.CredentialReserveMinutes != 5 || a.ExecutionAuthority == nil || *a.ExecutionAuthority || p.ExecutionAuthority == nil || *p.ExecutionAuthority || strings.TrimSpace(a.HumanMessage) == "" || a.RecordedAt.IsZero() || a.RecordedAt.After(now) || p.RecordedAt.IsZero() || p.RecordedAt.After(a.RecordedAt) || p.Status != "proposal-awaiting-explicit-approval" || p.SourcePath != managedLateNormalTailSupplementSourcePath || p.OwnerPath != managedLateNormalTailSupplementOwnerPath || p.SourceEntries != 434 || p.SourceLeases != 149 || !slices.Equal(p.Allowed, managedLateNormalTailSupplementAllowed()) || !slices.Equal(p.Forbidden, managedLateNormalTailSupplementForbidden()) || !slices.Equal(p.Completion, managedLateNormalTailSupplementCompletion()) || !p.OriginalDeadline.Equal(p.OriginalStartedAt.Add(managedLateOverallLimit)) || !p.ExtendedDeadline.Equal(p.OriginalStartedAt.Add(managedLateNormalTailSupplementLimit)) {
		return bad()
	}
	for _, sha := range []string{p.SourceRawSHA, p.OwnerSHA, p.OriginalAuthoritySHA, p.OriginalAuthorityRawSHA, p.OriginalReview, p.OriginalQualifiedBytesSHA, p.CompletedProofSHA, p.PreparedCheckerSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return bad()
		}
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
	if decodeEnvironmentJSON(sraw, &source) != nil || decodeEnvironmentJSON(oraw, &owner) != nil || deploymentplan.Digest(owner) != p.OwnerSHA || source.PlanSHA256 != parent || source.OwnerToken != owner.OwnerToken || owner.PlanSHA256 != parent || source.Environment != store.Environment || !managedLateNormalTailControllerSelected(source) || source.Entries[431].State != "completed" || len(source.KubernetesLeases) != p.SourceLeases {
		return bad()
	}
	oldRaw, err := readManagedLateNormalTailPair(store, parent)
	var old createManagedLateNormalTailAuthority
	var n createManagedLateNormalTail
	if err != nil || environmentPreflightBytesSHA256(oldRaw) != p.OriginalAuthorityRawSHA || decodeEnvironmentJSON(oldRaw, &old) != nil || deploymentplan.Digest(old) != p.OriginalAuthoritySHA || old.Review != p.OriginalReview || old.QualifiedBytesSHA != p.OriginalQualifiedBytesSHA || old.OwnerSHA != p.OwnerSHA || !old.StartedAt.Equal(p.OriginalStartedAt) || !old.Deadline.Equal(p.OriginalDeadline) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", old.Review, managedLateNormalTailFilename), &n) != nil {
		return bad()
	}
	authenticated, err := readManagedLateNormalTailAuthority(store, &n)
	if err != nil || authenticated == nil || deploymentplan.Digest(authenticated) != p.OriginalAuthoritySHA || n.SourceSHA != deploymentplan.Digest(managedLateNormalTailOriginalSource(source)) || n.CompletedProofSHA != p.CompletedProofSHA {
		return bad()
	}
	for _, entry := range source.Entries[433:] {
		if entry.StartedAt.Before(old.AdmittedAt) || entry.CompletedAt.After(old.Deadline) || entry.CompletedAt.After(p.RecordedAt) {
			return bad()
		}
	}
	proof, err := read(managedLateTerminalFinalizerCompletedPath(parent))
	if err != nil || environmentPreflightBytesSHA256(proof) != p.CompletedProofSHA {
		return bad()
	}
	return &createManagedLateNormalTailSupplementApproval{ProposalSHA: a.ProposalSHA, ApprovalSHA: environmentPreflightBytesSHA256(araw), ParentSHA: parent, SourceSHA: deploymentplan.Digest(source), SourceRawSHA: p.SourceRawSHA, OwnerSHA: p.OwnerSHA, OriginalAuthoritySHA: p.OriginalAuthoritySHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, OriginalReview: p.OriginalReview, OriginalQualifiedBytesSHA: p.OriginalQualifiedBytesSHA, CompletedProofSHA: p.CompletedProofSHA, PreparedCheckerSHA: p.PreparedCheckerSHA, OriginalStartedAt: p.OriginalStartedAt, OriginalDeadline: p.OriginalDeadline, ExtendedDeadline: p.ExtendedDeadline, ApprovedAt: a.RecordedAt}, nil
}
func prepareManagedLateNormalTailSupplement(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r == nil || r.normalTail == nil {
		return nil
	}
	if e == nil || store != e.Store || e.Check(ctx) != nil {
		return createOpenBaoACKBlock("normal-tail-supplement-source")
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateNormalTailSupplementApprovalPath)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		raw, pairErr := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256)
		if pairErr != nil || raw != nil {
			return createOpenBaoACKBlock("normal-tail-supplement-missing-approval")
		}
		return nil
	} else if err != nil {
		return err
	}
	approval, err := loadManagedLateNormalTailSupplementApproval(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	historical, sourceErr := managedLateNormalTailHistoricalSource(store, e)
	if sourceErr != nil {
		return sourceErr
	}
	if e.Check(ctx) != nil || store != e.Store || cfg.Environment != store.Environment || !managedLateNormalTailControllerSelected(historical.Journal) || deploymentplan.Digest(historical.Journal) != approval.SourceSHA || deploymentplan.Digest(e.owner) != approval.OwnerSHA || r.normalTail.Authority == nil || deploymentplan.Digest(r.normalTail.Authority) != approval.OriginalAuthoritySHA {
		return createOpenBaoACKBlock("normal-tail-supplement-exact434")
	}
	r.normalSupplement = &createManagedLateNormalTailSupplement{Approval: approval, CredentialSHA: managedLateNormalTailCredentialSHA(r.normalTail), CredentialExpires: r.normalTail.CredentialExpires}
	a, err := readManagedLateNormalTailSupplementAuthority(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	if managedLateRedisPrePauseSelected(e.Journal) && a == nil {
		return createOpenBaoACKBlock("normal-tail-pre-pause-existing180-required")
	}
	r.normalSupplement.Authority = a
	r.normalTail.Supplement = r.normalSupplement
	if err := prepareManagedLateNormalTailExtension(ctx, cfg, store, e, r); err != nil {
		return err
	}
	return e.Check(ctx)
}
func managedLateNormalTailSupplementPath(parent string, witness bool) string {
	name := "managed-late-normal-tail-timeout-supplement-authority.json"
	if witness {
		name = "managed-late-normal-tail-timeout-supplement-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}
func readManagedLateNormalTailSupplementPair(store secretStore, parent string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("normal-tail-supplement-authority-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLateNormalTailSupplementPath(parent, false), managedLateNormalTailSupplementPath(parent, true)}
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
func writeManagedLateNormalTailSupplementPair(store secretStore, parent string, raw []byte) error {
	old, err := readManagedLateNormalTailSupplementPair(store, parent)
	if err != nil || old != nil || len(raw) == 0 {
		return createOpenBaoACKBlock("normal-tail-supplement-authority-exists")
	}
	for _, w := range []bool{true, false} {
		p, err := privateDeploymentStatePath(store.Environment, managedLateNormalTailSupplementPath(parent, w))
		if err != nil || p != filepath.Join(store.Root, managedLateNormalTailSupplementPath(parent, w)) || writeExclusivePrivateDeploymentJSON(p, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(p)) != nil {
			return createOpenBaoACKBlock("normal-tail-supplement-exclusive-authority")
		}
	}
	got, err := readManagedLateNormalTailSupplementPair(store, parent)
	if err != nil || !bytes.Equal(got, raw) {
		return createOpenBaoACKBlock("normal-tail-supplement-authority-readback")
	}
	return nil
}
func newManagedLateNormalTailSupplementAuthority(r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, rawSHA string, caller, now time.Time) (*createManagedLateNormalTailSupplementAuthority, error) {
	bad := func() (*createManagedLateNormalTailSupplementAuthority, error) {
		return nil, createOpenBaoACKBlock("normal-tail-supplement-fresh-six-go")
	}
	if r == nil || r.normalTail == nil || r.normalSupplement == nil || r.normalSupplement.Approval == nil {
		return bad()
	}
	n, s := r.normalTail, r.normalSupplement
	p := s.Approval
	if n.CurrentSHA != p.SourceSHA || n.Authority == nil || deploymentplan.Digest(n.Authority) != p.OriginalAuthoritySHA || !n.Authority.StartedAt.Equal(p.OriginalStartedAt) || !n.Authority.Deadline.Equal(p.OriginalDeadline) || n.Authority.Review != p.OriginalReview || n.Authority.QualifiedBytesSHA != p.OriginalQualifiedBytesSHA || n.Authority.OwnerSHA != p.OwnerSHA || n.Authority.ParentSHA != p.ParentSHA || now.Before(p.ApprovedAt) || a.Plan.Schema != managedLateNormalTailSchema || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.ParentSHA != p.ParentSHA || a.Plan.ParentOwnerSHA != p.OwnerSHA || a.Plan.ParentJournalSHA != p.SourceSHA || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || a.Plan.KubeconfigSHA != n.KubeconfigSHA || a.Plan.Materials[managedLateNormalTailMaterial+"source"] != managedLateNormalTailDigest(n) || a.Plan.Materials[managedLateNormalTailSupplementMaterial+"intent"] != managedLateNormalTailSupplementIntent(r) || a.Plan.Materials[managedLateNormalTailSupplementMaterial+"policy"] != managedLateNormalTailSupplementPolicySHA() || !managedLateResidualCleanupEvidenceValid(n.NativeEvidence) || !managedLateNormalTailSupplementCredentialValid(n) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != deploymentplan.Digest(a.Plan) || len(a.Qualification.Gates) != 6 || s.CredentialSHA != managedLateNormalTailCredentialSHA(n) || s.CredentialSHA != n.NativeEvidence["current-credential"] || !s.CredentialExpires.Equal(n.CredentialExpires) {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != a.Qualification.PlanSHA256 {
			return bad()
		}
	}
	out := &createManagedLateNormalTailSupplementAuthority{Schema: managedLateNormalTailSupplementSchema, ParentSHA: p.ParentSHA, SourceSHA: p.SourceSHA, SourceRawSHA: p.SourceRawSHA, OwnerSHA: p.OwnerSHA, ProposalSHA: p.ProposalSHA, ApprovalSHA: p.ApprovalSHA, OriginalAuthoritySHA: p.OriginalAuthoritySHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, OriginalReview: p.OriginalReview, OriginalQualifiedBytesSHA: p.OriginalQualifiedBytesSHA, IntentSHA: managedLateNormalTailSupplementIntent(r), Review: a.Qualification.PlanSHA256, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: rawSHA, DescriptorSHA: managedLateNormalTailDigest(n), CredentialSHA: s.CredentialSHA, OriginalStartedAt: p.OriginalStartedAt, OriginalDeadline: p.OriginalDeadline, ExtendedDeadline: p.ExtendedDeadline, FirstCallerDeadline: caller, AdmittedAt: now, Deadline: managedLateNormalTailSupplementDeadline(p.OriginalStartedAt, caller, s.CredentialExpires), CredentialExpires: s.CredentialExpires, TotalLimit: managedLateNormalTailSupplementLimit}
	if !managedLateNormalTailSupplementAuthorityValid(out) {
		return bad()
	}
	return out, nil
}
func readManagedLateNormalTailSupplementAuthority(store secretStore, parent string) (*createManagedLateNormalTailSupplementAuthority, error) {
	raw, err := readManagedLateNormalTailSupplementPair(store, parent)
	if err != nil || raw == nil {
		return nil, err
	}
	bad := func() (*createManagedLateNormalTailSupplementAuthority, error) {
		return nil, createOpenBaoACKBlock("normal-tail-supplement-immutable-authority")
	}
	var header createManagedLateNormalTailSupplementAuthority
	if decodeEnvironmentJSON(raw, &header) != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(header)) || !managedLateNormalTailSupplementAuthorityValid(&header) {
		return bad()
	}
	approval, err := loadManagedLateNormalTailSupplementApproval(store, parent)
	if err != nil {
		return bad()
	}
	var n createManagedLateNormalTail
	var saved createManagedLateNormalTailSupplement
	base := filepath.Join("deployment/create/openbao-recovery", header.Review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateNormalTailFilename), &n) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateNormalTailSupplementFilename), &saved) != nil || deploymentplan.Digest(saved.Approval) != deploymentplan.Digest(approval) {
		return bad()
	}
	n.Authority, err = readManagedLateNormalTailCurrentAuthority(store, &n)
	if err != nil {
		return bad()
	}
	r := &createServiceSessionRecovery{normalTail: &n, normalSupplement: &saved}
	qraw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "qualification.json"), true)
	var a createFoundationRecoveryArtifact
	if err != nil || environmentPreflightBytesSHA256(qraw) != header.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != header.ArtifactSHA {
		return bad()
	}
	expected, err := newManagedLateNormalTailSupplementAuthority(r, a, header.QualifiedBytesSHA, header.FirstCallerDeadline, header.AdmittedAt)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(header) {
		return bad()
	}
	return &header, nil
}
func verifyManagedLateNormalTailSupplementRuntime(store secretStore, r *createServiceSessionRecovery) error {
	if r == nil || r.normalSupplement == nil {
		return nil
	}
	if r.normalTail == nil || r.normalSupplement.Authority == nil {
		return createOpenBaoACKBlock("normal-tail-supplement-admission-required")
	}
	a, err := readManagedLateNormalTailSupplementAuthority(store, r.normalTail.Approval.ParentSHA)
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.normalSupplement.Authority) {
		return createOpenBaoACKBlock("normal-tail-supplement-fixed-deadline")
	}
	if r.normalSupplement.Extension != nil {
		return verifyManagedLateNormalTailExtensionRuntime(store, r)
	}
	if !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("normal-tail-supplement-fixed-deadline")
	}
	return nil
}

func admitManagedLateNormalTailSupplement(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, rawSHA string) (context.Context, context.CancelFunc, error) {
	bad := func() (context.Context, context.CancelFunc, error) {
		return nil, nil, createOpenBaoACKBlock("normal-tail-supplement-current-admission")
	}
	if e == nil || r == nil || r.normalTail == nil || r.normalSupplement == nil || e.Check(ctx) != nil || r.normalSupplement.Approval == nil || r.normalSupplement.Approval.SourceSHA != deploymentplan.Digest(e.Journal) {
		return bad()
	}
	checker, checkerErr := environmentPreflightCheckerSHA256()
	if checkerErr != nil || checker != a.Plan.CheckerSHA || a.QualifiedAt.After(time.Now().UTC()) || !time.Now().Before(a.ValidUntil) || a.Qualification.PlanSHA256 != deploymentplan.Digest(a.Plan) || a.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) || a.Plan.Materials[managedLateNormalTailSupplementMaterial+"intent"] != managedLateNormalTailSupplementIntent(r) {
		return bad()
	}
	existing, err := readManagedLateNormalTailSupplementAuthority(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, nil, err
	}
	if existing == nil {
		caller, ok := ctx.Deadline()
		if !ok {
			return bad()
		}
		phase, err := newManagedLateNormalTailSupplementAuthority(r, a, rawSHA, caller, time.Now().UTC())
		if err != nil {
			return nil, nil, err
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", phase.Review, "qualification.json"), true)
		approval, approvalErr := loadManagedLateNormalTailSupplementApproval(store, e.Journal.PlanSHA256)
		if err != nil || environmentPreflightBytesSHA256(raw) != rawSHA || approvalErr != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(r.normalSupplement.Approval) || e.Check(ctx) != nil {
			return bad()
		}
		if managedLateSourceReuseActive(e) {
			if err := checkManagedLateSourceReuse(e); err != nil {
				return nil, nil, err
			}
		}
		invalidateManagedLateSourceReuse(e)
		if err := writeManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
			return nil, nil, err
		}
		existing = phase
	}
	if !time.Now().Before(existing.Deadline) || existing.IntentSHA != managedLateNormalTailSupplementIntent(r) {
		return bad()
	}
	r.normalSupplement.Authority = existing
	r.normalTail.Supplement = r.normalSupplement
	bounded, cancel := context.WithDeadline(ctx, existing.Deadline)
	if err := e.Check(bounded); err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}

func managedLateNormalTailSupplementAllowed() []string {
	return []string{"qualified-same-UID-Redis-archive-quiesce-RDB-AOF-cutover", "maintained-workloads", "original-external-planes", "environment-dns", "turn-acceptance", "full-rollout-acceptance", "final-volume-ledger", "original-CompleteAndRelease"}
}
func managedLateNormalTailSupplementForbidden() []string {
	return []string{"original-authority-or-receipt-rewrite", "clock-anchor-reset", "bootstrap-signing-or-identity-generation", "native-finalizer-or-revocation-replay", "completed-registration-ingress-or-cleanup-replay", "Redis-data-reset-or-emptyDir-rollback", "unknown-outcome-adoption", "arbitrary-new-volume", "cross-environment-or-store-fallback"}
}
func managedLateNormalTailSupplementCompletion() []string {
	return []string{"Redis-original-data-preserved-and-durable", "original18-volumes-retained", "protected19-unchanged", "FULL-rollout-acceptance", "original-parent-completed-and-fence-archived", "independent-standard-FULL-post-health-started-after-parent-completion"}
}
func managedLateNormalTailSupplementCredentialValid(n *createManagedLateNormalTail) bool {
	if n == nil || !environmentPreflightDigest.MatchString(n.KubeconfigSHA) || !environmentPreflightDigest.MatchString(n.CredentialSHA) {
		return false
	}
	if n.CredentialMode == "non-expiring-kubernetes-token" {
		return n.CredentialExpires.IsZero()
	}
	return (n.CredentialMode == "expiring-kubernetes-token" || n.CredentialMode == "kubernetes-client-certificate") && !n.CredentialExpires.IsZero()
}

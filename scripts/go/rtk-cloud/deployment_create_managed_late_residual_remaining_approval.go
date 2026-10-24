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

type createManagedLateResidualRemainingApproval struct {
	ProposalSHA, ApprovalSHA, ParentSHA, SourceSHA, SourceRawSHA          string
	OriginalSourceSHA, OwnerSHA, OriginalAuthoritySHA, OldAuthorityRawSHA string
	InventorySHA, CompletedSHA                                            string
	Objects, Completed                                                    []createManagedLateResidualCleanupObject
	CleanupLimit, OverallLimit                                            time.Duration
	ApprovedAt                                                            time.Time
}

type managedLateResidualRemainingApprovalRecord struct {
	managedLateResidualApprovalRecord
	HumanFollowup string `json:"human_followup"`
}

type managedLateResidualRemainingRetirement struct {
	Entry       int       `json:"entry"`
	Key         string    `json:"key"`
	ResourceID  string    `json:"resource_id"`
	UID         string    `json:"uid"`
	SessionID   string    `json:"session_id"`
	CompletedAt time.Time `json:"completed_at"`
}

type managedLateResidualRemainingProposal struct {
	Schema              string                                   `json:"schema"`
	Environment         string                                   `json:"environment"`
	ExecutionAuthority  bool                                     `json:"execution_authority"`
	ApprovalStatus      string                                   `json:"approval_status"`
	PreparedAt          time.Time                                `json:"prepared_at"`
	Parent              string                                   `json:"parent_plan_sha256"`
	SourceJournal       string                                   `json:"source_journal"`
	SourceRawSHA        string                                   `json:"source_journal_raw_sha256"`
	SourceEntries       int                                      `json:"source_journal_entry_count"`
	OwnerRawSHA         string                                   `json:"original_owner_raw_sha256"`
	OriginalProposalSHA string                                   `json:"original_approved_proposal_sha256"`
	OriginalApprovalSHA string                                   `json:"original_approval_sha256"`
	OldAuthorityRawSHA  string                                   `json:"prior_residual_pair_raw_sha256"`
	PriorStartedAt      time.Time                                `json:"prior_started_at"`
	PriorDeadline       time.Time                                `json:"prior_cleanup_deadline"`
	PriorOverall        time.Time                                `json:"prior_overall_deadline"`
	FailedRun           string                                   `json:"actual_failed_run"`
	FailedRunSHA        string                                   `json:"actual_failed_run_raw_sha256"`
	FailedAt            time.Time                                `json:"actual_failed_run_completed_at"`
	FailedElapsed       float64                                  `json:"actual_failed_run_elapsed_seconds"`
	Completed           []managedLateResidualRemainingRetirement `json:"completed_retirements"`
	Objects             []createManagedLateResidualCleanupObject `json:"remaining_objects"`
	CleanupMinutes      int                                      `json:"new_cleanup_window_minutes"`
	CallerMinutes       int                                      `json:"new_first_caller_overall_minutes"`
	ExpiryReserve       int                                      `json:"finite_current_credential_reserve_minutes"`
	FirstReserve        int                                      `json:"first_admission_reserve_minutes"`
	WindowRule          string                                   `json:"window_rule"`
	GrantSchema         string                                   `json:"proposed_grant_schema"`
	AdmissionPath       string                                   `json:"proposed_admission_path"`
	AuthorityPath       string                                   `json:"proposed_authority_path"`
	Native              struct {
		Session      string    `json:"session_id"`
		Fingerprint  string    `json:"fingerprint"`
		Issuer       string    `json:"service_ica_uuid"`
		Root         string    `json:"root_fingerprint"`
		FinalizedAt  time.Time `json:"genuine_native_finalized_at"`
		TombstoneSHA string    `json:"custody_tombstone_raw_sha256"`
	} `json:"native_identity"`
	Permitted []string `json:"permitted_actions"`
	Forbidden []string `json:"forbidden_actions"`
	Evidence  struct {
		Historical bool `json:"native_finalization_is_historical_genuine"`
		Fresh      bool `json:"fresh_native_and_current_credential_qualification_still_required"`
		Complete   bool `json:"completion_requires_genuine_full_acceptance_and_all_final_checks"`
	} `json:"evidence_limits"`
}

func managedLateResidualRemainingBase(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service")
}

// Approval describes only the exact unretired inventory. Fresh qualification
// and a different exclusive admission pair must establish execution authority.
func loadManagedLateResidualRemainingApproval(store secretStore, parent string) (*createManagedLateResidualRemainingApproval, error) {
	bad := func() (*createManagedLateResidualRemainingApproval, error) {
		return nil, createOpenBaoACKBlock("residual-remaining-operator-approval")
	}
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	base := managedLateResidualRemainingBase(parent)
	approvalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-operator-approval.json")
	approvalPath, err := privateDeploymentStatePath(store.Environment, approvalRel)
	if err != nil || approvalPath != filepath.Join(store.Root, approvalRel) {
		return bad()
	}
	if _, err := os.Lstat(approvalPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	read := func(relative, sha string) ([]byte, error) {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || sha != "" && !environmentPreflightDigest.MatchString(sha) {
			return nil, errors.New("invalid remaining cleanup evidence path")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || sha != "" && environmentPreflightBytesSHA256(raw) != sha {
			return nil, errors.New("remaining cleanup evidence differs")
		}
		return raw, nil
	}
	araw, err := read(approvalRel, "")
	var a managedLateResidualRemainingApprovalRecord
	if err != nil || decodeEnvironmentJSON(araw, &a) != nil || a.Schema != "rtk-cloud.residual-copy-cleanup-remaining-operator-approval/v1" || a.Environment != store.Environment || a.Parent != parent || a.ExecutionAuthority || a.Decision != "approved" || a.HumanMessage == "" || a.HumanFollowup == "" || a.ApprovedAt.IsZero() || a.ApprovedAt.After(time.Now()) || a.CleanupMinutes != 60 || a.CallerMinutes != 90 || !environmentPreflightDigest.MatchString(a.Proposal) || !environmentPreflightDigest.MatchString(a.Source) {
		return bad()
	}
	proposalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-proposal-60m.json")
	praw, err := read(proposalRel, a.Proposal)
	var p managedLateResidualRemainingProposal
	if err != nil || decodeEnvironmentJSON(praw, &p) != nil || !managedLateResidualRemainingProposalValid(p, a, base, store.Environment, parent) {
		return bad()
	}
	old, err := loadManagedLateResidualCleanupApproval(store, parent)
	if err != nil || old == nil || old.ProposalSHA != p.OriginalProposalSHA || old.ApprovalSHA != p.OriginalApprovalSHA {
		return bad()
	}
	pair, err := readManagedLateResidualCleanupPair(store, parent)
	var header createManagedLateResidualCleanupAuthority
	if err != nil || pair == nil || environmentPreflightBytesSHA256(pair) != p.OldAuthorityRawSHA || decodeEnvironmentJSON(pair, &header) != nil || !header.StartedAt.Equal(p.PriorStartedAt) || !header.Deadline.Equal(p.PriorDeadline) || !header.OverallDeadline.Equal(p.PriorOverall) {
		return bad()
	}
	var r createManagedLateRemaining
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &r) != nil || r.ResidualCleanup == nil || r.ParentSHA != parent || r.Review != header.Review || deploymentplan.Digest(r.ResidualCleanup.Approval) != deploymentplan.Digest(old) {
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
	if r.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, &r); err != nil || r.ResidualAuthority == nil || deploymentplan.Digest(r.ResidualAuthority) != deploymentplan.Digest(header) {
		return bad()
	}
	jraw, err := read(p.SourceJournal, p.SourceRawSHA)
	var source environmentCreateJournal
	if err != nil || decodeEnvironmentJSON(jraw, &source) != nil || source.PlanSHA256 != parent || source.Environment != store.Environment || len(source.Entries) != 403 || !managedLateResidualCleanupProgressValid(source, &r) {
		return bad()
	}
	owraw, err := read(filepath.Join(base, "managed-late-residual-copy-cleanup-source-owner.json"), p.OwnerRawSHA)
	var owner environmentCreateOwner
	if err != nil || decodeEnvironmentJSON(owraw, &owner) != nil || deploymentplan.Digest(owner) != old.OwnerSHA || owner.OwnerToken != source.OwnerToken || owner.Journal == "" || owner.PlanSHA256 != parent || owner.Environment != store.Environment || owner.Stack != source.Stack {
		return bad()
	}
	fraw, err := read(p.FailedRun, p.FailedRunSHA)
	var failed deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(fraw, &failed) != nil || failed["schema"] != "rtk-cloud.create-openbao-recovery-result/v1" || failed["completed"] != false || failed["decision"] != "NO-GO" || failed["parent_plan_sha256"] != parent || failed["environment"] != store.Environment {
		return bad()
	}
	if r.PostNativeCleanup == nil || p.Native.Session != r.SessionID || p.Native.Fingerprint != r.Fingerprint || p.Native.Issuer != r.Ceremony.Intermediate.ID || p.Native.Root != r.Ceremony.Root.RootFingerprint || !p.Native.FinalizedAt.Equal(r.PostNativeCleanup.Native.FinalizedAt) || p.Native.TombstoneSHA != r.PostNativeCleanup.TombstoneSHA {
		return bad()
	}
	completed, valid := managedLateResidualRemainingInventory(p, old, source, header)
	if !valid {
		return bad()
	}
	return &createManagedLateResidualRemainingApproval{ProposalSHA: a.Proposal, ApprovalSHA: environmentPreflightBytesSHA256(araw), ParentSHA: parent, SourceSHA: deploymentplan.Digest(source), SourceRawSHA: p.SourceRawSHA, OriginalSourceSHA: old.SourceSHA, OwnerSHA: old.OwnerSHA, OriginalAuthoritySHA: deploymentplan.Digest(header), OldAuthorityRawSHA: p.OldAuthorityRawSHA, InventorySHA: deploymentplan.Digest(p.Objects), CompletedSHA: deploymentplan.Digest(source.Entries[400:403]), Objects: slices.Clone(p.Objects), Completed: completed, CleanupLimit: 60 * time.Minute, OverallLimit: 90 * time.Minute, ApprovedAt: a.ApprovedAt}, nil
}

func managedLateResidualRemainingProposalValid(p managedLateResidualRemainingProposal, a managedLateResidualRemainingApprovalRecord, base, environment, parent string) bool {
	for _, sha := range []string{p.SourceRawSHA, p.OwnerRawSHA, p.OriginalProposalSHA, p.OriginalApprovalSHA, p.OldAuthorityRawSHA, p.FailedRunSHA, p.Native.Fingerprint, p.Native.Root, p.Native.TombstoneSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return p.Schema == "rtk-cloud.residual-copy-cleanup-remaining-proposal/v1" && p.Environment == environment && p.Parent == parent && !p.ExecutionAuthority && p.ApprovalStatus == "pending-explicit-human-decision" && !p.PreparedAt.IsZero() && !p.PreparedAt.After(a.ApprovedAt) && p.SourceJournal == filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-source-journal.json") && p.SourceRawSHA == a.Source && p.SourceEntries == 403 && p.FailedRun == filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-failed-run.json") && !p.FailedAt.IsZero() && !p.FailedAt.After(p.PreparedAt) && p.FailedElapsed > 0 && !p.PriorStartedAt.IsZero() && p.PriorDeadline.Equal(p.PriorStartedAt.Add(60*time.Minute)) && p.PriorOverall.After(p.PriorDeadline) && p.CleanupMinutes == 60 && p.CallerMinutes == 90 && p.ExpiryReserve == 5 && p.FirstReserve == 15 && p.WindowRule != "" && p.GrantSchema == "rtk-cloud.create-managed-late-residual-copy-cleanup-remaining/v1" && p.AdmissionPath == filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-grant-admission.json") && p.AuthorityPath == filepath.Join(base, "managed-late-residual-copy-cleanup-remaining-grant-authority.json") && environmentBootstrapOwnerUUID.MatchString(p.Native.Session) && environmentBootstrapOwnerUUID.MatchString(p.Native.Issuer) && !p.Native.FinalizedAt.IsZero() && p.Native.FinalizedAt.Before(p.PriorStartedAt) && len(p.Permitted) == 3 && len(p.Forbidden) == 4 && p.Evidence.Historical && p.Evidence.Fresh && p.Evidence.Complete && len(p.Completed) == 3 && len(p.Objects) == 21
}

func managedLateResidualRemainingInventory(p managedLateResidualRemainingProposal, old *createManagedLateResidualCleanupApproval, source environmentCreateJournal, authority createManagedLateResidualCleanupAuthority) ([]createManagedLateResidualCleanupObject, bool) {
	if old == nil || !managedLateResidualCleanupApprovalValid(old) || len(source.Entries) != 403 || len(p.Completed) != 3 || len(p.Objects) != 21 {
		return nil, false
	}
	allowed := map[string]createManagedLateResidualCleanupObject{}
	for _, object := range old.Objects {
		allowed[object.ResourceID] = object
	}
	completed := make([]createManagedLateResidualCleanupObject, 0, 3)
	seen := map[string]bool{}
	for i, item := range p.Completed {
		entry := source.Entries[400+i]
		object, ok := allowed[item.ResourceID]
		var receipt struct {
			ID     string `json:"resource_id"`
			UID    string `json:"uid"`
			Absent bool   `json:"absent"`
		}
		intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
		if !ok || seen[item.ResourceID] || item.Entry != 401+i || item.UID != object.UID || item.SessionID != object.SessionID || item.Key != "bootstrap-copy-retire-"+object.Namespace+"-"+object.SessionID || entry.Key != item.Key || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.StartedAt.Before(authority.StartedAt) || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(authority.Deadline) || entry.CompletedAt.After(p.FailedAt) || !entry.CompletedAt.Equal(item.CompletedAt) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.ID != object.ResourceID || receipt.UID != object.UID || !receipt.Absent {
			return nil, false
		}
		seen[item.ResourceID] = true
		completed = append(completed, object)
	}
	if !slices.IsSortedFunc(p.Objects, func(a, b createManagedLateResidualCleanupObject) int {
		return strings.Compare(a.ResourceID, b.ResourceID)
	}) {
		return nil, false
	}
	for _, object := range p.Objects {
		if allowed[object.ResourceID] != object || seen[object.ResourceID] {
			return nil, false
		}
		seen[object.ResourceID] = true
	}
	if len(seen) != len(allowed) {
		return nil, false
	}
	return completed, true
}

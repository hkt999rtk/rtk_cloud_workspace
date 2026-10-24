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

type createManagedLateResidualCleanupObject struct {
	ResourceID, UID, SessionID, Namespace string
}

type createManagedLateResidualCleanupApproval struct {
	ProposalSHA, ApprovalSHA, ParentSHA, SourceSHA, OwnerSHA, InventorySHA string
	Objects                                                                []createManagedLateResidualCleanupObject
	CleanupLimit, OverallLimit                                             time.Duration
	ApprovedAt                                                             time.Time
}

type managedLateResidualApprovalRecord struct {
	Schema             string    `json:"schema"`
	Environment        string    `json:"environment"`
	Parent             string    `json:"parent_sha256"`
	Source             string    `json:"source_sha256"`
	Proposal           string    `json:"proposal_sha256"`
	Decision           string    `json:"decision"`
	ApprovedAt         time.Time `json:"approved_at"`
	HumanMessage       string    `json:"human_message"`
	CleanupMinutes     int       `json:"requested_cleanup_minutes"`
	CallerMinutes      int       `json:"requested_new_caller_minutes"`
	ExecutionAuthority bool      `json:"execution_authority"`
	Note               string    `json:"note"`
}

type managedLateResidualProposal struct {
	Schema               string                              `json:"schema"`
	Authority            bool                                `json:"authority"`
	Status               string                              `json:"status"`
	CreatedAt            time.Time                           `json:"created_at"`
	Environment          string                              `json:"environment"`
	Parent               string                              `json:"parent_sha256"`
	Source               string                              `json:"source_sha256"`
	Journal              string                              `json:"journal"`
	JournalRawSHA        string                              `json:"journal_raw_sha256"`
	OwnerRawSHA          string                              `json:"owner_raw_sha256"`
	PostSHA              string                              `json:"old_post_native_pair_raw_sha256"`
	TerminalSHA          string                              `json:"old_terminal_pair_raw_sha256"`
	Historical           map[string]string                   `json:"old_historical_authority_raw_sha256"`
	CleanupDeadline      time.Time                           `json:"historical_cleanup_deadline"`
	OverallDeadline      time.Time                           `json:"historical_first_caller_overall_deadline"`
	FailureReport        string                              `json:"failure_report"`
	FailureReportSHA     string                              `json:"failure_report_raw_sha256"`
	FailureDiagnostic    string                              `json:"failure_diagnostic"`
	FailureDiagnosticSHA string                              `json:"failure_diagnostic_raw_sha256"`
	Remaining            string                              `json:"remaining_descriptor"`
	RemainingSHA         string                              `json:"remaining_descriptor_raw_sha256"`
	StaleSeedsSHA        string                              `json:"stale_seeds_sha256"`
	FiveACKProof         string                              `json:"historical_five_ack_proof_ref"`
	Objects              []managedLateResidualProposalObject `json:"objects"`
	CleanupMinutes       int                                 `json:"requested_cleanup_minutes"`
	CallerMinutes        int                                 `json:"requested_new_caller_minutes"`
	ExpiryReserveMinutes int                                 `json:"expiry_reserve_minutes"`
	FirstReserveMinutes  int                                 `json:"first_admission_reserve_minutes"`
	RequiresHuman        bool                                `json:"requires_explicit_human_approval_beyond_old_overall"`
	RequiresGO           bool                                `json:"requires_fresh_six_gate_GO_with_current_checker"`
	RequiresNative       bool                                `json:"requires_fresh_matching_native_finalization_tombstone_and_permanent_identity_evidence"`
	Permitted            []string                            `json:"permitted_after_object_retirement"`
	FinalChecks          []string                            `json:"independent_final_checks"`
	Forbidden            []string                            `json:"forbidden"`
	Implementation       string                              `json:"required_implementation"`
	Supersedes           string                              `json:"supersedes_unapproved_30m_proposal_raw_sha256"`
	Rationale            string                              `json:"requested_bounds_rationale"`
}

type managedLateResidualProposalObject struct {
	ResourceID     string `json:"resource_id"`
	UID            string `json:"uid"`
	SessionID      string `json:"session_id"`
	Namespace      string `json:"namespace"`
	Observation    string `json:"observation"`
	ObservationSHA string `json:"observation_raw_sha256"`
	Installed      int    `json:"saved_installed_count"`
	Proofs         []struct {
		Stage      string `json:"parent_stage"`
		Plan       string `json:"plan_sha256"`
		Journal    string `json:"journal"`
		JournalSHA string `json:"journal_raw_sha256"`
	} `json:"accepted_owner_proofs"`
}

// This reader authenticates an explicit operator decision, not an execution
// grant. A separate fresh full qualification and paired admission are required.
func loadManagedLateResidualCleanupApproval(store secretStore, parent string) (*createManagedLateResidualCleanupApproval, error) {
	bad := func() (*createManagedLateResidualCleanupApproval, error) {
		return nil, createOpenBaoACKBlock("residual-cleanup-operator-approval")
	}
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	base := filepath.Join("deployment/create", parent, "bootstrap/service")
	approvalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-operator-approval.json")
	approvalPath, err := privateDeploymentStatePath(store.Environment, approvalRel)
	if err != nil || approvalPath != filepath.Join(store.Root, approvalRel) {
		return bad()
	}
	if _, err := os.Lstat(approvalPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	memo := map[string][]byte{}
	read := func(relative, sha string) ([]byte, error) {
		if !environmentPreflightDigest.MatchString(sha) && sha != "" {
			return nil, errors.New("invalid residual evidence digest")
		}
		if prior, ok := memo[relative]; ok {
			if sha != "" && environmentPreflightBytesSHA256(prior) != sha {
				return nil, errors.New("residual evidence changed")
			}
			return prior, nil
		}
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return nil, errors.New("invalid residual evidence path")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || (sha != "" && environmentPreflightBytesSHA256(raw) != sha) {
			return nil, errors.New("residual evidence changed")
		}
		memo[relative] = raw
		return raw, nil
	}
	araw, err := read(approvalRel, "")
	var a managedLateResidualApprovalRecord
	if err != nil || decodeEnvironmentJSON(araw, &a) != nil || a.Schema != "rtk-cloud.residual-copy-cleanup-operator-approval/v1" || a.Environment != store.Environment || a.Parent != parent || a.Decision != "approved" || a.ExecutionAuthority || a.HumanMessage == "" || a.ApprovedAt.IsZero() || a.ApprovedAt.After(time.Now()) || a.CleanupMinutes != 60 || a.CallerMinutes != 90 || !environmentPreflightDigest.MatchString(a.Proposal) || !environmentPreflightDigest.MatchString(a.Source) {
		return bad()
	}
	proposalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-proposal-60m.json")
	praw, err := read(proposalRel, a.Proposal)
	var p managedLateResidualProposal
	if err != nil || decodeEnvironmentJSON(praw, &p) != nil || p.Schema != "rtk-cloud.residual-copy-cleanup-proposal/v1" || p.Authority || p.Status != "awaiting-explicit-operator-policy-approval" || p.Environment != store.Environment || p.Parent != parent || p.Source != a.Source || p.CleanupMinutes != a.CleanupMinutes || p.CallerMinutes != a.CallerMinutes || p.ExpiryReserveMinutes != 5 || p.FirstReserveMinutes != 15 || !p.RequiresHuman || !p.RequiresGO || !p.RequiresNative || len(p.Objects) != 24 || p.CreatedAt.IsZero() || p.CreatedAt.After(a.ApprovedAt) || p.CleanupDeadline.IsZero() || p.OverallDeadline.IsZero() || a.ApprovedAt.Before(p.CleanupDeadline) || a.ApprovedAt.Before(p.OverallDeadline) || !environmentPreflightDigest.MatchString(p.OwnerRawSHA) || !environmentPreflightDigest.MatchString(p.JournalRawSHA) || !environmentPreflightDigest.MatchString(p.StaleSeedsSHA) || !strings.HasPrefix(p.Journal, filepath.Join("deployment/create/executions", parent)+string(filepath.Separator)) {
		return bad()
	}
	postRaw, err := read(filepath.Join(base, "managed-late-post-native-cleanup-authority.json"), p.PostSHA)
	postWitness, other := read(filepath.Join(base, "managed-late-post-native-cleanup-admission.json"), p.PostSHA)
	var post createManagedLatePostNativeCleanupAuthority
	if err != nil || other != nil || string(postRaw) != string(postWitness) || decodeEnvironmentJSON(postRaw, &post) != nil || post.ParentSHA != parent || post.SourceSHA != p.Source || !post.Deadline.Equal(p.CleanupDeadline) || !environmentPreflightDigest.MatchString(post.OwnerSHA) {
		return bad()
	}
	termRaw, err := read(filepath.Join(base, "managed-late-terminal-finalizer-authority.json"), p.TerminalSHA)
	termWitness, other := read(filepath.Join(base, "managed-late-terminal-finalizer-admission.json"), p.TerminalSHA)
	var term createManagedLateTerminalFinalizerAuthority
	if err != nil || other != nil || string(termRaw) != string(termWitness) || decodeEnvironmentJSON(termRaw, &term) != nil || term.ParentSHA != parent || !term.OverallDeadline.Equal(p.OverallDeadline) {
		return bad()
	}
	// These are exact captured source bytes, never the changing live journal
	// or a fabricated execution. The current fence/prefix is checked separately.
	jraw, err := read(filepath.Join(base, "managed-late-residual-copy-cleanup-source-journal.json"), p.JournalRawSHA)
	var captured environmentCreateJournal
	if err != nil || decodeEnvironmentJSON(jraw, &captured) != nil || captured.PlanSHA256 != parent || captured.Environment != store.Environment || deploymentplan.Digest(captured) != p.Source || !managedLatePostNativeCleanupSelected(captured) {
		return bad()
	}
	owraw, err := read(filepath.Join(base, "managed-late-residual-copy-cleanup-source-owner.json"), p.OwnerRawSHA)
	var capturedOwner environmentCreateOwner
	if err != nil || decodeEnvironmentJSON(owraw, &capturedOwner) != nil || capturedOwner.Schema != environmentCreateOwnerSchema || capturedOwner.Environment != store.Environment || capturedOwner.PlanSHA256 != parent || capturedOwner.Journal != p.Journal || capturedOwner.OwnerToken != captured.OwnerToken || capturedOwner.Stack != captured.Stack || deploymentplan.Digest(capturedOwner) != post.OwnerSHA {
		return bad()
	}
	if len(p.Historical) != 3 {
		return bad()
	}
	for _, name := range []string{"managed-late-remaining-authority.json", "managed-late-installed-cleanup-authority.json", "managed-late-finalization-authority.json"} {
		if !environmentPreflightDigest.MatchString(p.Historical[name]) {
			return bad()
		}
		if _, err := read(filepath.Join(base, name), p.Historical[name]); err != nil {
			return bad()
		}
	}
	if p.Remaining != filepath.Join("deployment/create/openbao-recovery", post.Review, managedLateRemainingFilename) {
		return bad()
	}
	rraw, err := read(p.Remaining, p.RemainingSHA)
	var remaining createManagedLateRemaining
	if err != nil || decodeEnvironmentJSON(rraw, &remaining) != nil || remaining.PostNativeCleanup == nil || remaining.PostNativeCleanup.SourceSHA != p.Source || remaining.StaleSeedsSHA != p.StaleSeedsSHA || remaining.ParentSHA != parent || p.FiveACKProof != p.Remaining+"#/HistoricalSeedCleanup" {
		return bad()
	}
	if p.FailureReport != "deployment/reports/dev-device-trust-continuation-deployment.json" || !strings.HasPrefix(p.FailureDiagnostic, "deployment/reports/") {
		return bad()
	}
	// The CLI result is mutable on subsequent commands. The operator preserves
	// its exact failed bytes once; durable readers authenticate that snapshot.
	fraw, err := read(filepath.Join(base, "managed-late-residual-copy-cleanup-failed-run.json"), p.FailureReportSHA)
	var failed deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(fraw, &failed) != nil || failed["completed"] != false || failed["decision"] != "NO-GO" || failed["parent_plan_sha256"] != parent {
		return bad()
	}
	draw, err := read(p.FailureDiagnostic, p.FailureDiagnosticSHA)
	var diagnostic deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(draw, &diagnostic) != nil || diagnostic["authority"] != false || diagnostic["parent_sha256"] != parent || diagnostic["failed_report_raw_sha256"] != p.FailureReportSHA || diagnostic["fixed_cleanup_deadline"] != p.CleanupDeadline.Format(time.RFC3339Nano) || diagnostic["journal_entries"] != float64(400) {
		return bad()
	}
	out := &createManagedLateResidualCleanupApproval{ProposalSHA: a.Proposal, ApprovalSHA: environmentPreflightBytesSHA256(araw), ParentSHA: parent, SourceSHA: p.Source, OwnerSHA: post.OwnerSHA, CleanupLimit: 60 * time.Minute, OverallLimit: 90 * time.Minute, ApprovedAt: a.ApprovedAt}
	sessions, ids := map[string]bool{}, map[string]bool{}
	for _, object := range p.Objects {
		if object.ResourceID != "Secret/"+object.Namespace+"/service-bootstrap-"+object.SessionID || !environmentBootstrapOwnerUUID.MatchString(object.SessionID) || !environmentBootstrapOwnerUUID.MatchString(object.UID) || object.Namespace == "" || strings.ContainsAny(object.Namespace, "/\\\r\n") || ids[object.ResourceID] || object.Installed < 0 || object.Installed > 9 || len(object.Proofs) == 0 {
			return bad()
		}
		ids[object.ResourceID], sessions[object.SessionID] = true, true
		if object.Observation != filepath.Join(base, "session-observed.json") && !(strings.HasPrefix(object.Observation, filepath.Join(base, "session-renewal")+string(filepath.Separator)) && filepath.Base(object.Observation) == "observed.json") {
			return bad()
		}
		oraw, err := read(object.Observation, object.ObservationSHA)
		var observed createServiceSessionObserved
		if err != nil || decodeEnvironmentJSON(oraw, &observed) != nil || observed.Cleanup.SessionID != object.SessionID || observed.UIDs[object.Namespace] != object.UID {
			return bad()
		}
		var state deploymentplan.Object
		if decodeEnvironmentJSON(observed.Session, &state) != nil || state["session_id"] != object.SessionID {
			return bad()
		}
		// Early accepted observations omitted this descriptive field. Absence
		// may describe no saved ACKs, but never proves native unused state:
		// the existing fresh expired0/used5/8 classifiers still run per copy.
		if installed, present := state["installed_subjects"]; present {
			list, ok := installed.([]any)
			if !ok || len(list) != object.Installed {
				return bad()
			}
		} else if object.Installed != 0 {
			return bad()
		}
		for _, proof := range object.Proofs {
			if !environmentPreflightDigest.MatchString(proof.Plan) || proof.Journal != filepath.Join("deployment/rollback", proof.Plan, "journal.json") || proof.Stage == "" {
				return bad()
			}
			stage := createOpenBaoACKCompletionEntry(captured, proof.Stage)
			var receipt deploymentplan.Object
			if stage == nil || stage.State != "completed" || decodeEnvironmentJSON(stage.Receipt, &receipt) != nil || receipt["plan_sha256"] != proof.Plan || deploymentplan.Map(receipt["owners"])[object.ResourceID] != object.UID {
				return bad()
			}
			craw, err := read(proof.Journal, proof.JournalSHA)
			var child deploymentplan.Journal
			if err != nil || decodeEnvironmentJSON(craw, &child) != nil || child.PlanSHA256 != proof.Plan || !child.Accepted || child.Failed != "" || child.InFlight != "" || child.Owners[object.ResourceID] != object.UID || !slices.Contains(child.Completed, object.ResourceID) {
				return bad()
			}
		}
		out.Objects = append(out.Objects, createManagedLateResidualCleanupObject{ResourceID: object.ResourceID, UID: object.UID, SessionID: object.SessionID, Namespace: object.Namespace})
	}
	if len(sessions) != 8 || !slices.IsSortedFunc(out.Objects, func(a, b createManagedLateResidualCleanupObject) int {
		return strings.Compare(a.ResourceID, b.ResourceID)
	}) {
		return bad()
	}
	out.InventorySHA = deploymentplan.Digest(out.Objects)
	return out, nil
}

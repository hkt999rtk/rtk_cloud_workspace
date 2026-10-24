package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Public metadata only: the exact proposed wire shape is independently decoded
// so fixture field names cannot conceal a producer/consumer schema mismatch.
const managedLateNormalTailActualProposalWire = `{
  "schema": "rtk-cloud.remaining-normal-tail-proposal/v1",
  "environment": "dev",
  "parent_plan_sha256": "742be9e4e0311ac4842b0684b43f9b12907a23409b964fa48ce4e8bbc4ed81a7",
  "source_journal_path": "deployment/repairs/managed-late-remaining-normal-tail-source-journal.json",
  "source_journal_sha256": "887e3371ee56401edd8d6bd399eaba6248c0a75ab09d9c9d4055b3205903aef0",
  "source_owner_sha256": "6a205b72a33f78a8f18140b015597bdaa858cfd714a71ab90e25dbc791f544b1",
  "source_entries": 433,
  "source_leases": 148,
  "source428_sha256": "c35892f47c4b3376c92af4f57a0744ba35b5ee95f187b21cd59e190e2817259c",
  "prior_authority_sha256": "5b0294ff3cf63d4a0a7b7e51e9371a4a7ffcc1b3cad5b4d5e87a275b18386957",
  "prior_cleanup_deadline": "2026-10-17T08:30:20.431384Z",
  "prior_overall_deadline": "2026-10-17T08:56:01.755299Z",
  "terminal_completion_sha256": "24dc66851a88fee0a9e765b5ae268f00f0cfdf3163e6afc3ce6bcdaea7b96641",
  "failure_path": "deployment/reports/dev-ingress-dependency-order-failure-20261017T084510Z.json",
  "failure_sha256": "4cd48fe28511acd5aee2e7afc7637ade503dae75d3e32eb860edde662f978371",
  "failed_checker_sha256": "fc9cac8f9d980e4a88e1e55405d06e6d147946191004c258f539a4c76cce7708",
  "failed_review_sha256": "3defd24a9eb04635e379601d6c7f1fc2a201c039189e098bca7c7fd38c273183",
  "failed_qualification_sha256": "23807281cc45eaeea97661c239255af2bdcd8c14579e8781cc19016d32a45326",
  "pending_key": "bootstrap/ingress-admission",
  "pending_intent_sha256": "0a7958d159c66ebe0b6f851ac61141305f2c901780111efb342a09379ee90801",
  "pending_started_at": "2026-10-17T08:44:34.531981Z",
  "ingress_material_path": "deployment/create/material/742be9e4e0311ac4842b0684b43f9b12907a23409b964fa48ce4e8bbc4ed81a7/ingress-admission/identity.json",
  "ingress_material_sha256": "67f4dbacfab6591b4322a6389af342e68a986b7b432ac66f232512fe93c2c643",
  "ingress_secret_id": "Secret/video-cloud-dev-ingress/ingress-nginx-admission",
  "ingress_secret_uid": "c1cbf388-1033-4c0b-8ef4-0e0f0fadd58f",
  "ingress_identity_plan_sha256": "4452e98ce3700947a8a8bbe39f7816ada49855fe702d7c290e71c22ac69c93c3",
  "account_registration_plan_sha256": "35e78c7e2571957cdfe12b862a1f347e8064f05602405768118c9b3387ebb94c",
  "custody_retirement_plan_sha256": "65a079e9f10a9bacf5119b6ebde8d215f854c8c932e5f0edbfc82025306a4a35",
  "first_caller_overall_minutes": 90,
  "credential_reserve_minutes": 5,
  "required_reserve_at_first_admission_minutes": 15,
  "allowed_steps": [
    "resume-original-pending-ingress-admission",
    "ingress-controller-and-webhook",
    "maintained-workloads",
    "original-external-planes",
    "environment-dns",
    "turn-acceptance",
    "full-rollout-acceptance",
    "final-volume-ledger",
    "original-CompleteAndRelease"
  ],
  "forbidden_steps": [
    "bootstrap-session-or-identity-generation",
    "native-finalizer-POST",
    "signing-or-revocation-replay",
    "residual-copy-cleanup",
    "completed-account-registration-replay",
    "completed-custody-or-export-helper-recreation",
    "ingress-identity-regeneration-or-secret-replacement",
    "old-authority-or-receipt-rewrite",
    "cross-environment-or-worktree-fallback"
  ],
  "completion_requirements": [
    "FULL-rollout-acceptance",
    "original-parent-completed-and-fence-archived",
    "independent-standard-FULL-post-health-started-after-parent-completion",
    "protected19-unchanged",
    "original18-volumes-retained"
  ],
  "retry_reuses_original_new_phase_clock": true,
  "execution_authority": false,
  "status": "proposal-awaiting-explicit-approval",
  "recorded_at": "2026-10-17T08:49:08.464771+00:00"
}
`

func managedLateNormalTailApprovalFixture(t *testing.T) (managedLateNormalTailProposal, managedLateNormalTailApprovalRecord, time.Time) {
	t.Helper()
	var p managedLateNormalTailProposal
	if err := decodeEnvironmentJSON([]byte(managedLateNormalTailActualProposalWire), &p); err != nil {
		t.Fatal("actual proposal wire differs", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	p.RecordedAt = now.Add(-time.Minute)
	p.PendingStartedAt = now.Add(-10 * time.Minute)
	no := false
	a := managedLateNormalTailApprovalRecord{Schema: "rtk-cloud.remaining-normal-tail-operator-approval/v1", Environment: p.Environment, Parent: p.Parent, ProposalSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(p)), SourceRawSHA: p.SourceRawSHA, Approved: true, FirstCallerMinutes: 90, ExecutionAuthority: &no, RecordedAt: now, HumanMessage: "explicit isolated normal-tail-only approval"}
	return p, a, now
}

func TestManagedLateNormalTailApprovalRequiresExactExplicitNormalOnlyProposal(t *testing.T) {
	for _, mode := range []string{"genuine", "old-schema", "foreign-environment", "foreign-parent", "source-path", "source-sha", "source-count", "source-leases", "material-path", "secret-id", "secret-uid", "missing-authority", "execution-authority", "missing-retry-clock", "reset-clock", "caller120", "credential-reserve0", "first-reserve0", "status", "future-proposal", "proposal-after-approval", "pending-key", "pending-zero-time", "pending-after-proposal", "prior-zero-deadline", "prior-overall", "changed-allowed", "missing-forbidden", "missing-full-health", "malformed-history-pin", "outside-failure-path"} {
		t.Run(mode, func(t *testing.T) {
			p, a, now := managedLateNormalTailApprovalFixture(t)
			switch mode {
			case "old-schema":
				p.Schema = "rtk-cloud.residual-copy-cleanup-remaining-proposal/v1"
			case "foreign-environment":
				p.Environment = "staging"
			case "foreign-parent":
				p.Parent = strings.Repeat("a", 64)
			case "source-path":
				p.SourcePath = "worktrees/fake/source.json"
			case "source-sha":
				p.SourceRawSHA = strings.Repeat("a", 64)
			case "source-count":
				p.SourceEntries = 432
			case "source-leases":
				p.SourceLeases = 0
			case "material-path":
				p.MaterialPath = "deployment/create/material/foreign/identity.json"
			case "secret-id":
				p.SecretID = "Secret/foreign/ingress-nginx-admission"
			case "secret-uid":
				p.SecretUID = "foreign"
			case "missing-authority":
				p.ExecutionAuthority = nil
			case "execution-authority":
				yes := true
				p.ExecutionAuthority = &yes
			case "missing-retry-clock":
				p.RetryReusesClock = nil
			case "reset-clock":
				no := false
				p.RetryReusesClock = &no
			case "caller120":
				p.FirstCallerMinutes = 120
			case "credential-reserve0":
				p.CredentialReserve = 0
			case "first-reserve0":
				p.FirstAdmissionReserve = 0
			case "status":
				p.Status = "already-granted"
			case "future-proposal":
				p.RecordedAt = now.Add(time.Second)
				a.RecordedAt = now.Add(time.Minute)
			case "proposal-after-approval":
				p.RecordedAt = a.RecordedAt.Add(time.Second)
			case "pending-key":
				p.PendingKey = "bootstrap/native-finalizer"
			case "pending-zero-time":
				p.PendingStartedAt = time.Time{}
			case "pending-after-proposal":
				p.PendingStartedAt = p.RecordedAt.Add(time.Second)
			case "prior-zero-deadline":
				p.PriorCleanupDeadline = time.Time{}
			case "prior-overall":
				p.PriorOverallDeadline = p.PriorCleanupDeadline
			case "changed-allowed":
				p.Allowed = append(p.Allowed, "native-finalizer-POST")
			case "missing-forbidden":
				p.Forbidden = p.Forbidden[1:]
			case "missing-full-health":
				p.Completion = p.Completion[:2]
			case "malformed-history-pin":
				p.CompletedProofSHA = "not-a-digest"
			case "outside-failure-path":
				p.FailurePath = "../foreign.json"
			}
			if managedLateNormalTailProposalValid(p, a, a.Environment, a.Parent, now) != (mode == "genuine") {
				t.Fatal("normal-only proposal scope or immutable bindings accepted drift", mode)
			}
		})
	}
}

func TestManagedLateNormalTailApprovalRecordRequiresNewHumanDecisionAndBoundedClock(t *testing.T) {
	for _, mode := range []string{"genuine", "not-approved", "wrong-schema", "foreign-environment", "foreign-parent", "missing-human", "zero-time", "future-time", "caller120", "missing-authority", "authority-true", "invalid-proposal", "invalid-source"} {
		t.Run(mode, func(t *testing.T) {
			_, a, now := managedLateNormalTailApprovalFixture(t)
			environment, parent := a.Environment, a.Parent
			switch mode {
			case "not-approved":
				a.Approved = false
			case "wrong-schema":
				a.Schema = "rtk-cloud.residual-copy-cleanup-operator-approval/v1"
			case "foreign-environment":
				a.Environment = "staging"
			case "foreign-parent":
				a.Parent = strings.Repeat("a", 64)
			case "missing-human":
				a.HumanMessage = " "
			case "zero-time":
				a.RecordedAt = time.Time{}
			case "future-time":
				a.RecordedAt = now.Add(time.Second)
			case "caller120":
				a.FirstCallerMinutes = 120
			case "missing-authority":
				a.ExecutionAuthority = nil
			case "authority-true":
				yes := true
				a.ExecutionAuthority = &yes
			case "invalid-proposal":
				a.ProposalSHA = "foreign"
			case "invalid-source":
				a.SourceRawSHA = "foreign"
			}
			if managedLateNormalTailApprovalRecordValid(a, environment, parent, now) != (mode == "genuine") {
				t.Fatal("approval omitted explicit decision or changed selected scope", mode)
			}
		})
	}
}

func TestManagedLateNormalTailApprovalFilesNeverGrantWithoutAuthenticatedHistory(t *testing.T) {
	for _, mode := range []string{"both-absent", "unapproved-proposal", "approval-orphan", "source-absent", "changed-proposal", "malformed-approval", "unknown-field", "mode", "symlink", "directory"} {
		t.Run(mode, func(t *testing.T) {
			p, a, _ := managedLateNormalTailApprovalFixture(t)
			store := makeIsolatedTestSecretStore(t, "dev")
			write := func(rel string, raw []byte) {
				t.Helper()
				if err := ensurePrivateDirectory(filepath.Dir(filepath.Join(store.Root, rel))); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store.Root, rel), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "both-absent" && mode != "approval-orphan" {
				write(managedLateNormalTailProposalPath, mustProviderRecoveryJSON(p))
			}
			if mode != "both-absent" && mode != "unapproved-proposal" {
				write(managedLateNormalTailApprovalPath, mustProviderRecoveryJSON(a))
			}
			ap := filepath.Join(store.Root, managedLateNormalTailApprovalPath)
			switch mode {
			case "changed-proposal":
				write(managedLateNormalTailProposalPath, []byte(`{"changed":true}`))
			case "malformed-approval":
				write(managedLateNormalTailApprovalPath, []byte(`{"private-canary":`))
			case "unknown-field":
				raw := mustProviderRecoveryJSON(a)
				raw = append(raw[:len(raw)-1], []byte(`,"unknown":"private-canary"}`)...)
				write(managedLateNormalTailApprovalPath, raw)
			case "mode":
				if err := os.Chmod(ap, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(ap); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(store.Root, managedLateNormalTailProposalPath), ap); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(ap); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(ap, 0700); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadManagedLateNormalTailApproval(store, a.Parent)
			if mode == "both-absent" || mode == "unapproved-proposal" {
				if got != nil || err != nil {
					t.Fatal("unapproved proposal became authority or an unsafe fallback", err)
				}
			} else if got != nil || err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe/missing historical evidence granted approval or leaked input", mode, err)
			}
		})
	}
}

func TestManagedLateNormalTailApprovalAuthenticatesExpiredGOAtCapturedOperationTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	qualified := now.Add(-2 * time.Hour)
	parent := strings.Repeat("a", 64)
	owner := environmentCreateOwner{PlanSHA256: parent, OwnerToken: "historical-original-owner"}
	prefix := &environmentCreateExecution{owner: owner, Journal: environmentCreateJournal{PlanSHA256: parent, OwnerToken: owner.OwnerToken}}
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: parent, ParentJournalSHA: deploymentplan.Digest(prefix.Journal), ParentOwnerSHA: deploymentplan.Digest(owner), CheckerSHA: strings.Repeat("b", 64), KubeconfigSHA: strings.Repeat("c", 64), FoundationSHA: strings.Repeat("d", 64), FoundationJournalSHA: strings.Repeat("e", 64), Materials: map[string]string{}}, QualifiedAt: qualified, ValidUntil: qualified.Add(15 * time.Minute)}
	a.Plan.Plan.Environment = "dev"
	a.Plan.Plan.Stack = "video-cloud-dev"
	a.Plan.Plan.ConfigurationSHA256 = strings.Repeat("f", 64)
	review := deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256, a.Qualification.Decision = review, "GO"
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	p := managedLateNormalTailProposal{FailedCheckerSHA: a.Plan.CheckerSHA, FailedReviewSHA: review, FailedQualificationSHA: environmentPreflightBytesSHA256(raw)}
	if p.FailedQualificationSHA == deploymentplan.Digest(a) {
		t.Fatal("fixture failed to distinguish approved raw bytes from semantic object")
	}
	source := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 433)}
	source.Entries[428].StartedAt = qualified.Add(7 * time.Minute)
	for _, mode := range []string{"genuine-expired-history", "current-time", "before-old-go", "at-old-go-expiry", "missing-operation-time", "changed-raw", "semantic-hash", "changed-checker", "changed-old-source"} {
		t.Run(mode, func(t *testing.T) {
			copy := source
			copy.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
			proposal := p
			original := *prefix
			bytes := raw
			switch mode {
			case "current-time":
				copy.Entries[428].StartedAt = now
			case "before-old-go":
				copy.Entries[428].StartedAt = qualified.Add(-time.Second)
			case "at-old-go-expiry":
				copy.Entries[428].StartedAt = a.ValidUntil
			case "missing-operation-time":
				copy.Entries[428].StartedAt = time.Time{}
			case "changed-raw":
				bytes = mustProviderRecoveryJSON(a)
			case "semantic-hash":
				proposal.FailedQualificationSHA = deploymentplan.Digest(a)
			case "changed-checker":
				proposal.FailedCheckerSHA = strings.Repeat("0", 64)
			case "changed-old-source":
				original.Journal.OwnerToken = "foreign"
			}
			if managedLateNormalTailHistoricalQualificationValid(proposal, copy, &original, bytes) != (mode == "genuine-expired-history") {
				t.Fatal("historical qualification used a current clock or changed original evidence", mode)
			}
		})
	}
}

func TestManagedLateNormalTailApprovalKeepsCompletedNormalStagesInsideOldOverall(t *testing.T) {
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	source := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 433)}
	for i := range 5 {
		source.Entries[428+i].StartedAt = base.Add(time.Duration(i) * time.Minute)
		if i != 3 {
			source.Entries[428+i].CompletedAt = source.Entries[428+i].StartedAt.Add(time.Second)
		}
	}
	for _, mode := range []string{"genuine", "start-after-old-overall", "completion-after-old-overall", "future-vs-proposal", "missing-completion", "before-first-operation", "wrong-source-count"} {
		t.Run(mode, func(t *testing.T) {
			j := source
			j.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
			overall, recorded := base.Add(20*time.Minute), base.Add(30*time.Minute)
			switch mode {
			case "start-after-old-overall":
				j.Entries[431].StartedAt = overall.Add(time.Second)
			case "completion-after-old-overall":
				j.Entries[432].CompletedAt = overall.Add(time.Second)
			case "future-vs-proposal":
				recorded = base.Add(time.Minute)
			case "missing-completion":
				j.Entries[432].CompletedAt = time.Time{}
			case "before-first-operation":
				j.Entries[429].StartedAt = base.Add(-time.Second)
			case "wrong-source-count":
				j.Entries = j.Entries[:432]
			}
			if managedLateNormalTailHistoricalWindowValid(j, overall, recorded) != (mode == "genuine") {
				t.Fatal("old normal-operation window was extended or its genuine receipts rejected", mode)
			}
		})
	}
}

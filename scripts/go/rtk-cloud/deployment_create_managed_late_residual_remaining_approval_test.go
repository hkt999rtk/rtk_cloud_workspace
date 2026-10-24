package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualRemainingApprovalFixture(t *testing.T) (managedLateResidualRemainingProposal, managedLateResidualRemainingApprovalRecord, *createManagedLateResidualCleanupApproval, environmentCreateJournal, createManagedLateResidualCleanupAuthority) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	parent := strings.Repeat("a", 64)
	base := managedLateResidualRemainingBase(parent)
	old := &createManagedLateResidualCleanupApproval{ProposalSHA: strings.Repeat("b", 64), ApprovalSHA: strings.Repeat("c", 64), ParentSHA: parent, SourceSHA: strings.Repeat("d", 64), OwnerSHA: strings.Repeat("e", 64), CleanupLimit: 60 * time.Minute, OverallLimit: 90 * time.Minute, ApprovedAt: now.Add(-2 * time.Hour)}
	for session := 0; session < 8; session++ {
		sid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", session+1)
		for index, ns := range []string{"stack-video-cloud", "stack-account-manager", "stack-secrets"} {
			old.Objects = append(old.Objects, createManagedLateResidualCleanupObject{ResourceID: "Secret/" + ns + "/service-bootstrap-" + sid, UID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", session*3+index+1), SessionID: sid, Namespace: ns})
		}
	}
	slices.SortFunc(old.Objects, func(a, b createManagedLateResidualCleanupObject) int {
		return strings.Compare(a.ResourceID, b.ResourceID)
	})
	old.InventorySHA = deploymentplan.Digest(old.Objects)
	authority := createManagedLateResidualCleanupAuthority{StartedAt: now.Add(-90 * time.Minute), Deadline: now.Add(-30 * time.Minute), OverallDeadline: now.Add(-time.Minute)}
	p := managedLateResidualRemainingProposal{Schema: "rtk-cloud.residual-copy-cleanup-remaining-proposal/v1", Environment: "dev", ApprovalStatus: "pending-explicit-human-decision", PreparedAt: now.Add(-25 * time.Minute), Parent: parent, SourceJournal: filepathForRemainingApproval(base, "source-journal"), SourceRawSHA: strings.Repeat("f", 64), SourceEntries: 403, OwnerRawSHA: strings.Repeat("1", 64), OriginalProposalSHA: old.ProposalSHA, OriginalApprovalSHA: old.ApprovalSHA, OldAuthorityRawSHA: strings.Repeat("2", 64), PriorStartedAt: authority.StartedAt, PriorDeadline: authority.Deadline, PriorOverall: authority.OverallDeadline, FailedRun: filepathForRemainingApproval(base, "failed-run"), FailedRunSHA: strings.Repeat("3", 64), FailedAt: now.Add(-40 * time.Minute), FailedElapsed: 545.294, CleanupMinutes: 60, CallerMinutes: 90, ExpiryReserve: 5, FirstReserve: 15, WindowRule: "distinct fixed grant; retries never reset", GrantSchema: "rtk-cloud.create-managed-late-residual-copy-cleanup-remaining/v1", AdmissionPath: base + "/managed-late-residual-copy-cleanup-remaining-grant-admission.json", AuthorityPath: base + "/managed-late-residual-copy-cleanup-remaining-grant-authority.json", Permitted: []string{"retire exact21", "finish original396", "full acceptance"}, Forbidden: []string{"old grant rewrite", "mint replay", "identity replacement", "proposal as authority"}}
	p.Native.Session, p.Native.Issuer = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	p.Native.Fingerprint, p.Native.Root, p.Native.TombstoneSHA = strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64)
	p.Native.FinalizedAt = now.Add(-3 * time.Hour)
	p.Evidence.Historical, p.Evidence.Fresh, p.Evidence.Complete = true, true, true
	source := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 403)}
	for _, object := range old.Objects {
		if strings.HasPrefix(object.SessionID, "00000001-") {
			i := len(p.Completed)
			key := "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID
			start := authority.StartedAt.Add(time.Duration(i+1) * time.Minute)
			receipt, _ := json.Marshal(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "absent": true})
			entry := environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", StartedAt: start, CompletedAt: start.Add(time.Second), IntentSHA256: activeTransportJournalDigest(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}), Receipt: receipt}
			source.Entries[400+i] = entry
			p.Completed = append(p.Completed, managedLateResidualRemainingRetirement{Entry: 401 + i, Key: key, ResourceID: object.ResourceID, UID: object.UID, SessionID: object.SessionID, CompletedAt: entry.CompletedAt})
		} else {
			p.Objects = append(p.Objects, object)
		}
	}
	a := managedLateResidualRemainingApprovalRecord{managedLateResidualApprovalRecord: managedLateResidualApprovalRecord{Schema: "rtk-cloud.residual-copy-cleanup-remaining-operator-approval/v1", Environment: "dev", Parent: parent, Source: p.SourceRawSHA, Proposal: strings.Repeat("7", 64), Decision: "approved", ApprovedAt: now, HumanMessage: "yes, i approve", CleanupMinutes: 60, CallerMinutes: 90}, HumanFollowup: "and extend the time out period"}
	return p, a, old, source, authority
}

func filepathForRemainingApproval(base, kind string) string {
	return base + "/managed-late-residual-copy-cleanup-remaining-" + kind + ".json"
}

func TestManagedLateResidualRemainingApprovalRequiresDistinctExplicitBoundedProposal(t *testing.T) {
	for _, mode := range []string{"genuine", "old-schema", "execution-authority", "foreign-parent", "foreign-environment", "wrong-source", "wrong-source-count", "alternate-source-path", "future-prepared", "future-failure", "prior-clock-reset", "prior-overall-truncated", "cleanup90", "caller120", "reserve0", "no-fresh-native", "no-full-acceptance", "alternate-new-pair", "wrong-new-schema", "missing-completed", "extra-object", "missing-native-id", "finalization-after-old-start"} {
		t.Run(mode, func(t *testing.T) {
			p, a, _, _, _ := managedLateResidualRemainingApprovalFixture(t)
			switch mode {
			case "old-schema":
				p.Schema = "rtk-cloud.residual-copy-cleanup-proposal/v1"
			case "execution-authority":
				p.ExecutionAuthority = true
			case "foreign-parent":
				p.Parent = strings.Repeat("8", 64)
			case "foreign-environment":
				p.Environment = "staging"
			case "wrong-source":
				p.SourceRawSHA = strings.Repeat("8", 64)
			case "wrong-source-count":
				p.SourceEntries = 400
			case "alternate-source-path":
				p.SourceJournal = "deployment/other/source.json"
			case "future-prepared":
				p.PreparedAt = a.ApprovedAt.Add(time.Second)
			case "future-failure":
				p.FailedAt = p.PreparedAt.Add(time.Second)
			case "prior-clock-reset":
				p.PriorStartedAt = p.PriorStartedAt.Add(time.Minute)
			case "prior-overall-truncated":
				p.PriorOverall = p.PriorDeadline
			case "cleanup90":
				p.CleanupMinutes = 90
			case "caller120":
				p.CallerMinutes = 120
			case "reserve0":
				p.ExpiryReserve = 0
			case "no-fresh-native":
				p.Evidence.Fresh = false
			case "no-full-acceptance":
				p.Evidence.Complete = false
			case "alternate-new-pair":
				p.AdmissionPath = p.AuthorityPath
			case "wrong-new-schema":
				p.GrantSchema = managedLateResidualCleanupSchema
			case "missing-completed":
				p.Completed = p.Completed[:2]
			case "extra-object":
				p.Objects = append(p.Objects, p.Objects[0])
			case "missing-native-id":
				p.Native.Session = ""
			case "finalization-after-old-start":
				p.Native.FinalizedAt = p.PriorStartedAt.Add(time.Second)
			}
			before := deploymentplan.Digest(p)
			if managedLateResidualRemainingProposalValid(p, a, managedLateResidualRemainingBase(a.Parent), a.Environment, a.Parent) != (mode == "genuine") || deploymentplan.Digest(p) != before {
				t.Fatal("remaining proposal accepted changed scope or mutated evidence")
			}
		})
	}
}

func TestManagedLateResidualRemainingInventoryPreservesOldThreeAndExactRemainingTwentyOne(t *testing.T) {
	for _, mode := range []string{"genuine", "old-start-reset", "old-completed-after-deadline", "old-completed-after-failure", "old-time-diff", "zero-start", "zero-completed", "pending-old", "old-intent-drift", "old-uid-drift", "receipt-uid-drift", "receipt-not-absent", "completed-index", "completed-key", "completed-duplicate", "unknown-new-object", "changed-new-uid", "replayed-completed", "duplicate-new", "unsorted-new", "missing-new", "old-inventory-drift"} {
		t.Run(mode, func(t *testing.T) {
			p, _, old, source, authority := managedLateResidualRemainingApprovalFixture(t)
			switch mode {
			case "old-start-reset":
				authority.StartedAt = source.Entries[400].StartedAt.Add(time.Second)
			case "old-completed-after-deadline":
				source.Entries[400].CompletedAt = authority.Deadline.Add(time.Second)
				p.Completed[0].CompletedAt = source.Entries[400].CompletedAt
			case "old-completed-after-failure":
				p.FailedAt = source.Entries[400].CompletedAt.Add(-time.Second)
			case "old-time-diff":
				p.Completed[0].CompletedAt = p.Completed[0].CompletedAt.Add(time.Second)
			case "zero-start":
				source.Entries[400].StartedAt = time.Time{}
			case "zero-completed":
				source.Entries[400].CompletedAt = time.Time{}
			case "pending-old":
				source.Entries[400].State = "pending"
			case "old-intent-drift":
				source.Entries[400].IntentSHA256 = strings.Repeat("8", 64)
			case "old-uid-drift":
				p.Completed[0].UID = "99999999-9999-4999-8999-999999999999"
			case "receipt-uid-drift":
				source.Entries[400].Receipt = []byte(`{"resource_id":"foreign","uid":"99999999-9999-4999-8999-999999999999","absent":true}`)
			case "receipt-not-absent":
				var receipt deploymentplan.Object
				_ = json.Unmarshal(source.Entries[400].Receipt, &receipt)
				receipt["absent"] = false
				source.Entries[400].Receipt, _ = json.Marshal(receipt)
			case "completed-index":
				p.Completed[0].Entry = 400
			case "completed-key":
				p.Completed[0].Key = "foreign"
			case "completed-duplicate":
				p.Completed[1] = p.Completed[0]
			case "unknown-new-object":
				p.Objects[0].ResourceID = "Secret/foreign/service-bootstrap-foreign"
			case "changed-new-uid":
				p.Objects[0].UID = "99999999-9999-4999-8999-999999999999"
			case "replayed-completed":
				p.Objects[0] = old.Objects[0]
			case "duplicate-new":
				p.Objects[1] = p.Objects[0]
			case "unsorted-new":
				p.Objects[0], p.Objects[1] = p.Objects[1], p.Objects[0]
			case "missing-new":
				p.Objects = p.Objects[:20]
			case "old-inventory-drift":
				old.InventorySHA = strings.Repeat("8", 64)
			}
			before := deploymentplan.Digest(source)
			completed, ok := managedLateResidualRemainingInventory(p, old, source, authority)
			if ok != (mode == "genuine") || ok && len(completed) != 3 || deploymentplan.Digest(source) != before {
				t.Fatal("remaining inventory accepted altered old receipt or replayed/new identity")
			}
		})
	}
}

func TestManagedLateResidualRemainingApprovalRecordDecodesFollowupWithoutAuthority(t *testing.T) {
	_, a, _, _, _ := managedLateResidualRemainingApprovalFixture(t)
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var decoded managedLateResidualRemainingApprovalRecord
	if decodeEnvironmentJSON(raw, &decoded) != nil || decoded.HumanFollowup != "and extend the time out period" || decoded.Schema != a.Schema || decoded.ExecutionAuthority {
		t.Fatal("new human followup or original approval fields were lost")
	}
	var old managedLateResidualApprovalRecord
	if decodeEnvironmentJSON(raw, &old) == nil {
		t.Fatal("new approval accidentally accepted by old strict schema")
	}
}

func TestManagedLateResidualRemainingApprovalLoaderFailsClosedBeforeHistoricalReads(t *testing.T) {
	for _, mode := range []string{"absent", "unapproved", "execution-authority", "old-schema", "future-approved", "cleanup90", "caller120", "missing-human-followup", "foreign-parent", "foreign-environment", "invalid-source", "unknown-field", "unsafe-file", "symlink", "unsafe-parent", "proposal-missing"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			_, a, _, _, _ := managedLateResidualRemainingApprovalFixture(t)
			parent := a.Parent
			path := filepath.Join(store.Root, managedLateResidualRemainingBase(parent), "managed-late-residual-copy-cleanup-remaining-operator-approval.json")
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unapproved":
				a.Decision = "pending"
			case "execution-authority":
				a.ExecutionAuthority = true
			case "old-schema":
				a.Schema = "rtk-cloud.residual-copy-cleanup-operator-approval/v1"
			case "future-approved":
				a.ApprovedAt = time.Now().Add(time.Hour)
			case "cleanup90":
				a.CleanupMinutes = 90
			case "caller120":
				a.CallerMinutes = 120
			case "missing-human-followup":
				a.HumanFollowup = ""
			case "foreign-parent":
				a.Parent = strings.Repeat("9", 64)
			case "foreign-environment":
				a.Environment = "staging"
			case "invalid-source":
				a.Source = "private-canary"
			}
			raw, _ := json.Marshal(a)
			if mode == "unknown-field" {
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				fields["private-canary"] = true
				raw, _ = json.Marshal(fields)
			}
			if mode != "absent" {
				if mode == "symlink" {
					target := filepath.Join(store.Root, "public-fixture.json")
					if os.WriteFile(target, raw, 0600) != nil || os.Symlink(target, path) != nil {
						t.Fatal("fixture unavailable")
					}
				} else if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "unsafe-file" {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "unsafe-parent" {
					if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := loadManagedLateResidualRemainingApproval(store, parent)
			if mode == "absent" {
				if err != nil || result != nil {
					t.Fatal("true absence fabricated approval")
				}
			} else if err == nil || result != nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unapproved/unsafe remaining approval accepted or private diagnostics leaked")
			}
		})
	}
}

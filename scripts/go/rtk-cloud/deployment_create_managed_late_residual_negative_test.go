package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualAuditFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	r, a, postStart := managedLatePostNativeAuditFixture(t)
	old, err := newManagedLatePostNativeCleanupAuthority(r, a, postStart.Add(90*time.Minute), postStart, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	r.PostNativeAuthority = old
	now := r.LeafNotAfter.Add(10 * time.Minute)
	approval := &createManagedLateResidualCleanupApproval{ProposalSHA: strings.Repeat("7", 64), ApprovalSHA: strings.Repeat("8", 64), ParentSHA: r.ParentSHA, SourceSHA: r.PostNativeCleanup.SourceSHA, OwnerSHA: r.OwnerSHA, CleanupLimit: 60 * time.Minute, OverallLimit: 90 * time.Minute}
	for i := range 24 {
		sid := fmt.Sprintf("%08x-1111-4111-8111-%012x", 1+i/3, 1+i/3)
		ns := fmt.Sprintf("isolated-ns-%d", i%3)
		approval.Objects = append(approval.Objects, createManagedLateResidualCleanupObject{ResourceID: "Secret/" + ns + "/service-bootstrap-" + sid, Namespace: ns, SessionID: sid, UID: fmt.Sprintf("%08x-2222-4222-8222-%012x", 100+i, 100+i)})
	}
	approval.InventorySHA = deploymentplan.Digest(approval.Objects)
	approval.ApprovedAt = now.Add(-2 * time.Minute)
	r.ResidualCleanup = &createManagedLateResidualCleanup{Approval: approval, PostNativeSHA: deploymentplan.Digest(r.PostNativeCleanup), OldPostNativeAuthoritySHA: deploymentplan.Digest(old), CredentialMode: "non-expiring-kubernetes-token", CredentialSHA: strings.Repeat("9", 64), NativeEvidence: map[string]string{}}
	for key, value := range r.PostNativeCleanup.NativeEvidence {
		r.ResidualCleanup.NativeEvidence[key] = value
	}
	r.ResidualCleanup.NativeEvidence["current-credential"] = managedLateResidualCredentialSHA(r.ResidualCleanup)
	a.Plan.Materials[managedLateRemainingMaterial+"residual-cleanup"] = deploymentplan.Digest(r.ResidualCleanup)
	a.Plan.Materials[managedLateRemainingMaterial+"residual-policy"] = managedLateResidualCleanupPolicySHA()
	a.Plan.CheckerSHA = strings.Repeat("a", 64)
	a.QualifiedAt, a.ValidUntil = now.Add(-time.Minute), now.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	return r, a, now
}

func TestManagedLateResidualCleanupAuditFixedFreshClockAndHistoricalExpiry(t *testing.T) {
	for _, mode := range []string{"historical-expired-leaf", "caller-cap", "finite-current-credential", "finite-credential-too-short", "first-reserve", "caller-over-ninety", "missing-approval", "missing-object", "extra-object", "duplicate-uid", "duplicate-resource", "foreign-object-kind", "foreign-source", "foreign-owner", "wrong-old-authority", "old-finalization-outside-window", "wrong-policy", "missing-material", "wrong-review", "failed-gate", "gate-evidence", "expired-go", "missing-raw-hash", "missing-native-evidence", "foreign-current-credential-evidence", "foreign-credential-mode", "missing-credential-hash", "nonexpiring-with-expiry"} {
		t.Run(mode, func(t *testing.T) {
			r, a, now := managedLateResidualAuditFixture(t)
			old := deploymentplan.Digest([]any{r.Authority, r.CleanupAuthority, r.FinalizationAuthority, r.TerminalAuthority, r.PostNativeAuthority})
			if !now.After(r.LeafNotAfter) || !now.After(r.PostNativeAuthority.Deadline) {
				t.Fatal("fixture did not preserve expired historical bootstrap/native clocks")
			}
			overall, expected := now.Add(90*time.Minute), now.Add(60*time.Minute)
			pass := mode == "historical-expired-leaf" || mode == "caller-cap" || mode == "finite-current-credential"
			switch mode {
			case "caller-cap":
				overall = now.Add(20 * time.Minute)
				expected = overall
			case "finite-current-credential":
				r.ResidualCleanup.CredentialMode = "kubernetes-client-certificate"
				r.ResidualCleanup.CredentialExpires = now.Add(35 * time.Minute)
				r.ResidualCleanup.NativeEvidence["current-credential"] = managedLateResidualCredentialSHA(r.ResidualCleanup)
				expected = now.Add(30 * time.Minute)
				a.Plan.Materials[managedLateRemainingMaterial+"residual-cleanup"] = deploymentplan.Digest(r.ResidualCleanup)
				managedLateRemainingNegativeBind(t, r, &a)
			case "finite-credential-too-short":
				r.ResidualCleanup.CredentialMode = "expiring-kubernetes-token"
				r.ResidualCleanup.CredentialExpires = now.Add(20*time.Minute - time.Nanosecond)
				r.ResidualCleanup.NativeEvidence["current-credential"] = managedLateResidualCredentialSHA(r.ResidualCleanup)
				a.Plan.Materials[managedLateRemainingMaterial+"residual-cleanup"] = deploymentplan.Digest(r.ResidualCleanup)
				managedLateRemainingNegativeBind(t, r, &a)
			case "first-reserve":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "caller-over-ninety":
				overall = now.Add(90*time.Minute + time.Nanosecond)
			case "missing-approval":
				r.ResidualCleanup.Approval = nil
			case "missing-object":
				r.ResidualCleanup.Approval.Objects = r.ResidualCleanup.Approval.Objects[:23]
			case "extra-object":
				r.ResidualCleanup.Approval.Objects = append(r.ResidualCleanup.Approval.Objects, r.ResidualCleanup.Approval.Objects[0])
			case "duplicate-uid":
				r.ResidualCleanup.Approval.Objects[1].UID = r.ResidualCleanup.Approval.Objects[0].UID
			case "duplicate-resource":
				r.ResidualCleanup.Approval.Objects[1] = r.ResidualCleanup.Approval.Objects[0]
			case "foreign-object-kind":
				r.ResidualCleanup.Approval.Objects[0].ResourceID = "Deployment/isolated/foreign"
			case "foreign-source":
				r.ResidualCleanup.Approval.SourceSHA = strings.Repeat("b", 64)
			case "foreign-owner":
				r.ResidualCleanup.Approval.OwnerSHA = strings.Repeat("b", 64)
			case "wrong-old-authority":
				r.ResidualCleanup.OldPostNativeAuthoritySHA = strings.Repeat("b", 64)
			case "old-finalization-outside-window":
				r.PostNativeCleanup.Native.FinalizedAt = r.TerminalAuthority.Deadline.Add(time.Nanosecond)
			case "wrong-policy":
				a.Plan.Materials[managedLateRemainingMaterial+"residual-policy"] = strings.Repeat("b", 64)
			case "missing-material":
				delete(a.Plan.Materials, managedLateRemainingMaterial+"residual-cleanup")
			case "wrong-review":
				r.Review = strings.Repeat("b", 64)
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("b", 64)
			case "expired-go":
				now = a.ValidUntil
			case "missing-native-evidence":
				delete(r.ResidualCleanup.NativeEvidence, "copies")
			case "foreign-current-credential-evidence":
				r.ResidualCleanup.NativeEvidence["current-credential"] = strings.Repeat("b", 64)
				a.Plan.Materials[managedLateRemainingMaterial+"residual-cleanup"] = deploymentplan.Digest(r.ResidualCleanup)
				managedLateRemainingNegativeBind(t, r, &a)
			case "foreign-credential-mode":
				r.ResidualCleanup.CredentialMode = "expired-bootstrap-is-current"
			case "missing-credential-hash":
				r.ResidualCleanup.CredentialSHA = ""
			case "nonexpiring-with-expiry":
				r.ResidualCleanup.CredentialExpires = now.Add(time.Hour)
			}
			rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
			if mode == "missing-raw-hash" {
				rawSHA = ""
			}
			before := deploymentplan.Digest([]any{r, a})
			got, err := newManagedLateResidualCleanupAuthority(r, a, overall, now, rawSHA)
			if (err == nil) != pass {
				t.Fatalf("acceptance differs: %v", err)
			}
			if pass {
				if !got.StartedAt.Equal(now) || !got.Deadline.Equal(expected) || !got.OverallDeadline.Equal(overall) || got.QualifiedBytesSHA != rawSHA || got.ProposalSHA != r.ResidualCleanup.Approval.ProposalSHA || got.OldPostNativeAuthoritySHA != deploymentplan.Digest(r.PostNativeAuthority) {
					t.Fatal("new grant lost exact policy/proposal/old authority/caps")
				}
				changed := *got
				changed.Deadline = changed.Deadline.Add(time.Nanosecond)
				if managedLateResidualCleanupAuthorityMatches(r, &changed) {
					t.Fatal("stored cleanup clock could slide")
				}
			}
			if deploymentplan.Digest([]any{r, a}) != before {
				t.Fatal("new authority changed source")
			}
			if mode != "old-finalization-outside-window" && deploymentplan.Digest([]any{r.Authority, r.CleanupAuthority, r.FinalizationAuthority, r.TerminalAuthority, r.PostNativeAuthority}) != old {
				t.Fatal("historical authority changed")
			}
		})
	}
}

func TestManagedLateResidualCleanupAuditPairNeverRegenerates(t *testing.T) {
	for _, mode := range []string{"same", "primary-missing", "witness-missing", "mismatch", "public-witness", "symlink-witness"} {
		t.Run(mode, func(t *testing.T) {
			store, parent, _, _ := terminalWitnessFixture(t)
			primary, witness := filepath.Join(store.Root, managedLateResidualCleanupPath(parent, false)), filepath.Join(store.Root, managedLateResidualCleanupPath(parent, true))
			raw := []byte(`{"first_admission":"2030-01-01T00:00:00Z","fixed_minutes":60}`)
			if got, err := readManagedLateResidualCleanupPair(store, parent); err != nil || got != nil {
				t.Fatal("first absence refused", err)
			}
			if err := writeManagedLateResidualCleanupPair(store, parent, raw); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "primary-missing":
				err = os.Remove(primary)
			case "witness-missing":
				err = os.Remove(witness)
			case "mismatch":
				err = os.WriteFile(witness, []byte(`{"reset":true}`), 0600)
			case "public-witness":
				err = os.Chmod(witness, 0644)
			case "symlink-witness":
				if err = os.Remove(witness); err == nil {
					err = os.Symlink(primary, witness)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := readManagedLateResidualCleanupPair(store, parent)
			if mode == "same" {
				if err != nil || string(got) != string(raw) {
					t.Fatal("immutable pair changed", err)
				}
			} else if err == nil {
				t.Fatal("orphan/unsafe admission accepted")
			}
			if writeManagedLateResidualCleanupPair(store, parent, raw) == nil {
				t.Fatal("existing attempted grant admitted a new clock")
			}
		})
	}
}

func TestManagedLateResidualCleanupAuditPreservesPriorDescriptorWire(t *testing.T) {
	r, _, _ := managedLateResidualAuditFixture(t)
	prior := *r
	prior.ResidualCleanup, prior.ResidualAuthority = nil, nil
	if managedLateRemainingDigest(r) != managedLateRemainingDigest(&prior) {
		t.Fatal("new residual grant changed original descriptor digest")
	}
	typ, value := reflect.TypeOf(prior), reflect.ValueOf(prior)
	var fields []reflect.StructField
	var indices []int
	for i := range typ.NumField() {
		if typ.Field(i).PkgPath != "" || typ.Field(i).Tag.Get("json") == "-" {
			continue
		}
		if typ.Field(i).Name != "ResidualCleanup" && typ.Field(i).Name != "ResidualAuthority" {
			fields = append(fields, typ.Field(i))
			indices = append(indices, i)
		}
	}
	legacy := reflect.New(reflect.StructOf(fields)).Elem()
	for i, index := range indices {
		legacy.Field(i).Set(value.Field(index))
	}
	before, _ := json.Marshal(legacy.Interface())
	after, _ := json.Marshal(prior)
	if string(before) != string(after) {
		t.Fatal("new optional residual fields changed old canonical bytes")
	}
}

func managedLateResidualApprovalAuditFixture(t *testing.T) (secretStore, string, string, string, managedLateResidualProposal) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	e, late, now := managedLateRemaining389Fixture(t)
	parent, review := e.Journal.PlanSHA256, late.Review
	base := filepath.Join("deployment/create", parent, "bootstrap/service")
	write := func(relative string, value any) string {
		t.Helper()
		raw := mustProviderRecoveryJSON(value)
		path := filepath.Join(store.Root, relative)
		if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return environmentPreflightBytesSHA256(raw)
	}
	for i, key := range []string{"kubernetes/managed-late-log-retire-" + review, "kubernetes/managed-late-log-restore-" + review, "managed-late-log-verified-" + review, "openbao-host-ack-verified-isolated", "bootstrap-copy-retire-ns-a-isolated", "bootstrap-copy-retire-ns-b-isolated", "service-session-finalize", "bootstrap-renewed-cleanup-custody-" + review, "bootstrap-copy-retire-ns-a-current", "bootstrap-copy-retire-ns-b-current", "bootstrap-copy-retire-ns-c-current"} {
		at := now.Add(-3 * time.Hour).Add(time.Duration(i) * time.Second)
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: strings.Repeat("e", 64), Receipt: []byte(`{"isolated":true}`)})
	}
	e.Journal.Entries[395].State = "pending"
	e.Journal.Entries[395].CompletedAt = time.Time{}
	e.Journal.Entries[395].Receipt = nil
	journalRel := filepath.Join("deployment/create/executions", parent, "execution-isolated", "journal.json")
	e.owner.Schema = "rtk-cloud.create-owner/v1"
	e.owner.Environment = "dev"
	e.owner.Stack = e.Journal.Stack
	e.owner.Journal = journalRel
	e.owner.StartedAt = now.Add(-4 * time.Hour)
	p := managedLateResidualProposal{Schema: "rtk-cloud.residual-copy-cleanup-proposal/v1", Status: "awaiting-explicit-operator-policy-approval", CreatedAt: now.Add(-10 * time.Minute), Environment: "dev", Parent: parent, Journal: journalRel, CleanupDeadline: now.Add(-2 * time.Hour), OverallDeadline: now.Add(-time.Hour), CleanupMinutes: 60, CallerMinutes: 90, ExpiryReserveMinutes: 5, FirstReserveMinutes: 15, RequiresHuman: true, RequiresGO: true, RequiresNative: true, StaleSeedsSHA: strings.Repeat("f", 64), Historical: map[string]string{}}
	for group := range 8 {
		sid := fmt.Sprintf("%08x-1111-4111-8111-%012x", group+1, group+1)
		observation := createServiceSessionObserved{Cleanup: environmentBootstrapCleanupBinding{SessionID: sid}, UIDs: map[string]string{}, Session: []byte(fmt.Sprintf(`{"session_id":%q,"installed_subjects":[]}`, sid))}
		child := deploymentplan.Journal{PlanSHA256: fmt.Sprintf("%064x", group+100), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
		for ns := range 3 {
			namespace := fmt.Sprintf("isolated-ns-%d", ns)
			uid := fmt.Sprintf("%08x-2222-4222-8222-%012x", group*3+ns+100, group*3+ns+100)
			id := "Secret/" + namespace + "/service-bootstrap-" + sid
			observation.UIDs[namespace] = uid
			child.Owners[id] = uid
			child.Completed = append(child.Completed, id)
		}
		observationRel := filepath.Join(base, "session-renewal", fmt.Sprintf("%064x", group+10), "observed.json")
		observationSHA := write(observationRel, observation)
		childRel := filepath.Join("deployment/rollback", child.PlanSHA256, "journal.json")
		childSHA := write(childRel, child)
		stage := fmt.Sprintf("kubernetes/isolated-credential-%d", group)
		e.Journal.Entries[group] = environmentCreateJournalEntry{Key: stage, Phase: strings.TrimPrefix(stage, "kubernetes/"), State: "completed", StartedAt: now.Add(-4 * time.Hour), CompletedAt: now.Add(-3 * time.Hour), IntentSHA256: strings.Repeat("e", 64), Receipt: mustProviderRecoveryJSON(map[string]any{"plan_sha256": child.PlanSHA256, "owners": child.Owners})}
		for ns, uid := range observation.UIDs {
			object := managedLateResidualProposalObject{ResourceID: "Secret/" + ns + "/service-bootstrap-" + sid, UID: uid, SessionID: sid, Namespace: ns, Observation: observationRel, ObservationSHA: observationSHA}
			object.Proofs = append(object.Proofs, struct {
				Stage      string `json:"parent_stage"`
				Plan       string `json:"plan_sha256"`
				Journal    string `json:"journal"`
				JournalSHA string `json:"journal_raw_sha256"`
			}{stage, child.PlanSHA256, childRel, childSHA})
			p.Objects = append(p.Objects, object)
		}
	}
	sort.Slice(p.Objects, func(i, j int) bool { return p.Objects[i].ResourceID < p.Objects[j].ResourceID })
	p.Source = deploymentplan.Digest(e.Journal)
	p.JournalRawSHA = write(filepath.Join(base, "managed-late-residual-copy-cleanup-source-journal.json"), e.Journal)
	p.OwnerRawSHA = write(filepath.Join(base, "managed-late-residual-copy-cleanup-source-owner.json"), e.owner)
	post := createManagedLatePostNativeCleanupAuthority{ParentSHA: parent, SourceSHA: p.Source, OwnerSHA: deploymentplan.Digest(e.owner), Review: strings.Repeat("d", 64), Deadline: p.CleanupDeadline}
	p.PostSHA = write(filepath.Join(base, "managed-late-post-native-cleanup-authority.json"), post)
	write(filepath.Join(base, "managed-late-post-native-cleanup-admission.json"), post)
	term := createManagedLateTerminalFinalizerAuthority{ParentSHA: parent, OverallDeadline: p.OverallDeadline}
	p.TerminalSHA = write(filepath.Join(base, "managed-late-terminal-finalizer-authority.json"), term)
	write(filepath.Join(base, "managed-late-terminal-finalizer-admission.json"), term)
	for _, name := range []string{"managed-late-remaining-authority.json", "managed-late-installed-cleanup-authority.json", "managed-late-finalization-authority.json"} {
		p.Historical[name] = write(filepath.Join(base, name), map[string]string{"isolated": name})
	}
	p.Remaining = filepath.Join("deployment/create/openbao-recovery", post.Review, managedLateRemainingFilename)
	p.RemainingSHA = write(p.Remaining, createManagedLateRemaining{ParentSHA: parent, StaleSeedsSHA: p.StaleSeedsSHA, PostNativeCleanup: &createManagedLatePostNativeCleanup{SourceSHA: p.Source}})
	p.FiveACKProof = p.Remaining + "#/HistoricalSeedCleanup"
	p.FailureReport = "deployment/reports/dev-device-trust-continuation-deployment.json"
	p.FailureReportSHA = write(filepath.Join(base, "managed-late-residual-copy-cleanup-failed-run.json"), map[string]any{"completed": false, "decision": "NO-GO", "parent_plan_sha256": parent})
	p.FailureDiagnostic = "deployment/reports/isolated-fixed-window.json"
	p.FailureDiagnosticSHA = write(p.FailureDiagnostic, map[string]any{"authority": false, "parent_sha256": parent, "failed_report_raw_sha256": p.FailureReportSHA, "fixed_cleanup_deadline": p.CleanupDeadline.Format(time.RFC3339Nano), "journal_entries": 400})
	proposalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-proposal-60m.json")
	proposalSHA := write(proposalRel, p)
	approvalRel := filepath.Join(base, "managed-late-residual-copy-cleanup-operator-approval.json")
	write(approvalRel, managedLateResidualApprovalRecord{Schema: "rtk-cloud.residual-copy-cleanup-operator-approval/v1", Environment: "dev", Parent: parent, Source: p.Source, Proposal: proposalSHA, Decision: "approved", ApprovedAt: now.Add(-time.Minute), HumanMessage: "yes, exact isolated proposal", CleanupMinutes: 60, CallerMinutes: 90})
	return store, parent, proposalRel, approvalRel, p
}

func TestManagedLateResidualCleanupAuditApprovalAuthenticatesImmutableSourceAndAll24(t *testing.T) {
	for _, mode := range []string{"approved", "legacy-omitted-unused-installed", "legacy-omitted-nonzero-installed", "explicit-null-installed", "source-snapshot", "owner-snapshot", "missing-post-witness", "changed-child-journal", "changed-observation", "proposal-bytes", "public-approval", "symlink-proposal", "foreign-stage", "changed-valid-uid", "missing-object", "path-traversal"} {
		t.Run(mode, func(t *testing.T) {
			store, parent, proposalRel, approvalRel, p := managedLateResidualApprovalAuditFixture(t)
			base := filepath.Join("deployment/create", parent, "bootstrap/service")
			rewriteProposal := false
			var err error
			switch mode {
			case "legacy-omitted-unused-installed", "legacy-omitted-nonzero-installed", "explicit-null-installed":
				observationRel := p.Objects[0].Observation
				var observed createServiceSessionObserved
				raw, readErr := os.ReadFile(filepath.Join(store.Root, observationRel))
				if readErr != nil || json.Unmarshal(raw, &observed) != nil {
					t.Fatal("fixture observation unavailable", readErr)
				}
				state := map[string]any{"session_id": p.Objects[0].SessionID}
				if mode == "explicit-null-installed" {
					state["installed_subjects"] = nil
				}
				observed.Session = mustProviderRecoveryJSON(state)
				raw = mustProviderRecoveryJSON(observed)
				err = os.WriteFile(filepath.Join(store.Root, observationRel), raw, 0600)
				for i := range p.Objects {
					if p.Objects[i].Observation == observationRel {
						p.Objects[i].ObservationSHA = environmentPreflightBytesSHA256(raw)
					}
				}
				if mode == "legacy-omitted-nonzero-installed" {
					p.Objects[0].Installed = 1
				}
				rewriteProposal = true
			case "source-snapshot":
				err = os.WriteFile(filepath.Join(store.Root, base, "managed-late-residual-copy-cleanup-source-journal.json"), []byte(`{"changed":true}`), 0600)
			case "owner-snapshot":
				err = os.WriteFile(filepath.Join(store.Root, base, "managed-late-residual-copy-cleanup-source-owner.json"), []byte(`{"changed":true}`), 0600)
			case "missing-post-witness":
				err = os.Remove(filepath.Join(store.Root, base, "managed-late-post-native-cleanup-admission.json"))
			case "changed-child-journal":
				err = os.WriteFile(filepath.Join(store.Root, p.Objects[0].Proofs[0].Journal), []byte(`{"changed":true}`), 0600)
			case "changed-observation":
				err = os.WriteFile(filepath.Join(store.Root, p.Objects[0].Observation), []byte(`{"changed":true}`), 0600)
			case "proposal-bytes":
				raw := append(mustProviderRecoveryJSON(p), '\n')
				err = os.WriteFile(filepath.Join(store.Root, proposalRel), raw, 0600)
			case "public-approval":
				err = os.Chmod(filepath.Join(store.Root, approvalRel), 0644)
			case "symlink-proposal":
				err = os.Rename(filepath.Join(store.Root, proposalRel), filepath.Join(store.Root, proposalRel+".original"))
				if err == nil {
					err = os.Symlink(proposalRel+".original", filepath.Join(store.Root, proposalRel))
				}
			case "foreign-stage":
				p.Objects[0].Proofs[0].Stage = "kubernetes/foreign-accepted-stage"
				rewriteProposal = true
			case "changed-valid-uid":
				p.Objects[0].UID = "ffffffff-1111-4111-8111-111111111111"
				rewriteProposal = true
			case "missing-object":
				p.Objects = p.Objects[:23]
				rewriteProposal = true
			case "path-traversal":
				p.Objects[0].Observation = "../outside/observed.json"
				rewriteProposal = true
			}
			if err != nil {
				t.Fatal(err)
			}
			if rewriteProposal {
				raw := mustProviderRecoveryJSON(p)
				if err := os.WriteFile(filepath.Join(store.Root, proposalRel), raw, 0600); err != nil {
					t.Fatal(err)
				}
				var approval managedLateResidualApprovalRecord
				raw, err := os.ReadFile(filepath.Join(store.Root, approvalRel))
				if err != nil || json.Unmarshal(raw, &approval) != nil {
					t.Fatal("fixture approval unavailable", err)
				}
				approval.Proposal = environmentPreflightBytesSHA256(mustProviderRecoveryJSON(p))
				if err := os.WriteFile(filepath.Join(store.Root, approvalRel), mustProviderRecoveryJSON(approval), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadManagedLateResidualCleanupApproval(store, parent)
			if mode == "approved" || mode == "legacy-omitted-unused-installed" {
				if err != nil || got == nil || len(got.Objects) != 24 || got.InventorySHA != deploymentplan.Digest(got.Objects) {
					t.Fatal("exact approved synthetic source refused", err)
				}
			} else if err == nil {
				t.Fatal("unproved source/inventory/operator approval accepted")
			}
		})
	}
}

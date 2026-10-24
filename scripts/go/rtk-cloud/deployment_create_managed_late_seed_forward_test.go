package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func lateSeedForwardWrite(t *testing.T, store secretStore, path string, value any) {
	t.Helper()
	full := filepath.Join(store.Root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, mustProviderRecoveryJSON(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func lateSeedForwardSixGO(plan createFoundationRecoveryPlan, at time.Time) createFoundationRecoveryArtifact {
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: plan, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
	review := deploymentplan.Digest(plan)
	a.Qualification.PlanSHA256 = review
	a.Qualification.Decision = "GO"
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	return a
}
func lateSeedForwardAdmission(a createFoundationRecoveryArtifact, at time.Time) environmentCreateJournalEntry {
	review := deploymentplan.Digest(a.Plan)
	return environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review, "audit_sha256": strings.Repeat("a", 64)})}
}
func lateSeedForwardFrameFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedLateSessionRecovery, *createManagedLateSeedForward) {
	t.Helper()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private root")
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store := secretStore{ConfigRoot: root, Root: filepath.Join(root, "dev"), Environment: "dev"}
	if os.Mkdir(store.Root, 0700) != nil {
		t.Fatal("private selected env")
	}
	f := managedLateSeedForwardFixture(t)
	source := f.OriginalSource
	source.OwnerToken = "isolated-original-owner"
	f.OriginalSource = source
	owner := environmentCreateOwner{PlanSHA256: source.PlanSHA256, OwnerToken: source.OwnerToken}
	at := time.Now().UTC().Add(-time.Hour)
	original := lateSeedForwardSixGO(createFoundationRecoveryPlan{Schema: managedLateSessionSchema, ParentSHA: source.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(owner), ParentJournalSHA: deploymentplan.Digest(source)}, at)
	f.OriginalReview = deploymentplan.Digest(original.Plan)
	f.OriginalArtifactSHA = deploymentplan.Digest(original)
	f.SourceOwnerSHA = deploymentplan.Digest(owner)
	current := source
	current.Entries = []environmentCreateJournalEntry{lateSeedForwardAdmission(original, at.Add(time.Second)), {Key: "original-authorization", State: "completed", StartedAt: at.Add(3 * time.Second), CompletedAt: at.Add(4 * time.Second), Receipt: json.RawMessage(`{"real_test_receipt":true}`)}, {Key: "original-policy", State: "completed", StartedAt: at.Add(3 * time.Second), CompletedAt: at.Add(4 * time.Second), Receipt: json.RawMessage(`{"real_test_receipt":true}`)}, {Key: "original-role", State: "completed", StartedAt: at.Add(3 * time.Second), CompletedAt: at.Add(4 * time.Second), Receipt: json.RawMessage(`{"real_test_receipt":true}`)}}
	intent, err := environmentCreateKubernetesStageIntent(f.FailedPlan)
	if err != nil {
		t.Fatal(err)
	}
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "kubernetes/service-session-original-key-renewal-" + f.OriginalReview, Phase: "service-session-original-key-renewal-" + f.OriginalReview, State: "pending", StartedAt: at.Add(5 * time.Second), IntentSHA256: activeTransportJournalDigest(jsonRawObject(intent))})
	current.KubernetesLeases = []deploymentplan.LeaseOwner{{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: strings.Repeat("b", 64) + ":old-native"}, {ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "22222222-2222-4222-8222-222222222222", Holder: f.FailedPlanSHA + ":old-failed"}}
	f.SourceSHA = deploymentplan.Digest(current)
	f.Candidate = managedLateSeedForwardFixtureCandidate(t, f)
	fresh := lateSeedForwardSixGO(createFoundationRecoveryPlan{Schema: managedLateSeedForwardSchema, ParentSHA: source.PlanSHA256, ParentOwnerSHA: f.SourceOwnerSHA, ParentJournalSHA: f.SourceSHA, Materials: map[string]string{managedLateSeedForwardMaterial + "descriptor": managedLateSeedForwardSourceDigest(f), managedLateSeedForwardMaterial + "candidate": f.Candidate.SHA256()}}, at.Add(10*time.Minute))
	f.Review = deploymentplan.Digest(fresh.Plan)
	e := &environmentCreateExecution{Store: store, owner: owner, Journal: current}
	base := "deployment/create/openbao-recovery/" + f.OriginalReview
	lateSeedForwardWrite(t, store, base+"/qualification.json", original)
	lateSeedForwardWrite(t, store, base+"/source-journal.json", source)
	base = "deployment/create/openbao-recovery/" + f.Review
	lateSeedForwardWrite(t, store, base+"/qualification.json", fresh)
	lateSeedForwardWrite(t, store, base+"/source-journal.json", current)
	lateSeedForwardWrite(t, store, base+"/"+managedLateSeedForwardSourceFilename, f)
	lateSeedForwardWrite(t, store, "deployment/rollback/"+f.FailedPlanSHA+"/plan.json", f.FailedPlan)
	lateSeedForwardWrite(t, store, "deployment/rollback/"+f.FailedPlanSHA+"/journal.json", f.FailedJournal)
	e.Journal.Entries = append(e.Journal.Entries, lateSeedForwardAdmission(fresh, at.Add(10*time.Minute+time.Second)))
	defs, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		t.Fatal(err)
	}
	inputs := f.FailedPlan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-seed-trust-forward-"+f.Review, defs)
	actual, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	actualUID := "33333333-3333-4333-8333-333333333333"
	actualJournal := deploymentplan.Journal{PlanSHA256: actual.SHA256(), Completed: []string{actual.Resources[0].ID}, Owners: map[string]string{actual.Resources[0].ID: actualUID}, Accepted: true, AcceptanceStarted: true}
	intent, err = environmentCreateKubernetesStageIntent(actual)
	if err != nil {
		t.Fatal(err)
	}
	stage := "managed-late-seed-trust-forward-" + f.Review
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "completed", StartedAt: at.Add(10*time.Minute + 3*time.Second), CompletedAt: at.Add(10*time.Minute + 4*time.Second), IntentSHA256: activeTransportJournalDigest(jsonRawObject(intent)), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": actual.SHA256(), "owners": actualJournal.Owners})})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "44444444-4444-4444-8444-444444444444", Holder: actual.SHA256() + ":fresh-forward"})
	e.Journal.Entries[4].State = "completed"
	e.Journal.Entries[4].CompletedAt = at.Add(10*time.Minute + 5*time.Second)
	e.Journal.Entries[4].Receipt = mustProviderRecoveryJSON(managedLateSeedForwardAlias(f, actual))
	lateSeedForwardWrite(t, store, "deployment/rollback/"+actual.SHA256()+"/plan.json", actual)
	lateSeedForwardWrite(t, store, "deployment/rollback/"+actual.SHA256()+"/journal.json", actualJournal)
	late := &createManagedLateSessionRecovery{Review: f.OriginalReview, SeedCandidate: f.FailedPlan}
	return store, e, late, f
}

func TestManagedLateSeedForwardFrameKeepsOrdinaryCustodyUnchanged(t *testing.T) {
	source := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "source"}}}
	e := &environmentCreateExecution{Journal: source}
	for i := 0; i < 5; i++ {
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "old-step"})
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/service-custody-reader-renewal-" + strings.Repeat("a", 64)})
	f, entries, leases, err := managedLateSeedForwardFrame(secretStore{}, e, &createManagedLateSessionRecovery{}, source)
	if err != nil || f != nil || deploymentplan.Digest(entries) != deploymentplan.Digest(e.Journal.Entries) || deploymentplan.Digest(leases) != deploymentplan.Digest(e.Journal.KubernetesLeases) {
		t.Fatal("ordinary no-forward source changed", err)
	}
}
func TestManagedLateSeedForwardFrameAndEffectiveSeedPreserveFailedHistory(t *testing.T) {
	store, e, late, f := lateSeedForwardFrameFixture(t)
	loaded, entries, leases, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource)
	if err != nil || loaded == nil || len(entries) != 5 || len(leases) != 2 {
		t.Fatal("accepted forward frame", err)
	}
	plan, entry, journal, err := readManagedLateEffectiveSeed(store, e, late)
	if err != nil || !managedLateSeedForwardActualCandidate(f, plan) || !journal.Accepted || entry.Key != "kubernetes/service-session-original-key-renewal-"+f.OriginalReview {
		t.Fatal("effective genuine corrected seed", err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/service-custody-reader-renewal-" + f.OriginalReview, State: "pending"})
	if _, entries, _, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err != nil || len(entries) != 6 || entries[5].Key != e.Journal.Entries[7].Key {
		t.Fatal("downstream exact stage lost", err)
	}
}
func TestManagedLateSeedForwardFrameRejectsDriftAndDoesNotPanic(t *testing.T) {
	for _, name := range []string{"changed failed journal", "removed old lease", "wrong forward lease holder", "wrong forward lease UID", "wrong forward lease resource", "changed alias", "foreign next admission", "unknown forward stage", "changed original intent", "changed original start", "changed original phase", "missing real accepted child", "wrong actual bindings", "missing admission audit", "extra admission receipt", "wrong admission audit", "zero admission start", "zero admission completion", "changed admission intent"} {
		t.Run(name, func(t *testing.T) {
			store, e, late, f := lateSeedForwardFrameFixture(t)
			switch name {
			case "missing admission audit":
				e.Journal.Entries[5].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": f.Review})
			case "extra admission receipt":
				e.Journal.Entries[5].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": f.Review, "audit_sha256": strings.Repeat("a", 64), "foreign": "extra"})
			case "wrong admission audit":
				e.Journal.Entries[5].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": f.Review, "audit_sha256": "bad"})
			case "zero admission start":
				e.Journal.Entries[5].StartedAt = time.Time{}
			case "zero admission completion":
				e.Journal.Entries[5].CompletedAt = time.Time{}
			case "changed admission intent":
				e.Journal.Entries[5].IntentSHA256 = strings.Repeat("f", 64)
			case "changed failed journal":
				changed := f.FailedJournal
				changed.Accepted = true
				lateSeedForwardWrite(t, store, "deployment/rollback/"+f.FailedPlanSHA+"/journal.json", changed)
			case "removed old lease":
				e.Journal.KubernetesLeases = e.Journal.KubernetesLeases[:1]
			case "wrong forward lease holder":
				e.Journal.KubernetesLeases[2].Holder = strings.Repeat("f", 64) + ":foreign"
			case "wrong forward lease UID":
				e.Journal.KubernetesLeases[2].UID = "bad"
			case "wrong forward lease resource":
				e.Journal.KubernetesLeases[2].ResourceID = "Lease/foreign/other"
			case "changed alias":
				e.Journal.Entries[4].Receipt = json.RawMessage(`{"original_plan_sha256":"foreign"}`)
			case "foreign next admission":
				e.Journal.Entries[5].Key = "openbao-recovery-admission-" + strings.Repeat("f", 64)
			case "unknown forward stage":
				e.Journal.Entries[6].Key = "kubernetes/unknown"
			case "changed original intent":
				e.Journal.Entries[4].IntentSHA256 = strings.Repeat("f", 64)
			case "changed original start":
				e.Journal.Entries[4].StartedAt = e.Journal.Entries[4].StartedAt.Add(time.Second)
			case "changed original phase":
				e.Journal.Entries[4].Phase = "foreign"
			case "missing real accepted child":
				e.Journal.Entries = e.Journal.Entries[:6]
				e.Journal.KubernetesLeases = e.Journal.KubernetesLeases[:2]
			case "wrong actual bindings":
				plan, _, _, err := readManagedLateEffectiveSeed(store, e, late)
				if err != nil {
					t.Fatal(err)
				}
				plan.BindingsSHA256 = strings.Repeat("f", 64)
				if managedLateSeedForwardActualCandidate(f, plan) {
					t.Fatal("foreign binding admitted")
				}
				return
			}
			if _, _, _, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err == nil {
				t.Fatal("changed original/forward authority admitted")
			}
		})
	}
}

// Completed-owner readers normalize only the final parent's completion scalar,
// after authenticating its archive. The stopped child and all source metadata
// still have to reproduce the immutable admitted frame exactly.
func TestManagedLateSeedForwardCompletedParentBoundedFrame(t *testing.T) {
	store, e, late, f := lateSeedForwardFrameFixture(t)
	before := deploymentplan.Digest(e.Journal)
	e.Journal.CompletedAt = e.Journal.Entries[4].CompletedAt.Add(time.Minute)
	if _, _, _, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err == nil {
		t.Fatal("un-normalized completed parent changed the stopped source")
	}
	bounded := *e
	bounded.Journal = e.Journal
	bounded.Journal.CompletedAt = f.OriginalSource.CompletedAt
	plan, _, journal, err := readManagedLateEffectiveSeed(store, &bounded, late)
	if err != nil || !journal.Accepted || !managedLateSeedForwardActualCandidate(f, plan) || deploymentplan.Digest(bounded.Journal) != before || e.Journal.CompletedAt.IsZero() {
		t.Fatal("authenticated completed-parent frame lost the genuine seed", err)
	}
	bounded.Journal.OwnerToken = "foreign-owner"
	if _, _, _, err := managedLateSeedForwardFrame(store, &bounded, late, f.OriginalSource); err == nil {
		t.Fatal("completion normalization admitted changed source metadata")
	}
}

func TestManagedLateSeedForwardFrameBeforeAndDuringRealChild(t *testing.T) {
	store, e, late, f := lateSeedForwardFrameFixture(t)
	accepted := e.Journal
	var stopped environmentCreateJournal
	if err := createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", f.Review, "source-journal.json"), &stopped); err != nil {
		t.Fatal(err)
	}
	e.Journal = stopped
	if got, entries, leases, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err != nil || got != nil || deploymentplan.Digest(entries) != deploymentplan.Digest(stopped.Entries) || deploymentplan.Digest(leases) != deploymentplan.Digest(stopped.KubernetesLeases) {
		t.Fatal("unchanged authentic stopped seed changed", err)
	}
	e.Journal.Entries = append(e.Journal.Entries, accepted.Entries[5])
	if got, entries, leases, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err != nil || got == nil || len(entries) != 5 || len(leases) != 2 || entries[4].State != "pending" {
		t.Fatal("fresh admission alone lost the actual pending seed", err)
	}
	forward := accepted.Entries[6]
	var receipt struct {
		Plan   string            `json:"plan_sha256"`
		Owners map[string]string `json:"owners"`
	}
	if err := decodeEnvironmentJSON(forward.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	var actual deploymentplan.Plan
	var journal deploymentplan.Journal
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", receipt.Plan, "plan.json"), &actual) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", receipt.Plan, "journal.json"), &journal) != nil {
		t.Fatal("allocated actual child missing")
	}
	journal.Accepted, journal.AcceptanceStarted = false, false
	journal.Completed = nil
	lateSeedForwardWrite(t, store, "deployment/rollback/"+actual.SHA256()+"/journal.json", journal)
	forward.State, forward.Receipt, forward.CompletedAt = "pending", nil, time.Time{}
	e.Journal.Entries = append(e.Journal.Entries, forward)
	e.Journal.KubernetesLeases = accepted.KubernetesLeases
	if got, entries, leases, err := managedLateSeedForwardFrame(store, e, late, f.OriginalSource); err != nil || got == nil || len(entries) != 5 || len(leases) != 2 || entries[4].State != "pending" {
		t.Fatal("genuinely allocated pending child cannot fake the original alias", err)
	}
	if _, _, _, err := readManagedLateEffectiveSeed(store, e, late); err == nil {
		t.Fatal("unaccepted child granted effective seed authority")
	}
}

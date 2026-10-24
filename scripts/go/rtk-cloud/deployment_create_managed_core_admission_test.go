package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func managedCorePendingArtifactFixture(t *testing.T) (createFoundationRecoveryArtifact, environmentCreateJournal, environmentCreateJournalEntry, *createManagedCorePendingAdmission) {
	t.Helper()
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	oldReview := strings.Repeat("a", 64)
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("b", 64), OwnerToken: "isolated-original-owner"}
	for i := 0; i < 8; i++ {
		source.Entries = append(source.Entries, environmentCreateJournalEntry{Key: "original-" + string(rune('a'+i)), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("c", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"original": "isolated"})})
	}
	source.Entries[0].Key = "openbao-audit-enable"
	source.Entries[0].Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64)})
	keys := []string{"openbao-recovery-admission-" + oldReview, "kubernetes/openbao-host-ack-forward-" + oldReview, "openbao-managed-unseal-11111111-1111-4111-8111-111111111111", "kubernetes/managed-application-owners-forward-" + oldReview, "kubernetes/native-app-routing-profile", "native-app-routing-profile-proof", "kubernetes/managed-complete-issuer"}
	phases := []string{"recovery", "openbao-host-ack-forward-" + oldReview, "openbao-unseal", "managed-application-owners-forward-" + oldReview, "native-app-routing-profile", "bootstrap", "managed-complete-issuer"}
	for i, key := range keys {
		entry := environmentCreateJournalEntry{Key: key, Phase: phases[i], State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("e", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"isolated": "receipt"})}
		if i == 6 {
			entry.State = "pending"
			entry.CompletedAt = time.Time{}
			entry.Receipt = nil
		}
		source.Entries = append(source.Entries, entry)
	}
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedCorePartialSchema, ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), CheckerSHA: strings.Repeat("f", 64)}, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "pending", StartedAt: at.Add(time.Minute), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA})}
	current := source
	current.Entries = append(slices.Clone(source.Entries), entry)
	prior := &createManagedCorePendingAdmission{Review: review, SourceSHA: deploymentplan.Digest(current), StoppedSourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), CheckerSHA: a.Plan.CheckerSHA, AuditSHA: strings.Repeat("d", 64), Entry: entry}
	return a, current, entry, prior
}
func TestManagedCorePendingAdmissionRequiresOriginalSixGOAndOriginalWindow(t *testing.T) {
	a, current, entry, prior := managedCorePendingArtifactFixture(t)
	source := current
	source.Entries = current.Entries[:len(current.Entries)-1]
	if !managedCorePendingAdmissionArtifactValid(a, entry, source, prior.Review) {
		t.Fatal("genuine isolated historical six-GO admission rejected")
	}
	for _, mode := range []string{"schema", "mode", "checker", "decision", "gate-status", "gate-order", "gate-evidence", "gate-required", "extra-gate", "extended-window", "before-window", "after-window", "exact-expiry", "intent", "parent", "completed", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			var changed createFoundationRecoveryArtifact
			json.Unmarshal(mustProviderRecoveryJSON(a), &changed)
			v := entry
			switch mode {
			case "schema":
				changed.Schema = "foreign"
			case "mode":
				changed.Plan.Schema = "foreign"
			case "checker":
				changed.Plan.CheckerSHA = ""
			case "decision":
				changed.Qualification.Decision = "NO-GO"
			case "gate-status":
				changed.Qualification.Gates[0].Status = "FAIL"
			case "gate-order":
				changed.Qualification.Gates[0], changed.Qualification.Gates[1] = changed.Qualification.Gates[1], changed.Qualification.Gates[0]
			case "gate-evidence":
				changed.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("0", 64)
			case "gate-required":
				changed.Qualification.Gates[0].Required = false
			case "extra-gate":
				changed.Qualification.Gates = append(changed.Qualification.Gates, changed.Qualification.Gates[0])
			case "extended-window":
				changed.ValidUntil = changed.ValidUntil.Add(time.Minute)
			case "before-window":
				v.StartedAt = a.QualifiedAt.Add(-time.Nanosecond)
			case "after-window":
				v.StartedAt = a.ValidUntil.Add(time.Second)
			case "exact-expiry":
				v.StartedAt = a.ValidUntil
			case "intent":
				v.IntentSHA256 = strings.Repeat("0", 64)
			case "parent":
				changed.Plan.ParentSHA = strings.Repeat("0", 64)
			case "completed":
				v.State = "completed"
				v.CompletedAt = v.StartedAt
			case "receipt":
				v.Receipt = mustProviderRecoveryJSON(map[string]string{"fake": "receipt"})
			}
			if managedCorePendingAdmissionArtifactValid(changed, v, source, prior.Review) {
				t.Fatal("unqualified historical admission accepted", mode)
			}
		})
	}
}
func TestManagedCorePendingAdmissionSelectorBoundedToOneActualStoppedAdmission(t *testing.T) {
	_, current, _, prior := managedCorePendingArtifactFixture(t)
	if managedCorePendingAdmissionReview(current) != prior.Review || managedCorePartialReview(current) != strings.Repeat("a", 64) {
		t.Fatal("strict original Core lineage not selected")
	}
	for _, mode := range []string{"extra-tail", "receipt", "phase", "completed", "no-start", "replaced-core", "completed-core", "empty"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(current), &changed)
			last := len(changed.Entries) - 1
			switch mode {
			case "extra-tail":
				changed.Entries = append(changed.Entries, changed.Entries[last])
			case "receipt":
				changed.Entries[last].Receipt = mustProviderRecoveryJSON(map[string]string{"fake": "receipt"})
			case "phase":
				changed.Entries[last].Phase = "bootstrap"
			case "completed":
				changed.Entries[last].State = "completed"
				changed.Entries[last].CompletedAt = time.Now()
			case "no-start":
				changed.Entries[last].StartedAt = time.Time{}
			case "replaced-core":
				changed.Entries[last-1].Key = "foreign"
			case "completed-core":
				changed.Entries[last-1].State = "completed"
			case "empty":
				changed.Entries = nil
			}
			if managedCorePendingAdmissionReview(changed) != "" {
				t.Fatal("unbounded or unrelated pending checkpoint selected", mode)
			}
		})
	}
}
func TestManagedCorePendingAdmissionClosureAllowsOnlyActualIntendedAuditReceipt(t *testing.T) {
	_, source, _, prior := managedCorePendingArtifactFixture(t)
	if !managedCorePendingAdmissionPrefixMatches(source, source, prior) {
		t.Fatal("immutable pending source rejected")
	}
	completed := source
	completed.Entries = slices.Clone(source.Entries)
	last := len(source.Entries) - 1
	completed.Entries[last].State = "completed"
	completed.Entries[last].CompletedAt = prior.Entry.StartedAt.Add(time.Hour)
	completed.Entries[last].Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": prior.AuditSHA, "qualified_recovery_sha256": prior.Review})
	if !managedCorePendingAdmissionPrefixMatches(source, completed, prior) {
		t.Fatal("real intended observation completion rejected")
	}
	for _, mode := range []string{"audit", "review", "extra-field", "intent", "started", "key", "phase", "before-start", "owner", "extra-entry", "fake-core-close", "source-pin"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(completed), &changed)
			p := *prior
			receipt := map[string]string{"audit_sha256": prior.AuditSHA, "qualified_recovery_sha256": prior.Review}
			switch mode {
			case "audit":
				receipt["audit_sha256"] = strings.Repeat("0", 64)
			case "review":
				receipt["qualified_recovery_sha256"] = strings.Repeat("0", 64)
			case "extra-field":
				receipt["fake"] = "accepted"
			case "intent":
				changed.Entries[last].IntentSHA256 = strings.Repeat("0", 64)
			case "started":
				changed.Entries[last].StartedAt = changed.Entries[last].StartedAt.Add(time.Second)
			case "key":
				changed.Entries[last].Key = "foreign"
			case "phase":
				changed.Entries[last].Phase = "foreign"
			case "before-start":
				changed.Entries[last].CompletedAt = prior.Entry.StartedAt.Add(-time.Second)
			case "owner":
				changed.OwnerToken = "replacement"
			case "extra-entry":
				changed.Entries = append(changed.Entries, changed.Entries[last])
			case "fake-core-close":
				changed.Entries[last-1].State = "completed"
				changed.Entries[last-1].CompletedAt = time.Now()
			case "source-pin":
				p.SourceSHA = strings.Repeat("0", 64)
			}
			if mode == "audit" || mode == "review" || mode == "extra-field" {
				changed.Entries[last].Receipt = mustProviderRecoveryJSON(receipt)
			}
			if managedCorePendingAdmissionPrefixMatches(source, changed, &p) {
				t.Fatal("unqualified old admission mutation accepted", mode)
			}
		})
	}
}

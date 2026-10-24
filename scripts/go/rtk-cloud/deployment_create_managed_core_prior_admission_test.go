package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func corePriorAdmissionFixture() (environmentCreateJournal, *createManagedCorePartialRecovery, createFoundationRecoveryArtifact, []byte, time.Time) {
	now := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	audit := []byte("isolated original native audit readback")
	entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + strings.Repeat("b", 64), Phase: "recovery", State: "pending", StartedAt: now.Add(-time.Hour), IntentSHA256: strings.Repeat("c", 64)}
	j := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64), Entries: []environmentCreateJournalEntry{entry}}
	prior := &createManagedCorePendingAdmission{Review: strings.Repeat("b", 64), StoppedSourceSHA: strings.Repeat("d", 64), ArtifactSHA: strings.Repeat("e", 64), CheckerSHA: strings.Repeat("f", 64), CandidateSHA: strings.Repeat("1", 64), AuditSHA: environmentPreflightBytesSHA256(audit), Entry: entry, SourceSHA: deploymentplan.Digest(j)}
	core := &createManagedCorePartialRecovery{Action: managedCoreRestoreOnly, OriginalRepairReview: strings.Repeat("2", 64), SourceSHA: prior.SourceSHA, PriorAdmission: prior}
	p := createFoundationRecoveryPlan{Schema: managedCorePartialSchema, ParentJournalSHA: core.SourceSHA, Materials: map[string]string{"accepted-service-core-complete-issuer-partial-prior-admission": deploymentplan.Digest(prior), "accepted-service-core-complete-issuer-partial-forward-plan": prior.CandidateSHA, "accepted-service-core-complete-issuer-partial-action": deploymentplan.Digest(managedCoreRestoreOnly), "accepted-service-core-managed-complete-observation-action": deploymentplan.Digest(managedCoreRestoreOnly)}}
	review := deploymentplan.Digest(p)
	core.ForwardReview = review
	a := createFoundationRecoveryArtifact{Plan: p, Qualification: deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	return j, core, a, audit, now
}

func TestManagedCorePriorAdmissionRequiresFreshGOAndActualOriginalAudit(t *testing.T) {
	for _, mode := range []string{"original", "extra-entry", "changed-intent", "changed-start", "already-completed", "pending-receipt", "native-audit-drift", "empty-audit", "old-review-as-fresh", "source-drift", "foreign-schema", "candidate-drift", "prior-material-drift", "finalization-action", "gate-skip", "gate-evidence", "foreign-gate", "missing-gate", "expired-GO", "future-GO", "extended-window", "plan-body-drift"} {
		t.Run(mode, func(t *testing.T) {
			j, core, a, audit, now := corePriorAdmissionFixture()
			prior := core.PriorAdmission
			originalEntry := deploymentplan.Digest(prior.Entry)
			switch mode {
			case "extra-entry":
				j.Entries = append(j.Entries, j.Entries[0])
			case "changed-intent":
				j.Entries[0].IntentSHA256 = strings.Repeat("3", 64)
			case "changed-start":
				j.Entries[0].StartedAt = j.Entries[0].StartedAt.Add(time.Second)
			case "already-completed":
				j.Entries[0].State = "completed"
				j.Entries[0].CompletedAt = now
			case "pending-receipt":
				j.Entries[0].Receipt = []byte("{}")
			case "native-audit-drift":
				audit = []byte("different native audit")
			case "empty-audit":
				audit = nil
			case "old-review-as-fresh":
				core.ForwardReview = prior.Review
			case "source-drift":
				core.SourceSHA = strings.Repeat("4", 64)
			case "foreign-schema":
				a.Plan.Schema = "other"
			case "candidate-drift":
				a.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] = strings.Repeat("5", 64)
			case "prior-material-drift":
				a.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] = strings.Repeat("6", 64)
			case "finalization-action":
				core.Action = managedCoreFinalizeComplete
			case "gate-skip":
				a.Qualification.Gates[3].Status = "SKIPPED"
			case "gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = prior.Review
			case "foreign-gate":
				a.Qualification.Gates[3].ID = "other"
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "expired-GO":
				now = a.ValidUntil
			case "future-GO":
				now = a.QualifiedAt.Add(-time.Second)
			case "extended-window":
				a.ValidUntil = a.ValidUntil.Add(time.Second)
			case "plan-body-drift":
				a.Plan.ParentSHA = "other"
			}
			key, proof, err := managedCorePriorAdmissionProof(j, core, a, audit, now)
			if (err == nil) != (mode == "original") {
				t.Fatal(mode, err)
			}
			if deploymentplan.Digest(prior.Entry) != originalEntry {
				t.Fatal("verification changed original pending history")
			}
			if mode == "original" && (key != prior.Entry.Key || len(proof) != 2 || proof["audit_sha256"] != prior.AuditSHA || proof["qualified_recovery_sha256"] != prior.Review) {
				t.Fatal("receipt did not preserve the actual old admission authority")
			}
		})
	}
}

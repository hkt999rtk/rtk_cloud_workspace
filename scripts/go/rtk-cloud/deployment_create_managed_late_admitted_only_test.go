package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateExpiredAdmittedOnlyFixture(t *testing.T) (*environmentCreateExecution, environmentCreateJournal, createFoundationRecoveryArtifact, *createManagedLateSessionRecovery, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	parent := strings.Repeat("a", 64)
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-original-owner", AdmittedAt: now.Add(-3 * time.Hour)}
	for i := range 374 {
		at := now.Add(-2 * time.Hour).Add(time.Duration(i) * time.Millisecond)
		source.Entries = append(source.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-original-%03d", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("b", 64)})
	}
	e := &environmentCreateExecution{owner: environmentCreateOwner{PlanSHA256: parent, OwnerToken: source.OwnerToken}, Journal: source}
	f := managedLateExpiredRenewalNegativePinsFixture()
	f.ParentSHA, f.SourceSHA, f.SourceOwnerSHA = parent, deploymentplan.Digest(source), deploymentplan.Digest(e.owner)
	late := &createManagedLateSessionRecovery{Mode: managedLateSessionACKReady, ParentSHA: parent, SourceSHA: f.SourceSHA, SourceOwnerSHA: f.SourceOwnerSHA, ExpiredRenewal: f, OriginalSession: f.PriorSession.Session, NewSessionID: "77777777-7777-4777-8777-777777777777", OriginalKeySHA: f.OriginalKeySHA, OriginalCSRSHA: f.OriginalCSRSHA, Renewal: &createManagedSessionRecovery{}, SeedCandidate: &deploymentplan.Plan{Version: deploymentplan.Version}}
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: parent, ParentOwnerSHA: late.SourceOwnerSHA, ParentJournalSHA: late.SourceSHA, Materials: map[string]string{"accepted-service-core-late-session-source-descriptor": managedLateSessionSourceDigest(late)}}, QualifiedAt: now.Add(-time.Hour)}
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	late.Review = deploymentplan.Digest(a.Plan)
	f.Review = late.Review
	a.Qualification = deploymentplan.Qualification{PlanSHA256: late.Review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: late.Review})
	}
	entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + late.Review, Phase: "recovery", State: "completed", StartedAt: a.QualifiedAt.Add(time.Minute), CompletedAt: a.QualifiedAt.Add(2 * time.Minute), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": late.SourceSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("c", 64), "qualified_recovery_sha256": late.Review})}
	e.Journal.Entries = append(append([]environmentCreateJournalEntry(nil), source.Entries...), entry)
	return e, source, a, late, now
}

func TestManagedLateExpiredAdmittedOnlyKeepsSameProspectiveAuthority(t *testing.T) {
	e, source, a, late, now := managedLateExpiredAdmittedOnlyFixture(t)
	before := deploymentplan.Digest([]any{e.Journal, source, a, late})
	if !now.After(a.ValidUntil) || !managedLateExpiredAdmittedOnlyMembership(e, source, a, late, now) {
		t.Fatal("genuine admitted-only375 was not recognized as historical admission")
	}
	proof := managedLateExpiredAdmittedOnlyProof(e, a, late)
	if proof.SourceSHA != deploymentplan.Digest(e.Journal) || proof.OriginalSourceSHA != deploymentplan.Digest(source) || proof.OriginalReview != late.Review || proof.SessionID != late.NewSessionID || proof.DescriptorSHA != managedLateSessionSourceDigest(late) || proof.SeedPlanSHA != late.SeedCandidate.SHA256() || proof.KeySHA != late.OriginalKeySHA || proof.CSRSHA != late.OriginalCSRSHA {
		t.Fatal("continuation selected a new identity or lost the original admitted producer")
	}
	if deploymentplan.Digest([]any{e.Journal, source, a, late}) != before {
		t.Fatal("admitted-only observation mutated original evidence")
	}
	for _, mode := range []string{"extra-suffix", "pending-native-authorization", "seed-child", "extra-lease", "changed-prefix", "changed-owner", "foreign-review", "pending-admission", "zero-completion", "future-completion", "expired-original-admission", "changed-key", "changed-csr", "changed-SID", "changed-producer", "failed-original-GO", "changed-audit-receipt"} {
		t.Run(mode, func(t *testing.T) {
			var journal environmentCreateJournal
			var artifact createFoundationRecoveryArtifact
			var descriptor createManagedLateSessionRecovery
			_ = json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &journal)
			_ = json.Unmarshal(mustProviderRecoveryJSON(a), &artifact)
			_ = json.Unmarshal(mustProviderRecoveryJSON(late), &descriptor)
			changed := *e
			changed.Journal = journal
			switch mode {
			case "extra-suffix", "pending-native-authorization", "seed-child":
				changed.Journal.Entries = append(changed.Journal.Entries, environmentCreateJournalEntry{Key: mode, State: "pending"})
			case "extra-lease":
				changed.Journal.KubernetesLeases = append(changed.Journal.KubernetesLeases, deploymentplan.LeaseOwner{UID: "unexpected"})
			case "changed-prefix":
				changed.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-owner":
				changed.owner.OwnerToken = "foreign"
			case "foreign-review":
				descriptor.Review = strings.Repeat("f", 64)
			case "pending-admission":
				changed.Journal.Entries[374].State = "pending"
			case "zero-completion":
				changed.Journal.Entries[374].CompletedAt = time.Time{}
			case "future-completion":
				changed.Journal.Entries[374].CompletedAt = now.Add(time.Second)
			case "expired-original-admission":
				changed.Journal.Entries[374].StartedAt = artifact.ValidUntil
			case "changed-key":
				descriptor.OriginalKeySHA = strings.Repeat("f", 64)
			case "changed-csr":
				descriptor.OriginalCSRSHA = strings.Repeat("f", 64)
			case "changed-SID":
				descriptor.NewSessionID = "88888888-8888-4888-8888-888888888888"
			case "changed-producer":
				descriptor.SeedCandidate.BindingsSHA256 = "new-producer"
			case "failed-original-GO":
				artifact.Qualification.Gates[3].Status = "BLOCKED"
			case "changed-audit-receipt":
				changed.Journal.Entries[374].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": descriptor.Review, "audit_sha256": "unknown"})
			}
			if managedLateExpiredAdmittedOnlyMembership(&changed, source, artifact, &descriptor, now) {
				t.Fatal("unqualified progress or identity drift entered admitted-only scope")
			}
		})
	}
}

func TestManagedLateExpiredAdmittedOnlyRequiresFreshGOWithoutParentWrite(t *testing.T) {
	for _, mode := range []string{"fresh", "oldGO", "failed-gate", "wrong-checker", "changed-qualified-bytes", "changed-source", "changed-owner", "cancelled", "existing-installation-clock"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, original := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, original, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			for i := range 375 {
				at := time.Now().UTC().Add(-time.Second)
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-%03d", i), State: "completed", StartedAt: at, CompletedAt: at})
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			proof := createManagedLateExpiredAdmittedOnly{Schema: managedLateExpiredAdmittedOnlySchema, ParentSHA: e.Journal.PlanSHA256, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner)}
			pin := strings.Repeat("a", 64)
			a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: proof.OwnerSHA, ParentJournalSHA: proof.SourceSHA, CheckerSHA: checker, KubeconfigSHA: pin, FoundationSHA: pin, FoundationJournalSHA: pin, Plan: environmentCreatePlan{Environment: "dev", Stack: e.Journal.Stack, ConfigurationSHA256: pin}, Materials: map[string]string{managedLateExpiredAdmittedOnlyMaterial: deploymentplan.Digest(proof)}}, QualifiedAt: time.Now().UTC().Add(-time.Second)}
			if mode == "oldGO" {
				a.QualifiedAt = a.QualifiedAt.Add(-time.Hour)
			}
			if mode == "wrong-checker" {
				a.Plan.CheckerSHA = strings.Repeat("f", 64)
			}
			a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			review := deploymentplan.Digest(a.Plan)
			a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
			for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
				a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
			}
			if mode == "failed-gate" {
				a.Qualification.Gates[3].Status = "BLOCKED"
			}
			relative := filepath.Join("deployment/create/openbao-recovery", review, "qualification.json")
			if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(a)); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := readManagedLateQualificationFingerprint(store, a)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "changed-qualified-bytes":
				if err := store.write(relative, append(mustProviderRecoveryJSON(a), '\n'), true); err != nil {
					t.Fatal(err)
				}
			case "changed-source":
				proof.SourceSHA = strings.Repeat("f", 64)
			case "changed-owner":
				proof.OwnerSHA = strings.Repeat("f", 64)
			case "cancelled":
				cancel()
			case "existing-installation-clock":
				if err := writeEnvironmentCreateDerivedPrivateFile(store, managedLateInstallationClockPath(e.Journal.PlanSHA256), []byte("unknown-clock")); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			err = admitManagedLateExpiredAdmittedOnly(ctx, store, e, a, fingerprint, proof)
			if (err == nil) != (mode == "fresh") {
				t.Fatal("fresh-command continuation authority differs", err)
			}
			after, readErr := os.ReadFile(e.JournalPath)
			if readErr != nil || string(before) != string(after) || len(e.Journal.Entries) != 375 {
				t.Fatal("continuation rewrote old admission or appended parent history")
			}
		})
	}
}

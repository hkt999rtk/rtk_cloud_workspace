package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func mqttCompletionRecoveryFixture(t *testing.T) (secretStore, createMQTTRootCRLArtifact, environmentCreateJournal, environmentCreateJournal, environmentCreateOwner, mqttRootCRLCompletionRecoveryArtifact) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	store := secretStore{ConfigRoot: base, Root: filepath.Join(base, "dev"), Environment: "dev"}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	old, source, captured := mqttRootCRLCompletionFixture(t)
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: captured.Stack, PlanSHA256: captured.PlanSHA256, OwnerToken: captured.OwnerToken, Journal: "deployment/create/executions/" + captured.PlanSHA256 + "/execution-isolated/journal.json"}
	evidence, err := mqttRootCRLCompletionRecordedEvidence(old, source, captured)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	p := mqttRootCRLCompletionRecoveryPlan{Schema: mqttRootCRLCompletionRecoverySchema, Parent: old.Plan.Parent, OriginalReview: deploymentplan.Digest(old.Plan), OriginalArtifactSHA: deploymentplan.Digest(old), SourceJournalSHA: deploymentplan.Digest(captured), OwnerSHA: deploymentplan.Digest(owner), ConfigurationSHA: old.Plan.ConfigurationSHA, KubeconfigSHA: old.Plan.KubeconfigSHA, CheckerSHA: strings.Repeat("f", 64), NativeEvidence: evidence, QualifiedAt: now}
	a := mqttRootCRLCompletionRecoveryArtifact{Schema: mqttRootCRLCompletionRecoverySchema + "/artifact", Plan: p, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute), Qualification: deploymentplan.Qualification{PlanSHA256: deploymentplan.Digest(p), Decision: "GO"}}
	for _, id := range mqttRootCRLCompletionRecoveryGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: a.Qualification.PlanSHA256})
	}
	return store, old, source, captured, owner, a
}
func writeMQTTCompletionRecoveryFixture(t *testing.T, store secretStore, a mqttRootCRLCompletionRecoveryArtifact, captured environmentCreateJournal, owner environmentCreateOwner) {
	t.Helper()
	base := mqttRootCRLCompletionRecoveryBase(a.Plan.OriginalReview, deploymentplan.Digest(a.Plan))
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": captured, "source-owner.json": owner} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, name), value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMQTTRootCRLCompletionFreshReviewBindsQualificationEpochAndOriginalEvidence(t *testing.T) {
	store, old, source, captured, owner, a := mqttCompletionRecoveryFixture(t)
	writeMQTTCompletionRecoveryFixture(t, store, a, captured, owner)
	review := deploymentplan.Digest(a.Plan)
	if _, _, err := loadMQTTRootCRLCompletionRecovery(store, a.Plan.OriginalReview, review, old, source, owner); err != nil {
		t.Fatal(err)
	}
	next := a.Plan
	next.QualifiedAt = next.QualifiedAt.Add(time.Nanosecond)
	if deploymentplan.Digest(next) == review {
		t.Fatal("requalification reused an immutable artifact identity")
	}
	for _, mode := range []string{"failed-gate", "wrong-checker", "changed-owner", "wrong-epoch", "changed-evidence", "unknown-parent-tail"} {
		t.Run(mode, func(t *testing.T) {
			bad := a
			bad.Qualification.Gates = append([]deploymentplan.GateResult(nil), a.Qualification.Gates...)
			journal := cloneMQTTCompletionJournal(t, captured)
			switch mode {
			case "failed-gate":
				bad.Qualification.Gates[0].Status = "FAIL"
			case "wrong-checker":
				bad.Plan.CheckerSHA = "foreign"
			case "changed-owner":
				bad.Plan.OwnerSHA = strings.Repeat("b", 64)
			case "wrong-epoch":
				bad.QualifiedAt = bad.QualifiedAt.Add(time.Second)
			case "changed-evidence":
				bad.Plan.NativeEvidence = map[string]string{"fabricated": strings.Repeat("b", 64)}
			case "unknown-parent-tail":
				journal.Entries = append(journal.Entries, journal.Entries[0])
				bad.Plan.SourceJournalSHA = deploymentplan.Digest(journal)
			}
			// Qualifier fields are mutually bound; rehashing the plan does not repair
			// an invalid gate/owner/native source or non-monotonic parent journal.
			sha := deploymentplan.Digest(bad.Plan)
			bad.Qualification.PlanSHA256 = sha
			for i := range bad.Qualification.Gates {
				bad.Qualification.Gates[i].EvidenceSHA256 = sha
			}
			path := filepath.Join(store.Root, mqttRootCRLCompletionRecoveryBase(bad.Plan.OriginalReview, sha))
			if sha == review {
				path = filepath.Join(store.Root, mqttRootCRLCompletionRecoveryBase(bad.Plan.OriginalReview, review))
			}
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{"qualification.json": bad, "source-journal.json": journal, "source-owner.json": owner} {
				if err := os.WriteFile(filepath.Join(path, name), mustProviderRecoveryJSON(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := loadMQTTRootCRLCompletionRecovery(store, bad.Plan.OriginalReview, sha, old, source, owner); err == nil {
				t.Fatal("unqualified completion source accepted", mode)
			}
			// Restore the genuine fixture for later subcases.
			base := filepath.Join(store.Root, mqttRootCRLCompletionRecoveryBase(a.Plan.OriginalReview, review))
			for name, value := range map[string]any{"qualification.json": a, "source-journal.json": captured, "source-owner.json": owner} {
				if err := os.WriteFile(filepath.Join(base, name), mustProviderRecoveryJSON(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMQTTRootCRLCompletionMirrorWithoutPointerRetainsGenuineOldReviewAndTimes(t *testing.T) {
	store, old, source, captured, owner, a := mqttCompletionRecoveryFixture(t)
	writeMQTTCompletionRecoveryFixture(t, store, a, captured, owner)
	review := deploymentplan.Digest(a.Plan)
	final := cloneMQTTCompletionJournal(t, captured)
	evidence := a.Plan.NativeEvidence
	index := len(source.Entries)
	final.Entries[index].State = "completed"
	final.Entries[index].CompletedAt = time.Now().UTC().Add(-20 * time.Second)
	receipt := mqttRootCRLCompletionReceipt(old, evidence)
	final.Entries[index].Receipt = mustProviderRecoveryJSON(receipt)
	lease := deploymentplan.LeaseOwner{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: rootCRLLeasePlan(old.Plan.Configuration, review).SHA256() + ":fresh-observation-only"}
	proof := mqttRootCRLCompletionRecoveryExecution{Schema: mqttRootCRLCompletionRecoverySchema + "/completion", Review: review, OriginalReview: a.Plan.OriginalReview, SourceJournalSHA: a.Plan.SourceJournalSHA, FinalJournalSHA: deploymentplan.Digest(final), NativeEvidenceSHA: deploymentplan.Digest(evidence), Lease: lease, StartedAt: a.QualifiedAt.Add(time.Second), CompletedAt: time.Now().UTC().Add(-10 * time.Second)}
	base := mqttRootCRLCompletionRecoveryBase(a.Plan.OriginalReview, review)
	for name, value := range map[string]any{"completed.json": proof, filepath.Join("execution-leases", deploymentplan.Digest(lease), "lease.json"): lease} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, name), value); err != nil {
			t.Fatal(err)
		}
	}
	completion := createMQTTRootCRLCompletion{Schema: createMQTTRootCRLSchema + "/completion", Parent: old.Plan.Parent, Review: a.Plan.OriginalReview, Completed: true, SourceEntryCount: len(source.Entries), FinalEntryCount: len(final.Entries), FinalEntriesSHA: deploymentplan.Digest(final.Entries), FinalJournalSHA: deploymentplan.Digest(final), NativeEvidence: evidence, Receipt: receipt, CompletionReview: review}
	for name, value := range map[string]any{"completed.json": completion, "final-journal.json": final} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(mqttRootCRLBase(a.Plan.OriginalReview), name), value); err != nil {
			t.Fatal(err)
		}
	}
	e := &environmentCreateExecution{Store: store, owner: owner, Journal: final}
	mirrored, err := readMQTTRootCRLCompletionMirror(store, e, old, source)
	if err != nil || mirrored == nil || deploymentplan.Digest(mirrored) != deploymentplan.Digest(completion) {
		t.Fatal("genuine mirror could not repair missing pointer", err)
	}
	// A later fresh qualifier consumes this immutable mirror, preserving the old
	// completion-sidecar identity and original native timestamps byte for byte.
	raw := mustProviderRecoveryJSON(mirrored)
	if !strings.Contains(string(raw), review) || !final.Entries[index].CompletedAt.Equal(e.Journal.Entries[index].CompletedAt) {
		t.Fatal("old completion identity or time changed")
	}
	for _, mode := range []string{"missing-sidecar", "foreign-lease", "changed-top-time", "changed-child", "half-written-mirror"} {
		t.Run(mode, func(t *testing.T) {
			originalProofPath := filepath.Join(store.Root, base, "completed.json")
			mirrorPath := filepath.Join(store.Root, mqttRootCRLBase(a.Plan.OriginalReview), "completed.json")
			current := cloneMQTTCompletionJournal(t, final)
			switch mode {
			case "missing-sidecar":
				if err := os.Remove(originalProofPath); err != nil {
					t.Fatal(err)
				}
			case "foreign-lease":
				bad := proof
				bad.Lease.Holder = "foreign"
				if err := os.WriteFile(originalProofPath, mustProviderRecoveryJSON(bad), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-top-time":
				current.Entries[index].CompletedAt = current.Entries[index].CompletedAt.Add(time.Second)
			case "changed-child":
				current.Entries[index+1].Receipt = []byte(`{"fabricated":true}`)
			case "half-written-mirror":
				if err := os.WriteFile(mirrorPath, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ee := *e
			ee.Journal = current
			if _, err := readMQTTRootCRLCompletionMirror(store, &ee, old, source); err == nil {
				t.Fatal("unverified mirror accepted", mode)
			}
			if err := os.WriteFile(originalProofPath, mustProviderRecoveryJSON(proof), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mirrorPath, mustProviderRecoveryJSON(completion), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

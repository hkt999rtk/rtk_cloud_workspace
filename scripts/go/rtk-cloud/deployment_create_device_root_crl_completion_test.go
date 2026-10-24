package main

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceRootCRLCompletionFixture(t *testing.T) (secretStore, createDeviceRootCRLArtifact, createDeviceRootCRLCompletionArtifact, createDeviceRootCRLJournal, createDeviceRootCRLJournal) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	p, key, now := deviceRootCRLFixture(t)
	p.NativeEvidence = map[string]string{}
	for _, key := range []string{"accepted-service-core-managed-complete-current-native-volumes", managedLateAPICurrentMaterial, "accepted-service-core-managed-application-current-servers", "accepted-service-core-managed-application-current-clients"} {
		p.NativeEvidence[key] = strings.Repeat("a", 64)
	}
	old := deviceRootCRLArtifactFixture(p, now)
	source := createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: deploymentplan.Digest(p), SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "rotating", StartedAt: now.Add(-4 * time.Minute), Deadline: now.Add(26 * time.Minute), RotationStartedAt: now.Add(-3 * time.Minute), Lease: deploymentplan.LeaseOwner{ResourceID: "Lease/isolated-device-video-cloud/rtk-deployment-execution", UID: "55555555-5555-4555-8555-555555555555", Holder: rootCRLLeasePlan(p.Configuration, deploymentplan.Digest(p)).SHA256() + ":original"}, LeaseReleased: true, Evidence: map[string]string{}}
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{p.Root.ID, p.Root.RootFingerprint, "5", now.Add(-2 * time.Minute), now.Add(24 * time.Hour), entries})
	at := now.Add(-time.Minute)
	plan := createDeviceRootCRLCompletionPlan{Schema: createDeviceRootCRLCompletionSchema, OriginalReview: deploymentplan.Digest(p), OriginalArtifactSHA: deploymentplan.Digest(old), SourceRepairJournalSHA: deploymentplan.Digest(source), CheckerSHA: strings.Repeat("b", 64), ConfigurationSHA: p.ConfigurationSHA, KubeconfigSHA: p.KubeconfigSHA, ProviderSHA: p.ProviderSHA, QualifiedAt: at, Deadline: source.Deadline, Signed: signed, NativeEvidence: maps.Clone(p.NativeEvidence)}
	a := createDeviceRootCRLCompletionArtifact{Schema: createDeviceRootCRLCompletionSchema + "/artifact", Plan: plan, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
	deviceRootCRLCompletionFixtureGates(&a)
	current := source
	current.Stage, current.Signed = "completed", signed
	current.CompletionReview, current.CompletionStartedAt = deploymentplan.Digest(a.Plan), at.Add(time.Second)
	current.RotatedAt, current.ImportedAt, current.CompletedAt = now.Add(-30*time.Second), now.Add(-20*time.Second), now.Add(-10*time.Second)
	current.Lease.Holder = rootCRLLeasePlan(p.Configuration, current.CompletionReview).SHA256() + ":completion"
	current.Evidence = deviceRootCRLEvidenceFixture(p, 2)
	return store, old, a, source, current
}

func deviceRootCRLCompletionFixtureGates(a *createDeviceRootCRLCompletionArtifact) {
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createDeviceRootCRLGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
}

func deviceRootCRLCompletionFixtureWrite(t *testing.T, store secretStore, a createDeviceRootCRLCompletionArtifact, source createDeviceRootCRLJournal) {
	t.Helper()
	base := deviceRootCRLCompletionBase(a.Plan.OriginalReview, deploymentplan.Digest(a.Plan))
	for name, value := range map[string]any{"qualification.json": a, "source-repair-journal.json": source} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, name), value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeviceRootCRLCompletionAuthenticatesFreshSidecarAndOriginalClock(t *testing.T) {
	store, old, a, source, current := deviceRootCRLCompletionFixture(t)
	before := deploymentplan.Digest(source)
	deviceRootCRLCompletionFixtureWrite(t, store, a, source)
	if !deviceRootCRLCompletionQualificationValid(a, old) || !deviceRootCRLJournalValid(old, current, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, old, current) != nil {
		t.Fatal("genuine separately qualified number5 completion refused")
	}
	if deploymentplan.Digest(source) != before || !current.StartedAt.Equal(source.StartedAt) || !current.Deadline.Equal(source.Deadline) || !current.RotationStartedAt.Equal(source.RotationStartedAt) {
		t.Fatal("completion changed the original admitted clock or rotation intent")
	}
	for _, mode := range []string{"old-start", "old-deadline", "old-rotation", "old-source", "old-owner", "wrong-review", "missing-fresh-start", "fresh-start-before-GO", "fresh-start-at-expiry", "wrong-provider-result", "ordinary-rotation-stage", "new-original-epoch", "foreign-lease"} {
		t.Run(mode, func(t *testing.T) {
			j := current
			switch mode {
			case "old-start":
				j.StartedAt = j.StartedAt.Add(time.Second)
			case "old-deadline":
				j.Deadline = j.Deadline.Add(time.Second)
			case "old-rotation":
				j.RotationStartedAt = j.RotationStartedAt.Add(time.Second)
			case "old-source":
				j.SourceSHA = strings.Repeat("c", 64)
			case "old-owner":
				j.OwnerSHA = strings.Repeat("c", 64)
			case "wrong-review":
				j.CompletionReview = strings.Repeat("c", 64)
			case "missing-fresh-start":
				j.CompletionStartedAt = time.Time{}
			case "fresh-start-before-GO":
				j.CompletionStartedAt = a.QualifiedAt.Add(-time.Second)
			case "fresh-start-at-expiry":
				j.CompletionStartedAt = a.ValidUntil
			case "wrong-provider-result":
				j.Signed.Number = "6"
			case "ordinary-rotation-stage":
				j.Stage = "admitted"
			case "new-original-epoch":
				j.StartedAt = j.CompletionStartedAt
				j.Deadline = j.StartedAt.Add(30 * time.Minute)
			case "foreign-lease":
				j.Lease.Holder = "foreign:holder"
			}
			if validateDeviceRootCRLCompletionJournal(store, old, j) == nil {
				t.Fatal("completion weakened original history, fresh authority or lease binding")
			}
		})
	}
}

func TestDeviceRootCRLCompletionRejectsChangedSixGOAndSelector(t *testing.T) {
	store, old, a, source, _ := deviceRootCRLCompletionFixture(t)
	for _, mode := range []string{"old-artifact", "old-review", "configuration", "kubeconfig", "provider", "missing-checker", "qualified-time", "extended-window", "missing-gate", "optional-gate", "wrong-evidence", "changed-live-volume", "old-admission-outside-GO", "changed-old-deadline"} {
		t.Run(mode, func(t *testing.T) {
			localStore := makeIsolatedTestSecretStore(t, "dev")
			candidate := a
			candidate.Qualification.Gates = slices.Clone(a.Qualification.Gates)
			candidate.Plan.NativeEvidence = maps.Clone(a.Plan.NativeEvidence)
			captured := source
			switch mode {
			case "old-artifact":
				candidate.Plan.OriginalArtifactSHA = strings.Repeat("c", 64)
			case "old-review":
				candidate.Plan.OriginalReview = strings.Repeat("c", 64)
			case "configuration":
				candidate.Plan.ConfigurationSHA = strings.Repeat("c", 64)
			case "kubeconfig":
				candidate.Plan.KubeconfigSHA = strings.Repeat("c", 64)
			case "provider":
				candidate.Plan.ProviderSHA = strings.Repeat("c", 64)
			case "missing-checker":
				candidate.Plan.CheckerSHA = ""
			case "qualified-time":
				candidate.Plan.QualifiedAt = candidate.Plan.QualifiedAt.Add(time.Second)
			case "extended-window":
				candidate.ValidUntil = candidate.ValidUntil.Add(time.Second)
			case "missing-gate":
				candidate.Qualification.Gates = candidate.Qualification.Gates[:5]
			case "optional-gate":
				candidate.Qualification.Gates[0].Required = false
			case "wrong-evidence":
				candidate.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("c", 64)
			case "changed-live-volume":
				candidate.Plan.NativeEvidence["accepted-service-core-managed-complete-current-native-volumes"] = strings.Repeat("c", 64)
			case "old-admission-outside-GO":
				captured.StartedAt = old.ValidUntil
				captured.Deadline = captured.StartedAt.Add(30 * time.Minute)
				candidate.Plan.SourceRepairJournalSHA = deploymentplan.Digest(captured)
				candidate.Plan.Deadline = captured.Deadline
			case "changed-old-deadline":
				candidate.Plan.Deadline = candidate.Plan.Deadline.Add(time.Second)
			}
			if mode != "missing-gate" && mode != "optional-gate" && mode != "wrong-evidence" {
				deviceRootCRLCompletionFixtureGates(&candidate)
			}
			deviceRootCRLCompletionFixtureWrite(t, localStore, candidate, captured)
			if _, _, err := loadDeviceRootCRLCompletionArtifact(localStore, old, deploymentplan.Digest(candidate.Plan)); err == nil {
				t.Fatal("self-consistent changed completion authority accepted")
			}
		})
	}
	deviceRootCRLCompletionFixtureWrite(t, store, a, source)
	wrong := strings.Repeat("d", 64)
	base := deviceRootCRLCompletionBase(a.Plan.OriginalReview, wrong)
	for name, value := range map[string]any{"qualification.json": a, "source-repair-journal.json": source} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, name), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := loadDeviceRootCRLCompletionArtifact(store, old, wrong); err == nil {
		t.Fatal("other valid qualification substituted at requested review selector")
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func mqttRootCRLCompletionFixture(t *testing.T) (createMQTTRootCRLArtifact, environmentCreateJournal, environmentCreateJournal) {
	t.Helper()
	a, source, _ := mqttRootCRLJournalFixture(t)
	now := time.Now().UTC()
	a.Plan.CheckerSHA, a.Plan.SourceOwnerSHA = strings.Repeat("c", 64), strings.Repeat("d", 64)
	a.Plan.Root = environmentCreateRegisteredIssuer{ID: "14c1a269-e32e-4eac-aa93-db5ad8e85f7e", Domain: "mqtt"}
	a.Plan.Snapshot.Prior.Number = "1"
	a.QualifiedAt, a.ValidUntil = now.Add(-time.Hour), now.Add(-45*time.Minute)
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createMQTTRootCRLGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	current := source
	current.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
	current.KubernetesLeases = []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: rootCRLLeasePlan(a.Plan.Configuration, review).SHA256() + ":original-native-execution"}}
	key := "mqtt-root-crl-renewal-" + review
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "pending", StartedAt: now.Add(-55 * time.Minute), IntentSHA256: activeTransportJournalDigest(a.Plan)})
	for i := 0; i < 3; i++ {
		intent, receipt := any(mqttRootCRLImportIntent(a.Plan)), any(map[string]any{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request)})
		step := key + "/native-import"
		if i > 0 {
			target := a.Plan.Consumers[i-1]
			step = key + "/native-runtime-" + deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
			intent = mqttRootCRLRuntimeIntent(a.Plan, target)
			receipt = map[string]string{"native-publication": strings.Repeat("a", 64), target.ID + ":state": strings.Repeat("b", 64), target.ID + ":owner": deploymentplan.Digest(target), target.ID + ":native-ack": strings.Repeat("e", 64)}
		}
		// The final child completes after old GO expiry, under its originally
		// admitted lifecycle. Fresh completion must not retrospectively deny it.
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: step, Phase: "bootstrap", State: "completed", StartedAt: now.Add(time.Duration(-50+2*i) * time.Minute), CompletedAt: now.Add(time.Duration(-49+2*i) * time.Minute), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(receipt)})
	}
	current.Entries[len(current.Entries)-1].CompletedAt = now.Add(-44 * time.Minute)
	return a, source, current
}

func cloneMQTTCompletionJournal(t *testing.T, original environmentCreateJournal) environmentCreateJournal {
	t.Helper()
	var clone environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(original), &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestMQTTRootCRLCompletionOnlyRequiresOriginalGOAndThreeGenuineCompletedChildren(t *testing.T) {
	a, source, original := mqttRootCRLCompletionFixture(t)
	for _, scenario := range []string{"pending", "allocated", "completed", "missing-gate", "failed-gate", "foreign-checker-shape", "late-original-admission", "future-child", "early-child", "missing-import", "missing-second-ACK", "changed-import", "changed-owner", "conflicting-publication", "extra-parent-entry", "extra-old-scope-lease", "missing-lease", "changed-source", "changed-metadata", "wrong-top-receipt", "early-top-completion"} {
		t.Run(scenario, func(t *testing.T) {
			current := cloneMQTTCompletionJournal(t, original)
			artifact := a
			artifact.Qualification.Gates = append([]deploymentplan.GateResult(nil), a.Qualification.Gates...)
			index := len(source.Entries)
			evidence, err := mqttRootCRLCompletionRecordedEvidence(a, source, current)
			if err != nil {
				t.Fatal("genuine pending fixture rejected", err)
			}
			switch scenario {
			case "allocated", "completed", "wrong-top-receipt", "early-top-completion":
				current.Entries[index].State = "allocated"
				receipt := mqttRootCRLCompletionReceipt(a, evidence)
				if scenario == "wrong-top-receipt" {
					receipt.NativeEvidenceSHA = strings.Repeat("f", 64)
				}
				current.Entries[index].Receipt = mustProviderRecoveryJSON(receipt)
				if scenario == "completed" || scenario == "early-top-completion" {
					current.Entries[index].State = "completed"
					current.Entries[index].CompletedAt = time.Now().UTC().Add(-time.Second)
					if scenario == "early-top-completion" {
						current.Entries[index].CompletedAt = current.Entries[index+3].CompletedAt.Add(-time.Second)
					}
				}
			case "missing-gate":
				artifact.Qualification.Gates = artifact.Qualification.Gates[:5]
			case "failed-gate":
				artifact.Qualification.Gates[0].Status = "FAIL"
			case "foreign-checker-shape":
				artifact.Plan.CheckerSHA = "unqualified"
			case "late-original-admission":
				current.Entries[index].StartedAt = a.ValidUntil
			case "future-child":
				current.Entries[index+3].CompletedAt = time.Now().UTC().Add(time.Minute)
			case "early-child":
				current.Entries[index+2].StartedAt = current.Entries[index+1].CompletedAt.Add(-time.Second)
			case "missing-import", "missing-second-ACK":
				child := index + 1
				if scenario == "missing-second-ACK" {
					child = index + 3
				}
				current.Entries[child].State, current.Entries[child].Receipt, current.Entries[child].CompletedAt = "pending", nil, time.Time{}
			case "changed-import":
				current.Entries[index+1].Receipt = json.RawMessage(`{"fabricated":"native-two"}`)
			case "changed-owner", "conflicting-publication":
				var item map[string]string
				json.Unmarshal(current.Entries[index+3].Receipt, &item)
				key := "native-publication"
				if scenario == "changed-owner" {
					key = a.Plan.Consumers[1].ID + ":owner"
				}
				item[key] = strings.Repeat("f", 64)
				current.Entries[index+3].Receipt = mustProviderRecoveryJSON(item)
			case "extra-parent-entry":
				current.Entries = append(current.Entries, current.Entries[index+3])
			case "extra-old-scope-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, current.KubernetesLeases[0])
			case "missing-lease":
				current.KubernetesLeases = nil
			case "changed-source":
				current.Entries[0].Receipt = json.RawMessage(`{"different":"source"}`)
			case "changed-metadata":
				current.OwnerToken = "foreign-owner"
			}
			pass := scenario == "pending" || scenario == "allocated" || scenario == "completed"
			if mqttRootCRLCompletionSourceValid(artifact, source, current) != pass {
				t.Fatal("wrong completion-only source authority", scenario)
			}
			if _, err := mqttRootCRLCompletionRecordedEvidence(artifact, source, current); (err == nil) != pass {
				t.Fatal("wrong immutable two-consumer evidence authority", scenario, err)
			}
		})
	}
}

func TestMQTTRootCRLCompletionRetryCanOnlyAdvanceTheSameOriginalTop(t *testing.T) {
	a, source, captured := mqttRootCRLCompletionFixture(t)
	evidence, err := mqttRootCRLCompletionRecordedEvidence(a, source, captured)
	if err != nil {
		t.Fatal(err)
	}
	allocated := cloneMQTTCompletionJournal(t, captured)
	index := len(source.Entries)
	allocated.Entries[index].State = "allocated"
	allocated.Entries[index].Receipt = mustProviderRecoveryJSON(mqttRootCRLCompletionReceipt(a, evidence))
	completed := cloneMQTTCompletionJournal(t, allocated)
	completed.Entries[index].State = "completed"
	completed.Entries[index].CompletedAt = time.Now().UTC().Add(-time.Second)
	for _, pair := range []struct{ before, after environmentCreateJournal }{{captured, captured}, {captured, allocated}, {captured, completed}, {allocated, allocated}, {allocated, completed}, {completed, completed}} {
		if !mqttRootCRLCompletionMembership(a, source, pair.before, pair.after) {
			t.Fatal("actual same-top completion retry rejected")
		}
	}
	for _, scenario := range []string{"regress", "change-child-time", "change-child-evidence", "change-original-start", "change-completed-time", "change-allocated-receipt-bytes", "add-lease", "change-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			before := captured
			current := cloneMQTTCompletionJournal(t, completed)
			switch scenario {
			case "regress":
				before, current = allocated, captured
			case "change-child-time":
				current.Entries[index+3].CompletedAt = current.Entries[index+3].CompletedAt.Add(time.Second)
			case "change-child-evidence":
				var item map[string]string
				json.Unmarshal(current.Entries[index+3].Receipt, &item)
				item[a.Plan.Consumers[1].ID+":native-ack"] = strings.Repeat("f", 64)
				current.Entries[index+3].Receipt = mustProviderRecoveryJSON(item)
				changed, _ := mqttRootCRLCompletionChildEvidence(a, source, current)
				current.Entries[index].Receipt = mustProviderRecoveryJSON(mqttRootCRLCompletionReceipt(a, changed))
			case "change-original-start":
				current.Entries[index].StartedAt = current.Entries[index].StartedAt.Add(time.Second)
			case "change-completed-time":
				before = completed
				current.Entries[index].CompletedAt = current.Entries[index].CompletedAt.Add(-time.Second)
			case "change-allocated-receipt-bytes":
				before = allocated
				current.Entries[index].Receipt = append(current.Entries[index].Receipt, '\n')
			case "add-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, current.KubernetesLeases[0])
			case "change-metadata":
				current.ArtifactSHA256 = strings.Repeat("f", 64)
			}
			if mqttRootCRLCompletionMembership(a, source, before, current) {
				t.Fatal("completion retry rewrote original native history", scenario)
			}
		})
	}
}

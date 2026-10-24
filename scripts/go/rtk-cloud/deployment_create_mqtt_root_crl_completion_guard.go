package main

import (
	"bytes"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Completion authority cannot enter the signing/importing lifecycle. The old
// qualified admission and its three actual completed native children remain
// immutable; a fresh qualifier separately re-observes their current evidence.
func mqttRootCRLCompletionSourceValid(a createMQTTRootCRLArtifact, source, current environmentCreateJournal) bool {
	review := deploymentplan.Digest(a.Plan)
	now := time.Now().UTC()
	if a.Schema != createMQTTRootCRLSchema+"/artifact" || a.Plan.Schema != createMQTTRootCRLSchema || a.Plan.Parent != source.PlanSHA256 || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || !environmentPreflightDigest.MatchString(a.Plan.SourceOwnerSHA) || a.Plan.Root.Domain != "mqtt" || a.Plan.Root.ID != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" || a.Plan.Snapshot.Prior.Number != "1" || a.Plan.Signed.Number != "2" || !environmentPreflightDigest.MatchString(a.Plan.Signed.Digest) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createMQTTRootCRLGates) || len(current.Entries) != len(source.Entries)+4 || len(current.KubernetesLeases) != len(source.KubernetesLeases)+1 || !source.CompletedAt.IsZero() || !mqttRootCRLProgressValid(current, a, source, false) {
		return false
	}
	for i, expected := range createMQTTRootCRLGates {
		gate := a.Qualification.Gates[i]
		if gate.ID != expected || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != review {
			return false
		}
	}
	tail := current.Entries[len(source.Entries):]
	for i, child := range tail[1:] {
		if child.State != "completed" || child.CompletedAt.After(now) || child.StartedAt.Before(tail[0].StartedAt) || i > 0 && child.StartedAt.Before(tail[i].CompletedAt) {
			return false
		}
	}
	if tail[0].StartedAt.After(now) || tail[0].State == "completed" && (tail[0].CompletedAt.After(now) || tail[0].CompletedAt.Before(tail[3].CompletedAt)) {
		return false
	}
	evidence, err := mqttRootCRLCompletionChildEvidence(a, source, current)
	if err != nil {
		return false
	}
	if tail[0].State != "pending" {
		var actual createMQTTRootCRLReceipt
		if decodeEnvironmentJSON(tail[0].Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(mqttRootCRLCompletionReceipt(a, evidence)) {
			return false
		}
	}
	return true
}

func mqttRootCRLCompletionRecordedEvidence(a createMQTTRootCRLArtifact, source, current environmentCreateJournal) (map[string]string, error) {
	if !mqttRootCRLCompletionSourceValid(a, source, current) {
		return nil, mqttRootCRLError("completion-only-exact-original-native-tail")
	}
	return mqttRootCRLCompletionChildEvidence(a, source, current)
}

func mqttRootCRLCompletionChildEvidence(a createMQTTRootCRLArtifact, source, current environmentCreateJournal) (map[string]string, error) {
	combined := map[string]string{}
	for _, child := range current.Entries[len(source.Entries)+2:] {
		var observed map[string]string
		if decodeEnvironmentJSON(child.Receipt, &observed) != nil || len(observed) != 4 {
			return nil, mqttRootCRLError("completion-only-original-runtime-receipt")
		}
		for key, value := range observed {
			if previous, exists := combined[key]; exists && previous != value {
				return nil, mqttRootCRLError("completion-only-original-publication-conflict")
			}
			combined[key] = value
		}
	}
	return combined, nil
}

func mqttRootCRLCompletionReceipt(a createMQTTRootCRLArtifact, evidence map[string]string) createMQTTRootCRLReceipt {
	return createMQTTRootCRLReceipt{createMQTTRootCRLSchema, a.Plan.Parent, deploymentplan.Digest(a.Plan), filepath.ToSlash(filepath.Join(a.Plan.Candidate, "signed/revocations.pem")), a.Plan.Signed.Digest, deploymentplan.Digest(evidence), true}
}

// A retry may only advance the original top entry. It cannot add a fresh parent
// admission/lease, change a child receipt or make completed evidence mutable.
func mqttRootCRLCompletionMembership(a createMQTTRootCRLArtifact, source, captured, current environmentCreateJournal) bool {
	if !mqttRootCRLCompletionSourceValid(a, source, captured) || !mqttRootCRLCompletionSourceValid(a, source, current) {
		return false
	}
	index := len(source.Entries)
	before, after := captured.Entries[index], current.Entries[index]
	rank := map[string]int{"pending": 0, "allocated": 1, "completed": 2}
	if rank[after.State] < rank[before.State] || before.State == "completed" && activeTransportJournalDigest(before) != activeTransportJournalDigest(after) || before.State != "pending" && !bytes.Equal(before.Receipt, after.Receipt) {
		return false
	}
	fixed := after
	fixed.State, fixed.Receipt, fixed.CompletedAt = before.State, before.Receipt, before.CompletedAt
	if activeTransportJournalDigest(fixed) != activeTransportJournalDigest(before) {
		return false
	}
	normalized := current
	normalized.Entries = append([]environmentCreateJournalEntry(nil), current.Entries...)
	normalized.Entries[index] = before
	return deploymentplan.Digest(normalized) == deploymentplan.Digest(captured)
}

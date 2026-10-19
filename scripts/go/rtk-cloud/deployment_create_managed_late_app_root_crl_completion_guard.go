package main

import (
	"bytes"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
)

// This predicate receives the already authenticated original artifact. It
// grants only read-proof completion after the genuine import has completed.
func managedLateAppCompletionSourceValid(a createAppRootCRLArtifact, source, current environmentCreateJournal) bool {
	n := len(source.Entries)
	if a.Plan.Root.ID != managedLateAppRootID || a.Plan.Root.Domain != "app" || a.Plan.Snapshot.Prior.Number != "1" || a.Plan.Signed.Number != "2" || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || len(current.Entries) < n+3 || len(current.Entries) > n+4 || len(current.KubernetesLeases) != len(source.KubernetesLeases)+1 || !managedLateAppRootCRLProgressValid(a, source, current, false) {
		return false
	}
	imported := current.Entries[n+1]
	return imported.State == "completed" && !imported.CompletedAt.IsZero()
}
func managedLateAppCompletionEvidenceForTarget(a createAppRootCRLArtifact, evidence map[string]string, t createRootCRLStateTarget) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"consumer-owner-" + t.ID, "consumer-Pod-" + t.ID, "consumer-volume-" + t.ClaimID, "native-ACKs-" + a.Plan.Root.ID, "native-ACKs-" + a.Plan.Intermediate.ID} {
		out[k] = evidence[k]
	}
	for _, path := range t.Paths {
		k := "cache-" + t.ID + "-" + filepath.Base(path)
		out[k] = evidence[k]
	}
	return out
}
func managedLateAppCompletionEvidenceValid(a createAppRootCRLArtifact, evidence map[string]string) bool {
	if len(a.Plan.Consumers) != 2 {
		return false
	}
	keys := map[string]bool{}
	for _, t := range a.Plan.Consumers {
		observed := managedLateAppCompletionEvidenceForTarget(a, evidence, t)
		if !managedLateAppRootCRLConsumerEvidenceValid(a.Plan, t, observed) {
			return false
		}
		for k := range observed {
			keys[k] = true
		}
	}
	return len(evidence) == len(keys)
}
func managedLateAppCompletionMembership(a createAppRootCRLArtifact, source, captured, current environmentCreateJournal, evidence map[string]string) bool {
	if !managedLateAppCompletionSourceValid(a, source, captured) || !managedLateAppCompletionSourceValid(a, source, current) || !managedLateAppCompletionEvidenceValid(a, evidence) || len(current.Entries) < len(captured.Entries) {
		return false
	}
	n := len(source.Entries)
	normalized := current
	normalized.Entries = slices.Clone(current.Entries[:len(captured.Entries)])
	rank := map[string]int{"pending": 0, "allocated": 1, "completed": 2}
	for i := n; i < len(current.Entries); i++ {
		entry := current.Entries[i]
		if i == n+1 {
			continue
		} // the genuine import is frozen below
		expected := mustProviderRecoveryJSON(appRootCRLReceipt(a, evidence))
		if i > n {
			expected = mustProviderRecoveryJSON(managedLateAppCompletionEvidenceForTarget(a, evidence, a.Plan.Consumers[i-n-2]))
		}
		if entry.State != "pending" && activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(expected)) {
			return false
		}
		if i >= len(captured.Entries) {
			continue
		}
		old := captured.Entries[i]
		if rank[entry.State] < rank[old.State] || old.State == "completed" && deploymentplan.Digest(entry) != deploymentplan.Digest(old) || old.State != "pending" && !bytes.Equal(old.Receipt, entry.Receipt) {
			return false
		}
		fixed := entry
		fixed.State, fixed.CompletedAt, fixed.Receipt = old.State, old.CompletedAt, old.Receipt
		if deploymentplan.Digest(fixed) != deploymentplan.Digest(old) {
			return false
		}
		normalized.Entries[i] = old
	}
	return deploymentplan.Digest(normalized) == deploymentplan.Digest(captured)
}
func managedLateAppCompletionCheckpointReview(j environmentCreateJournal) string {
	for i, entry := range j.Entries {
		const prefix = "app-root-crl-renewal-"
		if !strings.HasPrefix(entry.Key, prefix) || strings.Contains(entry.Key, "/") {
			continue
		}
		review := strings.TrimPrefix(entry.Key, prefix)
		if i != 370 || !environmentPreflightDigest.MatchString(review) || len(j.Entries) < 373 || len(j.Entries) > 374 || j.Entries[i+1].Key != entry.Key+"/native-import" || j.Entries[i+1].State != "completed" {
			return ""
		}
		return review
	}
	return ""
}
func managedLateAppCompletionNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil || managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256) != nil {
		return false
	}
	review := managedLateAppCompletionCheckpointReview(e.Journal)
	if review == "" {
		return false
	}
	a, source, err := loadManagedLateAppRootCRLArtifact(store, e, review)
	return err == nil && len(source.Entries) == 370 && managedLateAppCompletionSourceValid(a, source, e.Journal)
}

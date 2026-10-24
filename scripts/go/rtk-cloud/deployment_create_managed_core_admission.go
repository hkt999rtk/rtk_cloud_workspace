package main

import (
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

// This actually stopped six-GO admission permits no writes by itself. A fresh
// qualification must verify its intended audit/auth receipt before core repair.
type createManagedCorePendingAdmission struct {
	Review, SourceSHA, StoppedSourceSHA, ArtifactSHA string
	CheckerSHA, CandidateSHA, AuditSHA               string
	Entry                                            environmentCreateJournalEntry
}

func managedCorePendingAdmissionReview(j environmentCreateJournal) string {
	if len(j.Entries) < 2 {
		return ""
	}
	entry := j.Entries[len(j.Entries)-1]
	review := strings.TrimPrefix(entry.Key, "openbao-recovery-admission-")
	if entry.Key != "openbao-recovery-admission-"+review || !environmentPreflightDigest.MatchString(review) || entry.Phase != "recovery" || entry.State != "pending" || entry.StartedAt.IsZero() || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
		return ""
	}
	before := j
	before.Entries = j.Entries[:len(j.Entries)-1]
	if managedCorePartialInitialReview(before) == "" {
		return ""
	}
	return review
}

func managedCorePartialReview(j environmentCreateJournal) string {
	if managedCoreInterruptedForwardReview(j) != "" {
		before := j
		before.Entries = slices.Clone(j.Entries[:len(j.Entries)-2])
		last := &before.Entries[len(before.Entries)-1]
		last.State, last.CompletedAt, last.Receipt = "pending", time.Time{}, nil
		return managedCorePartialReview(before)
	}
	if review := managedCorePartialInitialReview(j); review != "" {
		return review
	}
	if managedCorePendingAdmissionReview(j) != "" {
		before := j
		before.Entries = j.Entries[:len(j.Entries)-1]
		return managedCorePartialInitialReview(before)
	}
	return ""
}

type managedCorePendingAdmissionSource struct {
	Stopped, Historical *environmentCreateExecution
	Admission, Original createFoundationRecoveryArtifact
	Core                *createManagedCorePartialRecovery
	Prior               *createManagedCorePendingAdmission
}

func managedCorePendingAdmissionArtifactValid(a createFoundationRecoveryArtifact, entry environmentCreateJournalEntry, source environmentCreateJournal, review string) bool {
	if a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedCorePartialSchema || deploymentplan.Digest(a.Plan) != review || a.Plan.ParentSHA != source.PlanSHA256 || a.Plan.ParentJournalSHA != deploymentplan.Digest(source) || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 || a.QualifiedAt.IsZero() || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || entry.StartedAt.Before(a.QualifiedAt) || !entry.StartedAt.Before(a.ValidUntil) {
		return false
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || g.Status != "PASS" || !g.Required || g.EvidenceSHA256 != review {
			return false
		}
	}
	intent := map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}
	return entry.Key == "openbao-recovery-admission-"+review && entry.Phase == "recovery" && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 && entry.IntentSHA256 == deploymentplan.Digest(intent)
}

func readManagedCorePendingAdmissionSource(store secretStore, e *environmentCreateExecution) (*managedCorePendingAdmissionSource, error) {
	bad := func(stage string) (*managedCorePendingAdmissionSource, error) {
		return nil, managedCorePartialBlock("pending-admission-" + stage)
	}
	if e == nil || store.Root != e.Store.Root {
		return bad("original-owner")
	}
	review := managedCorePendingAdmissionReview(e.Journal)
	if review == "" {
		return bad("literal-checkpoint")
	}
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	var a createFoundationRecoveryArtifact
	var source environmentCreateJournal
	var owner environmentCreateOwner
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || !managedCorePendingAdmissionArtifactValid(a, entry, source, review) || len(e.Journal.Entries) != len(source.Entries)+1 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases) {
		return bad("original-six-go-artifact")
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:len(source.Entries)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) || readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != source.Environment || owner.Stack != source.Stack || owner.OwnerToken != source.OwnerToken || owner.PlanSHA256 != source.PlanSHA256 || deploymentplan.Digest(owner) != a.Plan.ParentOwnerSHA || e.owner.Schema != "" && deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) {
		return bad("unchanged-source-and-owner")
	}
	stopped := *e
	stopped.Journal = source
	historical, original, core, err := loadManagedCorePartialInitialSource(store, &stopped)
	if err != nil || core.Action != managedCoreRestoreOnly {
		return bad("original-stopped-core")
	}
	for key, value := range map[string]string{"original-review": core.OriginalRepairReview, "source-plan": core.Plan.SHA256(), "source-journal": deploymentplan.Digest(core.Journal), "source": deploymentplan.Digest(core), "action": deploymentplan.Digest(managedCoreRestoreOnly), "original-admission": core.ArtifactSHA} {
		if a.Plan.Materials["accepted-service-core-complete-issuer-partial-"+key] != value {
			return bad("original-material-" + key)
		}
	}
	if a.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || a.Plan.Plan.Environment != source.Environment || a.Plan.Plan.Stack != source.Stack || a.Plan.Plan.ConfigurationSHA256 != core.Plan.ConfigurationSHA256 || deploymentplan.Digest(a.Plan.Plan.Release) != core.Plan.ReleaseSHA256 {
		return bad("original-action-and-inputs")
	}
	var candidate deploymentplan.Plan
	if readExecutionJSON(filepath.Join(base, managedCorePartialForwardPlanFilename), &candidate) != nil || candidate.SHA256() != a.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] || validateManagedCorePartialCandidate(core, &candidate) != nil {
		return bad("original-fourteen-retain-two-update")
	}
	auditSHA := ""
	for _, old := range source.Entries {
		if old.Key != "openbao-audit-enable" {
			continue
		}
		var receipt map[string]string
		if auditSHA != "" || old.State != "completed" || old.CompletedAt.IsZero() || decodeEnvironmentJSON(old.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt["audit_sha256"]) {
			return bad("original-native-audit")
		}
		auditSHA = receipt["audit_sha256"]
	}
	if auditSHA == "" {
		return bad("original-native-audit")
	}
	prior := &createManagedCorePendingAdmission{Review: review, SourceSHA: deploymentplan.Digest(e.Journal), StoppedSourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), CheckerSHA: a.Plan.CheckerSHA, CandidateSHA: candidate.SHA256(), AuditSHA: auditSHA, Entry: entry}
	return &managedCorePendingAdmissionSource{Stopped: &stopped, Historical: historical, Admission: a, Original: original, Core: core, Prior: prior}, nil
}

func loadManagedCorePendingAdmissionSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePendingAdmission, error) {
	loaded, err := readManagedCorePendingAdmissionSource(store, e)
	if err != nil {
		return nil, createFoundationRecoveryArtifact{}, nil, err
	}
	return loaded.Stopped, loaded.Admission, loaded.Prior, nil
}

func loadManagedCorePartialSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePartialRecovery, error) {
	if e != nil && managedCoreInterruptedForwardReview(e.Journal) != "" {
		return loadManagedCoreInterruptedSource(store, e)
	}
	if e == nil || managedCorePendingAdmissionReview(e.Journal) == "" {
		return loadManagedCorePartialInitialSource(store, e)
	}
	loaded, err := readManagedCorePendingAdmissionSource(store, e)
	if err != nil {
		return nil, createFoundationRecoveryArtifact{}, nil, err
	}
	loaded.Core.SourceSHA = deploymentplan.Digest(e.Journal)
	loaded.Core.PriorAdmission = loaded.Prior
	return loaded.Historical, loaded.Original, loaded.Core, nil
}

// Only this intended old admission may progress after genuine fresh native
// audit/auth verification. Its immutable captured source remains pending.
func managedCorePendingAdmissionPrefixMatches(source, current environmentCreateJournal, prior *createManagedCorePendingAdmission) bool {
	if prior == nil {
		return deploymentplan.Digest(source) == deploymentplan.Digest(current)
	}
	if prior.SourceSHA != deploymentplan.Digest(source) || len(source.Entries) == 0 || len(current.Entries) != len(source.Entries) || deploymentplan.Digest(source.Entries[len(source.Entries)-1]) != deploymentplan.Digest(prior.Entry) {
		return false
	}
	if deploymentplan.Digest(source) == deploymentplan.Digest(current) {
		return true
	}
	entry := current.Entries[len(source.Entries)-1]
	var receipt map[string]string
	if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(prior.Entry.StartedAt) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || len(receipt) != 2 || receipt["audit_sha256"] != prior.AuditSHA || receipt["qualified_recovery_sha256"] != prior.Review {
		return false
	}
	normalized := current
	normalized.Entries = slices.Clone(current.Entries)
	entry.State, entry.CompletedAt, entry.Receipt = prior.Entry.State, prior.Entry.CompletedAt, prior.Entry.Receipt
	normalized.Entries[len(source.Entries)-1] = entry
	return deploymentplan.Digest(normalized) == deploymentplan.Digest(source)
}

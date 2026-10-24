package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateTerminalFinalizerSchema = "rtk-cloud.create-managed-late-terminal-finalizer-completion/v2"
const managedLateTerminalFinalizerLimit = 30 * time.Minute
const managedLateTerminalFinalizerFilename = "managed-late-terminal-finalizer-authority.json"

// This is a completion of the already recorded native operation, not another
// installation or signing grant. Earlier finalization clocks remain history.
type createManagedLateTerminalFinalizerCompletion struct {
	Schema, SourceSHA, PrefixSHA, ProofSHA, PendingSHA, OriginalAuthoritySHA string
	NativeEvidence                                                           map[string]string
}

type createManagedLateTerminalFinalizerAuthority struct {
	Schema, ParentSHA, SourceSHA, OwnerSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA     string
	DescriptorSHA, CompletionSHA, OriginalAuthoritySHA, OriginalRemainingSHA, OriginalInstalledSHA string
	SessionID, Fingerprint, KeySHA, CSRSHA                                                         string
	StartedAt, Deadline, OverallDeadline, OriginalOverallDeadline                                  time.Time
}

func managedLateTerminalFinalizerPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateTerminalFinalizerSchema, "phase": managedLateTerminalFinalizerLimit.String(), "caller": managedLateOverallLimit.String(), "expiry_reserve": managedLateExpiryReserve.String(), "reserve": managedLateFinalizerLimit.String(), "admission": "paired-exclusive-witness-first", "writes": "same-pending-native-finalizer-and-exact-cleanup"})
}
func managedLateTerminalFinalizerPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", managedLateTerminalFinalizerFilename)
}

func managedLateTerminalFinalizerSelected(j environmentCreateJournal) bool {
	if len(j.Entries) != 396 || !j.CompletedAt.IsZero() {
		return false
	}
	prefix := j
	prefix.Entries = prefix.Entries[:392]
	if !managedLateRemainingSelected(prefix) {
		return false
	}
	for _, entry := range j.Entries[392:395] {
		if entry.State != "completed" || entry.Phase == "" || entry.StartedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
			return false
		}
	}
	last := j.Entries[395]
	return last.Key == "service-session-finalize" && last.Phase == "bootstrap" && last.State == "pending" && !last.StartedAt.IsZero() && last.CompletedAt.IsZero() && len(last.Receipt) == 0
}

func managedLateTerminalFinalizerMembership(e *environmentCreateExecution, r *createManagedLateRemaining, late *createManagedLateSessionRecovery) bool {
	if e == nil || r == nil || late == nil || !managedLateTerminalFinalizerSelected(e.Journal) || !managedLateFinalizationMatches(r, r.FinalizationAuthority) || len(e.Journal.KubernetesLeases) != r.FinalizationAuthority.SourceLeases || r.OwnerSHA != deploymentplan.Digest(e.owner) {
		return false
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:392]
	if deploymentplan.Digest(prefix) != r.FinalizationAuthority.SourceSHA {
		return false
	}
	proof := e.Journal.Entries[392]
	if proof.Key != "openbao-host-ack-verified-"+late.OriginalRepairReview || proof.Phase != "openbao-host-ack-verification" || proof.CompletedAt.After(r.FinalizationAuthority.Deadline) {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range e.Journal.Entries[393:395] {
		matched := false
		for _, ns := range r.Ceremony.BootstrapCredentialNamespaces {
			if ns == r.Ceremony.BootstrapCredentialNamespace || seen[ns] || entry.Key != "bootstrap-copy-retire-"+ns+"-"+r.SessionID {
				continue
			}
			id := "Secret/" + ns + "/" + r.Ceremony.BootstrapCredentialSecret
			intent := deploymentplan.Object{"resource_id": id, "uid": r.Ceremony.CredentialUIDs[ns], "session_id": r.SessionID}
			receipt := deploymentplan.Object{"resource_id": id, "uid": r.Ceremony.CredentialUIDs[ns], "absent": true}
			if entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intent) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(receipt) || entry.StartedAt.Before(proof.CompletedAt) || entry.CompletedAt.After(r.FinalizationAuthority.Deadline) {
				return false
			}
			seen[ns], matched = true, true
		}
		if !matched {
			return false
		}
	}
	last := e.Journal.Entries[395]
	intent := deploymentplan.Object{"session_id": r.SessionID, "issuer_id": r.Ceremony.Intermediate.ID, "cleanup_binding": r.Ceremony.CleanupBinding}
	return len(seen) == 2 && last.IntentSHA256 == activeTransportJournalDigest(intent) && !last.StartedAt.Before(e.Journal.Entries[394].CompletedAt) && !last.StartedAt.Before(r.FinalizationAuthority.StartedAt) && !last.StartedAt.After(r.FinalizationAuthority.Deadline)
}

func loadManagedLateTerminalFinalizerSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, error) {
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createOpenBaoACKBlock("terminal-finalizer-source")
	}
	if e == nil || e.Check(ctx) != nil || !managedLateTerminalSourceSelected(e.Journal) {
		return bad()
	}
	bounded, viewErr := managedLateTerminalPureSourceView(ctx, e)
	if viewErr != nil {
		return nil, viewErr
	}
	r, err := loadManagedLateRemainingSource(ctx, cfg, store, bounded)
	if err != nil {
		return nil, err
	}
	remaining := r.ManagedLateRemaining
	terminalSource := *e
	terminalSource.Journal = e.Journal
	terminalSource.Journal.Entries = terminalSource.Journal.Entries[:396]
	if !managedLateTerminalFinalizerMembership(&terminalSource, remaining, r.ManagedLateSession) {
		return bad()
	}
	// Carry only the newly authenticated historical frame capability; native
	// state and the pending operation are never memoized.
	e.lateSourceReuse = bounded.lateSourceReuse
	proof, err := readCreateOpenBaoACKCompletion(store, e.Journal.PlanSHA256)
	if err != nil || proof == nil || proof.CompletedAt != e.Journal.Entries[392].CompletedAt || activeTransportJournalDigest(proof.Native) != e.Journal.Entries[392].IntentSHA256 {
		return bad()
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, r.ManagedLateSession.OriginalRepairReview)
	if err != nil {
		return nil, err
	}
	if err := validateCreateOpenBaoACKCompletion(ctx, cfg, store, e.Journal.PlanSHA256, original.Plan.Plan.Release, proof, environmentCIImageCommand(cfg), e); err != nil {
		return nil, err
	}
	remaining.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{Schema: managedLateTerminalFinalizerSchema, SourceSHA: deploymentplan.Digest(terminalSource.Journal), PrefixSHA: remaining.CurrentSourceSHA, ProofSHA: deploymentplan.Digest(proof), PendingSHA: deploymentplan.Digest(e.Journal.Entries[395]), OriginalAuthoritySHA: deploymentplan.Digest(remaining.FinalizationAuthority)}
	if e.lateSourceReuse != nil && managedLateSourceReuseActive(e) {
		e.lateSourceReuse.mu.Lock()
		e.lateSourceReuse.boundedSHA = remaining.CurrentSourceSHA
		e.lateSourceReuse.currentSHA = deploymentplan.Digest(e.Journal)
		e.lateSourceReuse.mu.Unlock()
	}
	var marker createManagedLateTerminalFinalizerAuthority
	if createOpenBaoACKCompletionRead(store, managedLateTerminalFinalizerPath(remaining.ParentSHA), &marker) == nil {
		var archived createManagedLateRemaining
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", marker.Review, managedLateRemainingFilename), &archived) != nil || archived.TerminalFinalizerCompletion == nil {
			return bad()
		}
		a, b := *remaining.TerminalFinalizerCompletion, *archived.TerminalFinalizerCompletion
		a.NativeEvidence, b.NativeEvidence = nil, nil
		if deploymentplan.Digest(a) != deploymentplan.Digest(b) {
			return bad()
		}
		remaining.TerminalFinalizerCompletion.NativeEvidence = archived.TerminalFinalizerCompletion.NativeEvidence
	}
	remaining.TerminalAuthority, err = readManagedLateTerminalFinalizerAuthority(store, remaining)
	if err == nil && (managedLatePostNativeCleanupSelected(e.Journal) || managedLateResidualCleanupSelected(e.Journal)) {
		err = loadManagedLatePostNativeCleanupDescriptor(ctx, cfg, store, e, r)
	}
	return r, err
}

func managedLateTerminalFinalizerAuthorityMatches(r *createManagedLateRemaining, a *createManagedLateTerminalFinalizerAuthority) bool {
	if r == nil || r.TerminalFinalizerCompletion == nil || !managedLateFinalizationMatches(r, r.FinalizationAuthority) || a == nil {
		return false
	}
	t := r.TerminalFinalizerCompletion
	if t.Schema != managedLateTerminalFinalizerSchema || t.PrefixSHA != r.CurrentSourceSHA || t.OriginalAuthoritySHA != deploymentplan.Digest(r.FinalizationAuthority) {
		return false
	}
	for _, sha := range []string{t.SourceSHA, t.PrefixSHA, t.ProofSHA, t.PendingSHA, t.OriginalAuthoritySHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.CompletionSHA, a.OriginalAuthoritySHA, a.OriginalRemainingSHA, a.OriginalInstalledSHA, a.Fingerprint, a.KeySHA, a.CSRSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return a.Schema == managedLateTerminalFinalizerSchema && a.ParentSHA == r.ParentSHA && a.SourceSHA == t.SourceSHA && a.OwnerSHA == r.OwnerSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.CompletionSHA == deploymentplan.Digest(t) && a.OriginalAuthoritySHA == deploymentplan.Digest(r.FinalizationAuthority) && a.OriginalRemainingSHA == deploymentplan.Digest(r.Authority) && a.OriginalInstalledSHA == deploymentplan.Digest(r.CleanupAuthority) && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && a.KeySHA == r.KeySHA && a.CSRSHA == r.CSRSHA && !a.StartedAt.IsZero() && !a.StartedAt.Before(r.FinalizationAuthority.StartedAt) && a.OriginalOverallDeadline.Equal(r.FinalizationAuthority.OverallDeadline) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.Deadline.Equal(managedLateTerminalFinalizerDeadline(a.StartedAt, a.OverallDeadline, r.LeafNotAfter, r.SessionExpires)) && !r.LeafNotAfter.Before(a.Deadline.Add(managedLateExpiryReserve)) && !r.SessionExpires.Before(a.Deadline.Add(managedLateExpiryReserve)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
}

func newManagedLateTerminalFinalizerAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLateTerminalFinalizerAuthority, error) {
	bad := func() (*createManagedLateTerminalFinalizerAuthority, error) {
		return nil, createOpenBaoACKBlock("terminal-finalizer-fresh-authority")
	}
	if r == nil || r.TerminalFinalizerCompletion == nil || !managedLateTerminalFinalizerEvidenceValid(r.TerminalFinalizerCompletion.NativeEvidence) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.TerminalFinalizerCompletion.SourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"terminal-completion"] != deploymentplan.Digest(r.TerminalFinalizerCompletion) || a.Plan.Materials[managedLateRemainingMaterial+"terminal-policy"] != managedLateTerminalFinalizerPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	for _, v := range r.TerminalFinalizerCompletion.NativeEvidence {
		if !environmentPreflightDigest.MatchString(v) {
			return bad()
		}
	}
	result := &createManagedLateTerminalFinalizerAuthority{Schema: managedLateTerminalFinalizerSchema, ParentSHA: r.ParentSHA, SourceSHA: r.TerminalFinalizerCompletion.SourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA, DescriptorSHA: managedLateRemainingDigest(r), CompletionSHA: deploymentplan.Digest(r.TerminalFinalizerCompletion), OriginalAuthoritySHA: deploymentplan.Digest(r.FinalizationAuthority), OriginalRemainingSHA: deploymentplan.Digest(r.Authority), OriginalInstalledSHA: deploymentplan.Digest(r.CleanupAuthority), SessionID: r.SessionID, Fingerprint: r.Fingerprint, KeySHA: r.KeySHA, CSRSHA: r.CSRSHA, StartedAt: now, OverallDeadline: overall, OriginalOverallDeadline: r.FinalizationAuthority.OverallDeadline, Deadline: managedLateTerminalFinalizerDeadline(now, overall, r.LeafNotAfter, r.SessionExpires)}
	if !managedLateTerminalFinalizerAuthorityMatches(r, result) {
		return bad()
	}
	return result, nil
}

func readManagedLateTerminalFinalizerAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateTerminalFinalizerAuthority, error) {
	bad := func() (*createManagedLateTerminalFinalizerAuthority, error) {
		return nil, createOpenBaoACKBlock("terminal-finalizer-immutable-authority")
	}
	if r == nil || !environmentPreflightDigest.MatchString(r.ParentSHA) {
		return bad()
	}
	path := filepath.Join(store.Root, managedLateTerminalFinalizerPath(r.ParentSHA))
	raw, err := readManagedLateTerminalFinalizerAuthorityPair(store, r.ParentSHA)
	if err != nil {
		return bad()
	}
	if raw == nil {
		return nil, nil
	}
	var result createManagedLateTerminalFinalizerAuthority
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || decodeEnvironmentJSON(raw, &result) != nil || !managedLateTerminalFinalizerAuthorityMatches(r, &result) {
		return bad()
	}
	canonical, _ := json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return bad()
	}
	qpath := filepath.Join(store.Root, "deployment/create/openbao-recovery", result.Review, "qualification.json")
	qraw, err := readBoundedEnvironmentFile(qpath, true)
	var a createFoundationRecoveryArtifact
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(qpath)) != nil || environmentPreflightBytesSHA256(qraw) != result.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != result.ArtifactSHA {
		return bad()
	}
	copy := *r
	copy.Review = result.Review
	expected, err := newManagedLateTerminalFinalizerAuthority(&copy, a, result.OverallDeadline, result.StartedAt, result.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return bad()
	}
	return &result, nil
}

func admitManagedLateTerminalFinalizer(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if e == nil || e.Check(ctx) != nil || !managedLateTerminalFinalizerSelected(e.Journal) || r == nil || r.TerminalFinalizerCompletion == nil || deploymentplan.Digest(e.Journal) != r.TerminalFinalizerCompletion.SourceSHA {
		return nil, createOpenBaoACKBlock("terminal-finalizer-exact396")
	}
	prior, err := readManagedLateTerminalFinalizerAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if time.Until(prior.Deadline) < managedLateFinalizerLimit {
			return nil, createOpenBaoACKBlock("terminal-finalizer-fixed-budget")
		}
		r.TerminalAuthority = prior
		return r.Authority, nil
	}
	phase, err := newManagedLateTerminalFinalizerAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateTerminalFinalizerPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateTerminalFinalizerPath(r.ParentSHA)) || ensurePrivateDirectory(filepath.Dir(path)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createOpenBaoACKBlock("terminal-finalizer-private-path")
	}
	invalidateManagedLateSourceReuse(e)
	raw, _ := json.Marshal(phase)
	if writeManagedLateTerminalFinalizerAuthorityPair(store, r.ParentSHA, raw) != nil {
		return nil, createOpenBaoACKBlock("terminal-finalizer-authority-exists")
	}
	r.TerminalAuthority = phase
	return r.Authority, e.Check(ctx)
}

func verifyManagedLateTerminalFinalizerSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r != nil && r.completedTail != nil {
		return verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r)
	}
	if e == nil || r == nil || r.ManagedLateRemaining == nil || r.ManagedLateSession == nil || e.Check(ctx) != nil || cfg.Environment != store.Environment || store.Root != e.Store.Root || store.ConfigRoot != e.Store.ConfigRoot || len(e.Journal.Entries) < 396 {
		return createOpenBaoACKBlock("terminal-finalizer-current-source")
	}
	view := *e
	view.Journal = e.Journal
	view.Journal.Entries = view.Journal.Entries[:396]
	if !managedLateTerminalFinalizerMembership(&view, r.ManagedLateRemaining, r.ManagedLateSession) {
		return createOpenBaoACKBlock("terminal-finalizer-original-intent")
	}
	if len(e.Journal.Entries) > 396 {
		var first createServiceSessionObserved
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-observed.json"), &first) != nil {
			return managedHistoricalSeedBlockAt("terminal-finalizer-original-observation")
		}
		if err := validateManagedLateFinalizerTail(e.Journal, r.ManagedLateSession, r.Ceremony, &e.Journal.Entries[395], append([]createServiceSessionObserved{first}, r.StaleSeeds...)); err != nil {
			return err
		}
	}
	t := r.ManagedLateRemaining.TerminalFinalizerCompletion
	if t == nil || t.Schema != managedLateTerminalFinalizerSchema || t.SourceSHA != deploymentplan.Digest(view.Journal) || t.PrefixSHA != r.ManagedLateRemaining.CurrentSourceSHA || t.PendingSHA != deploymentplan.Digest(e.Journal.Entries[395]) || t.OriginalAuthoritySHA != deploymentplan.Digest(r.ManagedLateRemaining.FinalizationAuthority) {
		return createOpenBaoACKBlock("terminal-finalizer-current-descriptor")
	}
	proof, err := readCreateOpenBaoACKCompletion(store, e.Journal.PlanSHA256)
	if err != nil || proof == nil || deploymentplan.Digest(proof) != t.ProofSHA {
		return createOpenBaoACKBlock("terminal-finalizer-original-proof")
	}
	return nil
}

func verifyManagedLateTerminalFinalizerRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.PostNativeCleanup != nil {
		return verifyManagedLatePostNativeCleanupRuntime(ctx, cfg, store, runtime)
	}
	r := runtime.ServiceSessionRecovery
	if err := verifyManagedLateTerminalFinalizerSource(ctx, cfg, store, runtime.Execution, r); err != nil {
		return err
	}
	remaining := r.ManagedLateRemaining
	actual, err := readManagedLateTerminalFinalizerAuthority(store, remaining)
	budget, budgetErr := managedLateActiveBudget(ctx)
	if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(remaining.TerminalAuthority) || !time.Now().Before(actual.Deadline) || budgetErr != nil || budget.TerminalAuthority == nil || deploymentplan.Digest(budget.TerminalAuthority) != deploymentplan.Digest(actual) || runtime.ExecutionSHA256 != remaining.ParentSHA || runtime.Backend.Store.Root != store.Root || runtime.Backend.Store.ConfigRoot != store.ConfigRoot || runtime.Backend.Store.Environment != store.Environment {
		return createOpenBaoACKBlock("terminal-finalizer-runtime-authority")
	}
	return nil
}

func beginManagedLateTerminalFinalizerBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || !managedLateTerminalFinalizerAuthorityMatches(r, r.TerminalAuthority) || time.Until(r.TerminalAuthority.Deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("terminal-finalizer-fixed-budget")
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.Before(r.TerminalAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("terminal-finalizer-original-caller")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"terminal-policy"] != managedLateTerminalFinalizerPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("terminal-finalizer-qualified-policy")
	}
	overall, stopOverall := context.WithDeadline(ctx, r.TerminalAuthority.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: r.TerminalAuthority.Deadline, Overall: overall, Limit: managedLateTerminalFinalizerLimit, TerminalAuthority: r.TerminalAuthority}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
	return phase, func() { cancel(); stopOverall() }, nil
}

func verifyManagedLateTerminalFinalizerAuthorityCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if r != nil && r.PostNativeCleanup != nil {
		return verifyManagedLatePostNativeCleanupAuthorityCurrent(store, e, r)
	}
	if e == nil || r == nil || r.TerminalFinalizerCompletion == nil || len(e.Journal.Entries) < 396 {
		return createOpenBaoACKBlock("terminal-finalizer-current-authority")
	}
	view := e.Journal
	view.Entries = view.Entries[:396]
	if deploymentplan.Digest(view) != r.TerminalFinalizerCompletion.SourceSHA {
		return createOpenBaoACKBlock("terminal-finalizer-immutable-prefix")
	}
	actual, err := readManagedLateTerminalFinalizerAuthority(store, r)
	if err == nil && actual == nil && r.TerminalAuthority == nil && len(e.Journal.Entries) == 396 {
		return nil
	} // read-only qualification; no mutation authority
	if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.TerminalAuthority) || !time.Now().Before(actual.Deadline) {
		return createOpenBaoACKBlock("terminal-finalizer-current-authority")
	}
	return nil
}

// The original393 mirror remains immutable. This separate receipt links the
// completed native intent to the terminal admission under the original cap.
type createManagedLateTerminalFinalizerCompleted struct {
	ResidualRemainingReview                                                  string `json:"residual_remaining_review_sha256,omitempty"`
	ResidualRemainingAuthoritySHA                                            string `json:"residual_remaining_authority_sha256,omitempty"`
	Schema, ParentSHA, Review, AuthoritySHA, SourceSHA, PendingSHA, EntrySHA string
	ResidualReview                                                           string `json:"residual_review_sha256,omitempty"`
	ResidualAuthoritySHA                                                     string `json:"residual_authority_sha256,omitempty"`
	PostNativeReview                                                         string `json:"post_native_review_sha256,omitempty"`
	PostNativeAuthoritySHA                                                   string `json:"post_native_authority_sha256,omitempty"`
	CompletedAt                                                              time.Time
}

func managedLateTerminalFinalizerCompletedPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", "managed-late-terminal-finalizer-completed.json")
}
func completeManagedLateTerminalFinalizer(ctx context.Context, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.TerminalFinalizerCompletion == nil {
		return nil
	}
	remaining := r.ManagedLateRemaining
	phase := remaining.TerminalAuthority
	if e == nil || e.Check(ctx) != nil || !managedLateTerminalFinalizerAuthorityMatches(remaining, phase) {
		return createOpenBaoACKBlock("terminal-finalizer-completed-authority")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	completionStart, completionDeadline := phase.StartedAt, phase.Deadline
	if remaining.PostNativeCleanup != nil {
		if !managedLatePostNativeCleanupAuthorityMatches(remaining, remaining.PostNativeAuthority) {
			return createOpenBaoACKBlock("post-native-cleanup-completed-authority")
		}
		completionStart, completionDeadline = remaining.PostNativeAuthority.StartedAt, remaining.PostNativeAuthority.Deadline
	}
	if remaining.ResidualCleanup != nil && remaining.ResidualRemaining == nil {
		if !managedLateResidualCleanupAuthorityMatches(remaining, remaining.ResidualAuthority) || !managedLateResidualRetirementsComplete(e.Journal, remaining.ResidualCleanup.Approval, remaining.ResidualAuthority.StartedAt, remaining.ResidualAuthority.Deadline) {
			return createOpenBaoACKBlock("residual-cleanup-completed-authority")
		}
		completionStart, completionDeadline = remaining.ResidualAuthority.StartedAt, remaining.ResidualAuthority.Deadline
	}
	if remaining.ResidualRemaining != nil {
		if !managedLateResidualRemainingAuthorityMatches(remaining, remaining.ResidualRemainingAuthority) || !managedLateResidualRemainingRetirementsComplete(e.Journal, remaining) {
			return createOpenBaoACKBlock("residual-remaining-completed-authority")
		}
		completionStart, completionDeadline = remaining.ResidualRemainingAuthority.StartedAt, remaining.ResidualRemainingAuthority.Deadline
	}
	if entry == nil || entry.State != "completed" || entry.CompletedAt.Before(completionStart) || entry.CompletedAt.After(completionDeadline) {
		return createOpenBaoACKBlock("terminal-finalizer-completed-native")
	}
	pending := *entry
	pending.State = "pending"
	pending.Receipt = nil
	pending.CompletedAt = time.Time{}
	if deploymentplan.Digest(pending) != remaining.TerminalFinalizerCompletion.PendingSHA {
		return createOpenBaoACKBlock("terminal-finalizer-original-pending")
	}
	proof := createManagedLateTerminalFinalizerCompleted{Schema: managedLateTerminalFinalizerSchema, ParentSHA: remaining.ParentSHA, Review: phase.Review, AuthoritySHA: deploymentplan.Digest(phase), SourceSHA: phase.SourceSHA, PendingSHA: deploymentplan.Digest(pending), EntrySHA: deploymentplan.Digest(entry), CompletedAt: entry.CompletedAt}
	if remaining.PostNativeCleanup != nil {
		var receipt struct {
			Native environmentCreateBootstrapFinalization `json:"native_finalization"`
			SHA    string                                 `json:"custody_tombstone_sha256"`
			Absent bool                                   `json:"copies_absent"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !receipt.Absent || !managedLatePostNativeCompletionReceiptMatches(remaining.PostNativeCleanup, receipt.Native, receipt.SHA) {
			return createOpenBaoACKBlock("post-native-cleanup-completed-native")
		}
		proof.PostNativeReview = remaining.PostNativeAuthority.Review
		proof.PostNativeAuthoritySHA = deploymentplan.Digest(remaining.PostNativeAuthority)
	}
	if remaining.ResidualCleanup != nil {
		proof.ResidualReview = remaining.ResidualAuthority.Review
		proof.ResidualAuthoritySHA = deploymentplan.Digest(remaining.ResidualAuthority)
	}
	if remaining.ResidualRemaining != nil {
		proof.ResidualRemainingReview = remaining.ResidualRemainingAuthority.Review
		proof.ResidualRemainingAuthoritySHA = deploymentplan.Digest(remaining.ResidualRemainingAuthority)
	}
	invalidateManagedLateSourceReuse(e)
	return writeEnvironmentCreateDerivedPrivateFile(e.Store, managedLateTerminalFinalizerCompletedPath(remaining.ParentSHA), mustProviderRecoveryJSON(proof))
}
func validateManagedLateTerminalFinalizerCompletion(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) error {
	if e == nil {
		return createOpenBaoACKBlock("terminal-finalizer-completed-parent")
	}
	_, err := os.Lstat(filepath.Join(store.Root, managedLateTerminalFinalizerPath(e.Journal.PlanSHA256)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return createOpenBaoACKBlock("terminal-finalizer-completed-marker")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	// Historical393 is readable while the genuine original finalizer is pending.
	if entry != nil && entry.State == "pending" {
		return nil
	}
	var phaseHeader createManagedLateTerminalFinalizerAuthority
	if createOpenBaoACKCompletionRead(store, managedLateTerminalFinalizerPath(e.Journal.PlanSHA256), &phaseHeader) != nil {
		return createOpenBaoACKBlock("terminal-finalizer-completed-marker")
	}
	var r createManagedLateRemaining
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", phaseHeader.Review, managedLateRemainingFilename), &r) != nil || r.Review != phaseHeader.Review || r.TerminalFinalizerCompletion == nil || r.OriginalReview != late.Review {
		return createOpenBaoACKBlock("terminal-finalizer-completed-source")
	}
	postPair, postPairErr := readManagedLatePostNativeCleanupPair(store, r.ParentSHA)
	if postPairErr != nil {
		return postPairErr
	}
	if postPair != nil {
		var header createManagedLatePostNativeCleanupAuthority
		var post createManagedLateRemaining
		if decodeEnvironmentJSON(postPair, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &post) != nil || post.Review != header.Review || post.PostNativeCleanup == nil || managedLateRemainingDigest(&post) != managedLateRemainingDigest(&r) || deploymentplan.Digest(post.TerminalFinalizerCompletion) != deploymentplan.Digest(r.TerminalFinalizerCompletion) {
			return createOpenBaoACKBlock("post-native-cleanup-completed-source")
		}
		r = post
	}
	residualPair, residualPairErr := readManagedLateResidualCleanupPair(store, r.ParentSHA)
	if residualPairErr != nil {
		return residualPairErr
	}
	if residualPair != nil {
		var header createManagedLateResidualCleanupAuthority
		var residual createManagedLateRemaining
		if decodeEnvironmentJSON(residualPair, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &residual) != nil || residual.Review != header.Review || residual.ResidualCleanup == nil || managedLateRemainingDigest(&residual) != managedLateRemainingDigest(&r) || deploymentplan.Digest(residual.PostNativeCleanup) != deploymentplan.Digest(r.PostNativeCleanup) {
			return createOpenBaoACKBlock("residual-cleanup-completed-source")
		}
		r = residual
	}
	remainingPair, remainingPairErr := readManagedLateResidualRemainingPair(store, r.ParentSHA)
	if remainingPairErr != nil {
		return remainingPairErr
	}
	if remainingPair != nil {
		var header createManagedLateResidualCleanupAuthority
		var remaining createManagedLateRemaining
		if decodeEnvironmentJSON(remainingPair, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &remaining) != nil || remaining.Review != header.Review || remaining.ResidualRemaining == nil || managedLateRemainingDigest(&remaining) != managedLateRemainingDigest(&r) || deploymentplan.Digest(remaining.ResidualCleanup) != deploymentplan.Digest(r.ResidualCleanup) {
			return createOpenBaoACKBlock("residual-remaining-completed-source")
		}
		r = remaining
	}
	var loadErr error
	r.Authority, loadErr = readManagedLateRemainingAuthority(store, &r)
	if loadErr != nil {
		return loadErr
	}
	r.CleanupAuthority, loadErr = readManagedLateInstalledCleanupAuthority(store, &r)
	if loadErr != nil {
		return loadErr
	}
	r.FinalizationAuthority, loadErr = readManagedLateFinalizationAuthority(store, &r)
	if loadErr != nil {
		return loadErr
	}
	phase, loadErr := readManagedLateTerminalFinalizerAuthority(store, &r)
	if loadErr != nil || phase == nil {
		return createOpenBaoACKBlock("terminal-finalizer-completed-authority")
	}
	r.TerminalAuthority = phase
	completionStart, completionDeadline := phase.StartedAt, phase.Deadline
	if r.PostNativeCleanup != nil {
		r.PostNativeAuthority, loadErr = readManagedLatePostNativeCleanupAuthority(store, &r)
		if loadErr != nil || r.PostNativeAuthority == nil {
			return createOpenBaoACKBlock("post-native-cleanup-completed-authority")
		}
		completionStart, completionDeadline = r.PostNativeAuthority.StartedAt, r.PostNativeAuthority.Deadline
	}
	if r.ResidualCleanup != nil {
		r.ResidualAuthority, loadErr = readManagedLateResidualCleanupAuthority(store, &r)
		if loadErr != nil || r.ResidualAuthority == nil || r.ResidualRemaining == nil && !managedLateResidualRetirementsComplete(e.Journal, r.ResidualCleanup.Approval, r.ResidualAuthority.StartedAt, r.ResidualAuthority.Deadline) {
			return createOpenBaoACKBlock("residual-cleanup-completed-authority")
		}
		completionStart, completionDeadline = r.ResidualAuthority.StartedAt, r.ResidualAuthority.Deadline
	}
	if r.ResidualRemaining != nil {
		r.ResidualRemainingAuthority, loadErr = readManagedLateResidualRemainingAuthority(store, &r)
		if loadErr != nil || r.ResidualRemainingAuthority == nil || !managedLateResidualRemainingRetirementsComplete(e.Journal, &r) {
			return createOpenBaoACKBlock("residual-remaining-completed-authority")
		}
		completionStart, completionDeadline = r.ResidualRemainingAuthority.StartedAt, r.ResidualRemainingAuthority.Deadline
	}
	if entry == nil || entry.State != "completed" || entry.CompletedAt.Before(completionStart) || entry.CompletedAt.After(completionDeadline) {
		return createOpenBaoACKBlock("terminal-finalizer-completed-authority")
	}
	var proof createManagedLateTerminalFinalizerCompleted
	pending := *entry
	pending.State = "pending"
	pending.Receipt = nil
	pending.CompletedAt = time.Time{}
	if createOpenBaoACKCompletionRead(store, managedLateTerminalFinalizerCompletedPath(r.ParentSHA), &proof) != nil || proof.Schema != managedLateTerminalFinalizerSchema || proof.ParentSHA != r.ParentSHA || proof.Review != phase.Review || proof.AuthoritySHA != deploymentplan.Digest(phase) || proof.SourceSHA != phase.SourceSHA || proof.PendingSHA != deploymentplan.Digest(pending) || proof.PendingSHA != r.TerminalFinalizerCompletion.PendingSHA || proof.EntrySHA != deploymentplan.Digest(entry) || !proof.CompletedAt.Equal(entry.CompletedAt) {
		return createOpenBaoACKBlock("terminal-finalizer-completed-receipt")
	}
	if r.ResidualCleanup != nil {
		if proof.ResidualReview != r.ResidualAuthority.Review || proof.ResidualAuthoritySHA != deploymentplan.Digest(r.ResidualAuthority) {
			return createOpenBaoACKBlock("residual-cleanup-completed-receipt")
		}
	} else if proof.ResidualReview != "" || proof.ResidualAuthoritySHA != "" {
		return createOpenBaoACKBlock("residual-cleanup-completed-marker")
	}
	if r.ResidualRemaining != nil {
		if proof.ResidualRemainingReview != r.ResidualRemainingAuthority.Review || proof.ResidualRemainingAuthoritySHA != deploymentplan.Digest(r.ResidualRemainingAuthority) {
			return createOpenBaoACKBlock("residual-remaining-completed-receipt")
		}
	} else if proof.ResidualRemainingReview != "" || proof.ResidualRemainingAuthoritySHA != "" {
		return createOpenBaoACKBlock("residual-remaining-completed-marker")
	}
	if r.PostNativeCleanup != nil {
		if proof.PostNativeReview != r.PostNativeAuthority.Review || proof.PostNativeAuthoritySHA != deploymentplan.Digest(r.PostNativeAuthority) || len(e.Journal.Entries) < 400 || len(e.Journal.KubernetesLeases) < r.FinalizationAuthority.SourceLeases {
			return createOpenBaoACKBlock("post-native-cleanup-completed-receipt")
		}
		postPrefix := e.Journal
		postPrefix.CompletedAt = time.Time{}
		postPrefix.Entries = append([]environmentCreateJournalEntry(nil), postPrefix.Entries[:400]...)
		postPrefix.Entries[395] = pending
		postPrefix.KubernetesLeases = postPrefix.KubernetesLeases[:r.FinalizationAuthority.SourceLeases]
		if deploymentplan.Digest(postPrefix) != r.PostNativeCleanup.SourceSHA || deploymentplan.Digest(postPrefix.Entries[396]) != r.PostNativeCleanup.CustodyEntrySHA {
			return createOpenBaoACKBlock("post-native-cleanup-completed-prefix")
		}
	} else if proof.PostNativeReview != "" || proof.PostNativeAuthoritySHA != "" {
		return createOpenBaoACKBlock("post-native-cleanup-completed-marker")
	}
	if len(e.Journal.Entries) < 396 || len(e.Journal.KubernetesLeases) < r.FinalizationAuthority.SourceLeases {
		return createOpenBaoACKBlock("terminal-finalizer-completed-prefix")
	}
	prefix := e.Journal
	prefix.CompletedAt = time.Time{}
	prefix.Entries = append([]environmentCreateJournalEntry(nil), prefix.Entries[:396]...)
	prefix.Entries[395] = pending
	prefix.KubernetesLeases = prefix.KubernetesLeases[:r.FinalizationAuthority.SourceLeases]
	if deploymentplan.Digest(prefix) != phase.SourceSHA {
		return createOpenBaoACKBlock("terminal-finalizer-completed-prefix")
	}
	var receipt struct {
		Native environmentCreateBootstrapFinalization `json:"native_finalization"`
		SHA    string                                 `json:"custody_tombstone_sha256"`
		Absent bool                                   `json:"copies_absent"`
	}
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !receipt.Absent || receipt.Native.SessionID != r.SessionID || receipt.Native.LeafSHA256 != r.Fingerprint || receipt.Native.FinalizedAt.Before(phase.StartedAt) || receipt.Native.FinalizedAt.After(entry.CompletedAt) || receipt.Native.FinalizedAt.After(phase.Deadline) || !environmentPreflightDigest.MatchString(receipt.SHA) {
		return createOpenBaoACKBlock("terminal-finalizer-completed-native")
	}
	if r.PostNativeCleanup != nil && !managedLatePostNativeCompletionReceiptMatches(r.PostNativeCleanup, receipt.Native, receipt.SHA) {
		return createOpenBaoACKBlock("post-native-cleanup-completed-native")
	}
	return nil
}

func managedLateTerminalFinalizerEvidenceValid(evidence map[string]string) bool {
	keys := []string{"owners", "all-volumes", "clients", "host-servers", "api-servers", "ca-registry", "current-crls", "sealed-session", "revocation", "service-crl", "service-crl-acks", "copies", "key", "csr"}
	if len(evidence) != len(keys) {
		return false
	}
	for _, key := range keys {
		if !environmentPreflightDigest.MatchString(evidence[key]) {
			return false
		}
	}
	return true
}

// Admission writes end the qualification phase. Re-derive immutable history
// once under a new396-bound manifest before the sole native mutation boundary.
func verifyManagedLateTerminalFinalizerMutation(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if err := verifyManagedLateTerminalFinalizerRuntime(ctx, cfg, store, runtime); err != nil {
		return err
	}
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if !managedLateSourceReuseActive(e) {
		loaded, err := loadManagedLateTerminalFinalizerSource(ctx, cfg, store, e)
		if err != nil {
			return err
		}
		if loaded.ManagedLateRemaining == nil || deploymentplan.Digest(loaded.ManagedLateRemaining.TerminalFinalizerCompletion) != deploymentplan.Digest(r.ManagedLateRemaining.TerminalFinalizerCompletion) {
			return createOpenBaoACKBlock("terminal-finalizer-mutation-source")
		}
	}
	observed := createFoundationRecoveryPlan{Materials: map[string]string{}}
	if err := verifyManagedLateTerminalFinalizerNative(ctx, cfg, store, e, runtime.Backend, r, &observed); err != nil {
		return err
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	return verifyManagedLateTerminalFinalizerRuntime(ctx, cfg, store, runtime)
}

// Re-establish only immutable ancestry after a known journal/cleanup write.
// The full existing finalizer-tail and native finalization checks still run.
func beginManagedLateTerminalHistoricalSourcePhase(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.TerminalFinalizerCompletion == nil {
		return nil
	}
	if err := verifyManagedLateTerminalFinalizerRuntime(ctx, cfg, store, environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: environmentKubernetesBackend{Store: store}, ServiceSessionRecovery: r}); err != nil {
		return err
	}
	if managedLateSourceReuseActive(e) {
		return checkManagedLateSourceReuse(e)
	}
	bounded, viewErr := managedLateTerminalPureSourceView(ctx, e)
	if viewErr != nil {
		return viewErr
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, bounded, r.ManagedLateSession); err != nil {
		return err
	}
	e.lateSourceReuse = bounded.lateSourceReuse
	if e.lateSourceReuse == nil {
		return managedLateSourceReuseBlock()
	}
	e.lateSourceReuse.mu.Lock()
	e.lateSourceReuse.boundedSHA = r.ManagedLateRemaining.CurrentSourceSHA
	e.lateSourceReuse.currentSHA = deploymentplan.Digest(e.Journal)
	e.lateSourceReuse.mu.Unlock()
	return checkManagedLateSourceReuse(e)
}

// The optional terminal argument of a native reader grants only its normal
// read after actual396 and the immutable392 source have both authenticated.
func verifyManagedLateTerminalNativeSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r != nil && r.completedTail != nil {
		return verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r)
	}
	if r == nil || verifyManagedLateTerminalFinalizerSource(ctx, cfg, store, e, r) != nil {
		return createOpenBaoACKBlock("terminal-finalizer-native-source")
	}
	historical, err := readManagedLateFinalizationAuthority(store, r.ManagedLateRemaining)
	if err != nil || historical == nil || deploymentplan.Digest(historical) != deploymentplan.Digest(r.ManagedLateRemaining.FinalizationAuthority) {
		return createOpenBaoACKBlock("terminal-finalizer-native-source-authority")
	}
	view, err := managedLateTerminalPureSourceView(ctx, e)
	if err != nil {
		return err
	}
	source, admitted, err := managedLateSessionRuntimeProgress(store, view, r.ManagedLateSession)
	if err != nil || source == nil || admitted == nil || deploymentplan.Digest(source.Journal) != r.ManagedLateSession.SourceSHA {
		return createOpenBaoACKBlock("terminal-finalizer-native-source-progress")
	}
	return nil
}

func managedLateTerminalFinalizerDeadline(start, overall, leaf, session time.Time) time.Time {
	deadline := start.Add(managedLateTerminalFinalizerLimit)
	for _, cutoff := range []time.Time{overall, leaf.Add(-managedLateExpiryReserve), session.Add(-managedLateExpiryReserve)} {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}

// The native payload and exact raw tombstone hash are a single qualified proof.
func managedLatePostNativeCompletionReceiptMatches(post *createManagedLatePostNativeCleanup, native environmentCreateBootstrapFinalization, tombstoneSHA string) bool {
	return post != nil && environmentPreflightDigest.MatchString(tombstoneSHA) && tombstoneSHA == post.TombstoneSHA && managedLateFinalizationEqual(native, post.Native)
}

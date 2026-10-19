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

const managedLateFinalizationOnlySchema = "rtk-cloud.create-managed-late-finalization-only/v1"
const managedLateFinalizationAuthorityFilename = "managed-late-finalization-authority.json"

type createManagedLateFinalizationAuthority struct {
	createManagedLateInstalledCleanupAuthority
	OriginalInstalledAuthoritySHA, OriginalInstalledQualifiedBytesSHA, LogProofSHA string
}

func managedLateFinalizationPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateFinalizationOnlySchema, "native": managedLateNativeLimit.String(), "caller": managedLateOverallLimit.String(), "reserve": managedLateExpiryReserve.String(), "finalizer": managedLateFinalizerLimit.String()})
}
func managedLateFinalizationAuthorityPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", managedLateFinalizationAuthorityFilename)
}
func managedLateFinalizationDeadline(start, overall, leaf, session time.Time) time.Time {
	deadline := start.Add(managedLateNativeLimit)
	for _, cutoff := range []time.Time{overall, leaf.Add(-managedLateExpiryReserve), session.Add(-managedLateExpiryReserve)} {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}
func managedLateFinalizationAuthorityValid(a *createManagedLateFinalizationAuthority) bool {
	if a == nil || a.Schema != managedLateFinalizationOnlySchema || a.PolicySHA != managedLateFinalizationPolicySHA() || !environmentBootstrapOwnerUUID.MatchString(a.SessionID) || a.SourceLeases < 3 {
		return false
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.OriginalAuthoritySHA, a.OriginalQualifiedBytesSHA, a.OriginalInstalledAuthoritySHA, a.OriginalInstalledQualifiedBytesSHA, a.LogProofSHA, a.DescriptorSHA, a.HelperObservationSHA, a.Fingerprint, a.KeySHA, a.CSRSHA, a.SeedObservationSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return !a.StartedAt.IsZero() && !a.InstalledAt.IsZero() && !a.StartedAt.Before(a.InstalledAt) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.LeafNotAfter.Equal(a.SessionExpires) && a.Deadline.Equal(managedLateFinalizationDeadline(a.StartedAt, a.OverallDeadline, a.LeafNotAfter, a.SessionExpires)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
}
func managedLateFinalizationMatches(r *createManagedLateRemaining, a *createManagedLateFinalizationAuthority) bool {
	return r != nil && r.FinalizationOnly && managedLateInstalledCleanupMatches(r, r.CleanupAuthority) && managedLateFinalizationAuthorityValid(a) && a.ParentSHA == r.ParentSHA && a.SourceSHA == r.CurrentSourceSHA && a.SourceLeases == r.SourceLeases+3 && a.OwnerSHA == r.OwnerSHA && a.OriginalAuthoritySHA == deploymentplan.Digest(r.Authority) && a.OriginalQualifiedBytesSHA == r.Authority.QualifiedBytesSHA && a.OriginalInstalledAuthoritySHA == deploymentplan.Digest(r.CleanupAuthority) && a.OriginalInstalledQualifiedBytesSHA == r.CleanupAuthority.QualifiedBytesSHA && a.LogProofSHA == r.LogProofSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.HelperObservationSHA == r.HelperObservationSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && a.KeySHA == r.KeySHA && a.CSRSHA == r.CSRSHA && a.SeedObservationSHA == r.SeedObservationSHA && a.InstalledAt.Equal(r.InstalledAt) && a.LeafNotAfter.Equal(r.LeafNotAfter) && a.SessionExpires.Equal(r.SessionExpires)
}
func newManagedLateFinalizationAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLateFinalizationAuthority, error) {
	bad := func() (*createManagedLateFinalizationAuthority, error) {
		return nil, createOpenBaoACKBlock("finalization-only-fresh-authority")
	}
	if r == nil || !r.FinalizationOnly || !managedLateInstalledCleanupMatches(r, r.CleanupAuthority) || !environmentPreflightDigest.MatchString(r.LogProofSHA) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.CurrentSourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"finalization-policy"] != managedLateFinalizationPolicySHA() || a.Plan.Materials[managedLateRemainingMaterial+"log-proof"] != r.LogProofSHA || a.Plan.Materials[managedLateRemainingMaterial+"cleanup-authority"] != deploymentplan.Digest(r.CleanupAuthority) || a.Plan.Materials[managedLateRemainingMaterial+"descriptor"] != managedLateRemainingDigest(r) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	result := &createManagedLateFinalizationAuthority{createManagedLateInstalledCleanupAuthority: createManagedLateInstalledCleanupAuthority{Schema: managedLateFinalizationOnlySchema, ParentSHA: r.ParentSHA, SourceSHA: r.CurrentSourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA, OriginalAuthoritySHA: deploymentplan.Digest(r.Authority), OriginalQualifiedBytesSHA: r.Authority.QualifiedBytesSHA, DescriptorSHA: managedLateRemainingDigest(r), HelperObservationSHA: r.HelperObservationSHA, SessionID: r.SessionID, Fingerprint: r.Fingerprint, KeySHA: r.KeySHA, CSRSHA: r.CSRSHA, SeedObservationSHA: r.SeedObservationSHA, PolicySHA: managedLateFinalizationPolicySHA(), SourceLeases: r.SourceLeases + 3, InstalledAt: r.InstalledAt, StartedAt: now, OverallDeadline: overall, LeafNotAfter: r.LeafNotAfter, SessionExpires: r.SessionExpires}, OriginalInstalledAuthoritySHA: deploymentplan.Digest(r.CleanupAuthority), OriginalInstalledQualifiedBytesSHA: r.CleanupAuthority.QualifiedBytesSHA, LogProofSHA: r.LogProofSHA}
	result.Deadline = managedLateFinalizationDeadline(now, overall, r.LeafNotAfter, r.SessionExpires)
	if !managedLateFinalizationMatches(r, result) {
		return bad()
	}
	return result, nil
}
func readManagedLateFinalizationAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateFinalizationAuthority, error) {
	bad := func() (*createManagedLateFinalizationAuthority, error) {
		return nil, createOpenBaoACKBlock("finalization-only-immutable-authority")
	}
	if r == nil || !environmentPreflightDigest.MatchString(r.ParentSHA) {
		return bad()
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateFinalizationAuthorityPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateFinalizationAuthorityPath(r.ParentSHA)) {
		return bad()
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var result createManagedLateFinalizationAuthority
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || decodeEnvironmentJSON(raw, &result) != nil || !managedLateFinalizationMatches(r, &result) {
		return bad()
	}
	canonical, _ := json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return bad()
	}
	var a createFoundationRecoveryArtifact
	qpath := filepath.Join(store.Root, "deployment/create/openbao-recovery", result.Review, "qualification.json")
	qraw, err := readBoundedEnvironmentFile(qpath, true)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(qpath)) != nil || environmentPreflightBytesSHA256(qraw) != result.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != result.ArtifactSHA {
		return bad()
	}
	copy := *r
	copy.Review = result.Review
	expected, err := newManagedLateFinalizationAuthority(&copy, a, result.OverallDeadline, result.StartedAt, result.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return bad()
	}
	return &result, nil
}
func verifyManagedLateFinalizationCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if e == nil || r == nil || r.FinalizationAuthority == nil || len(e.Journal.Entries) < 392 || len(e.Journal.KubernetesLeases) < r.FinalizationAuthority.SourceLeases {
		return createOpenBaoACKBlock("finalization-only-current-source")
	}
	originalInstalled, readErr := readManagedLateInstalledCleanupAuthority(store, r)
	if readErr != nil || originalInstalled == nil || deploymentplan.Digest(originalInstalled) != deploymentplan.Digest(r.CleanupAuthority) {
		return createOpenBaoACKBlock("finalization-only-installed-history")
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:392]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:r.FinalizationAuthority.SourceLeases]
	actual, err := readManagedLateFinalizationAuthority(store, r)
	if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.FinalizationAuthority) || deploymentplan.Digest(prefix) != actual.SourceSHA || !time.Now().Before(actual.Deadline) {
		return createOpenBaoACKBlock("finalization-only-current-authority")
	}
	return nil
}
func admitManagedLateFinalization(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if len(e.Journal.Entries) != 392 {
		return nil, createOpenBaoACKBlock("finalization-only-exact392")
	}
	prior, err := readManagedLateFinalizationAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if time.Until(prior.Deadline) < managedLateFinalizerLimit {
			return nil, createOpenBaoACKBlock("finalization-only-fixed-budget")
		}
		r.FinalizationAuthority = prior
		return r.Authority, verifyManagedLateFinalizationCurrent(store, e, r)
	}
	phase, err := newManagedLateFinalizationAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateFinalizationAuthorityPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateFinalizationAuthorityPath(r.ParentSHA)) || ensurePrivateDirectory(filepath.Dir(path)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createOpenBaoACKBlock("finalization-only-private-path")
	}
	invalidateManagedLateSourceReuse(e)
	raw, _ := json.Marshal(phase)
	if writeExclusivePrivateDeploymentJSON(path, raw) != nil {
		return nil, createOpenBaoACKBlock("finalization-only-authority-exists")
	}
	r.FinalizationAuthority = phase
	return r.Authority, e.Check(ctx)
}
func beginManagedLateFinalizationBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || !managedLateFinalizationMatches(r, r.FinalizationAuthority) || time.Until(r.FinalizationAuthority.Deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("finalization-only-fixed-budget")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("finalization-only-caller-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"finalization-policy"] != managedLateFinalizationPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("finalization-only-qualified-policy")
	}
	overall, stopOverall := context.WithDeadline(ctx, r.FinalizationAuthority.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: r.FinalizationAuthority.Deadline, Overall: overall, Limit: managedLateNativeLimit, FinalizationAuthority: r.FinalizationAuthority}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
	return phase, func() { cancel(); stopOverall() }, nil
}

func verifyManagedLateFinalizationReadSource(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if e == nil || r == nil || !r.FinalizationOnly || len(e.Journal.Entries) != 392 || !managedLateRemainingSelected(e.Journal) || e.Check(context.Background()) != nil || r.CurrentSourceSHA != deploymentplan.Digest(e.Journal) || managedLateRemainingRuntimePrefix(e, r) != nil {
		return createOpenBaoACKBlock("finalization-only-read-source")
	}
	_, markerErr := os.Lstat(filepath.Join(store.Root, managedLateFinalizationAuthorityPath(r.ParentSHA)))
	if !errors.Is(markerErr, os.ErrNotExist) {
		return createOpenBaoACKBlock("finalization-only-read-unknown-authority")
	}
	original, err := readManagedLateRemainingAuthority(store, r)
	if err != nil || original == nil || deploymentplan.Digest(original) != deploymentplan.Digest(r.Authority) {
		return createOpenBaoACKBlock("finalization-only-original-authority")
	}
	installed, err := readManagedLateInstalledCleanupAuthority(store, r)
	if err != nil || installed == nil || deploymentplan.Digest(installed) != deploymentplan.Digest(r.CleanupAuthority) {
		return createOpenBaoACKBlock("finalization-only-installed-authority")
	}
	return nil
}
func validateManagedLateFinalizationCompletion(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, proof *createOpenBaoACKCompletion) error {
	bad := func() error { return createOpenBaoACKBlock("completed-finalization-only-authority") }
	if e == nil || late == nil || proof == nil {
		return bad()
	}
	review := deploymentplan.String(proof.Native["finalization_only_review_sha256"])
	sha := deploymentplan.String(proof.Native["finalization_only_authority_sha256"])
	_, markerErr := os.Lstat(filepath.Join(store.Root, managedLateFinalizationAuthorityPath(e.Journal.PlanSHA256)))
	if review == "" && sha == "" && errors.Is(markerErr, os.ErrNotExist) {
		return nil
	}
	if !environmentPreflightDigest.MatchString(review) || !environmentPreflightDigest.MatchString(sha) {
		return bad()
	}
	var r createManagedLateRemaining
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateRemainingFilename), &r) != nil || r.Review != review || !r.FinalizationOnly || r.OriginalReview != late.Review || r.DescriptorSHA != managedLateSessionSourceDigest(late) || r.SessionID != late.NewSessionID {
		return bad()
	}
	var err error
	r.Authority, err = readManagedLateRemainingAuthority(store, &r)
	if err != nil || r.Authority == nil {
		return bad()
	}
	r.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, &r)
	if err != nil || r.CleanupAuthority == nil {
		return bad()
	}
	phase, err := readManagedLateFinalizationAuthority(store, &r)
	if err != nil || phase == nil || phase.Review != review || deploymentplan.Digest(phase) != sha || proof.CompletedAt.Before(phase.StartedAt) || proof.CompletedAt.After(phase.Deadline) || len(e.Journal.Entries) < 392 || len(e.Journal.KubernetesLeases) < phase.SourceLeases {
		return bad()
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:392]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:phase.SourceLeases]
	log, err := readManagedLateLogCompletion(store, e, late, proof.CompletedAt)
	if err != nil || log == nil || deploymentplan.Digest(log) != phase.LogProofSHA || deploymentplan.Digest(prefix) != phase.SourceSHA || phase.OriginalAuthoritySHA != deploymentplan.String(proof.Native["late_remaining_authority_sha256"]) || phase.OriginalInstalledAuthoritySHA != deploymentplan.String(proof.Native["installed_cleanup_authority_sha256"]) {
		return bad()
	}
	return nil
}

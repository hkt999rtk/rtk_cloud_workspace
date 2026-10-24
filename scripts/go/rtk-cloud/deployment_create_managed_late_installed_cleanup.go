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

const managedLateInstalledCleanupSchema = "rtk-cloud.create-managed-late-installed-cleanup/v1"
const managedLateInstalledCleanupFilename = "managed-late-installed-cleanup-authority.json"
const managedLateInstalledCleanupLimit = 30 * time.Minute

type createManagedLateInstalledCleanupAuthority struct {
	Schema, ParentSHA, SourceSHA, OwnerSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA string
	OriginalAuthoritySHA, OriginalQualifiedBytesSHA, DescriptorSHA, HelperObservationSHA       string
	SessionID, Fingerprint, KeySHA, CSRSHA, SeedObservationSHA, PolicySHA                      string
	SourceLeases                                                                               int
	InstalledAt, StartedAt, Deadline, OverallDeadline, LeafNotAfter, SessionExpires            time.Time
}

func managedLateInstalledCleanupPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateInstalledCleanupSchema, "phase": managedLateInstalledCleanupLimit.String(), "caller": managedLateOverallLimit.String(), "reserve": managedLateExpiryReserve.String(), "finalizer": managedLateFinalizerLimit.String()})
}
func managedLateInstalledCleanupPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", managedLateInstalledCleanupFilename)
}
func managedLateInstalledCleanupDeadline(epoch, overall, leaf, session time.Time) time.Time {
	deadline := epoch.Add(managedLateInstalledCleanupLimit)
	for _, cutoff := range []time.Time{overall, leaf.Add(-managedLateExpiryReserve), session.Add(-managedLateExpiryReserve)} {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}
func managedLateInstalledCleanupAuthorityValid(a *createManagedLateInstalledCleanupAuthority) bool {
	if a == nil || a.Schema != managedLateInstalledCleanupSchema || a.PolicySHA != managedLateInstalledCleanupPolicySHA() || !environmentBootstrapOwnerUUID.MatchString(a.SessionID) || a.SourceLeases < 1 {
		return false
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.OriginalAuthoritySHA, a.OriginalQualifiedBytesSHA, a.DescriptorSHA, a.HelperObservationSHA, a.Fingerprint, a.KeySHA, a.CSRSHA, a.SeedObservationSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return !a.InstalledAt.IsZero() && !a.StartedAt.Before(a.InstalledAt) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.LeafNotAfter.Equal(a.SessionExpires) && a.Deadline.Equal(managedLateInstalledCleanupDeadline(a.StartedAt, a.OverallDeadline, a.LeafNotAfter, a.SessionExpires)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
}
func managedLateInstalledCleanupMatches(r *createManagedLateRemaining, a *createManagedLateInstalledCleanupAuthority) bool {
	return r != nil && r.CleanupOnly && managedLateRemainingAuthorityMatches(r, r.Authority) && managedLateInstalledCleanupAuthorityValid(a) && a.ParentSHA == r.ParentSHA && a.SourceSHA == r.CleanupSourceSHA && a.OwnerSHA == r.OwnerSHA && a.OriginalAuthoritySHA == deploymentplan.Digest(r.Authority) && a.OriginalQualifiedBytesSHA == r.Authority.QualifiedBytesSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.HelperObservationSHA == r.HelperObservationSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && a.KeySHA == r.KeySHA && a.CSRSHA == r.CSRSHA && a.SeedObservationSHA == r.SeedObservationSHA && a.SourceLeases == r.SourceLeases+1 && a.InstalledAt.Equal(r.InstalledAt) && !r.InstalledAt.Before(r.Authority.AdmittedAt) && !r.InstalledAt.After(r.Authority.Deadline) && a.LeafNotAfter.Equal(r.LeafNotAfter) && a.SessionExpires.Equal(r.SessionExpires)
}
func newManagedLateInstalledCleanupAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLateInstalledCleanupAuthority, error) {
	bad := func() (*createManagedLateInstalledCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("installed-cleanup-fresh-authority")
	}
	if r == nil || !r.CleanupOnly || r.CurrentSourceSHA != r.CleanupSourceSHA || !managedLateRemainingAuthorityMatches(r, r.Authority) || r.InstalledAt.IsZero() || r.InstalledAt.Before(r.Authority.AdmittedAt) || r.InstalledAt.After(r.Authority.Deadline) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.CleanupSourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"cleanup-policy"] != managedLateInstalledCleanupPolicySHA() || a.Plan.Materials[managedLateRemainingMaterial+"helper-observation"] != r.HelperObservationSHA || a.Plan.Materials[managedLateRemainingMaterial+"descriptor"] != managedLateRemainingDigest(r) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	result := &createManagedLateInstalledCleanupAuthority{Schema: managedLateInstalledCleanupSchema, ParentSHA: r.ParentSHA, SourceSHA: r.CleanupSourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA, OriginalAuthoritySHA: deploymentplan.Digest(r.Authority), OriginalQualifiedBytesSHA: r.Authority.QualifiedBytesSHA, DescriptorSHA: managedLateRemainingDigest(r), HelperObservationSHA: r.HelperObservationSHA, SessionID: r.SessionID, Fingerprint: r.Fingerprint, KeySHA: r.KeySHA, CSRSHA: r.CSRSHA, SeedObservationSHA: r.SeedObservationSHA, PolicySHA: managedLateInstalledCleanupPolicySHA(), SourceLeases: r.SourceLeases + 1, InstalledAt: r.InstalledAt, StartedAt: now, OverallDeadline: overall, LeafNotAfter: r.LeafNotAfter, SessionExpires: r.SessionExpires}
	result.Deadline = managedLateInstalledCleanupDeadline(now, overall, r.LeafNotAfter, r.SessionExpires)
	if !managedLateInstalledCleanupMatches(r, result) {
		return bad()
	}
	return result, nil
}
func readManagedLateInstalledCleanupAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateInstalledCleanupAuthority, error) {
	bad := func() (*createManagedLateInstalledCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("installed-cleanup-immutable-authority")
	}
	if r == nil || !environmentPreflightDigest.MatchString(r.ParentSHA) {
		return bad()
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateInstalledCleanupPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateInstalledCleanupPath(r.ParentSHA)) {
		return bad()
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var authority createManagedLateInstalledCleanupAuthority
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || decodeEnvironmentJSON(raw, &authority) != nil || !managedLateInstalledCleanupMatches(r, &authority) {
		return bad()
	}
	canonical, _ := json.Marshal(authority)
	if !bytes.Equal(raw, canonical) {
		return bad()
	}
	var a createFoundationRecoveryArtifact
	qpath := filepath.Join(store.Root, "deployment/create/openbao-recovery", authority.Review, "qualification.json")
	qraw, err := readBoundedEnvironmentFile(qpath, true)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(qpath)) != nil || environmentPreflightBytesSHA256(qraw) != authority.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != authority.ArtifactSHA {
		return bad()
	}
	copy := *r
	copy.Review = authority.Review
	copy.CurrentSourceSHA = authority.SourceSHA
	expected, err := newManagedLateInstalledCleanupAuthority(&copy, a, authority.OverallDeadline, authority.StartedAt, authority.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(authority) {
		return bad()
	}
	return &authority, nil
}
func verifyManagedLateInstalledCleanupCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if e == nil || r == nil || r.CleanupAuthority == nil || len(e.Journal.Entries) < 389 || len(e.Journal.KubernetesLeases) < r.CleanupAuthority.SourceLeases {
		return createOpenBaoACKBlock("installed-cleanup-current-source")
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:389]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:r.CleanupAuthority.SourceLeases]
	actual, err := readManagedLateInstalledCleanupAuthority(store, r)
	if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.CleanupAuthority) || deploymentplan.Digest(prefix) != actual.SourceSHA || !time.Now().Before(actual.Deadline) {
		return createOpenBaoACKBlock("installed-cleanup-current-authority")
	}
	return nil
}
func admitManagedLateInstalledCleanup(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	prior, err := readManagedLateInstalledCleanupAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if time.Until(prior.Deadline) < managedLateFinalizerLimit {
			return nil, createOpenBaoACKBlock("installed-cleanup-fixed-budget")
		}
		r.CleanupAuthority = prior
		return r.Authority, verifyManagedLateInstalledCleanupCurrent(store, e, r)
	}
	if len(e.Journal.Entries) != 389 {
		return nil, createOpenBaoACKBlock("installed-cleanup-missing-original-authority")
	}
	authority, err := newManagedLateInstalledCleanupAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateInstalledCleanupPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateInstalledCleanupPath(r.ParentSHA)) || ensurePrivateDirectory(filepath.Dir(path)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createOpenBaoACKBlock("installed-cleanup-private-path")
	}
	invalidateManagedLateSourceReuse(e)
	raw, _ := json.Marshal(authority)
	if writeExclusivePrivateDeploymentJSON(path, raw) != nil {
		return nil, createOpenBaoACKBlock("installed-cleanup-authority-exists")
	}
	r.CleanupAuthority = authority
	return r.Authority, e.Check(ctx)
}
func beginManagedLateInstalledCleanupBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || !managedLateInstalledCleanupMatches(r, r.CleanupAuthority) || time.Until(r.CleanupAuthority.Deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("installed-cleanup-fixed-budget")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("installed-cleanup-caller-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"cleanup-policy"] != managedLateInstalledCleanupPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("installed-cleanup-qualified-policy")
	}
	overall, stopOverall := context.WithDeadline(ctx, r.CleanupAuthority.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.CleanupAuthority.StartedAt, Deadline: r.CleanupAuthority.Deadline, Overall: overall, Limit: managedLateInstalledCleanupLimit, InstalledCleanupAuthority: r.CleanupAuthority}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
	return phase, func() { cancel(); stopOverall() }, nil
}

// Expected native owner projection is also read before admission. This proves
// historical installation only; every mutation still uses the live phase guard.
func verifyManagedLateRemainingACKReadSource(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if r != nil && r.FinalizationOnly && r.FinalizationAuthority == nil {
		return verifyManagedLateFinalizationReadSource(store, e, r)
	}
	if r == nil || !r.CleanupOnly || r.CleanupAuthority != nil {
		return verifyManagedLateRemainingAuthorityCurrent(store, e, r)
	}
	if e == nil || e.Check(context.Background()) != nil || !managedLateRemainingSelected(e.Journal) || len(e.Journal.Entries) != 389 || r.CurrentSourceSHA != deploymentplan.Digest(e.Journal) || r.CleanupSourceSHA != r.CurrentSourceSHA || managedLateRemainingRuntimePrefix(e, r) != nil {
		return createOpenBaoACKBlock("installed-cleanup-read-source")
	}
	_, markerErr := os.Lstat(filepath.Join(store.Root, managedLateInstalledCleanupPath(r.ParentSHA)))
	if !errors.Is(markerErr, os.ErrNotExist) {
		return createOpenBaoACKBlock("installed-cleanup-read-unknown-authority")
	}
	original, err := readManagedLateRemainingAuthority(store, r)
	if err != nil || original == nil || deploymentplan.Digest(original) != deploymentplan.Digest(r.Authority) || r.InstalledAt.Before(original.AdmittedAt) || r.InstalledAt.After(original.Deadline) {
		return createOpenBaoACKBlock("installed-cleanup-historical-installation")
	}
	return nil
}

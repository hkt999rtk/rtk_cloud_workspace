package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLatePostNativeCleanupSchema = "rtk-cloud.create-managed-late-post-native-cleanup/v1"
const managedLatePostNativeCleanupLimit = 30 * time.Minute

type createManagedLatePostNativeCleanup struct {
	Schema, SourceSHA, TerminalSourceSHA, TerminalAuthoritySHA, CustodyEntrySHA string
	Native                                                                      environmentCreateBootstrapFinalization
	TombstoneSHA                                                                string
	NativeEvidence                                                              map[string]string
}

type createManagedLatePostNativeCleanupAuthority struct {
	Schema, ParentSHA, SourceSHA, OwnerSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA string
	DescriptorSHA, PostNativeSHA, TerminalAuthoritySHA, PendingSHA                             string
	SessionID, Fingerprint                                                                     string
	StartedAt, Deadline, OverallDeadline                                                       time.Time
}

func managedLateTerminalSourceSelected(j environmentCreateJournal) bool {
	return managedLateTerminalFinalizerSelected(j) || managedLatePostNativeCleanupSelected(j) || managedLateResidualCleanupSelected(j)
}

func managedLatePostNativeCleanupSelected(j environmentCreateJournal) bool {
	if len(j.Entries) != 400 || !j.CompletedAt.IsZero() {
		return false
	}
	prefix := j
	prefix.Entries = prefix.Entries[:396]
	if !managedLateTerminalFinalizerSelected(prefix) {
		return false
	}
	for i, entry := range j.Entries[396:] {
		if entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
			return false
		}
		if i == 0 {
			if entry.Key != "bootstrap-renewed-cleanup-custody-"+managedLateExpiredRenewalReview(j) {
				return false
			}
		} else if len(entry.Key) <= len("bootstrap-copy-retire-") || entry.Key[:len("bootstrap-copy-retire-")] != "bootstrap-copy-retire-" {
			return false
		}
	}
	return true
}

func managedLatePostNativeCleanupPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLatePostNativeCleanupSchema, "phase": managedLatePostNativeCleanupLimit.String(), "overall": "immutable-first-terminal-caller", "reserve": managedLateFinalizerLimit.String(), "expiry": managedLateExpiryReserve.String(), "writes": "remaining-recorded-historical-secret-retirements-and-original-finalizer-receipt", "admission": "paired-exclusive-witness-first"})
}

func managedLatePostNativeCleanupPath(parent string, witness bool) string {
	name := "managed-late-post-native-cleanup-authority.json"
	if witness {
		name = "managed-late-post-native-cleanup-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}

func readManagedLatePostNativeCleanupPair(store secretStore, parent string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("post-native-cleanup-admission-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLatePostNativeCleanupPath(parent, false), managedLatePostNativeCleanupPath(parent, true)}
	absent := 0
	for i, relative := range paths {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return bad()
		}
		paths[i] = path
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			absent++
		} else if err != nil {
			return bad()
		}
	}
	if absent == 2 {
		return nil, nil
	}
	if absent != 0 {
		return bad()
	}
	a, err := readBoundedEnvironmentFile(paths[0], true)
	b, other := readBoundedEnvironmentFile(paths[1], true)
	if err != nil || other != nil || len(a) == 0 || !bytes.Equal(a, b) {
		return bad()
	}
	return a, nil
}

func writeManagedLatePostNativeCleanupPair(store secretStore, parent string, raw []byte) error {
	prior, err := readManagedLatePostNativeCleanupPair(store, parent)
	if err != nil || len(prior) != 0 || len(raw) == 0 {
		return createOpenBaoACKBlock("post-native-cleanup-admission-pair-exists")
	}
	for _, witness := range []bool{true, false} {
		path, err := privateDeploymentStatePath(store.Environment, managedLatePostNativeCleanupPath(parent, witness))
		if err != nil || writeExclusivePrivateDeploymentJSON(path, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(path)) != nil {
			return createOpenBaoACKBlock("post-native-cleanup-admission-pair-write")
		}
	}
	got, err := readManagedLatePostNativeCleanupPair(store, parent)
	if err != nil || !bytes.Equal(got, raw) {
		return createOpenBaoACKBlock("post-native-cleanup-admission-pair-readback")
	}
	return nil
}

func managedLatePostNativeCleanupDeadline(start, overall time.Time, r *createManagedLateRemaining) time.Time {
	deadline := start.Add(managedLatePostNativeCleanupLimit)
	for _, cutoff := range []time.Time{overall, r.TerminalAuthority.OverallDeadline, r.LeafNotAfter.Add(-managedLateExpiryReserve), r.SessionExpires.Add(-managedLateExpiryReserve)} {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}

func managedLatePostNativeCleanupNativeValid(r *createManagedLateRemaining) bool {
	if r == nil || r.PostNativeCleanup == nil || r.TerminalAuthority == nil || !managedLateTerminalFinalizerAuthorityMatches(r, r.TerminalAuthority) {
		return false
	}
	p := r.PostNativeCleanup
	n := p.Native
	return p.Schema == managedLatePostNativeCleanupSchema && p.TerminalSourceSHA == r.TerminalFinalizerCompletion.SourceSHA && p.TerminalAuthoritySHA == deploymentplan.Digest(r.TerminalAuthority) && environmentPreflightDigest.MatchString(p.SourceSHA) && environmentPreflightDigest.MatchString(p.CustodyEntrySHA) && n.Environment == r.Ceremony.Root.Environment && n.SessionID == r.SessionID && n.LeafSHA256 == r.Fingerprint && environmentPreflightDigest.MatchString(n.CRLSHA256) && environmentPreflightDigest.MatchString(n.CleanupSHA256) && !n.FinalizedAt.Before(r.TerminalAuthority.StartedAt) && !n.FinalizedAt.After(r.TerminalAuthority.Deadline) && environmentPreflightDigest.MatchString(p.TombstoneSHA) && managedLateTerminalFinalizerEvidenceValid(p.NativeEvidence)
}

func managedLatePostNativeCleanupAuthorityMatches(r *createManagedLateRemaining, a *createManagedLatePostNativeCleanupAuthority) bool {
	if !managedLatePostNativeCleanupNativeValid(r) || a == nil {
		return false
	}
	p := r.PostNativeCleanup
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.PostNativeSHA, a.TerminalAuthoritySHA, a.PendingSHA, a.Fingerprint} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return a.Schema == managedLatePostNativeCleanupSchema && a.ParentSHA == r.ParentSHA && a.SourceSHA == p.SourceSHA && a.OwnerSHA == r.OwnerSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.PostNativeSHA == deploymentplan.Digest(p) && a.TerminalAuthoritySHA == deploymentplan.Digest(r.TerminalAuthority) && a.PendingSHA == r.TerminalFinalizerCompletion.PendingSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && !a.StartedAt.IsZero() && !a.StartedAt.Before(p.Native.FinalizedAt) && !a.OverallDeadline.After(r.TerminalAuthority.OverallDeadline) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.Deadline.Equal(managedLatePostNativeCleanupDeadline(a.StartedAt, a.OverallDeadline, r)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
}

func newManagedLatePostNativeCleanupAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLatePostNativeCleanupAuthority, error) {
	bad := func() (*createManagedLatePostNativeCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("post-native-cleanup-fresh-authority")
	}
	if r == nil || r.PostNativeCleanup == nil || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.PostNativeCleanup.SourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"post-native-cleanup"] != deploymentplan.Digest(r.PostNativeCleanup) || a.Plan.Materials[managedLateRemainingMaterial+"post-native-policy"] != managedLatePostNativeCleanupPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	if r.TerminalAuthority == nil {
		return bad()
	}
	if r.TerminalAuthority.OverallDeadline.Before(overall) {
		overall = r.TerminalAuthority.OverallDeadline
	}
	result := &createManagedLatePostNativeCleanupAuthority{Schema: managedLatePostNativeCleanupSchema, ParentSHA: r.ParentSHA, SourceSHA: r.PostNativeCleanup.SourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA, DescriptorSHA: managedLateRemainingDigest(r), PostNativeSHA: deploymentplan.Digest(r.PostNativeCleanup), TerminalAuthoritySHA: deploymentplan.Digest(r.TerminalAuthority), PendingSHA: r.TerminalFinalizerCompletion.PendingSHA, SessionID: r.SessionID, Fingerprint: r.Fingerprint, StartedAt: now, OverallDeadline: overall, Deadline: managedLatePostNativeCleanupDeadline(now, overall, r)}
	if !managedLatePostNativeCleanupAuthorityMatches(r, result) {
		return bad()
	}
	return result, nil
}

func readManagedLatePostNativeCleanupAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLatePostNativeCleanupAuthority, error) {
	bad := func() (*createManagedLatePostNativeCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("post-native-cleanup-immutable-authority")
	}
	if r == nil {
		return bad()
	}
	raw, err := readManagedLatePostNativeCleanupPair(store, r.ParentSHA)
	if err != nil {
		return bad()
	}
	if raw == nil {
		return nil, nil
	}
	var result createManagedLatePostNativeCleanupAuthority
	if decodeEnvironmentJSON(raw, &result) != nil || !managedLatePostNativeCleanupAuthorityMatches(r, &result) || !bytes.Equal(raw, mustProviderRecoveryJSON(result)) {
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
	expected, err := newManagedLatePostNativeCleanupAuthority(&copy, a, result.OverallDeadline, result.StartedAt, result.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return bad()
	}
	return &result, nil
}

func loadManagedLatePostNativeCleanupDescriptor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if e == nil || !(managedLatePostNativeCleanupSelected(e.Journal) || managedLateResidualCleanupSelected(e.Journal)) || r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.TerminalAuthority == nil {
		return createOpenBaoACKBlock("post-native-cleanup-source")
	}
	remaining := r.ManagedLateRemaining
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:400]
	remaining.PostNativeCleanup = &createManagedLatePostNativeCleanup{Schema: managedLatePostNativeCleanupSchema, SourceSHA: deploymentplan.Digest(prefix), TerminalSourceSHA: remaining.TerminalFinalizerCompletion.SourceSHA, TerminalAuthoritySHA: deploymentplan.Digest(remaining.TerminalAuthority), CustodyEntrySHA: deploymentplan.Digest(e.Journal.Entries[396])}
	if err := verifyManagedLatePostNativeCleanupSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	raw, err := readManagedLatePostNativeCleanupPair(store, remaining.ParentSHA)
	if err != nil {
		return err
	}
	if raw != nil {
		var header createManagedLatePostNativeCleanupAuthority
		var archived createManagedLateRemaining
		if decodeEnvironmentJSON(raw, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &archived) != nil || !managedLatePostNativeCleanupSourceMatches(remaining.PostNativeCleanup, archived.PostNativeCleanup) {
			return createOpenBaoACKBlock("post-native-cleanup-archived-source")
		}
		remaining.PostNativeCleanup = archived.PostNativeCleanup
		remaining.PostNativeAuthority, err = readManagedLatePostNativeCleanupAuthority(store, remaining)
		if err != nil {
			return err
		}
	}
	if err := loadManagedLateResidualCleanupDescriptor(store, remaining); err != nil {
		return err
	}
	if len(e.Journal.Entries) > 400 {
		if remaining.ResidualAuthority == nil {
			return createOpenBaoACKBlock("residual-cleanup-resume-missing-authority")
		}
		return verifyManagedLateResidualCleanupSource(ctx, cfg, store, e, r)
	}
	return nil
}

func managedLatePostNativeCleanupSourceMatches(a, b *createManagedLatePostNativeCleanup) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.Native, y.Native = environmentCreateBootstrapFinalization{}, environmentCreateBootstrapFinalization{}
	x.TombstoneSHA, y.TombstoneSHA = "", ""
	x.NativeEvidence, y.NativeEvidence = nil, nil
	return deploymentplan.Digest(x) == deploymentplan.Digest(y)
}

func verifyManagedLatePostNativeCleanupSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	bad := func() error { return createOpenBaoACKBlock("post-native-cleanup-current-source") }
	if e == nil || r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.PostNativeCleanup == nil || e.Check(ctx) != nil || store != e.Store || len(e.Journal.Entries) < 400 {
		return bad()
	}
	if err := verifyManagedLateTerminalFinalizerSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	remaining, p := r.ManagedLateRemaining, r.ManagedLateRemaining.PostNativeCleanup
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:400]
	if !managedLatePostNativeCleanupSelected(prefix) || p.Schema != managedLatePostNativeCleanupSchema || p.SourceSHA != deploymentplan.Digest(prefix) || p.TerminalSourceSHA != remaining.TerminalFinalizerCompletion.SourceSHA || p.TerminalAuthoritySHA != deploymentplan.Digest(remaining.TerminalAuthority) || p.CustodyEntrySHA != deploymentplan.Digest(e.Journal.Entries[396]) || remaining.TerminalAuthority == nil || e.Journal.Entries[396].StartedAt.Before(remaining.TerminalAuthority.StartedAt) || e.Journal.Entries[396].CompletedAt.After(remaining.TerminalAuthority.Deadline) {
		return bad()
	}
	return nil
}

func verifyManagedLatePostNativeCleanupAuthorityCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if r != nil && r.ResidualCleanup != nil {
		return verifyManagedLateResidualCleanupAuthorityCurrent(store, e, r)
	}
	if e == nil || r == nil || r.PostNativeCleanup == nil || len(e.Journal.Entries) < 400 {
		return createOpenBaoACKBlock("post-native-cleanup-authority-source")
	}
	a, err := readManagedLatePostNativeCleanupAuthority(store, r)
	if err == nil && a == nil && r.PostNativeAuthority == nil && len(e.Journal.Entries) == 400 {
		return nil
	}
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.PostNativeAuthority) || !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("post-native-cleanup-current-authority")
	}
	return nil
}

func verifyManagedLatePostNativeCleanupRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.ResidualCleanup != nil {
		return verifyManagedLateResidualCleanupRuntime(ctx, cfg, store, runtime)
	}
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if err := verifyManagedLatePostNativeCleanupSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	a, err := readManagedLatePostNativeCleanupAuthority(store, r.ManagedLateRemaining)
	budget, budgetErr := managedLateActiveBudget(ctx)
	if err != nil || a == nil || budgetErr != nil || budget.PostNativeAuthority == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.ManagedLateRemaining.PostNativeAuthority) || deploymentplan.Digest(a) != deploymentplan.Digest(budget.PostNativeAuthority) || runtime.ExecutionSHA256 != r.ManagedLateRemaining.ParentSHA || runtime.Backend.Store != store || !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("post-native-cleanup-runtime-authority")
	}
	return nil
}

func verifyManagedLatePostNativeCleanupMutation(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if err := verifyManagedLatePostNativeCleanupRuntime(ctx, cfg, store, runtime); err != nil {
		return err
	}
	if !managedLateSourceReuseActive(runtime.Execution) {
		loaded, err := loadManagedLateTerminalFinalizerSource(ctx, cfg, store, runtime.Execution)
		if err != nil {
			return err
		}
		if loaded.ManagedLateRemaining == nil || deploymentplan.Digest(loaded.ManagedLateRemaining.PostNativeCleanup) != deploymentplan.Digest(runtime.ServiceSessionRecovery.ManagedLateRemaining.PostNativeCleanup) {
			return createOpenBaoACKBlock("post-native-cleanup-mutation-source")
		}
	}
	observed := createFoundationRecoveryPlan{Materials: map[string]string{}}
	if err := verifyManagedLateTerminalFinalizerNative(ctx, cfg, store, runtime.Execution, runtime.Backend, runtime.ServiceSessionRecovery, &observed); err != nil {
		return err
	}
	if err := checkManagedLateSourceReuse(runtime.Execution); err != nil {
		return err
	}
	return verifyManagedLatePostNativeCleanupRuntime(ctx, cfg, store, runtime)
}

func admitManagedLatePostNativeCleanup(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if e == nil || e.Check(ctx) != nil || !managedLatePostNativeCleanupSelected(e.Journal) || r == nil || r.PostNativeCleanup == nil || r.PostNativeCleanup.SourceSHA != deploymentplan.Digest(e.Journal) || r.TerminalAuthority == nil {
		return nil, createOpenBaoACKBlock("post-native-cleanup-admission-source")
	}
	prior, err := readManagedLatePostNativeCleanupAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if !time.Now().Before(prior.Deadline) || overall.Before(prior.Deadline) {
			return nil, createOpenBaoACKBlock("post-native-cleanup-fixed-budget")
		}
		r.PostNativeAuthority = prior
		return r.Authority, nil
	}
	phase, err := newManagedLatePostNativeCleanupAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLatePostNativeCleanupPath(r.ParentSHA, false))
	if err != nil || path != filepath.Join(store.Root, managedLatePostNativeCleanupPath(r.ParentSHA, false)) || ensurePrivateDirectory(filepath.Dir(path)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createOpenBaoACKBlock("post-native-cleanup-private-path")
	}
	invalidateManagedLateSourceReuse(e)
	if err := writeManagedLatePostNativeCleanupPair(store, r.ParentSHA, mustProviderRecoveryJSON(phase)); err != nil {
		return nil, err
	}
	r.PostNativeAuthority = phase
	return r.Authority, e.Check(ctx)
}

func beginManagedLatePostNativeCleanupBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || r == nil || !managedLatePostNativeCleanupAuthorityMatches(r, r.PostNativeAuthority) || !time.Now().Before(r.PostNativeAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("post-native-cleanup-fixed-budget")
	}
	if cutoff, ok := ctx.Deadline(); !ok || cutoff.Before(r.PostNativeAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("post-native-cleanup-caller-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"post-native-policy"] != managedLatePostNativeCleanupPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("post-native-cleanup-qualified-policy")
	}
	overall, stop := context.WithDeadline(ctx, r.PostNativeAuthority.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: r.PostNativeAuthority.Deadline, Overall: overall, Limit: managedLatePostNativeCleanupLimit, PostNativeAuthority: r.PostNativeAuthority}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
	return phase, func() { cancel(); stop() }, nil
}

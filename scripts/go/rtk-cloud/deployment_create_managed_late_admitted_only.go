package main

import (
	"context"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateExpiredAdmittedOnlySchema = "rtk-cloud.create-managed-late-expired-unused-admitted-only/v1"
const managedLateExpiredAdmittedOnlyFilename = "managed-late-expired-unused-admitted-only.json"
const managedLateExpiredAdmittedOnlyMaterial = managedLateExpiredRenewalMaterial + "admitted-only"

// This records a new qualification's source, not another native authority or
// installation epoch. OriginalReview continues to own every prospective step.
type createManagedLateExpiredAdmittedOnly struct {
	Schema, ParentSHA, SourceSHA, OwnerSHA string
	OriginalReview, OriginalArtifactSHA    string
	OriginalSourceSHA, AdmissionSHA        string
	DescriptorSHA, RenewalSHA              string
	SessionID, KeySHA, CSRSHA, SeedPlanSHA string
}

func managedLateExpiredAdmittedOnlyMembership(e *environmentCreateExecution, source environmentCreateJournal, a createFoundationRecoveryArtifact, late *createManagedLateSessionRecovery, now time.Time) bool {
	if e == nil || late == nil || late.ExpiredRenewal == nil || late.Renewal == nil || late.SeedCandidate == nil || len(source.Entries) != 374 || len(e.Journal.Entries) != 375 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases) || !e.Journal.CompletedAt.IsZero() || !source.CompletedAt.IsZero() || late.SourceSHA != deploymentplan.Digest(source) || late.SourceOwnerSHA != deploymentplan.Digest(e.owner) || late.ParentSHA != e.Journal.PlanSHA256 || late.ExpiredRenewal.SourceSHA != late.SourceSHA || late.ExpiredRenewal.SourceOwnerSHA != late.SourceOwnerSHA || late.ExpiredRenewal.ParentSHA != late.ParentSHA || late.OriginalSession != late.ExpiredRenewal.PriorSession.Session || late.NewSessionID == late.OriginalSession || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) {
		return false
	}
	for _, entry := range source.Entries {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(now) {
			return false
		}
	}
	entry := e.Journal.Entries[374]
	if entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.After(now) || !managedLateAdmissionMatches(a, source, entry, late) {
		return false
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:374]
	return deploymentplan.Digest(prefix) == deploymentplan.Digest(source)
}

func managedLateExpiredAdmittedOnlyProof(e *environmentCreateExecution, a createFoundationRecoveryArtifact, late *createManagedLateSessionRecovery) createManagedLateExpiredAdmittedOnly {
	return createManagedLateExpiredAdmittedOnly{
		Schema: managedLateExpiredAdmittedOnlySchema, ParentSHA: e.Journal.PlanSHA256,
		SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner),
		OriginalReview: late.Review, OriginalArtifactSHA: deploymentplan.Digest(a), OriginalSourceSHA: late.SourceSHA,
		AdmissionSHA: deploymentplan.Digest(e.Journal.Entries[374]), DescriptorSHA: managedLateSessionSourceDigest(late),
		RenewalSHA: managedLateExpiredRenewalDigest(late.ExpiredRenewal), SessionID: late.NewSessionID,
		KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, SeedPlanSHA: late.SeedCandidate.SHA256(),
	}
}

// Foundation transport readers precede the prepared profile. Establish only
// this exact admitted source phase before they reconstruct its history.
func beginManagedLateEarlyAdmittedSourcePhase(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) error {
	if e != nil && managedLateCompletedTailSelected(e.Journal) {
		_, err := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
		return err
	}
	if e != nil && managedLateTerminalSourceSelected(e.Journal) {
		_, err := loadManagedLateTerminalFinalizerSource(ctx, cfg, store, e)
		return err
	}
	if e == nil || len(e.Journal.Entries) != 375 && !managedLateRemainingSelected(e.Journal) {
		return nil
	}
	review := managedLateExpiredRenewalReview(e.Journal)
	if review == "" {
		return nil
	}
	bad := func() error { return managedLateExpiredRenewalBlock("early-admitted-original-authority") }
	if cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || store.Root != e.Store.Root || store.ConfigRoot != e.Store.ConfigRoot || store.Environment != e.Store.Environment || e.Check(ctx) != nil {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != managedLateExpiredRenewalSchema || a.Plan.Plan.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
		return bad()
	}
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.Review != review {
		return bad()
	}
	if len(e.Journal.Entries) == 375 && !managedLateExpiredAdmittedOnlyMembership(e, source, a, &late, time.Now().UTC()) || managedLateRemainingSelected(e.Journal) && !managedLateRemainingMembership(e, &late, time.Now().UTC()) {
		return bad()
	}
	return beginManagedLateAdmittedSourcePhase(ctx, store, e, &late)
}

// Reuse exactly the admitted e2 producer. RuntimeProgress authenticates the
// original374 ancestry and original admission; no native state is inferred.
func loadManagedLateExpiredAdmittedOnlySource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, createManagedLateExpiredAdmittedOnly, error) {
	var proof createManagedLateExpiredAdmittedOnly
	bad := func() (*createServiceSessionRecovery, createManagedLateExpiredAdmittedOnly, error) {
		return nil, proof, managedLateExpiredRenewalBlock("admitted-only-original-authority")
	}
	if e == nil || len(e.Journal.Entries) != 375 || store.Root != e.Store.Root || store.Environment != "dev" {
		return bad()
	}
	if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
		return nil, proof, err
	}
	review := managedLateExpiredRenewalReview(e.Journal)
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != managedLateExpiredRenewalSchema {
		return bad()
	}
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || !managedLateExpiredAdmittedOnlyMembership(e, source, a, &late, time.Now().UTC()) {
		return bad()
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, &late); err != nil {
		return nil, proof, err
	}
	captured, admitted, err := managedLateSessionRuntimeProgress(store, e, &late)
	if err != nil || captured == nil || admitted == nil || deploymentplan.Digest(captured.Journal) != deploymentplan.Digest(source) || deploymentplan.Digest(*admitted) != deploymentplan.Digest(a) {
		return bad()
	}
	// The old GO is read only at its genuine admission time. It does not qualify
	// this new command; fresh qualification is separately bound to actual375.
	plan := a.Plan
	r, err := loadManagedLateExpiredRenewalQualifiedContent(ctx, cfg, store, captured, &plan, review, e.Journal.Entries[374].StartedAt)
	if err != nil || r == nil || r.ManagedLateSession == nil || managedLateSessionSourceDigest(r.ManagedLateSession) != managedLateSessionSourceDigest(&late) {
		return bad()
	}
	proof = managedLateExpiredAdmittedOnlyProof(e, a, &late)
	return r, proof, nil
}

func prepareManagedLateExpiredAdmittedOnly(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, proof, err := loadManagedLateExpiredAdmittedOnlySource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	a, _, err := loadAdmittedCreateRecovery(store, e, proof.OriginalReview)
	if err != nil {
		return nil, err
	}
	p.Plan, p.Schema = a.Plan.Plan, managedLateExpiredRenewalSchema
	for key, value := range a.Plan.Materials {
		p.Materials[key] = value
	}
	p.Materials[managedLateExpiredAdmittedOnlyMaterial] = deploymentplan.Digest(proof)
	// Includes unfiltered prospective SID/deployment/request 0/0/0, certificate
	// absence, original expired-unused9/0, old signer policy/role and all current
	// identities, owners, CRLs, cache ACKs and volumes. No write is authorized here.
	if err := verifyManagedLateExpiredRenewalCurrent(ctx, cfg, store, e, b, r, p); err != nil {
		return nil, err
	}
	return r, nil
}

func loadManagedLateExpiredAdmittedOnlyQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string, a createFoundationRecoveryArtifact) (*createServiceSessionRecovery, error) {
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, managedLateExpiredRenewalBlock("admitted-only-fresh-six-go")
	}
	if !managedLateQualifiedArtifactValid(a, e, *p, review, time.Now().UTC(), managedLateExpiredRenewalSchema) {
		return bad()
	}
	r, expected, err := loadManagedLateExpiredAdmittedOnlySource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	var captured createManagedLateExpiredAdmittedOnly
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateExpiredAdmittedOnlyFilename), &captured) != nil || deploymentplan.Digest(captured) != deploymentplan.Digest(expected) || a.Plan.Materials[managedLateExpiredAdmittedOnlyMaterial] != deploymentplan.Digest(expected) {
		return bad()
	}
	for name, value := range managedLateQualifiedMaterialPins(r.ManagedLateSession) {
		if a.Plan.Materials["accepted-service-core-late-session-"+name] != value {
			return bad()
		}
	}
	if a.Plan.Materials[managedLateExpiredRenewalMaterial+"descriptor"] != managedLateExpiredRenewalDigest(r.ManagedLateExpiredRenewal) {
		return bad()
	}
	*p = a.Plan
	return r, nil
}

// This consumes the fresh GO immediately before continuing the old admission.
// It appends no parent entry and cannot change the old artifact or review.
func admitManagedLateExpiredAdmittedOnly(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, proof createManagedLateExpiredAdmittedOnly) error {
	if e == nil || e.Check(ctx) != nil || proof.Schema != managedLateExpiredAdmittedOnlySchema || proof.SourceSHA != deploymentplan.Digest(e.Journal) || proof.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.Materials[managedLateExpiredAdmittedOnlyMaterial] != deploymentplan.Digest(proof) || !managedLateQualifiedArtifactValid(a, e, a.Plan, deploymentplan.Digest(a.Plan), time.Now().UTC(), managedLateExpiredRenewalSchema) {
		return managedLateExpiredRenewalBlock("admitted-only-current-authority")
	}
	currentSHA, err := readManagedLateQualificationFingerprint(store, a)
	if err != nil || !environmentPreflightDigest.MatchString(bytesSHA) || currentSHA != bytesSHA {
		return managedLateExpiredRenewalBlock("admitted-only-qualified-bytes")
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || checker != a.Plan.CheckerSHA || !time.Now().Before(a.ValidUntil) {
		return managedLateExpiredRenewalBlock("admitted-only-current-checker-or-expiry")
	}
	if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
		return err
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	invalidateManagedLateSourceReuse(e)
	return nil
}

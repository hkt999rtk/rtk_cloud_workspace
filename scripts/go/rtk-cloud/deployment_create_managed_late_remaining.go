package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateRemainingSchema = "rtk-cloud.create-managed-late-same-session-remaining/v1"
const managedLateRemainingFilename = "managed-late-same-session-remaining.json"
const managedLateRemainingMaterial = "accepted-service-core-late-remaining-"
const managedLateRemainingAuthorityFilename = "managed-late-remaining-authority.json"
const managedLateRemainingLimit = 120 * time.Minute

// This scope continues only the accepted e2 generation at its exact Log-stop
// checkpoint. It never changes the old descriptor or any producer recipe.
type createManagedLateRemaining struct {
	completedTail                                                               *createManagedLateCompletedTail
	ResidualRemaining                                                           *createManagedLateResidualRemaining           `json:"residual_remaining,omitempty"`
	ResidualRemainingAuthority                                                  *createManagedLateResidualCleanupAuthority    `json:"-"`
	ResidualCleanup                                                             *createManagedLateResidualCleanup             `json:"residual_cleanup,omitempty"`
	ResidualAuthority                                                           *createManagedLateResidualCleanupAuthority    `json:"-"`
	PostNativeCleanup                                                           *createManagedLatePostNativeCleanup           `json:"post_native_cleanup,omitempty"`
	PostNativeAuthority                                                         *createManagedLatePostNativeCleanupAuthority  `json:"-"`
	TerminalFinalizerCompletion                                                 *createManagedLateTerminalFinalizerCompletion `json:"terminal_finalizer_completion,omitempty"`
	TerminalAuthority                                                           *createManagedLateTerminalFinalizerAuthority  `json:"-"`
	FinalizationOnly                                                            bool                                          `json:"finalization_only,omitempty"`
	LogProofSHA                                                                 string                                        `json:"log_proof_sha256,omitempty"`
	FinalizationAuthority                                                       *createManagedLateFinalizationAuthority       `json:"-"`
	CleanupOnly                                                                 bool                                          `json:"cleanup_only,omitempty"`
	CleanupSourceSHA                                                            string                                        `json:"cleanup_source_sha256,omitempty"`
	InstalledAt                                                                 time.Time                                     `json:"installed_at,omitzero"`
	CleanupAuthority                                                            *createManagedLateInstalledCleanupAuthority   `json:"-"`
	Review, ParentSHA, SourceSHA, OwnerSHA, OriginalReview, OriginalArtifactSHA string
	DescriptorSHA, SeedObservationSHA, CredentialObservationSHA                 string
	OwnersPlanSHA, OwnersJournalSHA, StopPlanSHA, StopJournalSHA                string
	SessionID, Fingerprint, KeySHA, CSRSHA                                      string
	StaleSeedsSHA                                                               string
	HistoricalSeedCleanup                                                       *createManagedHistoricalSeedCleanup
	SourceLeases                                                                int
	NotBefore, LeafNotAfter, SessionExpires                                     time.Time
	Ceremony                                                                    environmentCreateServiceCeremony
	Authority                                                                   *createManagedLateRemainingAuthority `json:"-"`
	CurrentSourceSHA                                                            string                               `json:"current_source_sha256,omitempty"`
	HelperObservationSHA                                                        string                               `json:"helper_observation_sha256,omitempty"`
}

type createManagedLateRemainingAuthority struct {
	QualifiedBytesSHA, PolicySHA                                                   string
	Schema, ParentSHA, SourceSHA, OwnerSHA, OriginalReview, Review, CheckerSHA     string
	ArtifactSHA, DescriptorSHA, SessionID, Fingerprint, KeySHA, CSRSHA             string
	SeedObservationSHA                                                             string
	StartedAt, Deadline, OverallDeadline, AdmittedAt, LeafNotAfter, SessionExpires time.Time
}

func managedLateRemainingDigest(r *createManagedLateRemaining) string {
	if r == nil {
		return ""
	}
	c := *r
	c.Review = ""
	c.ResidualRemaining, c.ResidualRemainingAuthority = nil, nil
	c.ResidualCleanup, c.ResidualAuthority = nil, nil
	c.Authority = nil
	c.CurrentSourceSHA, c.HelperObservationSHA = "", ""
	c.CleanupOnly, c.CleanupSourceSHA, c.InstalledAt, c.CleanupAuthority = false, "", time.Time{}, nil
	c.FinalizationOnly, c.LogProofSHA, c.FinalizationAuthority = false, "", nil
	c.TerminalFinalizerCompletion, c.TerminalAuthority = nil, nil
	c.PostNativeCleanup, c.PostNativeAuthority = nil, nil
	return deploymentplan.Digest(c)
}

func managedLateRemainingSelected(j environmentCreateJournal) bool {
	if len(j.Entries) >= 396 {
		return managedLateTerminalSourceSelected(j)
	}
	if len(j.Entries) < 388 || len(j.Entries) > 392 || !j.CompletedAt.IsZero() {
		return false
	}
	review := managedLateExpiredRenewalReview(j)
	if review == "" || j.Entries[387].Key != "kubernetes/managed-late-log-stop-"+review {
		return false
	}
	keys := []string{"kubernetes/managed-late-log-existing-ack-", "kubernetes/managed-late-log-retire-", "kubernetes/managed-late-log-restore-", "managed-late-log-verified-"}
	for i, entry := range j.Entries[388:] {
		if entry.Key != keys[i]+review || entry.State != "completed" {
			return false
		}
	}
	return true
}

func managedLateRemainingMembership(e *environmentCreateExecution, late *createManagedLateSessionRecovery, now time.Time) bool {
	if e == nil || late == nil || late.ExpiredRenewal == nil || !managedLateRemainingSelected(e.Journal) || late.Review != managedLateExpiredRenewalReview(e.Journal) || late.ParentSHA != e.Journal.PlanSHA256 || late.SourceOwnerSHA != deploymentplan.Digest(e.owner) {
		return false
	}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(now) || len(entry.Receipt) == 0 {
			return false
		}
	}
	return true
}

func managedLateRemainingSessionValid(current managedSessionState, remaining *createManagedLateRemaining, subjects []string, now time.Time) bool {
	if remaining == nil || current.Session != remaining.SessionID || current.Status != "active" || current.OtherActive != 0 || current.Fingerprint != remaining.Fingerprint || !current.Expires.Equal(remaining.SessionExpires) || !current.Expires.After(now.Add(managedLateFinalizerLimit+managedLateExpiryReserve)) || !slices.Equal(current.Subjects, subjects) || len(current.Subjects) != 9 || (remaining.CurrentSourceSHA == "" && len(current.Installed) != 8) || (remaining.CurrentSourceSHA != "" && len(current.Installed) != 9) {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range current.Installed {
		if remaining.CurrentSourceSHA == "" && subject == "service:video-cloud-logingester" || !slices.Contains(subjects, subject) || seen[subject] {
			return false
		}
		seen[subject] = true
	}
	return true
}

func loadManagedLateRemainingSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, error) {
	if e != nil && managedLateTerminalSourceSelected(e.Journal) {
		return loadManagedLateTerminalFinalizerSource(ctx, cfg, store, e)
	}
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createOpenBaoACKBlock("late-remaining-original-checkpoint")
	}
	if e == nil || store.Root != e.Store.Root || !managedLateRemainingSelected(e.Journal) {
		return bad()
	}
	if err := checkManagedLatePureSourceExecution(ctx, e); err != nil {
		return nil, err
	}
	if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
		return nil, err
	}
	review := managedLateExpiredRenewalReview(e.Journal)
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	var late createManagedLateSessionRecovery
	if err != nil || a.Plan.Schema != managedLateExpiredRenewalSchema || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || !managedLateRemainingMembership(e, &late, time.Now().UTC()) {
		return bad()
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, &late); err != nil {
		return nil, err
	}
	captured, admitted, err := managedLateSessionRuntimeProgress(store, e, &late)
	if err != nil || deploymentplan.Digest(captured.Journal) != deploymentplan.Digest(source) || deploymentplan.Digest(*admitted) != deploymentplan.Digest(a) {
		return bad()
	}
	plan := a.Plan
	r, err := loadManagedLateExpiredRenewalQualifiedContent(ctx, cfg, store, captured, &plan, review, e.Journal.Entries[374].StartedAt)
	if err != nil {
		return nil, err
	}
	children, _, _, err := managedLateCurrentChildren(store, e, &late)
	if err != nil {
		return nil, err
	}
	credentials := children["kubernetes/service-credential-secrets-renewal-"+review]
	owners := children["kubernetes/managed-late-session-owners-"+review]
	stop := children["kubernetes/managed-late-log-stop-"+review]
	outer := createOpenBaoACKCompletionEntry(e.Journal, "late-cleanup-authorization-"+review)
	var observed createServiceSessionObserved
	var seed createManagedLateSeedObservation
	if outer == nil || credentials.Plan == nil || owners.Plan == nil || stop.Plan == nil || !managedCoreSameQualifiedPlan(owners.Plan, late.OwnerCandidate) || !managedCoreSameQualifiedPlan(stop.Plan, late.LogStopCandidate) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", late.ParentSHA, "bootstrap/service/session-renewal", review, "observed.json"), &observed) != nil || validateManagedLateAllocatedCredentialProof(cfg, &late, credentials.Plan, credentials.Journal, observed, outer.CompletedAt) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-seed-observed.json"), &seed) != nil {
		return bad()
	}
	leaf, err := parseFirstCertificatePEM([]byte(seed.CertificatePEM))
	if err != nil || leaf == nil || seed.KeySHA != late.OriginalKeySHA || seed.CSRSHA != late.OriginalCSRSHA || seed.Session.Session != late.NewSessionID || seed.Session.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !seed.Session.Expires.Equal(leaf.NotAfter) {
		return bad()
	}
	ceremony := late.SourceCeremony
	ceremony.BootstrapSessionID = late.NewSessionID
	ceremony.BootstrapCredentialSecret = "service-bootstrap-" + late.NewSessionID
	ceremony.CredentialUIDs = observed.UIDs
	ceremony.CleanupBinding = observed.Cleanup
	remaining := &createManagedLateRemaining{ParentSHA: late.ParentSHA, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), OriginalReview: review, OriginalArtifactSHA: deploymentplan.Digest(a), DescriptorSHA: managedLateSessionSourceDigest(&late), SeedObservationSHA: deploymentplan.Digest(seed), CredentialObservationSHA: deploymentplan.Digest(observed), OwnersPlanSHA: owners.Plan.SHA256(), OwnersJournalSHA: deploymentplan.Digest(owners.Journal), StopPlanSHA: stop.Plan.SHA256(), StopJournalSHA: deploymentplan.Digest(stop.Journal), SessionID: late.NewSessionID, Fingerprint: seed.Session.Fingerprint, KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, SourceLeases: len(e.Journal.KubernetesLeases), NotBefore: leaf.NotBefore, LeafNotAfter: leaf.NotAfter, SessionExpires: seed.Session.Expires, Ceremony: ceremony}
	if len(e.Journal.Entries) >= 389 {
		var marker createManagedLateRemainingAuthority
		var original createManagedLateRemaining
		if createOpenBaoACKCompletionRead(store, managedLateRemainingAuthorityPath(remaining.ParentSHA), &marker) != nil || !environmentPreflightDigest.MatchString(marker.Review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", marker.Review, managedLateRemainingFilename), &original) != nil || original.SourceLeases < 0 || original.SourceLeases > len(e.Journal.KubernetesLeases) {
			return bad()
		}
		prefix := e.Journal
		prefix.Entries = prefix.Entries[:388]
		prefix.KubernetesLeases = prefix.KubernetesLeases[:original.SourceLeases]
		remaining.SourceSHA, remaining.SourceLeases = deploymentplan.Digest(prefix), original.SourceLeases
		remaining.CurrentSourceSHA = deploymentplan.Digest(e.Journal)
		remaining.CleanupOnly = true
		remaining.InstalledAt = e.Journal.Entries[388].CompletedAt
		if remaining.SourceLeases+1 > len(e.Journal.KubernetesLeases) {
			return bad()
		}
		installed := e.Journal
		installed.Entries = installed.Entries[:389]
		installed.KubernetesLeases = installed.KubernetesLeases[:remaining.SourceLeases+1]
		remaining.CleanupSourceSHA = deploymentplan.Digest(installed)
		if remaining.SourceSHA != original.SourceSHA || managedLateRemainingRuntimePrefix(e, remaining) != nil {
			return bad()
		}
	}
	if !remaining.CleanupOnly && !time.Now().Before(remaining.NotBefore.Add(managedLateRemainingLimit-managedLateFinalizerLimit)) {
		return nil, createOpenBaoACKBlock("late-remaining-fixed-leaf-deadline")
	}
	r.ManagedLateRemaining = remaining
	r.Ceremony = ceremony
	r.Renewal = true
	if err := prepareManagedLateRemainingCopyRetirement(store, e, r); err != nil {
		return nil, err
	}
	// A prior combined receipt may be reused, never regenerated. Its archived
	// proof is restored before the receipt's exact descriptor is authenticated.
	markerPath := filepath.Join(store.Root, managedLateRemainingAuthorityPath(remaining.ParentSHA))
	if _, statErr := os.Lstat(markerPath); statErr == nil {
		var marker createManagedLateRemainingAuthority
		var archived createManagedLateRemaining
		if createOpenBaoACKCompletionRead(store, managedLateRemainingAuthorityPath(remaining.ParentSHA), &marker) != nil || !environmentPreflightDigest.MatchString(marker.Review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", marker.Review, managedLateRemainingFilename), &archived) != nil || !managedLateRemainingCleanupBaseMatches(remaining, &archived) || restoreManagedLateRemainingHistoricalCleanup(r, archived.HistoricalSeedCleanup) != nil {
			return nil, managedHistoricalSeedBlockAt("remaining-existing-marker")
		}
		remaining.Authority, err = readManagedLateRemainingAuthority(store, remaining)
		if err != nil || remaining.Authority == nil || remaining.CleanupOnly && (remaining.InstalledAt.Before(remaining.Authority.AdmittedAt) || remaining.InstalledAt.After(remaining.Authority.Deadline) || e.Journal.Entries[388].StartedAt.Before(remaining.Authority.AdmittedAt)) {
			return nil, managedHistoricalSeedBlockAt("remaining-existing-authority")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, managedHistoricalSeedBlockAt("remaining-marker-lstat")
	}
	if remaining.CleanupOnly {
		path := filepath.Join(store.Root, managedLateInstalledCleanupPath(remaining.ParentSHA))
		if _, statErr := os.Lstat(path); statErr == nil {
			var marker createManagedLateInstalledCleanupAuthority
			var archived createManagedLateRemaining
			if createOpenBaoACKCompletionRead(store, managedLateInstalledCleanupPath(remaining.ParentSHA), &marker) != nil || !environmentPreflightDigest.MatchString(marker.Review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", marker.Review, managedLateRemainingFilename), &archived) != nil || !managedLateRemainingCleanupBaseMatches(remaining, &archived) || archived.CleanupSourceSHA != remaining.CleanupSourceSHA {
				return bad()
			}
			remaining.HelperObservationSHA = archived.HelperObservationSHA
			remaining.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, remaining)
			if err != nil || remaining.CleanupAuthority == nil {
				return bad()
			}
		} else if !errors.Is(statErr, os.ErrNotExist) || len(e.Journal.Entries) > 389 {
			return bad()
		}
	}
	if len(e.Journal.Entries) == 392 {
		log, err := readManagedLateLogCompletion(store, e, &late, time.Now().UTC())
		if err != nil || log == nil || remaining.CleanupAuthority == nil || log.RestoredAt.After(remaining.CleanupAuthority.Deadline) || e.Journal.Entries[391].CompletedAt.After(remaining.CleanupAuthority.Deadline) || e.Journal.Entries[391].StartedAt.Before(remaining.CleanupAuthority.StartedAt) {
			return bad()
		}
		remaining.FinalizationOnly = true
		remaining.LogProofSHA = deploymentplan.Digest(log)
		remaining.FinalizationAuthority, err = readManagedLateFinalizationAuthority(store, remaining)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func managedLateRemainingCurrentSource(r *createManagedLateRemaining) string {
	if r == nil {
		return ""
	}
	if r.ResidualRemaining != nil {
		return r.ResidualRemaining.Approval.SourceSHA
	}
	if r.PostNativeCleanup != nil {
		return r.PostNativeCleanup.SourceSHA
	}
	if r.TerminalFinalizerCompletion != nil {
		return r.TerminalFinalizerCompletion.SourceSHA
	}
	if r.CurrentSourceSHA != "" {
		return r.CurrentSourceSHA
	}
	return r.SourceSHA
}

func managedLateRemainingPins(r *createManagedLateRemaining) map[string]string {
	pins := map[string]string{"source": r.SourceSHA, "descriptor": managedLateRemainingDigest(r), "ceremony": deploymentplan.Digest(r.Ceremony), "authority": deploymentplan.Digest(r.Authority), "policy": managedLateRemainingPolicySHA()}
	if r.CurrentSourceSHA != "" {
		pins["current-source"] = r.CurrentSourceSHA
		pins["helper-observation"] = r.HelperObservationSHA
	}
	if r.CleanupOnly {
		pins["cleanup-policy"] = managedLateInstalledCleanupPolicySHA()
		pins["cleanup-source"] = r.CleanupSourceSHA
		pins["cleanup-authority"] = deploymentplan.Digest(r.CleanupAuthority)
	}
	if r.FinalizationOnly {
		pins["finalization-policy"] = managedLateFinalizationPolicySHA()
		pins["log-proof"] = r.LogProofSHA
		pins["finalization-authority"] = deploymentplan.Digest(r.FinalizationAuthority)
	}
	if r.TerminalFinalizerCompletion != nil {
		pins["terminal-completion"] = deploymentplan.Digest(r.TerminalFinalizerCompletion)
		pins["terminal-policy"] = managedLateTerminalFinalizerPolicySHA()
	}
	if r.ResidualRemaining != nil {
		pins["residual-remaining"] = deploymentplan.Digest(r.ResidualRemaining)
		pins["residual-remaining-policy"] = managedLateResidualRemainingPolicySHA()
	}
	if r.ResidualCleanup != nil {
		pins["residual-cleanup"] = deploymentplan.Digest(r.ResidualCleanup)
		pins["residual-policy"] = managedLateResidualCleanupPolicySHA()
	}
	if r.PostNativeCleanup != nil {
		pins["post-native-cleanup"] = deploymentplan.Digest(r.PostNativeCleanup)
		pins["post-native-policy"] = managedLatePostNativeCleanupPolicySHA()
	}
	return pins
}

func prepareManagedLateRemaining(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, err := loadManagedLateRemainingSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	a, err := readManagedLateRemainingAuthority(store, r.ManagedLateRemaining)
	if err != nil {
		return nil, err
	}
	r.ManagedLateRemaining.Authority = a
	if err := verifyManagedLateRemainingCurrent(ctx, cfg, store, e, b, r, p); err != nil {
		return nil, err
	}
	return r, nil
}

func verifyManagedLateRemainingCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		return verifyManagedLateTerminalFinalizerNative(ctx, cfg, store, e, b, r, p)
	}
	remaining := r.ManagedLateRemaining
	if remaining == nil || r.ManagedLateSession == nil || remaining.OriginalReview != r.ManagedLateSession.Review || r.Ceremony.BootstrapSessionID != remaining.SessionID {
		return createOpenBaoACKBlock("late-remaining-typed-source")
	}
	if err := managedLateRemainingRuntimePrefix(e, remaining); err != nil {
		return err
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, remaining.OriginalReview)
	if err != nil {
		return err
	}
	if p.Materials == nil {
		p.Materials = map[string]string{}
	}
	for key, value := range original.Plan.Materials {
		if _, ok := p.Materials[key]; !ok {
			p.Materials[key] = value
		}
	}
	p.Plan = original.Plan.Plan
	p.Schema = managedLateExpiredRenewalSchema
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, r.ManagedLateSession); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return err
	}
	proof, err := verifyManagedLateDeviceRootCRLFreshness(ctx, cfg, store, e, b, r)
	if err != nil {
		return err
	}
	for key, value := range proof {
		p.Materials[managedLateExpiredRenewalMaterial+key] = value
	}
	if len(e.Journal.Entries) >= 388 && len(e.Journal.Entries) <= 392 {
		state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, remaining.SessionID)
		subjects, subjectErr := environmentCreateBootstrapSubjects(cfg)
		if err != nil || subjectErr != nil || !managedLateRemainingSessionValid(state, remaining, subjects, time.Now().UTC()) {
			return createOpenBaoACKBlock("late-remaining-current-installation-set")
		}
	}
	if len(e.Journal.Entries) >= 389 && len(e.Journal.Entries) <= 392 {
		late := r.ManagedLateSession
		_, journal, err := managedLateLogAccepted(store, e, late, "existing-ack", "")
		if err != nil || late.LogHelperCandidate == nil || len(late.LogHelperCandidate.Resources) != 1 {
			return createOpenBaoACKBlock("late-remaining-accepted-helper")
		}
		producer := *late.LogProducer
		current, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, remaining.SessionID)
		if err != nil {
			return err
		}
		producer.FreshReview, producer.NewSession = late.Review, current
		var observation createManagedLateLogObservation
		if len(e.Journal.Entries) == 389 {
			observation, err = readManagedLateLogObservation(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, late, &producer, journal.Owners[late.LogHelperCandidate.Resources[0].ID])
		} else {
			err = createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-log-observation.json"), &observation)
		}
		if err != nil || validateManagedLateLogObservation(late, observation, journal.Owners[late.LogHelperCandidate.Resources[0].ID]) != nil {
			return createOpenBaoACKBlock("late-remaining-terminal-helper")
		}
		finished, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_finished_at"]))
		if remaining.CleanupOnly && (finishErr != nil || finished.Before(remaining.Authority.AdmittedAt) || finished.After(remaining.InstalledAt) || finished.After(remaining.Authority.Deadline)) {
			return createOpenBaoACKBlock("installed-cleanup-original-completion-time")
		}
		observedSHA := deploymentplan.Digest(observation)
		if remaining.HelperObservationSHA != "" && remaining.HelperObservationSHA != observedSHA {
			return createOpenBaoACKBlock("late-remaining-terminal-helper-drift")
		}
		remaining.HelperObservationSHA = observedSHA
	}
	priorCleanup := remaining.HistoricalSeedCleanup
	if err := qualifyManagedLateHistoricalSeedCleanup(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	if remaining.CurrentSourceSHA != "" && priorCleanup != nil {
		proof := r.ManagedHistoricalSeedCleanup
		if proof == nil || proof.SourceSHA != deploymentplan.Digest(e.Journal) || priorCleanup.SourceSHA != remaining.SourceSHA {
			return managedHistoricalSeedBlockAt("remaining-refreshed-proof")
		}
		copy := *proof
		copy.SourceSHA = remaining.SourceSHA // unchanged original authority; actual389 is separately qualified above
		r.ManagedHistoricalSeedCleanup = &copy
		p.Materials[managedHistoricalSeedCleanupMaterial] = deploymentplan.Digest(&copy)
	}
	if priorCleanup != nil && deploymentplan.Digest(priorCleanup) != deploymentplan.Digest(r.ManagedHistoricalSeedCleanup) {
		return managedHistoricalSeedBlockAt("remaining-proof-drift")
	}
	if err := restoreManagedLateRemainingHistoricalCleanup(r, r.ManagedHistoricalSeedCleanup); err != nil {
		return err
	}
	if remaining.StaleSeedsSHA != deploymentplan.Digest(r.StaleSeeds) {
		return managedHistoricalSeedBlockAt("remaining-stale-seed-set")
	}
	if _, err := managedLateOriginalVolumePins(p.Materials); err != nil {
		return err
	}
	for key, value := range managedLateQualifiedMaterialPins(r.ManagedLateSession) {
		p.Materials["accepted-service-core-late-session-"+key] = value
	}
	for key, value := range managedLateRemainingPins(remaining) {
		p.Materials[managedLateRemainingMaterial+key] = value
	}
	return nil
}

func managedLateRemainingRuntimePrefix(e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if e == nil || r == nil || len(e.Journal.Entries) < 388 || r.SourceLeases < 0 || len(e.Journal.KubernetesLeases) < r.SourceLeases || deploymentplan.Digest(e.owner) != r.OwnerSHA {
		return createOpenBaoACKBlock("late-remaining-runtime-prefix")
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:388]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:r.SourceLeases]
	if deploymentplan.Digest(prefix) != r.SourceSHA {
		return createOpenBaoACKBlock("late-remaining-runtime-prefix")
	}
	return nil
}

func loadManagedLateRemainingQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	var a createFoundationRecoveryArtifact
	base := filepath.Join("deployment/create/openbao-recovery", review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || !managedLateQualifiedArtifactValid(a, e, *p, review, time.Now().UTC(), managedLateExpiredRenewalSchema) {
		return nil, createOpenBaoACKBlock("late-remaining-fresh-six-go")
	}
	r, err := loadManagedLateRemainingSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	var archived createManagedLateRemaining
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateRemainingFilename), &archived) != nil {
		return nil, createOpenBaoACKBlock("late-remaining-immutable-source")
	}
	if err := restoreManagedLateRemainingQualifiedSource(r, &archived, a.Plan.Materials, review); err != nil {
		return nil, err
	}
	authority, err := readManagedLateRemainingAuthority(store, r.ManagedLateRemaining)
	if err != nil {
		return nil, err
	}
	r.ManagedLateRemaining.Authority = authority
	r.ManagedLateRemaining.HelperObservationSHA = archived.HelperObservationSHA
	if r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		if archived.TerminalFinalizerCompletion == nil {
			return nil, createOpenBaoACKBlock("terminal-finalizer-qualified-descriptor")
		}
		current, saved := *r.ManagedLateRemaining.TerminalFinalizerCompletion, *archived.TerminalFinalizerCompletion
		current.NativeEvidence, saved.NativeEvidence = nil, nil
		if deploymentplan.Digest(current) != deploymentplan.Digest(saved) {
			return nil, createOpenBaoACKBlock("terminal-finalizer-qualified-source")
		}
		r.ManagedLateRemaining.TerminalFinalizerCompletion = archived.TerminalFinalizerCompletion
		r.ManagedLateRemaining.TerminalAuthority, err = readManagedLateTerminalFinalizerAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.PostNativeCleanup != nil {
		if !managedLatePostNativeCleanupSourceMatches(r.ManagedLateRemaining.PostNativeCleanup, archived.PostNativeCleanup) {
			return nil, createOpenBaoACKBlock("post-native-cleanup-qualified-source")
		}
		r.ManagedLateRemaining.PostNativeCleanup = archived.PostNativeCleanup
		r.ManagedLateRemaining.PostNativeAuthority, err = readManagedLatePostNativeCleanupAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.ResidualCleanup != nil {
		if !managedLateResidualCleanupSourceMatches(r.ManagedLateRemaining.ResidualCleanup, archived.ResidualCleanup) {
			return nil, createOpenBaoACKBlock("residual-cleanup-qualified-source")
		}
		r.ManagedLateRemaining.ResidualCleanup = archived.ResidualCleanup
		r.ManagedLateRemaining.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.ResidualRemaining != nil {
		if !managedLateResidualRemainingSourceMatches(r.ManagedLateRemaining.ResidualRemaining, archived.ResidualRemaining) {
			return nil, createOpenBaoACKBlock("residual-remaining-qualified-source")
		}
		r.ManagedLateRemaining.ResidualRemaining = archived.ResidualRemaining
		r.ManagedLateRemaining.ResidualRemainingAuthority, err = readManagedLateResidualRemainingAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.FinalizationOnly {
		r.ManagedLateRemaining.FinalizationAuthority, err = readManagedLateFinalizationAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.CleanupOnly {
		r.ManagedLateRemaining.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, r.ManagedLateRemaining)
		if err != nil {
			return nil, err
		}
	}
	if r.ManagedLateRemaining.CurrentSourceSHA != "" && !environmentPreflightDigest.MatchString(archived.HelperObservationSHA) {
		return nil, createOpenBaoACKBlock("late-remaining-terminal-helper-pin")
	}
	for key, value := range managedLateRemainingPins(r.ManagedLateRemaining) {
		if a.Plan.Materials[managedLateRemainingMaterial+key] != value {
			return nil, createOpenBaoACKBlock("late-remaining-material")
		}
	}
	r.ManagedLateRemaining.Review = review
	*p = a.Plan
	return r, nil
}

func restoreManagedLateRemainingQualifiedSource(r *createServiceSessionRecovery, archived *createManagedLateRemaining, materials map[string]string, review string) error {
	if r == nil || r.ManagedLateRemaining == nil || archived == nil || archived.Review != review || archived.CurrentSourceSHA != r.ManagedLateRemaining.CurrentSourceSHA || archived.CleanupOnly != r.ManagedLateRemaining.CleanupOnly || archived.FinalizationOnly != r.ManagedLateRemaining.FinalizationOnly || archived.LogProofSHA != r.ManagedLateRemaining.LogProofSHA || archived.CleanupSourceSHA != r.ManagedLateRemaining.CleanupSourceSHA || !managedLateRemainingCleanupBaseMatches(r.ManagedLateRemaining, archived) || restoreManagedLateRemainingHistoricalCleanup(r, archived.HistoricalSeedCleanup) != nil || materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(r.ManagedHistoricalSeedCleanup) {
		return createOpenBaoACKBlock("late-remaining-immutable-source")
	}
	return nil
}

// Terminal native qualification rebuilds original e2 materials first. The
// already authenticated Remaining proof has a distinct Source388 binding and
// must replace that inherited pin after the fresh native checks complete.
func bindManagedLateRemainingHistoricalCleanupMaterial(r *createServiceSessionRecovery, materials map[string]string) error {
	if r == nil || r.ManagedLateRemaining == nil || materials == nil || restoreManagedLateRemainingHistoricalCleanup(r, r.ManagedLateRemaining.HistoricalSeedCleanup) != nil {
		return managedHistoricalSeedBlockAt("remaining-bind-historical-proof")
	}
	materials[managedHistoricalSeedCleanupMaterial] = deploymentplan.Digest(r.ManagedHistoricalSeedCleanup)
	return nil
}

func managedLateRemainingCleanupBaseMatches(current, archived *createManagedLateRemaining) bool {
	if current == nil || archived == nil {
		return false
	}
	a, b := *current, *archived
	a.HistoricalSeedCleanup, b.HistoricalSeedCleanup = nil, nil
	return managedLateRemainingDigest(&a) == managedLateRemainingDigest(&b)
}

func prepareManagedLateRemainingCopyRetirement(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateSession == nil || r.ManagedLateSession.ExpiredRenewal == nil || r.ManagedLateSession.ExpiredRenewal.PriorLate == nil {
		return managedHistoricalSeedBlockAt("remaining-retirement-runtime")
	}
	for _, late := range []*createManagedLateSessionRecovery{r.ManagedLateSession, r.ManagedLateSession.ExpiredRenewal.PriorLate} {
		if err := prepareManagedLateOriginalSeedRetirement(store, e, r, late); err != nil {
			return err
		}
	}
	r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
	return nil
}

func restoreManagedLateRemainingHistoricalCleanup(r *createServiceSessionRecovery, proof *createManagedHistoricalSeedCleanup) error {
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateSession == nil || proof == nil {
		return managedHistoricalSeedBlockAt("remaining-restore-runtime")
	}
	remaining, late := r.ManagedLateRemaining, r.ManagedLateSession
	if proof.SourceSHA != remaining.SourceSHA || proof.CurrentSession != late.NewSessionID || proof.ClientsSHA != late.OriginalClientsSHA || remaining.StaleSeedsSHA != deploymentplan.Digest(r.StaleSeeds) {
		return managedHistoricalSeedBlockAt("remaining-restore-lineage")
	}
	var observed *createServiceSessionObserved
	for i := range r.StaleSeeds {
		if r.StaleSeeds[i].Cleanup.SessionID == proof.State.Session {
			if observed != nil {
				return managedHistoricalSeedBlockAt("remaining-restore-duplicate-observation")
			}
			observed = &r.StaleSeeds[i]
		}
	}
	if observed == nil || proof.ObservationSHA != deploymentplan.Digest(observed) {
		return managedHistoricalSeedBlockAt("remaining-restore-observation")
	}
	s := proof.State
	state := managedSessionState{Session: s.Session, Issuer: s.Issuer, Root: s.Root, Fingerprint: s.Fingerprint, Status: s.Status, Expires: s.Expires, Subjects: slices.Clone(s.Subjects), Installed: slices.Clone(s.Installed)}
	snapshot, err := validateManagedHistoricalSeedState(state, *observed, r.Ceremony, late.SourceRenewedIntent.Subjects, time.Now().UTC())
	if err != nil || deploymentplan.Digest(snapshot) != deploymentplan.Digest(s) {
		return managedHistoricalSeedBlockAt("remaining-restore-state")
	}
	copy := *proof
	copy.State.Subjects, copy.State.Installed = slices.Clone(s.Subjects), slices.Clone(s.Installed)
	r.ManagedHistoricalSeedCleanup, remaining.HistoricalSeedCleanup = &copy, &copy
	return nil
}

func managedLateRemainingHistoricalCleanupAuthority(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, proof *createManagedHistoricalSeedCleanup) error {
	if r == nil || r.ManagedLateRemaining == nil || proof == nil || deploymentplan.Digest(proof) != deploymentplan.Digest(r.ManagedLateRemaining.HistoricalSeedCleanup) || proof.SourceSHA != r.ManagedLateRemaining.SourceSHA || proof.CurrentSession != r.ManagedLateRemaining.SessionID {
		return managedHistoricalSeedBlockAt("remaining-cleanup-proof")
	}
	if err := verifyManagedLateRemainingAuthorityCurrent(store, e, r.ManagedLateRemaining); err != nil {
		return err
	}
	if r.ManagedLateRemaining.Authority == nil {
		return managedHistoricalSeedBlockAt("remaining-cleanup-authority")
	}
	var a createFoundationRecoveryArtifact
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", r.ManagedLateRemaining.Authority.Review, "qualification.json"), &a) != nil || a.Plan.Materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(proof) || a.Plan.Materials["accepted-service-core-managed-application-current-clients"] != proof.ClientsSHA {
		return managedHistoricalSeedBlockAt("remaining-cleanup-materials")
	}
	return nil
}

func managedLateRemainingAuthorityPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", managedLateRemainingAuthorityFilename)
}

// The remaining action has its own signed-leaf origin. The original caller
// deadline and genuine native expiry still cap its once-recorded authority.
func managedLateRemainingDeadline(epoch, overall, leaf, session time.Time) time.Time {
	deadline := epoch.Add(managedLateRemainingLimit)
	for _, cutoff := range []time.Time{overall, leaf.Add(-managedLateExpiryReserve), session.Add(-managedLateExpiryReserve)} {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}

func managedLateRemainingPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{
		"schema":                "rtk-cloud.create-managed-late-remaining-policy/v2",
		"signed_leaf_limit":     managedLateRemainingLimit.String(),
		"original_caller_limit": managedLateOverallLimit.String(),
		"expiry_reserve":        managedLateExpiryReserve.String(),
		"finalizer":             managedLateFinalizerLimit.String(),
	})
}

func managedLateRemainingAuthorityValid(a *createManagedLateRemainingAuthority) bool {
	if a == nil || a.Schema != managedLateRemainingSchema || a.PolicySHA != managedLateRemainingPolicySHA() || !environmentBootstrapOwnerUUID.MatchString(a.SessionID) {
		return false
	}
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.OriginalReview, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.Fingerprint, a.KeySHA, a.CSRSHA, a.SeedObservationSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return !a.StartedAt.IsZero() && a.StartedAt.Before(a.LeafNotAfter) && !a.AdmittedAt.Before(a.StartedAt) && !a.OverallDeadline.IsZero() && !a.OverallDeadline.After(a.AdmittedAt.Add(managedLateOverallLimit)) && a.LeafNotAfter.Equal(a.SessionExpires) && a.Deadline.Equal(managedLateRemainingDeadline(a.StartedAt, a.OverallDeadline, a.LeafNotAfter, a.SessionExpires)) && !a.AdmittedAt.After(a.Deadline.Add(-managedLateFinalizerLimit))
}

func managedLateRemainingAuthorityMatches(r *createManagedLateRemaining, a *createManagedLateRemainingAuthority) bool {
	return r != nil && managedLateRemainingAuthorityValid(a) && a.ParentSHA == r.ParentSHA && a.SourceSHA == r.SourceSHA && a.OwnerSHA == r.OwnerSHA && a.OriginalReview == r.OriginalReview && a.DescriptorSHA == managedLateRemainingDigest(r) && a.SeedObservationSHA == r.SeedObservationSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && a.KeySHA == r.KeySHA && a.CSRSHA == r.CSRSHA && a.StartedAt.Equal(r.NotBefore) && a.LeafNotAfter.Equal(r.LeafNotAfter) && a.SessionExpires.Equal(r.SessionExpires)
}

func newManagedLateRemainingAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLateRemainingAuthority, error) {
	if r == nil || r.Review != deploymentplan.Digest(a.Plan) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.SourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"descriptor"] != managedLateRemainingDigest(r) || a.Plan.Materials[managedLateRemainingMaterial+"policy"] != managedLateRemainingPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return nil, createOpenBaoACKBlock("late-remaining-authority")
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return nil, createOpenBaoACKBlock("late-remaining-six-go")
		}
	}
	clock := &createManagedLateRemainingAuthority{Schema: managedLateRemainingSchema, ParentSHA: r.ParentSHA, SourceSHA: r.SourceSHA, OwnerSHA: r.OwnerSHA, OriginalReview: r.OriginalReview, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), DescriptorSHA: managedLateRemainingDigest(r), SessionID: r.SessionID, Fingerprint: r.Fingerprint, KeySHA: r.KeySHA, CSRSHA: r.CSRSHA, SeedObservationSHA: r.SeedObservationSHA, StartedAt: r.NotBefore, OverallDeadline: overall, AdmittedAt: now, LeafNotAfter: r.LeafNotAfter, SessionExpires: r.SessionExpires}
	clock.QualifiedBytesSHA = bytesSHA
	clock.PolicySHA = managedLateRemainingPolicySHA()
	clock.Deadline = managedLateRemainingDeadline(clock.StartedAt, overall, clock.LeafNotAfter, clock.SessionExpires)
	if !managedLateRemainingAuthorityValid(clock) {
		return nil, createOpenBaoACKBlock("late-remaining-fixed-leaf-deadline")
	}
	return clock, nil
}

func readManagedLateRemainingAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateRemainingAuthority, error) {
	if r == nil || !environmentPreflightDigest.MatchString(r.ParentSHA) {
		return nil, createOpenBaoACKBlock("late-remaining-authority-parent")
	}
	path, err := privateDeploymentStatePath(store.Environment, managedLateRemainingAuthorityPath(r.ParentSHA))
	if err != nil || path != filepath.Join(store.Root, managedLateRemainingAuthorityPath(r.ParentSHA)) {
		return nil, createOpenBaoACKBlock("late-remaining-authority-path")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if _, statErr := os.Lstat(dir); statErr == nil && validateEnvironmentPreflightDirectory(dir) != nil || statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return nil, createOpenBaoACKBlock("late-remaining-authority-path")
			}
			if dir == store.Root {
				break
			}
		}
		return nil, nil
	} else if err != nil {
		return nil, createOpenBaoACKBlock("late-remaining-authority-path")
	}
	var authority createManagedLateRemainingAuthority
	raw, err := readBoundedEnvironmentFile(path, true)
	canonical, _ := json.Marshal(authority)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || decodeEnvironmentJSON(raw, &authority) != nil {
		return nil, createOpenBaoACKBlock("late-remaining-authority-content")
	}
	canonical, _ = json.Marshal(authority)
	if !bytes.Equal(raw, canonical) || !managedLateRemainingAuthorityMatches(r, &authority) {
		return nil, createOpenBaoACKBlock("late-remaining-authority-binding")
	}
	var a createFoundationRecoveryArtifact
	qualificationPath := filepath.Join(store.Root, "deployment/create/openbao-recovery", authority.Review, "qualification.json")
	qualificationRaw, readErr := readBoundedEnvironmentFile(qualificationPath, true)
	if readErr != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(qualificationPath)) != nil || environmentPreflightBytesSHA256(qualificationRaw) != authority.QualifiedBytesSHA || decodeEnvironmentJSON(qualificationRaw, &a) != nil || deploymentplan.Digest(a.Plan) != authority.Review || deploymentplan.Digest(a) != authority.ArtifactSHA {
		return nil, createOpenBaoACKBlock("late-remaining-authority-qualification")
	}
	copy := *r
	copy.Review = authority.Review
	copy.Authority = nil
	expected, err := newManagedLateRemainingAuthority(&copy, a, authority.OverallDeadline, authority.AdmittedAt, authority.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(authority) {
		return nil, createOpenBaoACKBlock("late-remaining-authority-qualification")
	}
	return &authority, nil
}

func admitManagedLateRemaining(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if e == nil || e.Check(ctx) != nil || r == nil || (managedLateRemainingCurrentSource(r) != deploymentplan.Digest(e.Journal) && !managedLateResidualCleanupProgressValid(e.Journal, r)) || r.OwnerSHA != deploymentplan.Digest(e.owner) || !managedLateQualifiedArtifactValid(a, e, a.Plan, r.Review, time.Now().UTC(), managedLateExpiredRenewalSchema) {
		return nil, createOpenBaoACKBlock("late-remaining-admission-source")
	}
	actual, err := readManagedLateQualificationFingerprint(store, a)
	if err != nil || actual != bytesSHA {
		return nil, createOpenBaoACKBlock("late-remaining-qualified-bytes")
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || checker != a.Plan.CheckerSHA {
		return nil, createOpenBaoACKBlock("late-remaining-current-checker")
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return nil, err
	}
	if r.ResidualRemaining != nil {
		return admitManagedLateResidualRemaining(ctx, store, e, a, bytesSHA, r, overall)
	}
	if r.ResidualCleanup != nil {
		return admitManagedLateResidualCleanup(ctx, store, e, a, bytesSHA, r, overall)
	}
	if r.PostNativeCleanup != nil {
		return admitManagedLatePostNativeCleanup(ctx, store, e, a, bytesSHA, r, overall)
	}
	if r.TerminalFinalizerCompletion != nil {
		return admitManagedLateTerminalFinalizer(ctx, store, e, a, bytesSHA, r, overall)
	}
	if r.FinalizationOnly {
		return admitManagedLateFinalization(ctx, store, e, a, bytesSHA, r, overall)
	}
	if r.CleanupOnly {
		return admitManagedLateInstalledCleanup(ctx, store, e, a, bytesSHA, r, overall)
	}
	prior, err := readManagedLateRemainingAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if time.Until(prior.Deadline) < managedLateFinalizerLimit {
			return nil, createOpenBaoACKBlock("late-remaining-fixed-leaf-deadline")
		}
		invalidateManagedLateSourceReuse(e)
		r.Authority = prior
		return prior, nil
	}
	if r.CurrentSourceSHA != "" {
		return nil, createOpenBaoACKBlock("late-remaining-resume-missing-authority")
	}
	authority, err := newManagedLateRemainingAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	relative := managedLateRemainingAuthorityPath(r.ParentSHA)
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) || ensurePrivateDirectory(filepath.Dir(path)) != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createOpenBaoACKBlock("late-remaining-authority-path")
	}
	invalidateManagedLateSourceReuse(e)
	raw, err := json.Marshal(authority)
	if err != nil || writeExclusivePrivateDeploymentJSON(path, raw) != nil {
		return nil, createOpenBaoACKBlock("late-remaining-authority-exists-or-unavailable")
	}
	r.Authority = authority
	return authority, e.Check(ctx)
}

func restoreManagedLateRemainingCeremony(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.Authority == nil {
		return environmentCreateServiceCeremony{}, createOpenBaoACKBlock("late-remaining-admitted-authority")
	}
	if r.completedTail != nil {
		remaining, late := r.ManagedLateRemaining, r.ManagedLateSession
		if late == nil || runtime.ExecutionSHA256 != remaining.ParentSHA || runtime.Backend.Store != store || late.Review != remaining.OriginalReview || late.NewSessionID != remaining.SessionID || r.Ceremony.BootstrapSessionID != remaining.SessionID || deploymentplan.Digest(r.Ceremony) != deploymentplan.Digest(remaining.Ceremony) {
			return environmentCreateServiceCeremony{}, createOpenBaoACKBlock("completed-normal-tail-ceremony")
		}
		if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, runtime.Execution, r); err != nil {
			return environmentCreateServiceCeremony{}, err
		}
		r.Renewal = true
		return r.Ceremony, runtime.Execution.Check(ctx)
	}
	if _, err := managedLateActiveBudget(ctx); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	if err := verifyManagedLateRemainingPreparationSource(ctx, cfg, store, runtime); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	if r.ManagedLateRemaining.TerminalFinalizerCompletion == nil {
		if err := verifyServiceSessionAuthentication(ctx, cfg, store, runtime.Execution, runtime.Backend, r); err != nil {
			return environmentCreateServiceCeremony{}, err
		}
	}
	if r.ManagedLateRemaining.ResidualCleanup == nil {
		if err := verifyManagedLateSeedBudget(ctx, r.ManagedLateRemaining.LeafNotAfter, r.ManagedLateRemaining.SessionExpires); err != nil {
			return environmentCreateServiceCeremony{}, err
		}
	}
	r.Renewal = true
	r.Ceremony = r.ManagedLateRemaining.Ceremony
	return r.Ceremony, nil
}

// These two preparation boundaries perform no mutation in Remaining. The
// real helper/retire/restore callbacks retain their full fresh native observer.
func verifyManagedLateRemainingPreparationSource(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if r := runtime.ServiceSessionRecovery; r != nil && r.completedTail != nil {
		return verifyManagedLateCompletedTailSource(ctx, cfg, store, runtime.Execution, r)
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		return verifyManagedLateTerminalFinalizerRuntime(ctx, cfg, store, runtime)
	}
	bad := func(stage string) error { return createOpenBaoACKBlock("late-remaining-preparation-" + stage) }
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if ctx == nil || e == nil || e.Check(ctx) != nil {
		return bad("execution-fence")
	}
	budget, err := managedLateActiveBudget(ctx)
	if err != nil {
		return err
	}
	if !managedLateRemainingPreparationBindings(cfg, store, runtime, budget) {
		return bad("exact-profile")
	}
	if err := verifyManagedLateRemainingAuthorityCurrent(store, e, r.ManagedLateRemaining); err != nil {
		return err
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, r.ManagedLateSession); err != nil {
		return err
	}
	source, artifact, err := managedLateSessionRuntimeProgress(store, e, r.ManagedLateSession)
	if err != nil || source == nil || artifact == nil || artifact.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(artifact) != r.ManagedLateRemaining.OriginalArtifactSHA || deploymentplan.Digest(source.Journal) != r.ManagedLateSession.SourceSHA || artifact.Plan.ParentJournalSHA != r.ManagedLateSession.SourceSHA || artifact.Plan.ParentOwnerSHA != r.ManagedLateRemaining.OwnerSHA || artifact.Plan.Plan.ConfigurationSHA256 != deploymentplan.Digest(cfg) || artifact.Plan.Materials["accepted-service-core-late-session-source-descriptor"] != r.ManagedLateRemaining.DescriptorSHA {
		return bad("runtime-source")
	}
	return e.Check(ctx)
}

func managedLateRemainingPreparationBindings(cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, budget managedLateRuntimeBudget) bool {
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || r.ManagedLateRemaining == nil || r.ManagedLateSession == nil {
		return false
	}
	remaining, late := r.ManagedLateRemaining, r.ManagedLateSession
	return cfg.Environment == "dev" && cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] == "standard" && cfg.Values["CLOUD_STACK_NAME"] == e.Journal.Stack && store.Environment == cfg.Environment && store.Root == e.Store.Root && store.ConfigRoot == e.Store.ConfigRoot && store.Environment == e.Store.Environment && runtime.ExecutionSHA256 == remaining.ParentSHA && runtime.Backend.Store.Root == store.Root && runtime.Backend.Store.ConfigRoot == store.ConfigRoot && runtime.Backend.Store.Environment == store.Environment && remaining.ParentSHA == e.Journal.PlanSHA256 && remaining.OwnerSHA == deploymentplan.Digest(e.owner) && r.Review == late.Review && late.Review == remaining.OriginalReview && remaining.DescriptorSHA == managedLateSessionSourceDigest(late) && late.SourceOwnerSHA == remaining.OwnerSHA && late.NewSessionID == remaining.SessionID && r.Renewal && r.Ceremony.BootstrapSessionID == remaining.SessionID && r.Ceremony.CleanupBinding.SessionID == remaining.SessionID && deploymentplan.Digest(r.Ceremony) == deploymentplan.Digest(remaining.Ceremony) && managedLateRemainingAuthorityMatches(remaining, remaining.Authority) && !budget.PostMint && budget.InstallationClock == nil && managedLateRuntimeBudgetValid(budget) && ((remaining.FinalizationOnly && managedLateFinalizationMatches(remaining, budget.FinalizationAuthority) && deploymentplan.Digest(budget.FinalizationAuthority) == deploymentplan.Digest(remaining.FinalizationAuthority)) || (!remaining.FinalizationOnly && remaining.CleanupOnly && managedLateInstalledCleanupMatches(remaining, budget.InstalledCleanupAuthority) && deploymentplan.Digest(budget.InstalledCleanupAuthority) == deploymentplan.Digest(remaining.CleanupAuthority)) || (!remaining.CleanupOnly && budget.RemainingAuthority != nil && deploymentplan.Digest(budget.RemainingAuthority) == deploymentplan.Digest(remaining.Authority)))
}

func verifyManagedLateRemainingAuthorityCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if r != nil && r.completedTail != nil {
		return verifyManagedLateCompletedTailFrame(context.Background(), store, e, r.completedTail)
	}
	if r != nil && r.TerminalFinalizerCompletion != nil {
		return verifyManagedLateTerminalFinalizerAuthorityCurrent(store, e, r)
	}
	if err := managedLateRemainingRuntimePrefix(e, r); err != nil {
		return err
	}
	if r.Authority == nil {
		return nil
	} // Qualification grants no remaining mutation.
	actual, err := readManagedLateRemainingAuthority(store, r)
	if r.FinalizationOnly {
		if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.Authority) {
			return createOpenBaoACKBlock("finalization-only-original-authority")
		}
		return verifyManagedLateFinalizationCurrent(store, e, r)
	}
	if r.CleanupOnly {
		if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.Authority) {
			return createOpenBaoACKBlock("installed-cleanup-original-authority")
		}
		return verifyManagedLateInstalledCleanupCurrent(store, e, r)
	}
	if err != nil || actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.Authority) || !time.Now().Before(actual.Deadline) {
		return createOpenBaoACKBlock("late-remaining-current-authority")
	}
	return nil
}

// The independent completed proof pins this receipt even after fence archival.
// It reads only the captured388 prefix; later deployment entries are not input
// to the original runtime parser and cannot grant replacement authority.
func validateManagedLateRemainingCompletion(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, proof *createOpenBaoACKCompletion) error {
	cleanupReview := deploymentplan.String(proof.Native["installed_cleanup_review_sha256"])
	cleanupSHA := deploymentplan.String(proof.Native["installed_cleanup_authority_sha256"])
	if e == nil {
		return createOpenBaoACKBlock("completed-installed-cleanup-parent")
	}
	_, cleanupMarkerErr := os.Lstat(filepath.Join(store.Root, managedLateInstalledCleanupPath(e.Journal.PlanSHA256)))
	if !errors.Is(cleanupMarkerErr, os.ErrNotExist) && (!environmentPreflightDigest.MatchString(cleanupReview) || !environmentPreflightDigest.MatchString(cleanupSHA)) || (cleanupReview == "") != (cleanupSHA == "") {
		return createOpenBaoACKBlock("completed-installed-cleanup-proof-required")
	}
	review := deploymentplan.String(proof.Native["late_remaining_review_sha256"])
	sha := deploymentplan.String(proof.Native["late_remaining_authority_sha256"])
	if review == "" && sha == "" {
		if e == nil {
			return createOpenBaoACKBlock("completed-late-remaining-authority")
		}
		marker, err := readManagedLateRemainingAuthority(store, &createManagedLateRemaining{ParentSHA: e.Journal.PlanSHA256})
		if err != nil || marker != nil {
			return createOpenBaoACKBlock("completed-late-remaining-authority")
		}
		return nil
	}
	var remaining createManagedLateRemaining
	if e == nil || late == nil || len(e.Journal.Entries) < 388 || !environmentPreflightDigest.MatchString(review) || !environmentPreflightDigest.MatchString(sha) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateRemainingFilename), &remaining) != nil || remaining.Review != review || remaining.OriginalReview != late.Review || remaining.DescriptorSHA != managedLateSessionSourceDigest(late) || remaining.KeySHA != late.OriginalKeySHA || remaining.CSRSHA != late.OriginalCSRSHA || remaining.SessionID != late.NewSessionID {
		return createOpenBaoACKBlock("completed-late-remaining-source")
	}
	normalized := *e
	normalized.Journal = e.Journal
	normalized.Journal.CompletedAt = time.Time{}
	if err := managedLateRemainingRuntimePrefix(&normalized, &remaining); err != nil {
		return err
	}
	authority, err := readManagedLateRemainingAuthority(store, &remaining)
	if err != nil || authority == nil || authority.Review != review || deploymentplan.Digest(authority) != sha || proof.CompletedAt.Before(authority.AdmittedAt) || (proof.CompletedAt.After(authority.Deadline) && deploymentplan.String(proof.Native["installed_cleanup_review_sha256"]) == "") {
		return createOpenBaoACKBlock("completed-late-remaining-authority")
	}
	if review := deploymentplan.String(proof.Native["installed_cleanup_review_sha256"]); review != "" {
		var cleanup createManagedLateRemaining
		if !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateRemainingFilename), &cleanup) != nil || cleanup.Review != review || !cleanup.CleanupOnly || managedLateRemainingDigest(&cleanup) != managedLateRemainingDigest(&remaining) {
			return createOpenBaoACKBlock("completed-installed-cleanup-source")
		}
		cleanup.Authority = authority
		phase, err := readManagedLateInstalledCleanupAuthority(store, &cleanup)
		if err != nil || phase == nil || phase.Review != review || deploymentplan.Digest(phase) != deploymentplan.String(proof.Native["installed_cleanup_authority_sha256"]) || phase.OriginalAuthoritySHA != sha || proof.CompletedAt.Before(phase.StartedAt) || (proof.CompletedAt.After(phase.Deadline) && deploymentplan.String(proof.Native["finalization_only_review_sha256"]) == "") {
			return createOpenBaoACKBlock("completed-installed-cleanup-authority")
		}
		if len(normalized.Journal.Entries) < 389 || phase.SourceLeases > len(normalized.Journal.KubernetesLeases) {
			return createOpenBaoACKBlock("completed-installed-cleanup-prefix")
		}
		prefix := normalized.Journal
		prefix.Entries = prefix.Entries[:389]
		prefix.KubernetesLeases = prefix.KubernetesLeases[:phase.SourceLeases]
		var observation createManagedLateLogObservation
		if deploymentplan.Digest(prefix) != phase.SourceSHA || !prefix.Entries[388].CompletedAt.Equal(phase.InstalledAt) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-log-observation.json"), &observation) != nil || deploymentplan.Digest(observation) != phase.HelperObservationSHA {
			return createOpenBaoACKBlock("completed-installed-cleanup-helper")
		}

	}
	if err := validateManagedLateFinalizationCompletion(store, &normalized, late, proof); err != nil {
		return err
	}
	return nil
}

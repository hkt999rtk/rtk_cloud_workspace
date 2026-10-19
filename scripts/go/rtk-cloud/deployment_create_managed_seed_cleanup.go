package main

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedHistoricalSeedCleanupMaterial = "accepted-service-core-managed-historical-seed-cleanup"

// This snapshot binds only the expired seed's immutable native fields. The
// dynamic count of other active sessions is checked separately for current e0.
type managedHistoricalSeedState struct {
	Session, Issuer, Root, Fingerprint, Status string
	Expires                                    time.Time
	Subjects, Installed                        []string
}

type createManagedHistoricalSeedCleanup struct {
	SourceSHA, CurrentSession, ObservationSHA, ClientsSHA string
	State                                                 managedHistoricalSeedState
}

func managedHistoricalSeedBlock() error {
	return createProviderRecoveryBlock("used expired seed cleanup requires its exact authenticated original observation, preserved client lineage and five genuine native ACKs")
}

func managedHistoricalSeedStateOf(state managedSessionState) managedHistoricalSeedState {
	return managedHistoricalSeedState{Session: state.Session, Issuer: state.Issuer, Root: state.Root, Fingerprint: state.Fingerprint, Status: state.Status, Expires: state.Expires, Subjects: slices.Clone(state.Subjects), Installed: slices.Clone(state.Installed)}
}

func validateManagedHistoricalSeedState(state managedSessionState, observed createServiceSessionObserved, current environmentCreateServiceCeremony, subjects []string, now time.Time) (managedHistoricalSeedState, error) {
	var saved managedSessionState
	if decodeEnvironmentJSON(observed.Session, &saved) != nil || state.Session == current.BootstrapSessionID || state.Session != observed.Cleanup.SessionID || state.Session != saved.Session || !environmentBootstrapOwnerUUID.MatchString(state.Session) || state.Status != "expired" || state.Expires.IsZero() || state.Expires.After(now) || !state.Expires.Equal(saved.Expires) || state.Issuer != current.Intermediate.ID || state.Issuer != saved.Issuer || state.Root != current.Root.RootFingerprint || state.Root != saved.Root || !environmentPreflightDigest.MatchString(state.Fingerprint) || state.Fingerprint != saved.Fingerprint || !slices.Equal(state.Subjects, subjects) || !slices.Equal(saved.Subjects, subjects) || len(subjects) != 9 || len(observed.UIDs) != 3 || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, current.Root.Environment) != nil {
		return managedHistoricalSeedState{}, managedHistoricalSeedBlock()
	}
	expected := []string{"service:account-manager", "service:certissuer", "service:pki-controller", "service:pkibroker", "service:video-cloud-api"}
	installed := slices.Clone(state.Installed)
	slices.Sort(installed)
	if !slices.Equal(installed, expected) {
		return managedHistoricalSeedState{}, managedHistoricalSeedBlock()
	}
	seen := map[string]bool{}
	for _, subject := range subjects {
		if seen[subject] {
			return managedHistoricalSeedState{}, managedHistoricalSeedBlock()
		}
		seen[subject] = true
	}
	for _, subject := range installed {
		if !seen[subject] {
			return managedHistoricalSeedState{}, managedHistoricalSeedBlock()
		}
	}
	for _, uid := range observed.UIDs {
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return managedHistoricalSeedState{}, managedHistoricalSeedBlock()
		}
	}
	snapshot := managedHistoricalSeedStateOf(state)
	snapshot.Installed = installed
	return snapshot, nil
}

func qualifyManagedHistoricalSeedCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r != nil && r.ManagedLateSession != nil {
		return qualifyManagedLateHistoricalSeedCleanup(ctx, cfg, store, e, b, r, p)
	}
	if r == nil || r.ManagedApplicationPartial == nil {
		return nil
	}
	profile, err := managedApplicationCurrentSessionLifetime(r.ManagedApplicationPartial)
	if err != nil {
		return err
	}
	if profile != managedApplicationTrustACKSessionLifetime {
		return nil
	}
	if e == nil || p == nil || p.Materials == nil || store.Root != e.Store.Root || b.Store.Root != store.Root || e.Check(ctx) != nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil {
		return managedHistoricalSeedBlock()
	}
	if r.ManagedCorePartial != nil {
		if r.Review != r.ManagedCorePartial.OriginalRepairReview || managedCoreObservationSource(store, e, r.ManagedCorePartial) != nil {
			return managedHistoricalSeedBlock()
		}
	} else if _, _, err := managedApplicationInterruptedLeaseSource(e, e.Journal, r); err != nil {
		return err
	}
	return qualifyManagedHistoricalSeedCleanupNative(ctx, cfg, store, e, b, r, p)
}

// Native proof only: callers independently fence the exact original or late source.
func qualifyManagedHistoricalSeedCleanupNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	historical := r.ManagedTransportRecovery.Partial
	var clients managedTransportPreservedClients
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", historical.SourceReview, "managed-clients-preserved.json")
	if readExecutionJSON(path, &clients) != nil || clients.Renewed != r.Ceremony.BootstrapSessionID || clients.Original == r.Intent.SessionID || clients.Original == clients.Renewed || clients.Clients != historical.ClientsSHA || clients.Key != r.OriginalKeySHA || clients.CSR != r.OriginalCSRSHA {
		return managedHistoricalSeedBlock()
	}
	var observed *createServiceSessionObserved
	for index := range r.StaleSeeds {
		if r.StaleSeeds[index].Cleanup.SessionID == clients.Original {
			if observed != nil {
				return managedHistoricalSeedBlock()
			}
			observed = &r.StaleSeeds[index]
		}
	}
	if observed == nil {
		return managedHistoricalSeedBlock()
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, clients.Original)
	if err != nil {
		return err
	}
	snapshot, err := validateManagedHistoricalSeedState(state, *observed, r.Ceremony, subjects, time.Now().UTC())
	if err != nil {
		return err
	}
	proof := &createManagedHistoricalSeedCleanup{SourceSHA: deploymentplan.Digest(e.Journal), CurrentSession: r.Ceremony.BootstrapSessionID, ObservationSHA: deploymentplan.Digest(observed), ClientsSHA: clients.Clients, State: snapshot}
	r.ManagedHistoricalSeedCleanup = proof
	p.Materials[managedHistoricalSeedCleanupMaterial] = deploymentplan.Digest(proof)
	return e.Check(ctx)
}

func verifyManagedHistoricalSeedCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony, observed createServiceSessionObserved) (bool, error) {
	if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.ManagedLateSession != nil {
		known, err := verifyManagedLateHistoricalSeedCleanup(ctx, cfg, store, runtime, current, observed)
		if err != nil || known {
			return known, err
		}
		return verifyManagedLateResidualInstalledHistory(ctx, cfg, store, runtime, current, observed)
	}
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedHistoricalSeedCleanup == nil {
		return false, nil
	}
	proof := r.ManagedHistoricalSeedCleanup
	if observed.Cleanup.SessionID != proof.State.Session {
		return false, nil
	}
	if e == nil || e.Check(ctx) != nil || store.Root != e.Store.Root || current.BootstrapSessionID != proof.CurrentSession || deploymentplan.Digest(observed) != proof.ObservationSHA || !environmentPreflightDigest.MatchString(r.Review) {
		return false, managedHistoricalSeedBlock()
	}
	review, schema, err := managedHistoricalSeedAdmissionContext(r)
	if err != nil {
		return false, err
	}
	artifact, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || artifact.Plan.Schema != schema || artifact.Plan.ParentJournalSHA != proof.SourceSHA || deploymentplan.Digest(source) != proof.SourceSHA || artifact.Plan.Materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(proof) || artifact.Plan.Materials["accepted-service-core-managed-application-current-clients"] != proof.ClientsSHA {
		return false, managedHistoricalSeedBlock()
	}
	original := *e
	original.Journal = source
	if r.ManagedCorePartial != nil {
		if artifact.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(string(managedCoreFinalizeComplete)) {
			return false, managedHistoricalSeedBlock()
		}

		if r.Review != r.ManagedCorePartial.OriginalRepairReview || managedCoreObservationSource(store, &original, r.ManagedCorePartial) != nil {
			return false, managedHistoricalSeedBlock()
		}
	} else {
		_, _, forward, err := loadManagedApplicationInterruptedSource(store, &original)
		if err != nil || forward.Previous == nil || forward.Previous.Previous == nil || forward.Previous.Previous.Previous != nil || !managedApplicationInterruptedNativeCheckpoint(store, &original) {
			return false, managedHistoricalSeedBlock()
		}
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return false, err
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, proof.State.Session)
	if err != nil {
		return false, err
	}
	snapshot, err := validateManagedHistoricalSeedState(state, observed, current, subjects, time.Now().UTC())
	if err != nil || deploymentplan.Digest(snapshot) != deploymentplan.Digest(proof.State) {
		return false, managedHistoricalSeedBlock()
	}
	return true, e.Check(ctx)
}

func managedHistoricalSeedAdmissionContext(r *createServiceSessionRecovery) (string, string, error) {
	if r == nil || !environmentPreflightDigest.MatchString(r.Review) {
		return "", "", managedHistoricalSeedBlock()
	}
	if r.ManagedLateSession != nil {
		if r.Review != r.ManagedLateSession.Review || r.ManagedLateSession.Mode != managedLateSessionACKReady || !environmentPreflightDigest.MatchString(r.ManagedLateSession.SourceSHA) {
			return "", "", managedHistoricalSeedBlock()
		}
		return r.ManagedLateSession.Review, managedLateSessionSchema, nil
	}
	if r.ManagedCorePartial == nil {
		return r.Review, "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1", nil
	}
	core := r.ManagedCorePartial
	if core.OriginalRepairReview != r.Review || !environmentPreflightDigest.MatchString(core.ForwardReview) || core.ForwardReview == core.OriginalRepairReview || !environmentPreflightDigest.MatchString(core.SourceSHA) || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil {
		return "", "", managedHistoricalSeedBlock()
	}
	return core.ForwardReview, "rtk-cloud.create-managed-complete-issuer-partial-recovery/v1", nil
}

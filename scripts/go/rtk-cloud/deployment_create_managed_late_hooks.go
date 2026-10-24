// PRIVATE ready-to-integrate hook helpers; no native execution performed.
package main

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRecordedAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, string, string, error) {
	bad := func(stage string) (string, string, string, error) {
		return "", "", "", managedCoreObservationBlock("late-qualified-native-auth-" + stage)
	}
	if e == nil || r == nil || r.ManagedLateSession == nil {
		return bad("profile")
	}
	if e.Check(ctx) != nil {
		return bad("execution-fence")
	}
	actualExecution := e
	late := r.ManagedLateSession
	if r.completedTail != nil {
		if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
			return bad("completed-tail-source")
		}
		e = r.completedTail.view
	} else if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		if err := verifyManagedLateTerminalFinalizerSource(ctx, cfg, store, e, r); err != nil {
			return bad("terminal-source")
		}
		view := *e
		view.Journal = e.Journal
		view.Journal.Entries = view.Journal.Entries[:392]
		e = &view
	} else if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, late); err != nil {
		return bad("runtime-source-phase")
	}
	var source *createServiceSessionRecovery
	if late.Mode == managedLateSessionMQTTRepair || late.Mode == managedLateSessionOpenBaoTLSRepair {
		p := &createFoundationRecoveryPlan{Materials: map[string]string{}}
		var err error
		source, err = authenticateManagedLateObservationSource(cfg, store, e, r, p, late, late.Mode)
		if err != nil {
			return bad("repair-source")
		}
	} else if late.SourceSHA == deploymentplan.Digest(e.Journal) {
		p := &createFoundationRecoveryPlan{Materials: map[string]string{}}
		var loaded *createManagedLateSessionRecovery
		var err error
		source, loaded, err = loadManagedLateEffectiveSource(cfg, store, e, p, late)
		if err != nil || loaded.SourceSHA != late.SourceSHA || loaded.SourceArtifactSHA != late.SourceArtifactSHA || deploymentplan.Digest(loaded.SourceCeremony) != deploymentplan.Digest(late.SourceCeremony) || deploymentplan.Digest(loaded.SourceRenewedIntent) != deploymentplan.Digest(late.SourceRenewedIntent) {
			return bad("current-source")
		}
	} else {
		captured, admitted, err := managedLateSessionRuntimeProgress(store, e, late)
		if err != nil || admitted == nil || !managedLateSchemaMatches(admitted.Plan.Schema, late) || admitted.Plan.ParentJournalSHA != late.SourceSHA {
			return bad("runtime-progress")
		}
		p := &createFoundationRecoveryPlan{Plan: admitted.Plan.Plan, Materials: map[string]string{}}
		source, _, err = loadManagedLateEffectiveSource(cfg, store, captured, p, late)
		if err != nil {
			return bad("runtime-source")
		}
	}
	intent := late.SourceRenewedIntent
	// Prepared future intent is never authority. Only the actual completed
	// qualified native wrapper plus both real inner receipts selects new role.
	if managedLateSeedAuthorizationCompleted(e.Journal, late) {
		intent = source.Intent
		intent.SessionID = late.NewSessionID
	}
	if intent.SessionID != late.OriginalSession && intent.SessionID != late.NewSessionID || r.Ceremony.BootstrapSessionID != late.OriginalSession && r.Ceremony.BootstrapSessionID != late.NewSessionID {
		return bad("session-selection")
	}
	if r.Ceremony.BootstrapSessionID == late.NewSessionID {
		seed, _, _, err := readManagedLateEffectiveSeed(store, e, late)
		if err != nil || late.Renewal == nil || seed.SHA256() != late.Renewal.CandidateSHA256 && !managedCoreSameQualifiedPlan(seed, r.RenewalCandidate) && !managedLateEffectiveSeedIsForward(store, e, late, seed) || intent.SessionID != late.NewSessionID {
			return bad("effective-seed")
		}
	}
	controller := deploymentplan.Resource{}
	for _, resource := range late.AcceptedCore.Resources {
		if resource.ID == "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller" {
			controller = resource
			controller.UID = late.AcceptedCoreJournal.Owners[resource.ID]
		}
	}
	if late.SourceSHA != deploymentplan.Digest(e.Journal) && late.Mode == managedLateSessionACKReady {
		children, _, _, err := managedLateCurrentChildren(store, e, late)
		if err != nil {
			return bad("current-children")
		}
		owners, _, err := managedLateCurrentOwners(source, late, children)
		if err != nil {
			return bad("current-owners")
		}
		selected, ok := owners[controller.ID]
		if !ok {
			return bad("controller-selection")
		}
		controller = selected
	}
	current, err := b.Read(ctx, controller.ID)
	if err != nil {
		return bad("controller-read")
	}
	if controller.ID == "" || current == nil || deploymentplan.Map(current["metadata"])["uid"] != controller.UID || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(controller.Desired, current) {
		return bad("controller-owner")
	}
	ready := controller
	ready.Action = "retain"
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = b.Wait(bounded, ready)
	cancel()
	if err != nil {
		return bad("controller-ready")
	}
	auth := *r
	auth.Intent, auth.RenewedIntent = intent, intent
	// This is a guarded native primitive, not an ancestry bypass. The ordinary
	// getter independently binds the native signer text and exact recorded role.
	return verifyServiceSessionRecordedAuthenticationNative(ctx, cfg, store, actualExecution, b, &auth)
}

func readManagedLateRoutingCapability(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) (managedCoreRoutingPublication, error) {
	var empty managedCoreRoutingPublication
	if e == nil || p == nil || r == nil || r.ManagedLateSession == nil {
		return empty, managedCoreObservationBlock("late-routing-profile")
	}
	pureSource := e
	if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		if err := verifyManagedLateTerminalNativeSource(ctx, cfg, store, e, r); err != nil {
			return empty, err
		}
		var err error
		pureSource, err = managedLateTerminalCapabilitySourceView(ctx, cfg, store, e, r)
		if err != nil {
			return empty, err
		}
	}
	source, err := authenticateManagedLateObservationSource(cfg, store, pureSource, r, p, r.ManagedLateSession, r.ManagedLateSession.Mode)
	if err != nil {
		return empty, err
	}
	path, err := store.safePath("pki/app/routing.json")
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return empty, managedCoreObservationBlock("late-routing-path")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return empty, err
	}
	actual, err := b.Read(ctx, "ConfigMap/"+e.Journal.Stack+"-video-cloud/"+environmentAppRoutingDeclaration)
	if err != nil {
		return empty, err
	}
	proof, err := validateManagedCoreRoutingPublication(source.ManagedCorePartial, e.Journal, raw, actual)
	if err != nil || p.Materials["accepted-service-core-managed-late-observation-routing"] != deploymentplan.Digest(proof) {
		return empty, managedCoreObservationBlock("late-routing-qualification-drift")
	}
	return proof, nil
}

// This view supplies only routing ancestry after the caller has authenticated
// the complete terminal source. It cannot pass Execution.Check or native IO.
func managedLateTerminalCapabilitySourceView(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) (*environmentCreateExecution, error) {
	if r != nil && r.completedTail != nil {
		if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
			return nil, err
		}
		return r.completedTail.view, nil
	}
	bad := func() (*environmentCreateExecution, error) {
		return nil, managedCoreObservationBlock("late-routing-terminal-view")
	}
	if e == nil || r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.TerminalFinalizerCompletion == nil || store != e.Store || cfg.Environment != store.Environment || !managedLateTerminalSourceSelected(e.Journal) {
		return bad()
	}
	remaining, terminal := r.ManagedLateRemaining, r.ManagedLateRemaining.TerminalFinalizerCompletion
	if remaining.ParentSHA != e.Journal.PlanSHA256 || remaining.OwnerSHA != deploymentplan.Digest(e.owner) || terminal.Schema != managedLateTerminalFinalizerSchema || terminal.SourceSHA != managedLateTerminalCapabilityPrefix(e.Journal) || terminal.PrefixSHA != remaining.CurrentSourceSHA {
		return bad()
	}
	view, err := managedLateTerminalPureSourceView(ctx, e)
	if err != nil || deploymentplan.Digest(view.Journal) != terminal.PrefixSHA {
		return bad()
	}
	return view, nil
}

func qualifyManagedLateHistoricalSeedCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r == nil || r.ManagedLateSession == nil {
		return managedHistoricalSeedBlockAt("qualification-runtime")
	}
	late := r.ManagedLateSession
	if late.Mode == managedLateSessionMQTTRepair || late.Mode == managedLateSessionOpenBaoTLSRepair {
		return nil
	}
	if late.Mode != managedLateSessionACKReady || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) || late.NewSessionID == late.OriginalSession {
		return managedHistoricalSeedBlockAt("qualification-session")
	}
	source, err := authenticateManagedLateObservationSource(cfg, store, e, r, p, late, late.Mode)
	if err != nil {
		return err
	}
	// Shared existing primitive is reached only after this exact late source,
	// never by clearing old Core ancestry to satisfy an unrelated guard.
	source.Intent = late.SourceRenewedIntent
	if f := late.ExpiredRenewal; f != nil {
		source.Ceremony = f.PriorLate.SourceCeremony
		source.Intent = f.PriorLate.SourceRenewedIntent
	}
	if err := qualifyManagedHistoricalSeedCleanupNative(ctx, cfg, store, e, b, source, p); err != nil {
		return err
	}
	proof := source.ManagedHistoricalSeedCleanup
	expectedPrevious := late.OriginalSession
	if late.ExpiredRenewal != nil {
		expectedPrevious = late.ExpiredRenewal.PriorLate.OriginalSession
	}
	if proof == nil || proof.CurrentSession != expectedPrevious {
		return managedHistoricalSeedBlockAt("qualification-proof-session")
	}
	proof.CurrentSession = late.NewSessionID // planned authority, no future UID/state
	copy := *proof
	copy.State.Subjects = slices.Clone(proof.State.Subjects)
	copy.State.Installed = slices.Clone(proof.State.Installed)
	r.ManagedHistoricalSeedCleanup = &copy
	late.HistoricalSeedCleanup = &copy
	p.Materials[managedHistoricalSeedCleanupMaterial] = deploymentplan.Digest(&copy)
	if err := prepareManagedLateOriginalSeedRetirement(store, e, r, late); err != nil {
		return err
	}
	if late.ExpiredRenewal != nil {
		if err := prepareManagedLateOriginalSeedRetirement(store, e, r, late.ExpiredRenewal.PriorLate); err != nil {
			return err
		}
	}
	p.Materials["accepted-service-core-late-session-original-copy-retirement"] = deploymentplan.Digest(map[string]any{"session": late.OriginalSession, "cleanup": late.SourceCeremony.CleanupBinding, "uids": late.SourceCeremony.CredentialUIDs})
	return nil
}

func verifyManagedLateHistoricalSeedCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony, observed createServiceSessionObserved) (bool, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateSession == nil || e == nil || e.Check(ctx) != nil {
		return false, managedHistoricalSeedBlockAt("late-runtime")
	}
	late := r.ManagedLateSession
	if late.Mode != managedLateSessionACKReady || current.BootstrapSessionID != late.NewSessionID || r.Review != late.Review {
		return false, managedHistoricalSeedBlockAt("late-current-session")
	}
	artifact, err := verifyManagedLateNativeFinalization(ctx, cfg, store, runtime, current)
	if err != nil {
		return false, err
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return false, err
	}
	clients, err := readManagedSessionClientsPreserved(ctx, cfg, runtime, current, subjects)
	if err != nil || clients != late.OriginalClientsSHA {
		return false, managedHistoricalSeedBlockAt("late-preserved-clients")
	}
	if late.ExpiredRenewal != nil {
		if matched, handled, err := verifyManagedLateExpiredHistoricalCopy(ctx, cfg, store, runtime, current, observed, artifact); handled {
			return matched, err
		}
	}
	if observed.Cleanup.SessionID == late.OriginalSession {
		if deploymentplan.Digest(observed.Cleanup) != deploymentplan.Digest(late.SourceCeremony.CleanupBinding) || deploymentplan.Digest(observed.UIDs) != deploymentplan.Digest(late.SourceCeremony.CredentialUIDs) {
			return false, managedHistoricalSeedBlockAt("late-original-copy")
		}
		old, _, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
		if err != nil || old.Status != "expired" || old.OtherActive != 0 || old.Expires.After(time.Now().UTC()) || old.Session != late.OriginalSession || old.Issuer != current.Intermediate.ID || old.Root != current.Root.RootFingerprint {
			return false, managedHistoricalSeedBlockAt("late-original-state")
		}
		declared := map[string]bool{}
		for _, subject := range subjects {
			declared[subject] = true
		}
		seen := map[string]bool{}
		for _, subject := range old.Installed {
			if !declared[subject] || seen[subject] {
				return false, managedHistoricalSeedBlockAt("late-original-ack-set")
			}
			seen[subject] = true
		}
		if len(seen) != 8 || seen["service:video-cloud-logingester"] || !slices.Equal(old.Subjects, subjects) {
			return false, managedHistoricalSeedBlockAt("late-original-eight-acks")
		}
		descriptor := managedCoreCompletedSession{Action: string(managedCoreRestoreOnly), Session: old.Session, Issuer: old.Issuer, Root: old.Root, Fingerprint: old.Fingerprint, Expires: old.Expires, Subjects: slices.Clone(old.Subjects), Installed: slices.Clone(old.Installed)}
		slices.Sort(descriptor.Installed)
		if artifact.Plan.Materials["accepted-service-core-managed-complete-current-session"] != deploymentplan.Digest(descriptor) {
			return false, managedHistoricalSeedBlockAt("late-original-descriptor")
		}
		return true, e.Check(ctx)
	}
	proof := r.ManagedHistoricalSeedCleanup
	if proof == nil || observed.Cleanup.SessionID != proof.State.Session {
		return false, nil
	}
	if proof.CurrentSession != late.NewSessionID || proof.ClientsSHA != late.OriginalClientsSHA || proof.ObservationSHA != deploymentplan.Digest(observed) {
		return false, managedHistoricalSeedBlockAt("late-five-ack-proof")
	}
	if r.ManagedLateRemaining != nil {
		if err := managedLateRemainingHistoricalCleanupAuthority(store, e, r, proof); err != nil {
			return false, err
		}
	} else if proof.SourceSHA != late.SourceSHA || artifact.Plan.Materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(proof) {
		return false, managedHistoricalSeedBlockAt("late-five-ack-authority")
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, proof.State.Session)
	if err != nil {
		return false, err
	}
	snapshot, err := validateManagedHistoricalSeedState(old, observed, current, subjects, time.Now().UTC())
	if err != nil || deploymentplan.Digest(snapshot) != deploymentplan.Digest(proof.State) {
		return false, managedHistoricalSeedBlockAt("late-five-ack-state")
	}
	return true, e.Check(ctx)
}

func managedLateTerminalCapabilityPrefix(j environmentCreateJournal) string {
	if len(j.Entries) < 396 {
		return ""
	}
	j.Entries = j.Entries[:396]
	return deploymentplan.Digest(j)
}

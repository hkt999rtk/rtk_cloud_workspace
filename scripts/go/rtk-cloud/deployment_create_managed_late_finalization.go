// PRIVATE ONLY. Native finalization precedes historical-copy retirement. A
// sealed complete session and genuine native finalization/tombstone authorize
// only the exact old copies, never issuance or a replacement identity.
package main

import (
	"context"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"time"
)

type managedLateCustodyTombstone struct {
	Schema       string                                 `json:"schema"`
	Finalization environmentCreateBootstrapFinalization `json:"finalization"`
	Root         string                                 `json:"root_sha256"`
	Files        map[string]string                      `json:"file_sha256"`
}

// This bounded view ends BEFORE the historical736 verification. Generic later
// finalization/account/ingress entries grant no late runtime authority. Only
// the final parent CompletedAt scalar is normalized to the authenticated source
// value, after the original owner/archive has been authenticated by the caller.
func managedLateVerifiedProgressPrefix(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*environmentCreateExecution, *createFoundationRecoveryArtifact, error) {
	bad := func() (*environmentCreateExecution, *createFoundationRecoveryArtifact, error) {
		return nil, nil, createOpenBaoACKBlock("late-exact-verified-progress-prefix")
	}
	if e == nil || late == nil {
		return bad()
	}
	admitted, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil {
		return nil, nil, err
	}
	index := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "openbao-host-ack-verified-"+late.OriginalRepairReview {
			if index != -1 {
				return bad()
			}
			index = i
		}
	}
	if index <= len(source.Entries)+1 || e.Journal.Entries[index-1].Key != "managed-late-log-verified-"+late.Review || e.Journal.Entries[index-1].State != "completed" {
		return bad()
	}
	steps, err := managedLateProgressSteps(late)
	if err != nil {
		return nil, nil, err
	}
	leases := len(source.KubernetesLeases)
	for _, step := range steps {
		if step.Kind != "inner" && step.Kind != "log-proof" {
			leases++
		}
	}
	// The caller has authenticated the completed owner/archive. Only this
	// final scalar differs from the genuine stopped source; all entry and
	// lease bytes remain available to the forward frame's exact checks.
	normalized := *e
	normalized.Journal = e.Journal
	normalized.Journal.CompletedAt = source.CompletedAt
	appView, app, appErr := managedLateVerifiedAppFrame(store, &normalized, late)
	if appErr != nil {
		return nil, nil, appErr
	}
	if app != nil {
		leases++
		normalized = *appView
	}
	if f, _, _, frameErr := managedLateSeedForwardFrame(store, &normalized, late, source); frameErr != nil {
		return nil, nil, frameErr
	} else if f != nil {
		leases++
	}
	if leases > len(e.Journal.KubernetesLeases) {
		return bad()
	}
	bounded := *e
	bounded.Journal = e.Journal
	bounded.Journal.Entries = slices.Clone(e.Journal.Entries[:index])
	bounded.Journal.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases[:leases])
	bounded.Journal.CompletedAt = source.CompletedAt
	captured, a, err := managedLateSessionRuntimeProgress(store, &bounded, late)
	if err != nil || a == nil || deploymentplan.Digest(captured.Journal) != late.SourceSHA || deploymentplan.Digest(*a) != deploymentplan.Digest(admitted) {
		return bad()
	}
	return &bounded, a, nil
}

func prepareManagedLateOriginalSeedRetirement(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	if r == nil || late == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil {
		return managedHistoricalSeedBlockAt("prepare-retirement-runtime")
	}
	var observed createServiceSessionObserved
	sourceReview := r.ManagedTransportRecovery.Partial.SourceReview
	if late.ExpiredRenewal != nil {
		sourceReview = late.ExpiredRenewal.PriorReview
	}
	path := filepath.Join(store.Root, "deployment/create", late.ParentSHA, "bootstrap/service/session-renewal", sourceReview, "observed.json")
	var state managedSessionState
	if readExecutionJSON(path, &observed) != nil || decodeEnvironmentJSON(observed.Session, &state) != nil || state.Session != late.OriginalSession || state.Issuer != late.SourceCeremony.Intermediate.ID || state.Root != late.SourceCeremony.Root.RootFingerprint || state.Fingerprint != late.OriginalSessionState.Fingerprint || !state.Expires.Equal(late.OriginalSessionState.Expires) || observed.Reader != late.SourceCeremony.CustodyReader || deploymentplan.Digest(observed.Cleanup) != deploymentplan.Digest(late.SourceCeremony.CleanupBinding) || deploymentplan.Digest(observed.UIDs) != deploymentplan.Digest(late.SourceCeremony.CredentialUIDs) {
		return managedHistoricalSeedBlockAt("prepare-retirement-observation")
	}
	count := 0
	for _, old := range r.StaleSeeds {
		if old.Cleanup.SessionID == late.OriginalSession {
			count++
			if deploymentplan.Digest(old) != deploymentplan.Digest(observed) {
				return managedHistoricalSeedBlockAt("prepare-retirement-duplicate")
			}
		}
	}
	if count > 1 {
		return managedHistoricalSeedBlockAt("prepare-retirement-count")
	}
	if count == 0 {
		r.StaleSeeds = append(r.StaleSeeds, observed)
	}
	return nil
}

func managedLateFinalizedSessionCurrent(current managedSessionState, observed createManagedLateSeedObservation, late *createManagedLateSessionRecovery, subjects []string, native environmentCreateBootstrapFinalization, tombstone managedLateCustodyTombstone, now time.Time) bool {
	installed := slices.Clone(current.Installed)
	slices.Sort(installed)
	expected := slices.Clone(subjects)
	slices.Sort(expected)
	if current.Session != late.NewSessionID || current.Issuer != late.SourceCeremony.Intermediate.ID || current.Root != late.SourceCeremony.Root.RootFingerprint || current.Status != "sealed" && current.Status != "expired" || current.Status == "expired" && current.Expires.After(now) || current.OtherActive != 0 || current.Fingerprint != observed.Session.Fingerprint || !current.Expires.Equal(observed.Session.Expires) || !slices.Equal(current.Subjects, subjects) || !slices.Equal(installed, expected) || len(installed) != 9 || native.Environment != late.SourceCeremony.Root.Environment || native.SessionID != current.Session || native.LeafSHA256 != current.Fingerprint || !environmentPreflightDigest.MatchString(native.CRLSHA256) || !environmentPreflightDigest.MatchString(native.CleanupSHA256) || native.FinalizedAt.IsZero() || native.FinalizedAt.After(now) || tombstone.Schema != "rtk-cloud.bootstrap-credentials/v1" || !managedLateFinalizationEqual(tombstone.Finalization, native) || tombstone.Root != current.Root || len(tombstone.Files) != 3 || tombstone.Files["bootstrap.key"] != late.OriginalKeySHA || tombstone.Files["bootstrap.csr"] != late.OriginalCSRSHA || tombstone.Files["bootstrap.crt"] != environmentPreflightBytesSHA256([]byte(observed.CertificatePEM)) {
		return false
	}
	return true
}

func managedLateFinalizationEqual(a, b environmentCreateBootstrapFinalization) bool {
	return a.Environment == b.Environment && a.SessionID == b.SessionID && a.LeafSHA256 == b.LeafSHA256 && a.CRLSHA256 == b.CRLSHA256 && a.CleanupSHA256 == b.CleanupSHA256 && a.FinalizedAt.Equal(b.FinalizedAt)
}

func verifyManagedLateNativeFinalization(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony) (*createFoundationRecoveryArtifact, error) {
	bad := func(stage string) (*createFoundationRecoveryArtifact, error) {
		return nil, managedHistoricalSeedBlockAt(stage)
	}
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateSession == nil || e == nil || e.Check(ctx) != nil {
		return bad("finalization-runtime")
	}
	late := r.ManagedLateSession
	if err := beginManagedLateTerminalHistoricalSourcePhase(ctx, cfg, store, e, r); err != nil {
		return nil, err
	}
	bounded, artifact, err := managedLateVerifiedProgressPrefix(store, e, late)
	if err != nil {
		return nil, err
	}
	verified := createOpenBaoACKCompletionEntry(e.Journal, "openbao-host-ack-verified-"+late.OriginalRepairReview)
	var verification deploymentplan.Object
	if verified == nil || verified.State != "completed" || verified.Phase != "openbao-host-ack-verification" || decodeEnvironmentJSON(verified.Receipt, &verification) != nil || verified.IntentSHA256 != activeTransportJournalDigest(verification) || verification["late_review_sha256"] != late.Review || verification["session_id"] != late.NewSessionID || verified.CompletedAt.IsZero() {
		return bad("finalization-host-ack")
	}
	finalizer := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	intent := deploymentplan.Object{"session_id": current.BootstrapSessionID, "issuer_id": current.Intermediate.ID, "cleanup_binding": current.CleanupBinding}
	if finalizer == nil || finalizer.Phase != "bootstrap" || finalizer.State != "pending" && finalizer.State != "completed" || finalizer.IntentSHA256 != activeTransportJournalDigest(intent) || finalizer.StartedAt.Before(verified.CompletedAt) || current.BootstrapSessionID != late.NewSessionID || r.Review != late.Review || len(e.Journal.KubernetesLeases) != len(bounded.Journal.KubernetesLeases) {
		return bad("finalization-entry")
	}
	var first createServiceSessionObserved
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", late.ParentSHA, "bootstrap/service/session-observed.json"), &first) != nil || first.Cleanup.SessionID != r.Intent.SessionID {
		return bad("finalization-original-observation")
	}
	oldCopies := append([]createServiceSessionObserved{first}, r.StaleSeeds...)
	if err := validateManagedLateFinalizerTail(e.Journal, late, current, finalizer, oldCopies); err != nil {
		return nil, err
	}
	query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + late.NewSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var native environmentCreateBootstrapFinalization
	if err != nil || decodeEnvironmentJSON(raw, &native) != nil || native.FinalizedAt.Before(finalizer.StartedAt) {
		return bad("finalization-native-row")
	}
	var observed createManagedLateSeedObservation
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), &observed) != nil || observed.Review != late.Review || observed.Parent != late.ParentSHA || observed.KeySHA != late.OriginalKeySHA || observed.CSRSHA != late.OriginalCSRSHA {
		return bad("finalization-seed-observation")
	}
	marker, err := runtime.Backend.command(ctx, nil, "--namespace", current.BootstrapCredentialNamespace, "exec", "deployment/"+current.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	var tombstone managedLateCustodyTombstone
	state, _, stateErr := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	subjects, subjectsErr := environmentCreateBootstrapSubjects(cfg)
	if err != nil || stateErr != nil || subjectsErr != nil || decodeEnvironmentJSON(marker, &tombstone) != nil || !managedLateFinalizedSessionCurrent(state, observed, late, subjects, native, tombstone, time.Now().UTC()) {
		return bad("finalization-state")
	}
	if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.ResidualCleanup != nil && !managedLateResidualFinalizationBeforeExpiry(r, observed, state, native, time.Now().UTC()) {
		return bad("finalization-residual-deadline")
	}
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", current.BootstrapCredentialNamespace, "exec", "deployment/"+current.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", `test ! -e /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt`); err != nil {
		return bad("finalization-custody-tombstone")
	}
	for _, namespace := range current.BootstrapCredentialNamespaces {
		secret, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+current.BootstrapCredentialSecret)
		if err != nil || secret != nil {
			return bad("finalization-current-copy-retirement")
		}
	}
	clients, err := readManagedSessionClientsPreserved(ctx, cfg, runtime, current, subjects)
	if err != nil || clients != late.OriginalClientsSHA {
		return bad("finalization-preserved-clients")
	}
	return artifact, e.Check(ctx)
}

func validateManagedLateFinalizerTail(j environmentCreateJournal, late *createManagedLateSessionRecovery, current environmentCreateServiceCeremony, finalizer *environmentCreateJournalEntry, stale []createServiceSessionObserved) error {
	bad := func(stage string) error { return managedHistoricalSeedBlockAt(stage) }
	index := -1
	for i, entry := range j.Entries {
		if entry.Key == "openbao-host-ack-verified-"+late.OriginalRepairReview {
			index = i
		}
	}
	if index < 0 {
		return bad("finalizer-tail-host-ack")
	}
	copies := map[string]string{}
	for _, ns := range current.BootstrapCredentialNamespaces {
		copies["bootstrap-copy-retire-"+ns+"-"+current.BootstrapSessionID] = current.CredentialUIDs[ns]
	}
	for _, old := range stale {
		for ns, uid := range old.UIDs {
			copies["bootstrap-copy-retire-"+ns+"-"+old.Cleanup.SessionID] = uid
		}
	}
	// Earliest unused original seed remains bound to its original parent observed
	// file; its literal copies are validated by the ordinary retirement loop.
	cleanupSeen := false
	finalizeSeen := false
	seenKeys := map[string]bool{}
	for _, entry := range j.Entries[index+1:] {
		if seenKeys[entry.Key] {
			return bad("finalizer-tail-duplicate")
		}
		seenKeys[entry.Key] = true
		if entry.Key == "service-session-finalize" {
			if finalizeSeen || entry.IntentSHA256 != finalizer.IntentSHA256 {
				return bad("finalizer-tail-finalize")
			}
			finalizeSeen = true
			continue
		}
		if entry.Key == "bootstrap-renewed-cleanup-custody-"+late.Review {
			if !finalizeSeen || cleanupSeen || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("finalizer-tail-custody")
			}
			var receipt deploymentplan.Object
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || entry.IntentSHA256 != activeTransportJournalDigest(receipt) || receipt["session_id"] != late.NewSessionID || receipt["key_sha256"] != late.OriginalKeySHA || receipt["csr_sha256"] != late.OriginalCSRSHA {
				return bad("finalizer-tail-custody-receipt")
			}
			cleanupSeen = true
			continue
		}
		uid, ok := copies[entry.Key]
		if !ok || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.IsZero() {
			return bad("finalizer-tail-copy-entry")
		}
		var receipt struct {
			ID     string `json:"resource_id"`
			UID    string `json:"uid"`
			Absent bool   `json:"absent"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.UID != uid || !receipt.Absent {
			return bad("finalizer-tail-copy-receipt")
		}
		// Bind the resource/name/namespace/session from the original current or
		// archived observed UID inventory; no same-UID foreign intent is accepted.
		matched := false
		for _, candidate := range append([]createServiceSessionObserved{{Cleanup: current.CleanupBinding, UIDs: current.CredentialUIDs}}, stale...) {
			for ns, expectedUID := range candidate.UIDs {
				sid := candidate.Cleanup.SessionID
				if entry.Key != "bootstrap-copy-retire-"+ns+"-"+sid {
					continue
				}
				expectedID := "Secret/" + ns + "/service-bootstrap-" + sid
				expected := deploymentplan.Object{"resource_id": expectedID, "uid": expectedUID, "session_id": sid}
				if matched || receipt.ID != expectedID || entry.IntentSHA256 != activeTransportJournalDigest(expected) || sid == late.NewSessionID && (finalizeSeen || ns == current.BootstrapCredentialNamespace) || sid != late.NewSessionID && (!finalizeSeen || !cleanupSeen) {
					return bad("finalizer-tail-copy-order")
				}
				matched = true
			}
		}
		if !matched || finalizeSeen && !cleanupSeen {
			return bad("finalizer-tail-copy-match")
		}
	}
	if !finalizeSeen || !cleanupSeen {
		return bad("finalizer-tail-incomplete")
	}
	return nil
}

func allowManagedLateExpiredCredentialCopy(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace string) (bool, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateSession == nil {
		return false, nil
	}
	if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.ResidualCleanup != nil && !managedLateResidualCopyAllowed(r, s, namespace) {
		return false, managedHistoricalSeedBlockAt("copy-residual-approval")
	}
	late := r.ManagedLateSession
	if !slices.Contains(late.SourceCeremony.BootstrapCredentialNamespaces, namespace) {
		return false, managedHistoricalSeedBlockAt("copy-source-namespace")
	}
	for _, observed := range r.StaleSeeds {
		if observed.Cleanup.SessionID == s.BootstrapSessionID && observed.UIDs[namespace] == s.CredentialUIDs[namespace] {
			state, _, err := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
			subjects, subjectsErr := environmentCreateBootstrapSubjects(cfg)
			if err != nil || subjectsErr != nil {
				return false, managedHistoricalSeedBlockAt("copy-native-state")
			}
			if len(state.Installed) == 0 {
				// An expired-renewal source already carries the admitted original
				// observation and native no-mint descriptor. Authenticate that
				// authority before consulting the later remaining-phase snapshot;
				// finalization necessarily retires these copies before that snapshot
				// exists.
				if _, _, exactExpiredSource := managedLateExpiredHistoricalCopySource(late, observed); exactExpiredSource {
					allowed, err := verifyManagedHistoricalSeedCleanup(ctx, cfg, runtime.Execution.Store, runtime, r.Ceremony, observed)
					return allowed, err
				}
				// Earlier unused seeds are bound by the exact stale-set prefix in
				// the admitted Core/Application recovery. Later e0/e1 observations
				// use the expired-renewal branch above. The remaining-phase digest
				// is deliberately unavailable until after finalization retires these
				// copies.
				artifact, err := verifyManagedLateNativeFinalization(ctx, cfg, runtime.Execution.Store, runtime, r.Ceremony)
				if err != nil {
					return false, err
				}
				accepted := artifact.Plan.Materials["accepted-service-core-managed-partial-stale-seeds"]
				if stage := managedLateUnusedHistoricalCopyMismatchAtAuthority(r, observed, s, namespace, state, subjects, time.Now().UTC(), accepted); stage != "" {
					return false, managedHistoricalSeedBlockAt("copy-unused-history-" + stage)
				}
				return true, runtime.Execution.Check(ctx)
			}
			allowed, err := verifyManagedHistoricalSeedCleanup(ctx, cfg, runtime.Execution.Store, runtime, r.Ceremony, observed)
			if err != nil || allowed {
				return allowed, err
			}
			return false, managedHistoricalSeedBlockAt("copy-used-history")
		}
	}
	var first createServiceSessionObserved
	if s.BootstrapSessionID == r.Intent.SessionID && createOpenBaoACKCompletionRead(runtime.Execution.Store, filepath.Join("deployment/create", late.ParentSHA, "bootstrap/service/session-observed.json"), &first) == nil && first.Cleanup.SessionID == r.Intent.SessionID && first.UIDs[namespace] == s.CredentialUIDs[namespace] {
		if _, err := verifyManagedLateNativeFinalization(ctx, cfg, runtime.Execution.Store, runtime, r.Ceremony); err != nil {
			return false, err
		}
		state, _, err := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
		if err != nil || state.Status != "expired" || state.Expires.After(time.Now().UTC()) || state.OtherActive != 0 || len(state.Installed) != 0 || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint {
			return false, managedHistoricalSeedBlockAt("copy-first-native-state")
		}
		return true, runtime.Execution.Check(ctx)
	}
	return false, managedHistoricalSeedBlockAt("copy-observation-match")
}

// This grants no source authority: the caller has already verified the actual
// finalizer, the admitted stale observation and original installed clients.
// A used seed must continue through its separate exact ACK-history proof.
func managedLateUnusedHistoricalCopyMatches(r *createServiceSessionRecovery, observed createServiceSessionObserved, copy environmentCreateServiceCeremony, namespace string, state managedSessionState, subjects []string, now time.Time) bool {
	return managedLateUnusedHistoricalCopyMismatch(r, observed, copy, namespace, state, subjects, now) == ""
}

func managedLateUnusedHistoricalCopyMismatch(r *createServiceSessionRecovery, observed createServiceSessionObserved, copy environmentCreateServiceCeremony, namespace string, state managedSessionState, subjects []string, now time.Time) string {
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil {
		return "runtime"
	}
	return managedLateUnusedHistoricalCopyMismatchAtAuthority(r, observed, copy, namespace, state, subjects, now, r.ManagedLateRemaining.StaleSeedsSHA)
}

func managedLateUnusedHistoricalCopyMismatchAtAuthority(r *createServiceSessionRecovery, observed createServiceSessionObserved, copy environmentCreateServiceCeremony, namespace string, state managedSessionState, subjects []string, now time.Time, acceptedStaleSeedsSHA string) string {
	if r == nil || r.ManagedLateSession == nil {
		return "runtime"
	}
	if !managedLateAdmittedStaleObservation(r.StaleSeeds, observed, acceptedStaleSeedsSHA) {
		return "stale-seed-set"
	}
	if !slices.Contains(r.ManagedLateSession.SourceCeremony.BootstrapCredentialNamespaces, namespace) {
		return "source-namespace"
	}
	if !slices.Contains(copy.BootstrapCredentialNamespaces, namespace) {
		return "copy-namespace"
	}
	if !environmentBootstrapOwnerUUID.MatchString(copy.CredentialUIDs[namespace]) {
		return "copy-owner"
	}
	if copy.BootstrapSessionID != observed.Cleanup.SessionID {
		return "copy-session"
	}
	if copy.CredentialUIDs[namespace] != observed.UIDs[namespace] {
		return "observation-owner"
	}
	if copy.BootstrapCredentialSecret != "service-bootstrap-"+observed.Cleanup.SessionID {
		return "copy-name"
	}
	count := 0
	for _, stale := range r.StaleSeeds {
		if stale.Cleanup.SessionID == observed.Cleanup.SessionID {
			if deploymentplan.Digest(stale) != deploymentplan.Digest(observed) {
				return "observation-drift"
			}
			count++
		}
	}
	var saved managedSessionState
	if count != 1 {
		return "observation-count"
	}
	if decodeEnvironmentJSON(observed.Session, &saved) != nil {
		return "observation-state"
	}
	if saved.Session != observed.Cleanup.SessionID {
		return "saved-session"
	}
	if state.Status != "expired" {
		return "native-status"
	}
	if len(subjects) != 9 {
		return "subject-count"
	}
	if len(state.Installed) != 0 {
		return "native-installed"
	}
	if len(saved.Installed) != 0 {
		return "saved-installed"
	}
	// The stale observation can predate a same-key ICA successor. Its exact
	// admitted native issuer remains authoritative for retiring that old copy;
	// binding it to the current ceremony's successor ID rejects valid history.
	historicalCopy := copy
	historicalCopy.Intermediate.ID = saved.Issuer
	if !managedSessionMayRenew(state, historicalCopy, subjects, now) {
		return "native-renewable-state"
	}
	if state.Session != saved.Session {
		return "session-lineage"
	}
	if state.Issuer != saved.Issuer {
		return "issuer-lineage"
	}
	if state.Root != saved.Root {
		return "root-lineage"
	}
	if state.Fingerprint != saved.Fingerprint {
		return "fingerprint-lineage"
	}
	if !state.Expires.Equal(saved.Expires) {
		return "expiry-lineage"
	}
	if !slices.Equal(state.Subjects, saved.Subjects) {
		return "subject-lineage"
	}
	if saved.Status != "active" && saved.Status != "expired" {
		return "saved-status"
	}
	return ""
}

// A later recovery may append separately authenticated e0/e1 observations.
// Accept only the unique original prefix whose digest was captured by the
// qualified Core/Application plan, and only an exact member of that prefix.
func managedLateAdmittedStaleObservation(stale []createServiceSessionObserved, observed createServiceSessionObserved, acceptedSHA string) bool {
	if !environmentPreflightDigest.MatchString(acceptedSHA) {
		return false
	}
	for i := 1; i <= len(stale); i++ {
		if deploymentplan.Digest(stale[:i]) != acceptedSHA {
			continue
		}
		count := 0
		for _, candidate := range stale[:i] {
			if candidate.Cleanup.SessionID == observed.Cleanup.SessionID && deploymentplan.Digest(candidate) == deploymentplan.Digest(observed) {
				count++
			}
		}
		return count == 1
	}
	return false
}

func managedLateCompletedCredentialRetirement(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace string) (bool, error) {
	if runtime.ServiceSessionRecovery == nil || runtime.ServiceSessionRecovery.ManagedLateSession == nil {
		return false, nil
	}
	e := runtime.Execution
	uid := s.CredentialUIDs[namespace]
	if e == nil || e.Check(ctx) != nil || !environmentBootstrapOwnerUUID.MatchString(uid) {
		return false, managedHistoricalSeedBlockAt("completed-copy-runtime")
	}
	key := "bootstrap-copy-retire-" + namespace + "-" + s.BootstrapSessionID
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return false, nil
	}
	id := "Secret/" + namespace + "/" + s.BootstrapCredentialSecret
	intent := deploymentplan.Object{"resource_id": id, "uid": uid, "session_id": s.BootstrapSessionID}
	expected := deploymentplan.Object{"resource_id": id, "uid": uid, "absent": true}
	if entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(expected) {
		return false, managedHistoricalSeedBlockAt("completed-copy-receipt")
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil || actual != nil {
		return false, managedHistoricalSeedBlockAt("completed-copy-readback")
	}
	return true, e.Check(ctx)
}

package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the current ceremony of the authenticated fresh program can resume a
// pending finalizer. Historical renewal/managed-late cleanup keeps its own
// archive checks; a missing Secret or tombstone is never admission authority.
func currentFreshFinalizerCeremony(store secretStore, e *environmentCreateExecution, s environmentCreateServiceCeremony) (*environmentFreshProductProgram, error) {
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil, nil
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return nil, err
	}
	installed, err := readEffectiveFreshProductInstalled(store, e, &program.Lifecycle)
	if err != nil {
		return nil, err
	}
	if installed.Ceremony.BootstrapSessionID != s.BootstrapSessionID {
		return nil, nil // An ancestor is handled by the original archive protocol.
	}
	if deploymentplan.Digest(installed.Ceremony) != deploymentplan.Digest(s) {
		return nil, environmentCreateBootstrapBlocker("current fresh finalizer differs from its authenticated installed ceremony")
	}
	return program, nil
}

// Observe native completion independently of custody cleanup. A 503 can precede
// native completion; a lost response can follow it. Neither state implies that
// the later PVC tombstone exists. Reads never mark the original entry complete.
func observePendingFreshFinalizer(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) (environmentCreateBootstrapFinalization, bool, bool, error) {
	var result environmentCreateBootstrapFinalization
	e := runtime.Execution
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, environmentCreateServiceFinalizationKey(runtime)) == nil {
		return result, false, false, nil
	}
	program, err := currentFreshFinalizerCeremony(store, e, s)
	if err != nil || program == nil {
		return result, false, false, err
	}
	bad := func() (environmentCreateBootstrapFinalization, bool, bool, error) {
		return result, false, true, environmentCreateBootstrapBlocker("pending fresh finalization lacks its exact original intent, complete native roster or authoritative completion state")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, environmentCreateServiceFinalizationKey(runtime))
	intent := deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding}
	if e.Store != store || runtime.Backend.Store != store || e.Check(ctx) != nil || cfg.Environment != e.Journal.Environment || entry.Phase != "bootstrap" || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() || entry.StartedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return bad()
	}
	if err := verifyEnvironmentCreateFinalizerInstallations(ctx, cfg, runtime, s); err != nil {
		return result, false, true, err
	}
	subjects := environmentCreateServiceEnrollmentSubjects(program.Lifecycle.Policy)
	state, _, readErr := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
	if readErr != nil || len(subjects) == 0 || state.Session != s.BootstrapSessionID || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint || !environmentPreflightDigest.MatchString(state.Fingerprint) || state.OtherActive != 0 || !slices.Equal(state.Subjects, subjects) || state.Status != "active" && state.Status != "sealed" && state.Status != "expired" || state.Expires.IsZero() || state.Status == "expired" && state.Expires.After(time.Now().UTC()) {
		return bad()
	}
	query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil {
		return bad()
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return result, false, true, e.Check(ctx)
	}
	if decodeEnvironmentJSON(raw, &result) != nil || result.Environment != cfg.Environment || result.SessionID != s.BootstrapSessionID || result.LeafSHA256 != state.Fingerprint || !environmentPreflightDigest.MatchString(result.CRLSHA256) || !environmentPreflightDigest.MatchString(result.CleanupSHA256) || result.FinalizedAt.IsZero() || result.FinalizedAt.Before(entry.StartedAt) || result.FinalizedAt.After(time.Now().UTC()) {
		return bad()
	}
	return result, true, true, e.Check(ctx)
}

// Native completion is committed after the original primary Secret/policy are
// deleted. It remains authoritative if that reply is lost before local custody
// cleanup. This read-only history observation never fabricates a local receipt.
func observeFreshFinalizedPrimary(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, owners map[string]deploymentplan.Resource, read func(context.Context, string) (deploymentplan.Object, error), retired map[string]string) error {
	if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil || createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize") == nil {
		return nil
	}
	missing := false
	for id := range owners {
		if strings.HasPrefix(id, "Secret/"+e.Journal.Stack+"-video-cloud/service-bootstrap-") {
			actual, err := read(ctx, id)
			if err != nil {
				return err
			}
			missing = missing || actual == nil
		}
	}
	if !missing {
		return nil
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return err
	}
	installed, err := readEffectiveFreshProductInstalled(store, e, &program.Lifecycle)
	if err != nil {
		return err
	}
	s := installed.Ceremony
	id := "Secret/" + s.BootstrapCredentialNamespace + "/" + s.BootstrapCredentialSecret
	owner, tracked := owners[id]
	if !tracked {
		return nil
	}
	actual, err := read(ctx, id)
	if err != nil || actual != nil {
		return err // Ordinary owner validation rejects a replacement.
	}
	native, ok := b.(interface {
		nativeEnvironmentBackend() environmentKubernetesBackend
	})
	if !ok || owner.UID != s.CredentialUIDs[s.BootstrapCredentialNamespace] {
		return environmentCreateBootstrapBlocker("missing original primary credential has no selected native finalization observer")
	}
	cfg := deploymentConfig{Environment: e.Journal.Environment, Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native.nativeEnvironmentBackend()}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize"); entry.State == "completed" {
		if _, err := verifyProductFinalization(ctx, cfg, runtime, &program.Lifecycle, installed); err != nil {
			return err
		}
		retired[id] = owner.UID
		return e.Check(ctx)
	}
	_, done, handled, err := observePendingFreshFinalizer(ctx, cfg, store, runtime, s)
	if err != nil || !handled || !done {
		return environmentCreateBootstrapBlocker("missing original primary credential requires authoritative same-session native finalization")
	}
	retired[id] = owner.UID
	return e.Check(ctx)
}

// This is the original DELETE producer reconciling its own interrupted result.
// Preflight still consumes completed receipts only. Native finalization happens
// after secondary copies are deleted, so it cannot be their prerequisite.
func reconcileFreshCredentialRetirement(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace string) (bool, bool, error) {
	e := runtime.Execution
	if e == nil {
		return false, false, nil
	}
	key := "bootstrap-copy-retire-" + namespace + "-" + s.BootstrapSessionID
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return false, false, nil
	}
	program, err := currentFreshFinalizerCeremony(e.Store, e, s)
	if err != nil || program == nil {
		return false, false, err
	}
	bad := func() (bool, bool, error) {
		return false, true, environmentCreateBootstrapBlocker("fresh credential retirement differs from its exact original UID, intent or native outcome")
	}
	id, uid := "Secret/"+namespace+"/"+s.BootstrapCredentialSecret, s.CredentialUIDs[namespace]
	intent := deploymentplan.Object{"resource_id": id, "uid": uid, "session_id": s.BootstrapSessionID}
	receipt := deploymentplan.Object{"resource_id": id, "uid": uid, "absent": true}
	if cfg.Environment != e.Journal.Environment || runtime.Backend.Store != e.Store || e.Check(ctx) != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || entry.Phase != "bootstrap" || entry.StartedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return bad()
	}
	switch entry.State {
	case "pending":
		if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
			return bad()
		}
	case "allocated", "completed":
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(receipt) || (entry.State == "allocated") != entry.CompletedAt.IsZero() || entry.State == "completed" && entry.CompletedAt.Before(entry.StartedAt) {
			return bad()
		}
	default:
		return bad()
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil {
		return bad()
	}
	if actual != nil {
		meta := deploymentplan.Map(actual["metadata"])
		if entry.State != "pending" || meta["uid"] != uid || deploymentplan.String(meta["resourceVersion"]) == "" {
			return bad()
		}
		return false, true, e.Check(ctx)
	}
	if entry.State == "pending" {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
			return false, true, err
		}
	}
	if entry.State != "completed" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return false, true, err
		}
	}
	return true, true, e.Check(ctx)
}

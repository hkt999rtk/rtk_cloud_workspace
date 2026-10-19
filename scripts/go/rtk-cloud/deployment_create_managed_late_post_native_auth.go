package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"
)

// The original signer and shared Kubernetes role remain authenticated by the
// ordinary caller. Only a genuinely finalized, source-bound session can replace
// its old policy-presence requirement with exact session-policy absence.
func verifyManagedLatePostNativeAuthenticationSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, policy string) error {
	if r != nil && r.completedTail != nil {
		if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	} else if r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.ResidualCleanup != nil {
		if err := verifyManagedLateResidualCleanupSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	} else {
		if err := verifyManagedLatePostNativeCleanupSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	}
	remaining, late := r.ManagedLateRemaining, r.ManagedLateSession
	if !managedLatePostNativeCleanupNativeValid(remaining) || policy != "rtk-"+cfg.Environment+"-service-bootstrap-"+late.NewSessionID || r.Ceremony.BootstrapSessionID != late.NewSessionID {
		return createOpenBaoACKBlock("post-native-auth-finalized-source")
	}
	var seed createManagedLateSeedObservation
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), &seed) != nil {
		return createOpenBaoACKBlock("post-native-auth-original-seed")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	subjects, subjectsErr := environmentCreateBootstrapSubjects(cfg)
	if err != nil || subjectsErr != nil || !managedLateTerminalSessionForRecovery(state, seed, r, subjects, time.Now().UTC()) {
		return createOpenBaoACKBlock("post-native-auth-finalized-nine")
	}
	// Observe into a local copy: target qualification must never rewrite the
	// already captured native finalization or immutable cleanup descriptor.
	copyRecovery, copyRemaining, copyPost := *r, *remaining, *remaining.PostNativeCleanup
	copyRemaining.PostNativeCleanup = &copyPost
	copyRecovery.ManagedLateRemaining = &copyRemaining
	runtime.ServiceSessionRecovery = &copyRecovery
	if err := observeManagedLatePostNativeFinalization(ctx, cfg, runtime, seed, state, subjects); err != nil {
		return err
	}
	if !managedLateFinalizationEqual(copyPost.Native, remaining.PostNativeCleanup.Native) || copyPost.TombstoneSHA != remaining.PostNativeCleanup.TombstoneSHA {
		return createOpenBaoACKBlock("post-native-auth-original-finalization")
	}
	for _, namespace := range r.Ceremony.BootstrapCredentialNamespaces {
		secret, err := b.Read(ctx, "Secret/"+namespace+"/"+r.Ceremony.BootstrapCredentialSecret)
		if err != nil || secret != nil {
			return createOpenBaoACKBlock("post-native-auth-current-copies-absent")
		}
	}
	return e.Check(ctx)
}

func verifyManagedLatePostNativePolicyAbsent(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, token []byte, name string) error {
	prefix := "rtk-" + cfg.Environment + "-service-bootstrap-"
	if cfg.Environment != "dev" || !strings.HasPrefix(name, prefix) || !environmentBootstrapOwnerUUID.MatchString(strings.TrimPrefix(name, prefix)) || len(bytes.TrimSpace(token)) == 0 {
		return createOpenBaoACKBlock("post-native-auth-exact-session-policy")
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy list -format=json\n")
	if err != nil || !managedLatePostNativePolicyListAbsent(raw, name) {
		return createOpenBaoACKBlock("post-native-auth-session-policy-absence")
	}
	return nil
}

func managedLatePostNativePolicyListAbsent(raw []byte, name string) bool {
	if len(raw) == 0 || len(raw) > 1<<20 || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || name == "" {
		return false
	}
	var policies []string
	if decodeEnvironmentJSON(raw, &policies) != nil || len(policies) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, policy := range policies {
		if policy == "" || strings.TrimSpace(policy) != policy || seen[policy] || policy == name {
			return false
		}
		seen[policy] = true
	}
	return true
}

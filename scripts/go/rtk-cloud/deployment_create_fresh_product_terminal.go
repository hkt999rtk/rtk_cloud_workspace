package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This proof is local to one observation. The timestamp authenticates only the
// already-completed temporary credential; permanent outputs are checked at now.
type freshProductTerminalObservation struct {
	review string
	at     time.Time
	state  managedSessionState
}

func observeFreshProductTerminalSession(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, backend environmentKubernetesBackend, q secondSessionAdmission, review string, state managedSessionState, now time.Time) (*freshProductTerminalObservation, error) {
	bad := func() (*freshProductTerminalObservation, error) {
		return nil, environmentCreateBootstrapBlocker("terminal Service session requires its exact completed fresh program and current permanent outputs")
	}
	if e == nil || e.Store != store || backend.Store != store || now.IsZero() || e.Check(ctx) != nil {
		return bad()
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil, nil
	}
	head, headReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil || headReview != review || deploymentplan.Digest(q) != review || deploymentplan.Digest(*head) != review {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil {
		return bad()
	}
	if !done {
		return nil, nil
	}
	deadline, err := sessionEffectiveDeadline(ctx, store, e, q, now)
	if err != nil || !now.Before(deadline) {
		return bad()
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return nil, err
	}
	binding := program.Lifecycle.FreshResume
	if binding == nil || program.SelectedPlan == nil || deploymentplan.Digest(program.SelectedPlan) != binding.SelectedPlanSHA256 {
		return bad()
	}
	installedPath, err := store.safePath(productIdentitySessionPath(&program.Lifecycle))
	if err != nil {
		return bad()
	}
	if _, err = os.Lstat(installedPath); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return bad()
	}
	installed, err := readProductInstalled(store, &program.Lifecycle)
	if err != nil {
		return nil, err
	}
	if binding.SessionReviewSHA256 != review {
		bridgePath, err := store.safePath(freshProductSuccessorPath(e.Journal.PlanSHA256, review))
		if err != nil {
			return nil, err
		}
		if _, err = os.Lstat(bridgePath); os.IsNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		installed, err = readEffectiveFreshProductInstalled(store, e, &program.Lifecycle)
		if err != nil {
			return nil, err
		}
	}
	want := q.Renewal.RenewedIntent
	if state.Session != want.SessionID || state.Issuer != want.IssuerID || state.Root != want.RootSHA || !slices.Equal(state.Subjects, want.Subjects) || state.OtherActive != 0 || state.Expires.IsZero() || state.Status == "expired" && state.Expires.After(now) || state.Status != "active" && state.Status != "expired" && state.Status != "sealed" {
		return bad()
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(want.Subjects, subject) {
			return bad()
		}
		seen[subject] = true
	}
	if len(seen) != len(want.Subjects) {
		return nil, nil
	}

	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-batches-"+review)
	var receipt serviceSessionBatchReceipt
	if entry == nil || json.Unmarshal(entry.Receipt, &receipt) != nil || !validServiceSessionBatchReceipt(store, e, q, review, receipt) || receipt.ACK.Fingerprint != state.Fingerprint || !receipt.ACK.Expires.Equal(state.Expires) || !receipt.ObservedAt.Before(now) {
		return bad()
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: backend}
	if current, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && current.Execution == e && current.Backend.Store == store {
		runtime = current
		runtime.Backend = backend
	}
	if cfg.Values["PKI_STATE_BACKEND"] == "openbao" {
		runtime.Custody, err = createEnvironmentCustodyBinding(cfg, program.Lifecycle.Bootstrap, e.Journal.PlanSHA256, "")
		if err != nil || runtime.Custody == nil {
			return bad()
		}
		runtime.Custody.ServiceRootSHA256 = installed.Ceremony.Root.RootFingerprint
		runtime.ServiceCeremony = &installed.Ceremony
		caPath, _, pathErr := initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
		if pathErr != nil {
			return bad()
		}
		runtime.OpenBao.CACertPath = caPath
	} else if runtime.PKIClient == nil {
		return bad()
	}
	if err := verifyFreshProductTerminalOutputs(ctx, cfg, runtime, program, installed, state, now); err != nil {
		return nil, err
	}
	return &freshProductTerminalObservation{review: review, at: receipt.ObservedAt, state: state}, nil
}

func verifyFreshProductTerminalOutputs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, program *environmentFreshProductProgram, installed *environmentProductIdentitySession, state managedSessionState, now time.Time) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("terminal fresh product continuation requires unchanged installed outputs, complete native ACKs and closed enrollment")
	}
	if program == nil || installed == nil || runtime.Execution == nil || now.IsZero() || state.OtherActive != 0 {
		return bad()
	}
	l := &program.Lifecycle
	sourceInstalled, err := readProductInstalled(runtime.Backend.Store, l)
	if err != nil {
		return err
	}
	s := installed.Ceremony
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, l.Bootstrap)
	if err != nil || len(subjects) == 0 || !slices.Equal(subjects, state.Subjects) || state.Session != s.BootstrapSessionID || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint || !environmentPreflightDigest.MatchString(state.Fingerprint) {
		return bad()
	}
	ack := productSessionInstallations{Session: state.Session, Issuer: state.Issuer, Root: state.Root, Status: state.Status, Expires: state.Expires, Subjects: state.Subjects, Installed: state.Installed}
	if done, err := productInstallationsComplete(ack, s, subjects, now); err != nil || !done {
		return bad()
	}
	const closureStage = "fresh-product-enrollment-closed"
	closure := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/"+closureStage)
	var closed *deploymentplan.Plan
	if closure == nil || closure.State != "completed" {
		// Issuance is already complete. Authenticate the current original owner
		// chain and let the ordinary close-only producer finish on this session.
		if closure != nil {
			closed, err = readEnvironmentCreateStageInput(runtime.Backend.Store, runtime.Execution, closureStage)
			if err != nil || closed == nil || closure.State == "pending" && len(closure.Receipt) != 0 {
				return bad()
			}
			j, exists, err := readEnvironmentCreateStageProgress(runtime.Backend.Store, runtime.Execution, closureStage, closed)
			if err != nil || exists && j.InFlight != "" {
				return bad()
			}
		}
		if _, err := currentEnvironmentCreateAccountRegistrationCore(ctx, cfg, runtime, nil); err != nil {
			return err
		}
	} else {
		closed, err = verifyAcceptedStageAfterRecordedHistory(ctx, runtime.Backend.Store, runtime.Execution, runtime.Backend, "kubernetes/"+closureStage)
		if err != nil || closed == nil {
			return bad()
		}
	}
	expected, err := freshProductEnrollmentClosedDefinitions(runtime.Backend.Store, runtime.Execution, l.Bootstrap, sourceInstalled.Ceremony)
	if err != nil || len(expected) != 1 {
		return bad()
	}
	if closed != nil {
		if cfg.Values["PKI_STATE_BACKEND"] == "openbao" {
			if program.SelectedPlan == nil {
				return bad()
			}
			expected, err = projectEnvironmentCreateCustodyStage(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, runtime.Backend.Store, program.SelectedPlan, runtime.Execution, expected, closed)
			if err != nil {
				return err
			}
		}
		if len(expected) != len(closed.Resources) {
			return bad()
		}
		byID := map[string]deploymentplan.Object{}
		for _, definition := range expected {
			id := deploymentplan.ID(definition.Object)
			if byID[id] != nil {
				return bad()
			}
			byID[id] = definition.Object
		}
		for _, resource := range closed.Resources {
			if byID[resource.ID] == nil || !deploymentplan.Matches(byID[resource.ID], resource.Desired) {
				return bad()
			}
			delete(byID, resource.ID)
		}
	}

	if err := verifyFreshProductOriginalOutputs(ctx, cfg, runtime, l, sourceInstalled); err != nil {
		return err
	}
	return runtime.Execution.Check(ctx)
}

func verifyCompletedIssuerSessionContinuation(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, backend environmentKubernetesBackend, q secondSessionAdmission, original createServiceSessionIntent, old managedSessionState, now time.Time) error {
	read := func(ctx context.Context, session string) (managedSessionState, error) {
		state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: backend}, session)
		return state, err
	}
	latest, err := read(ctx, q.Renewal.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	// The registry reads may straddle seed expiry. Permanent-state and
	// deadline checks use the actual time after the latest native read.
	now = time.Now().UTC()
	proof, err := observeFreshProductTerminalSession(ctx, cfg, store, e, backend, q, deploymentplan.Digest(q), latest, now)
	if err != nil {
		return err
	}
	if proof == nil {
		return verifyCompletedIssuerSession(ctx, store, e, q, original, old, now, read)
	}
	next := q.Renewal.RenewedIntent
	historical := old
	historical.OtherActive = 0 // The fully verified current head has no competing live session.
	if old.OtherActive < 0 || old.OtherActive > 1 || old.Status != "expired" || next.IssuerID != original.IssuerID || next.RootSHA != original.RootSHA || !slices.Equal(next.Subjects, original.Subjects) || !partialSessionOriginalStatePreserved(q.Renewal.OriginalState, historical, now) || !issuerEnrollmentSessionExpired(original, historical, now) {
		return createProviderRecoveryBlock("terminal Service session changed its original expired lineage")
	}
	return e.Check(ctx)
}

// Keep the ordinary live-session observer strict. Only a fully authenticated
// terminal proof permits historical native rows to have no clock-live seed.
func secondSessionTerminalHistoricalState(saved, current managedSessionState, terminal bool) bool {
	if !terminal {
		return secondSessionObservedExpiredState(saved, current)
	}
	return current.OtherActive >= 0 && current.OtherActive <= 1 && current.Status == "expired" && saved.Session == current.Session && saved.Issuer == current.Issuer && saved.Root == current.Root && saved.Fingerprint == current.Fingerprint && saved.Expires.Equal(current.Expires) && slices.Equal(saved.Subjects, current.Subjects) && slices.Equal(saved.Installed, current.Installed)
}

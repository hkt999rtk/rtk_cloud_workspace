package main

import (
	"context"
	"slices"
	"time"
)

// sessionExistingConsumersACKed checks actual registry acknowledgements for
// already-installed consumers only. Subjects not yet created remain the
// responsibility of normal deployment and its final completion check.
func validServiceSessionACKState(q secondSessionAdmission, state managedSessionState, now time.Time) bool {
	r := q.Renewal
	want := r.RenewedIntent
	if state.Session != want.SessionID || state.Issuer != want.IssuerID || state.Root != want.RootSHA || state.Status != "active" || !state.Expires.After(now) || !environmentPreflightDigest.MatchString(state.Fingerprint) || len(want.Subjects) == 0 || !slices.Equal(state.Subjects, want.Subjects) {
		return false
	}
	installed := map[string]bool{}
	for _, subject := range state.Installed {
		if installed[subject] || !slices.Contains(want.Subjects, subject) {
			return false
		}
		installed[subject] = true
	}
	for _, prior := range [][]string{r.OriginalState.Installed, r.PreviousState.Installed, q.RequiredACK} {
		for _, subject := range prior {
			if !slices.Contains(want.Subjects, subject) {
				return false
			}
		}
	}
	return true
}

func sessionExistingConsumersACKed(q secondSessionAdmission, state managedSessionState, now time.Time) bool {
	if !validServiceSessionACKState(q, state, now) {
		return false
	}
	want := q.Renewal.RenewedIntent
	installed := map[string]bool{}
	for _, subject := range state.Installed {
		installed[subject] = true
	}
	for _, prior := range [][]string{q.Renewal.OriginalState.Installed, q.Renewal.PreviousState.Installed, q.RequiredACK} {
		for _, subject := range prior {
			if !slices.Contains(want.Subjects, subject) || !installed[subject] {
				return false
			}
		}
	}
	return true
}

func waitServiceSessionExistingConsumersACK(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, q secondSessionAdmission) (managedSessionState, error) {
	if runtime.Execution == nil || runtime.Execution.Check(ctx) != nil || !validSecondSessionAdmission(runtime.Execution.Store, runtime.Execution, q) {
		return managedSessionState{}, createProviderRecoveryBlock("Service session ACK verification requires its admitted original execution")
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	return waitServiceSessionACKWith(ctx, q, func(ctx context.Context) (managedSessionState, error) {
		state, _, err := readManagedSessionState(ctx, cfg, runtime, q.Renewal.RenewedIntent.SessionID)
		return state, err
	}, func() time.Time { return time.Now().UTC() }, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			return nil
		}
	}, func(ctx context.Context) error { return runtime.Execution.Check(ctx) })
}

func waitServiceSessionACKWith(ctx context.Context, q secondSessionAdmission, read func(context.Context) (managedSessionState, error), now func() time.Time, wait func(context.Context) error, check func(context.Context) error) (managedSessionState, error) {
	for {
		state, err := read(ctx)
		if err != nil {
			return managedSessionState{}, err
		}
		t := now()
		if !validServiceSessionACKState(q, state, t) {
			return managedSessionState{}, createProviderRecoveryBlock("renewed Service session identity, subjects or lifetime changed before ACK completion")
		}
		if sessionExistingConsumersACKed(q, state, t) {
			return state, check(ctx)
		}
		if err := wait(ctx); err != nil {
			return managedSessionState{}, err
		}
	}
}

package main

import (
	"context"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original Secret remains a historical identity anchor after renewal. Only
// the latest admitted, completed session may authorize continuation; the normal
// successor observer separately checks its live Job, custody and credentials.
func verifyCompletedIssuerSession(ctx context.Context, store secretStore, e *environmentCreateExecution, q secondSessionAdmission, original createServiceSessionIntent, old managedSessionState, now time.Time, read func(context.Context, string) (managedSessionState, error)) error {
	bad := func() error {
		return createProviderRecoveryBlock("completed Service session does not preserve its original identity and active consumer ACKs")
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil || head == nil || deploymentplan.Digest(q) != review || deploymentplan.Digest(*head) != review || read == nil {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil || !done {
		return bad()
	}
	latest, err := read(ctx, q.Renewal.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	if !completedIssuerSessionStatesValid(q, original, old, latest, now) {
		return bad()
	}
	return e.Check(ctx)
}

func completedIssuerSessionStatesValid(q secondSessionAdmission, original createServiceSessionIntent, old, latest managedSessionState, now time.Time) bool {
	next := q.Renewal.RenewedIntent
	if old.OtherActive != 1 || old.Status != "expired" || latest.OtherActive != 0 || next.IssuerID != original.IssuerID || next.RootSHA != original.RootSHA || !slices.Equal(next.Subjects, original.Subjects) {
		return false
	}
	historical := old
	historical.OtherActive = 0 // The sole active session is independently proven below.
	return partialSessionOriginalStatePreserved(q.Renewal.OriginalState, historical, now) && issuerEnrollmentSessionExpired(original, historical, now) && issuerEnrollmentSessionUsable(next, latest, now) && sessionExistingConsumersACKed(q, latest, now)
}

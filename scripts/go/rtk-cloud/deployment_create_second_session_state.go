package main

import (
	"slices"
	"time"
)

// secondSessionPredecessorValid checks the native session rows and issuance
// inventory for a successor that has itself expired. The caller must also
// authenticate the first successor's admitted journal and Job, verify custody
// of its certificate and the original key/CSR, and freeze this inventory in
// a new admission before invoking the managed renewal executor.
func secondSessionPredecessorValid(p *partialSessionContinuationProof, original, predecessor managedSessionState, service environmentCreateServiceCeremony, clients []managedSessionClient, now time.Time) bool {
	if p == nil {
		return false
	}
	return sessionLineagePredecessorValid(p, p.RenewedIntent, p.Recovery.OriginalClientDigests, original, predecessor, service, clients, now)
}

func sessionLineagePredecessorValid(p *partialSessionContinuationProof, intent createServiceSessionIntent, originalClientDigests []string, original, predecessor managedSessionState, service environmentCreateServiceCeremony, clients []managedSessionClient, now time.Time) bool {
	if p == nil || p.Recovery.OriginalState == nil || original.Status != "expired" || !partialSessionOriginalStatePreserved(*p.Recovery.OriginalState, original, now) ||
		!issuerEnrollmentSessionExpired(intent, predecessor, now) || p.Intent.SessionID == intent.SessionID || intent.IssuerID != p.Intent.IssuerID || intent.RootSHA != p.Intent.RootSHA || !slices.Equal(intent.Subjects, p.Intent.Subjects) || !slices.Equal(intent.Namespaces, p.Intent.Namespaces) || intent.Claim != p.Intent.Claim ||
		service.Intermediate.ID != p.Intent.IssuerID || service.Root.RootFingerprint != p.Intent.RootSHA {
		return false
	}
	proof := &createManagedSessionRecovery{PartialInventory: true, OriginalClientDigests: originalClientDigests}
	return partialSessionClientsPreserved(clients, service, p.Intent.Subjects, original, proof, now) &&
		partialSessionClientsPreserved(clients, service, p.Intent.Subjects, predecessor, proof, now)
}

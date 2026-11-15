package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSecondSessionPredecessorPreservesIssuedAndInstalledIdentities(t *testing.T) {
	service, subjects, clients, now := managedSessionClientFixture(t)
	service.Root.RootFingerprint = strings.Repeat("a", 64)
	originalID, successorID := "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"
	original := managedSessionState{Session: originalID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "expired", Expires: now.Add(-2 * time.Hour), Subjects: subjects, Installed: []string{subjects[0]}}
	prior := managedSessionState{Session: successorID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("c", 64), Status: "active", Expires: now.Add(-time.Minute), Subjects: subjects, Installed: slices.Clone(subjects[:5])}
	p := &partialSessionContinuationProof{Intent: createServiceSessionIntent{SessionID: originalID, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Subjects: subjects}, RenewedIntent: createServiceSessionIntent{SessionID: successorID, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Subjects: subjects}, Recovery: createManagedSessionRecovery{PartialInventory: true, OriginalState: &original, OriginalClientDigests: partialSessionClientDigests(clients[:3])}}
	if !secondSessionPredecessorValid(p, original, prior, service, clients, now) {
		t.Fatal("valid expired successor rejected")
	}
	for _, mode := range []string{"old-session", "old-installations", "prior-session", "prior-active", "competing-active", "prior-issuer", "prior-root", "prior-subjects", "prior-installations", "missing-client", "changed-client"} {
		t.Run(mode, func(t *testing.T) {
			old, next, rows := original, prior, slices.Clone(clients)
			switch mode {
			case "old-session":
				old.Session = successorID
			case "old-installations":
				old.Installed = []string{subjects[1]}
			case "prior-session":
				next.Session = originalID
			case "prior-active":
				next.Expires = now.Add(time.Minute)
			case "competing-active":
				next.OtherActive = 1
			case "prior-issuer":
				next.Issuer = "foreign"
			case "prior-root":
				next.Root = strings.Repeat("d", 64)
			case "prior-subjects":
				next.Subjects = next.Subjects[1:]
			case "prior-installations":
				next.Installed = []string{"service:foreign"}
			case "missing-client":
				rows = rows[1:]
			case "changed-client":
				rows[0].Revoked = &now
			}
			if secondSessionPredecessorValid(p, old, next, service, rows, now) {
				t.Fatal("changed predecessor or permanent identity accepted")
			}
		})
	}
}

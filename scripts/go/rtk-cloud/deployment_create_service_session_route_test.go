package main

import (
	"strings"
	"testing"
	"time"
)

func TestCompletedServiceSessionSkipsExpiredPredecessorRepair(t *testing.T) {
	for _, tc := range []struct {
		resume, admission, completed, want bool
	}{
		{true, true, false, true},
		{true, true, true, false},
		{false, true, false, false},
		{true, false, false, false},
	} {
		var session *secondSessionAdmission
		if tc.completed {
			session = &secondSessionAdmission{}
		}
		if got := shouldTryExpiredSessionPrivateRepair(tc.resume, tc.admission, session); got != tc.want {
			t.Fatalf("resume=%v admission=%v completed=%v: got %v", tc.resume, tc.admission, tc.completed, got)
		}
	}
}

func TestCompletedServiceSessionRouteAfterExpiration(t *testing.T) {
	now := time.Now().UTC()
	intent := createServiceSessionIntent{SessionID: "11111111-1111-4111-8111-111111111111", IssuerID: "issuer", RootSHA: strings.Repeat("a", 64), Subjects: []string{"service:openbao", "service:api"}}
	q := secondSessionAdmission{Renewal: secondSessionQualification{RenewedIntent: intent, PreviousState: managedSessionState{Installed: []string{"service:openbao"}}}}
	state := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(time.Hour), Subjects: intent.Subjects, Installed: []string{"service:openbao"}}
	if got := completedServiceSessionAction(q, state, now); got != serviceSessionContinueCreate {
		t.Fatal("active acknowledged session must continue create", got)
	}
	state.Installed = nil
	if got := completedServiceSessionAction(q, state, now); got != serviceSessionInvalid {
		t.Fatal("missing ACK must not start create", got)
	}
	state.Installed = []string{"service:openbao"}
	state.Status, state.Expires = "expired", now.Add(-time.Second)
	if got := completedServiceSessionAction(q, state, now); got != serviceSessionRenew {
		t.Fatal("expired session must qualify the next lineage member", got)
	}
	state.OtherActive = 1
	if got := completedServiceSessionAction(q, state, now); got != serviceSessionInvalid {
		t.Fatal("competing active session must be rejected", got)
	}
}

func TestServiceSessionACKsSurviveInterruptedPriorBatch(t *testing.T) {
	now := time.Now().UTC()
	intent := createServiceSessionIntent{SessionID: "11111111-1111-4111-8111-111111111111", IssuerID: "issuer", RootSHA: strings.Repeat("a", 64), Subjects: []string{"service:a", "service:b", "service:c"}}
	q := secondSessionAdmission{Renewal: secondSessionQualification{RenewedIntent: intent, OriginalState: managedSessionState{Installed: []string{"service:a"}}, PreviousState: managedSessionState{Installed: []string{"service:b"}}}}
	history := []secondSessionAdmission{{Renewal: secondSessionQualification{PreviousState: managedSessionState{Installed: []string{"service:c", "service:a"}}}}}
	q.RequiredACK = serviceSessionRequiredACKs(q, history)
	state := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(time.Hour), Subjects: intent.Subjects, Installed: []string{"service:a", "service:b"}}
	if sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("lost ACK from an earlier partially completed generation")
	}
	state.Installed = append(state.Installed, "service:c")
	if !sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("actual ACKs across the full lineage were rejected")
	}
}

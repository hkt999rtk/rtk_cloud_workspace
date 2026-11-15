package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestServiceSessionExistingConsumerACKsAreRealAndStaged(t *testing.T) {
	now := time.Now().UTC()
	intent := createServiceSessionIntent{SessionID: "11111111-1111-4111-8111-111111111111", IssuerID: "issuer", RootSHA: strings.Repeat("a", 64), Subjects: []string{"service:openbao", "service:api", "service:future"}}
	q := secondSessionAdmission{Renewal: secondSessionQualification{RenewedIntent: intent, OriginalState: managedSessionState{Installed: []string{"service:openbao"}}, PreviousState: managedSessionState{Installed: []string{"service:api"}}}}
	state := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Status: "active", Expires: now.Add(time.Hour), Fingerprint: strings.Repeat("b", 64), Subjects: intent.Subjects, Installed: []string{"service:openbao"}}
	if sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("missing existing API ACK was accepted")
	}
	state.Installed = append(state.Installed, "service:api")
	if !sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("uncreated future service should not block this batch")
	}
	reads, waits := 0, 0
	read := func(context.Context) (managedSessionState, error) {
		reads++
		observed := state
		if reads == 1 {
			observed.Installed = []string{"service:openbao"}
		}
		return observed, nil
	}
	if _, err := waitServiceSessionACKWith(t.Context(), q, read, func() time.Time { return now }, func(context.Context) error { waits++; return nil }, func(context.Context) error { return nil }); err != nil || reads != 2 || waits != 1 {
		t.Fatal("missing ACK was not rechecked after arrival", err, reads, waits)
	}
	state.Installed = append(state.Installed, "foreign")
	if sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("unregistered ACK was accepted")
	}
	state.Installed = []string{"service:openbao", "service:api"}
	state.Session = "22222222-2222-4222-8222-222222222222"
	if sessionExistingConsumersACKed(q, state, now) {
		t.Fatal("ACKs from another session were accepted")
	}
}

package main

import (
	"slices"
	"testing"
	"time"
)

func TestSecondSessionObservedExpiredStatePreservesOriginal(t *testing.T) {
	now := time.Now().UTC()
	saved := managedSessionState{Session: "old", Issuer: "issuer", Root: "root", Fingerprint: "fingerprint", Status: "active", Expires: now.Add(-time.Hour), Subjects: []string{"one", "two"}, Installed: []string{"one"}}
	current := saved
	current.Status, current.OtherActive = "expired", 1
	if !secondSessionObservedExpiredState(saved, current) {
		t.Fatal("native predecessor did not transition to expired")
	}
	for _, mode := range []string{"session", "issuer", "root", "fingerprint", "expiry", "subjects", "installed", "status", "competing"} {
		t.Run(mode, func(t *testing.T) {
			bad := current
			switch mode {
			case "session":
				bad.Session = "foreign"
			case "issuer":
				bad.Issuer = "foreign"
			case "root":
				bad.Root = "foreign"
			case "fingerprint":
				bad.Fingerprint = "foreign"
			case "expiry":
				bad.Expires = now
			case "subjects":
				bad.Subjects = []string{"foreign"}
			case "installed":
				bad.Installed = append(slices.Clone(bad.Installed), "two")
			case "status":
				bad.Status = "active"
			case "competing":
				bad.OtherActive = 2
			}
			if secondSessionObservedExpiredState(saved, bad) {
				t.Fatal("changed predecessor was accepted")
			}
		})
	}
}

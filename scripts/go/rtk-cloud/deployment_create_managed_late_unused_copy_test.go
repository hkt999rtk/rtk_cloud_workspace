package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateUnusedHistoricalCopyRequiresExactAdmittedEmptySession(t *testing.T) {
	for _, mode := range []string{"original", "saved-expired", "active", "sealed", "future-expiry", "expiry-drift", "session-drift", "issuer-drift", "root-drift", "fingerprint-drift", "other-active", "one-ack", "five-acks", "eight-acks", "saved-used", "saved-sealed", "saved-fingerprint", "malformed-saved", "subject-drift", "missing-subject", "source-sha", "missing-remaining", "duplicate-source", "unknown-source", "copy-uid", "invalid-uid", "copy-name", "copy-namespace", "source-namespace"} {
		t.Run(mode, func(t *testing.T) {
			state, observed, ceremony, subjects, now := managedHistoricalSeedFixture(t)
			state.Installed, state.OtherActive = nil, 0
			ceremony.BootstrapSessionID = state.Session
			ceremony.BootstrapCredentialSecret = "service-bootstrap-" + state.Session
			ceremony.BootstrapCredentialNamespace = "video-cloud-dev-video-cloud"
			ceremony.BootstrapCredentialNamespaces = []string{"video-cloud-dev-video-cloud", "video-cloud-dev-account-manager", "video-cloud-dev-secrets"}
			ceremony.CredentialUIDs = cloneStringMap(observed.UIDs)
			r := &createServiceSessionRecovery{ManagedLateSession: &createManagedLateSessionRecovery{SourceCeremony: ceremony}, ManagedLateRemaining: &createManagedLateRemaining{}, StaleSeeds: []createServiceSessionObserved{observed}}
			var saved managedSessionState
			if err := decodeEnvironmentJSON(observed.Session, &saved); err != nil {
				t.Fatal(err)
			}
			savedChanged := false
			switch mode {
			case "saved-expired":
				saved.Status, savedChanged = "expired", true
			case "active", "sealed":
				state.Status = mode
			case "future-expiry":
				state.Expires = now.Add(time.Hour)
			case "expiry-drift":
				state.Expires = state.Expires.Add(-time.Second)
			case "session-drift":
				state.Session = "99999999-9999-4999-8999-999999999999"
			case "issuer-drift":
				state.Issuer = "99999999-9999-4999-8999-999999999999"
			case "root-drift":
				state.Root = strings.Repeat("d", 64)
			case "fingerprint-drift":
				state.Fingerprint = strings.Repeat("d", 64)
			case "other-active":
				state.OtherActive = 1
			case "one-ack":
				state.Installed = slices.Clone(subjects[:1])
			case "five-acks":
				state.Installed = slices.Clone(subjects[:5])
			case "eight-acks":
				state.Installed = slices.Clone(subjects[:8])
			case "saved-used":
				saved.Installed, savedChanged = slices.Clone(subjects[:5]), true
			case "saved-sealed":
				saved.Status, savedChanged = "sealed", true
			case "saved-fingerprint":
				saved.Fingerprint, savedChanged = strings.Repeat("d", 64), true
			case "subject-drift":
				state.Subjects = slices.Clone(subjects)
				state.Subjects[0] = "service:foreign"
			case "missing-subject":
				state.Subjects = slices.Clone(subjects[:8])
			case "copy-uid":
				ceremony.CredentialUIDs[ceremony.BootstrapCredentialNamespace] = "99999999-9999-4999-8999-999999999999"
			case "invalid-uid":
				ceremony.CredentialUIDs[ceremony.BootstrapCredentialNamespace] = ""
			case "copy-name":
				ceremony.BootstrapCredentialSecret = "service-bootstrap-foreign"
			case "copy-namespace":
				ceremony.BootstrapCredentialNamespaces = ceremony.BootstrapCredentialNamespaces[1:]
			case "source-namespace":
				r.ManagedLateSession.SourceCeremony.BootstrapCredentialNamespaces = nil
			}
			if savedChanged {
				observed.Session = mustProviderRecoveryJSON(saved)
			}
			if mode == "malformed-saved" {
				observed.Session = []byte(`{"session_id":`)
			}
			r.StaleSeeds[0] = observed
			if mode == "duplicate-source" {
				r.StaleSeeds = append(r.StaleSeeds, observed)
			}
			if mode == "unknown-source" {
				r.StaleSeeds = nil
			}
			r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
			if mode == "source-sha" {
				r.ManagedLateRemaining.StaleSeedsSHA = strings.Repeat("e", 64)
			}
			if mode == "missing-remaining" {
				r.ManagedLateRemaining = nil
			}
			before := deploymentplan.Digest(deploymentplan.Object{"recovery": r, "copy": ceremony, "observation": observed, "native": state})
			got := managedLateUnusedHistoricalCopyMatches(r, observed, ceremony, ceremony.BootstrapCredentialNamespace, state, subjects, now)
			if got != (mode == "original" || mode == "saved-expired") {
				t.Fatal("unused copy accepted changed source or used session", mode)
			}
			if before != deploymentplan.Digest(deploymentplan.Object{"recovery": r, "copy": ceremony, "observation": observed, "native": state}) {
				t.Fatal("copy validation changed original source")
			}
		})
	}
}

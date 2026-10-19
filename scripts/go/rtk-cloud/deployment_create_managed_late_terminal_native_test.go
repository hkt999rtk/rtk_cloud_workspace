package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateTerminalPendingFixture() (environmentCreateJournal, environmentCreateServiceCeremony, time.Time) {
	now := time.Date(2026, 10, 17, 0, 29, 38, 903778000, time.UTC)
	s := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}, CleanupBinding: environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: "11111111-1111-4111-8111-111111111111", NamespaceUID: "33333333-3333-4333-8333-333333333333", SecretUID: "44444444-4444-4444-8444-444444444444", PolicySHA256: strings.Repeat("a", 64)}}
	j := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 396)}
	j.Entries[395] = environmentCreateJournalEntry{Key: "service-session-finalize", Phase: "bootstrap", State: "pending", StartedAt: now, IntentSHA256: activeTransportJournalDigest(deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding})}
	return j, s, now
}

func TestManagedLateTerminalFinalizerRetainsExactOriginalPendingIntent(t *testing.T) {
	for _, mode := range []string{"genuine", "changed-session", "changed-issuer", "changed-cleanup-uid", "changed-intent", "wrong-phase", "already-completed", "zero-start", "completed-time", "partial-receipt", "unknown-suffix", "short-prefix", "duplicate-finalizer", "not-last", "parent-completed"} {
		t.Run(mode, func(t *testing.T) {
			j, s, now := managedLateTerminalPendingFixture()
			switch mode {
			case "changed-session":
				s.BootstrapSessionID = "55555555-5555-4555-8555-555555555555"
			case "changed-issuer":
				s.Intermediate.ID = "55555555-5555-4555-8555-555555555555"
			case "changed-cleanup-uid":
				s.CleanupBinding.SecretUID = "55555555-5555-4555-8555-555555555555"
			case "changed-intent":
				j.Entries[395].IntentSHA256 = strings.Repeat("f", 64)
			case "wrong-phase":
				j.Entries[395].Phase = "recovery"
			case "already-completed":
				j.Entries[395].State = "completed"
			case "zero-start":
				j.Entries[395].StartedAt = time.Time{}
			case "completed-time":
				j.Entries[395].CompletedAt = now.Add(time.Second)
			case "partial-receipt":
				j.Entries[395].Receipt = []byte(`{"native":"unknown"}`)
			case "unknown-suffix":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unknown"})
			case "short-prefix":
				j.Entries = j.Entries[1:]
			case "duplicate-finalizer":
				j.Entries[394] = j.Entries[395]
			case "not-last":
				j.Entries[394], j.Entries[395] = j.Entries[395], j.Entries[394]
			case "parent-completed":
				j.CompletedAt = now.Add(time.Second)
			}
			before := deploymentplan.Digest(j)
			if managedLateTerminalFinalizerPendingIntent(j, s) != (mode == "genuine") || deploymentplan.Digest(j) != before {
				t.Fatal("pending intent accepted substitution or changed original journal")
			}
		})
	}
}

func TestManagedLateTerminalFinalizerRequiresTypedMarkerAndNativeProfile(t *testing.T) {
	ordinary := environmentCreateBootstrapRuntime{ServiceSessionRecovery: &createServiceSessionRecovery{ManagedLateRemaining: &createManagedLateRemaining{FinalizationOnly: true}}}
	if managedLateTerminalFinalizerRequested(ordinary) || managedLateTerminalFinalizerRequested(environmentCreateBootstrapRuntime{}) {
		t.Fatal("ordinary profile acquired terminal branch")
	}
	ordinary.ServiceSessionRecovery.ManagedLateRemaining.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{}
	if !managedLateTerminalFinalizerRequested(ordinary) {
		t.Fatal("typed marker not routed to independent authority guard")
	}
	err := verifyManagedLateTerminalFinalizerNative(context.Background(), deploymentConfig{}, secretStore{}, nil, environmentKubernetesBackend{}, ordinary.ServiceSessionRecovery, &createFoundationRecoveryPlan{Materials: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "terminal-finalizer-native-profile") {
		t.Fatal("unadmitted marker reached native observation")
	}
}

func TestManagedLateTerminalSealedSessionRequiresExactNineAndGenuineLife(t *testing.T) {
	_, s, now := managedLateTerminalPendingFixture()
	s.Root.RootFingerprint = strings.Repeat("a", 64)
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	seed := managedSessionState{Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Root: s.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(8 * time.Hour), Subjects: subjects}
	for _, mode := range []string{"genuine", "active", "expired", "foreign-session", "foreign-issuer", "foreign-root", "foreign-fingerprint", "other-active", "changed-expiry", "insufficient-life", "missing-ack", "duplicate-ack", "foreign-ack", "changed-roster"} {
		t.Run(mode, func(t *testing.T) {
			current, original := seed, seed
			current.Status, current.Installed = "sealed", slices.Clone(subjects)
			switch mode {
			case "active", "expired":
				current.Status = mode
			case "foreign-session":
				current.Session = "55555555-5555-4555-8555-555555555555"
			case "foreign-issuer":
				current.Issuer = "55555555-5555-4555-8555-555555555555"
			case "foreign-root":
				current.Root = strings.Repeat("c", 64)
			case "foreign-fingerprint":
				current.Fingerprint = strings.Repeat("c", 64)
			case "other-active":
				current.OtherActive = 1
			case "changed-expiry":
				current.Expires = current.Expires.Add(time.Second)
			case "insufficient-life":
				current.Expires, original.Expires = now.Add(20*time.Minute), now.Add(20*time.Minute)
			case "missing-ack":
				current.Installed = current.Installed[:8]
			case "duplicate-ack":
				current.Installed[8] = current.Installed[0]
			case "foreign-ack":
				current.Installed[8] = "service:foreign"
			case "changed-roster":
				current.Subjects = slices.Clone(subjects)
				current.Subjects[8] = "service:foreign"
			}
			if managedLateTerminalSealedSession(current, original, s, subjects, now) != (mode == "genuine") {
				t.Fatal("sealed native state accepted missing or substituted authority")
			}
		})
	}
}

func TestManagedLateTerminalCurrentSignedCRLRequiresSameSerialAndCausalTimes(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	leaf, err := x509.ParseCertificate(f.leaf)
	if err != nil {
		t.Fatal(err)
	}
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.leaf}))
	revokedAt := f.now.Add(903778 * time.Microsecond)
	this := f.now.Add(10 * time.Second)
	for _, mode := range []string{"genuine-delayed-provider", "same-second-provider", "prior-provider-time", "future-provider-time", "after-original-authority", "missing-original-authority", "wrong-serial", "missing-serial", "wrong-issuer", "changed-der-digest"} {
		t.Run(mode, func(t *testing.T) {
			entry := x509.RevocationListEntry{SerialNumber: leaf.SerialNumber, RevocationTime: f.now.Add(time.Second)}
			switch mode {
			case "same-second-provider":
				entry.RevocationTime = f.now
			case "prior-provider-time":
				entry.RevocationTime = f.now.Add(-time.Second)
			case "future-provider-time":
				entry.RevocationTime = this.Add(time.Second)
			case "wrong-serial":
				entry.SerialNumber = big.NewInt(9999)
			}
			entries := []x509.RevocationListEntry{entry}
			if mode == "missing-serial" {
				entries = nil
			}
			der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(11), ThisUpdate: this, NextUpdate: this.Add(72 * time.Hour), RevokedCertificateEntries: entries}, f.icaCertificate, f.icaKey)
			if err != nil {
				t.Fatal(err)
			}
			record := managedLateCRLRecord{IssuerID: "22222222-2222-4222-8222-222222222222", Digest: environmentPreflightBytesSHA256(der), Number: "11", PEM: string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), ThisUpdate: this, NextUpdate: this.Add(72 * time.Hour)}
			issuer := record.IssuerID
			if mode == "wrong-issuer" {
				issuer = "33333333-3333-4333-8333-333333333333"
			}
			if mode == "changed-der-digest" {
				record.Digest = strings.Repeat("f", 64)
			}
			deadline := f.now.Add(time.Minute)
			if mode == "after-original-authority" {
				deadline = f.now
			} else if mode == "missing-original-authority" {
				deadline = time.Time{}
			}
			want := mode == "genuine-delayed-provider" || mode == "same-second-provider"
			if managedLateTerminalCRLCovers(record, issuer, certificate, revokedAt, deadline) != want {
				t.Fatal("current signed CRL did not bind exact serial and causal interval")
			}
		})
	}
}

func TestManagedLateTerminalRevocationCannotReopenHistoricalAuthority(t *testing.T) {
	start := time.Date(2026, 10, 16, 23, 50, 5, 0, time.UTC)
	authority := &createManagedLateFinalizationAuthority{createManagedLateInstalledCleanupAuthority: createManagedLateInstalledCleanupAuthority{StartedAt: start, Deadline: start.Add(50 * time.Minute)}}
	for _, mode := range []string{"genuine", "boundary", "seal-before", "revoke-before", "seal-after", "revoke-after", "missing-authority", "zero-start", "reversed-bound"} {
		t.Run(mode, func(t *testing.T) {
			original := *authority
			observed := managedLateTerminalRevocation{SealedAt: start.Add(39 * time.Minute), RevokedAt: start.Add(39 * time.Minute)}
			selected := &original
			switch mode {
			case "boundary":
				observed.SealedAt, observed.RevokedAt = start, original.Deadline
			case "seal-before":
				observed.SealedAt = start.Add(-time.Nanosecond)
			case "revoke-before":
				observed.RevokedAt = start.Add(-time.Nanosecond)
			case "seal-after":
				observed.SealedAt = original.Deadline.Add(time.Nanosecond)
			case "revoke-after":
				observed.RevokedAt = original.Deadline.Add(time.Nanosecond)
			case "missing-authority":
				selected = nil
			case "zero-start":
				selected.StartedAt = time.Time{}
			case "reversed-bound":
				selected.Deadline = start.Add(-time.Second)
			}
			if managedLateTerminalRevocationWithinAuthority(observed, selected) != (mode == "genuine" || mode == "boundary") {
				t.Fatal("terminal cleanup allowed bootstrap operation outside original authority")
			}
		})
	}
}

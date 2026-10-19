package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is a routing fixture, not fabricated deployment authority. The outer
// readers must still authenticate every source artifact and actual clock.
func managedLateAppTransportSourceFixture() *environmentCreateExecution {
	parent, review := strings.Repeat("a", 64), strings.Repeat("b", 64)
	now := time.Now().UTC().Add(-time.Hour)
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-App-transport-owner", Journal: "deployment/create/isolated-App-transport/journal.json"}
	journal := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: owner.Environment, Stack: owner.Stack, PlanSHA256: parent, OwnerToken: owner.OwnerToken, Entries: make([]environmentCreateJournalEntry, 370)}
	for i := range journal.Entries {
		journal.Entries[i] = environmentCreateJournalEntry{Key: "isolated-original-entry", Phase: "bootstrap", State: "completed", IntentSHA256: parent, StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: mustProviderRecoveryJSON(map[string]string{"fixture": "routing-only"})}
	}
	journal.Entries[361].Key = "kubernetes/service-session-original-key-renewal-" + review
	journal.Entries[362].Key = "openbao-recovery-admission-" + strings.Repeat("c", 64)
	journal.Entries[366].Key = "kubernetes/service-cleanup-binding-renewal-" + review
	return &environmentCreateExecution{owner: owner, Journal: journal}
}

func TestManagedLateAppTransportOnlyLiteralOriginal370UsesPreInstallationReader(t *testing.T) {
	original := managedLateAppTransportSourceFixture()
	before := deploymentplan.Digest(original.Journal)
	if !managedLateAppTransportBeforeInstallation(original) {
		t.Fatal("literal uninstalled routing source refused")
	}
	if deploymentplan.Digest(original.Journal) != before {
		t.Fatal("routing reader changed actual source")
	}
	for _, mode := range []string{"nil-frame", "short-source", "pending-installation-admission", "completed-installation-admission", "unknown-suffix", "owner-stage-suffix", "unpeeled-App-tail", "overall-completed", "pending-original", "allocated-original", "missing-original-start", "missing-original-completion", "backwards-original-time", "missing-original-receipt", "wrong-seed-key", "malformed-seed-review", "wrong-forward-admission", "different-cleanup-review"} {
		t.Run(mode, func(t *testing.T) {
			frame := *original
			frame.Journal = original.Journal
			frame.Journal.Entries = slices.Clone(original.Journal.Entries)
			selected := &frame
			fresh := strings.Repeat("d", 64)
			switch mode {
			case "nil-frame":
				selected = nil
			case "short-source":
				frame.Journal.Entries = frame.Journal.Entries[:369]
			case "pending-installation-admission", "completed-installation-admission":
				state := "pending"
				if mode == "completed-installation-admission" {
					state = "completed"
				}
				entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + fresh, Phase: "recovery", State: state, StartedAt: time.Now().UTC().Add(-time.Minute)}
				if state == "completed" {
					entry.CompletedAt = entry.StartedAt.Add(time.Second)
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": fresh})
				}
				frame.Journal.Entries = append(frame.Journal.Entries, entry)
			case "unknown-suffix":
				frame.Journal.Entries = append(frame.Journal.Entries, environmentCreateJournalEntry{Key: "unknown-installation-outcome", State: "pending"})
			case "owner-stage-suffix":
				frame.Journal.Entries = append(frame.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/managed-late-session-owners-" + fresh, State: "pending"})
			case "unpeeled-App-tail":
				key := "app-root-crl-renewal-" + fresh
				for _, suffix := range []string{"", "/native-import", "/native-runtime-video-cloud-api-app-pki", "/native-runtime-mqtt-pki"} {
					now := time.Now().UTC().Add(-time.Minute)
					frame.Journal.Entries = append(frame.Journal.Entries, environmentCreateJournalEntry{Key: key + suffix, Phase: "bootstrap", State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: mustProviderRecoveryJSON(map[string]string{"fixture": "routing-only"})})
				}
			case "overall-completed":
				frame.Journal.CompletedAt = time.Now().UTC()
			case "pending-original":
				frame.Journal.Entries[365].State = "pending"
			case "allocated-original":
				frame.Journal.Entries[365].State = "allocated"
			case "missing-original-start":
				frame.Journal.Entries[365].StartedAt = time.Time{}
			case "missing-original-completion":
				frame.Journal.Entries[365].CompletedAt = time.Time{}
			case "backwards-original-time":
				frame.Journal.Entries[365].CompletedAt = frame.Journal.Entries[365].StartedAt.Add(-time.Second)
			case "missing-original-receipt":
				frame.Journal.Entries[365].Receipt = nil
			case "wrong-seed-key":
				frame.Journal.Entries[361].Key = "foreign-seed"
			case "malformed-seed-review":
				frame.Journal.Entries[361].Key = "kubernetes/service-session-original-key-renewal-invalid"
			case "wrong-forward-admission":
				frame.Journal.Entries[362].Key = "another-admission"
			case "different-cleanup-review":
				frame.Journal.Entries[366].Key = "kubernetes/service-cleanup-binding-renewal-" + fresh
			}
			unchanged := deploymentplan.Digest(frame.Journal)
			if managedLateAppTransportBeforeInstallation(selected) {
				t.Fatal("remaining installation bypassed strong Post clock/frame", mode)
			}
			if deploymentplan.Digest(frame.Journal) != unchanged {
				t.Fatal("routing guard rewrote a source frame", mode)
			}
		})
	}
}

func TestManagedLateAppTransportSelectedFailuresCannotBecomeOrdinaryScope(t *testing.T) {
	for _, mode := range []string{"unproved-App-tail", "App-plus-unknown-suffix", "App-plus-pending-installation", "App-plus-foreign-Post-artifact", "pending-Post-without-source-or-clock", "completed-Post-without-source-or-clock"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			e := managedLateAppTransportSourceFixture()
			e.Store = store
			parent := e.Journal.PlanSHA256
			now := time.Now().UTC().Add(-time.Minute)
			postArtifact := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLatePostMintSchema, ParentSHA: parent, ParentJournalSHA: deploymentplan.Digest(e.Journal), ParentOwnerSHA: deploymentplan.Digest(e.owner)}}
			postReview := deploymentplan.Digest(postArtifact.Plan)
			if strings.HasPrefix(mode, "App-") || mode == "unproved-App-tail" {
				appKey := "app-root-crl-renewal-" + strings.Repeat("d", 64)
				for _, suffix := range []string{"", "/native-import", "/native-runtime-video-cloud-api-app-pki", "/native-runtime-mqtt-pki"} {
					e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: appKey + suffix, Phase: "bootstrap", State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: mustProviderRecoveryJSON(map[string]string{"fixture": "routing-only"})})
				}
			}
			switch mode {
			case "App-plus-unknown-suffix":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unknown-after-App", State: "pending"})
			case "App-plus-pending-installation", "App-plus-foreign-Post-artifact", "pending-Post-without-source-or-clock", "completed-Post-without-source-or-clock":
				entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + postReview, Phase: "recovery", State: "pending", StartedAt: now}
				if mode == "completed-Post-without-source-or-clock" {
					entry.State = "completed"
					entry.CompletedAt = now.Add(time.Second)
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": postReview, "audit_sha256": strings.Repeat("e", 64), "installation_clock_sha256": strings.Repeat("f", 64)})
				}
				e.Journal.Entries = append(e.Journal.Entries, entry)
				if mode == "App-plus-foreign-Post-artifact" {
					postArtifact.Plan.Schema = "foreign-Post-authority"
				}
				lateSeedForwardWrite(t, store, filepath.Join("deployment/create/openbao-recovery", postReview, "qualification.json"), postArtifact)
			}
			before := deploymentplan.Digest(e.Journal)
			lateSeedForwardWrite(t, store, "deployment/.execution-active/owner.json", e.owner)
			lateSeedForwardWrite(t, store, e.owner.Journal, e.Journal)
			minimum, selected, err := managedLatePostMintTransportMinimum(store, parent)
			if !selected || err == nil || minimum != 2*time.Hour {
				t.Fatal("selected App/Post uncertainty fell back to ordinary historical transport", mode, selected, err, minimum)
			}
			var actual environmentCreateJournal
			if readExecutionJSON(filepath.Join(store.Root, e.owner.Journal), &actual) != nil || deploymentplan.Digest(actual) != before {
				t.Fatal("scope failure changed actual source history")
			}
			if clock, err := readManagedLateInstallationClock(store, parent); err != nil || clock != nil {
				t.Fatal("transport failure fabricated installation clock", err)
			}
		})
	}
}

func TestManagedLateAppTransportPendingOrMissingClockCannotAuthorizeInstallation(t *testing.T) {
	for _, state := range []string{"pending", "completed"} {
		t.Run(state, func(t *testing.T) {
			store, post, artifact, overall, epoch := managedLateInstallationNegativeFixture(t)
			clock, err := newManagedLateInstallationClock(post, artifact, post.SourceOwnerSHA, overall, epoch)
			if err != nil {
				t.Fatal(err)
			}
			entry := managedLateInstallationNegativeAdmission(t, post, artifact, clock)
			entry.State = state
			if state == "pending" {
				entry.CompletedAt = time.Time{}
				entry.Receipt = nil
			}
			frame := managedLateAppTransportSourceFixture()
			frame.Store = store
			frame.Journal.PlanSHA256 = artifact.Plan.ParentSHA
			frame.Journal.Entries = append(frame.Journal.Entries, entry)
			if managedLateAppTransportBeforeInstallation(frame) {
				t.Fatal("actual admission reused original370-only source route")
			}
			if _, err := validateManagedLateInstallationAdmission(store, frame, post, artifact, entry); err == nil {
				t.Fatal("remaining installation had no genuine immutable first clock", state)
			}
			if actual, err := readManagedLateInstallationClock(store, artifact.Plan.ParentSHA); err != nil || actual != nil {
				t.Fatal("missing clock regenerated during transport validation", err)
			}
		})
	}
}

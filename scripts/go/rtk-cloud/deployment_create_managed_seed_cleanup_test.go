package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedHistoricalSeedFixture(t *testing.T) (managedSessionState, createServiceSessionObserved, environmentCreateServiceCeremony, []string, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	subjects := []string{"service:account-manager", "service:certissuer", "service:emqx-pki", "service:factory-enroll", "service:openbao", "service:pki-controller", "service:pkibroker", "service:video-cloud-api", "service:video-cloud-logingester"}
	current := environmentCreateServiceCeremony{BootstrapSessionID: "e0a93344-3c64-4a00-87d5-dc57a4e0d48b", Root: environmentCreateRegisteredIssuer{Environment: "dev", RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "995a2ac1-14d3-4126-8090-837b61f45511"}}
	state := managedSessionState{Session: "7b50e791-186c-4e67-80d5-905688626214", Issuer: current.Intermediate.ID, Root: current.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "expired", Expires: now.Add(-time.Hour), Subjects: subjects, Installed: []string{"service:pki-controller", "service:certissuer", "service:account-manager", "service:pkibroker", "service:video-cloud-api"}, OtherActive: 1}
	saved := state
	saved.Status = "active"
	saved.Installed = nil
	saved.OtherActive = 0
	observed := createServiceSessionObserved{Session: mustProviderRecoveryJSON(saved), UIDs: map[string]string{"video-cloud-dev-video-cloud": "11111111-1111-4111-8111-111111111111", "video-cloud-dev-account-manager": "22222222-2222-4222-8222-222222222222", "video-cloud-dev-secrets": "33333333-3333-4333-8333-333333333333"}, Cleanup: environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: state.Session, NamespaceUID: "44444444-4444-4444-8444-444444444444", SecretUID: "11111111-1111-4111-8111-111111111111", PolicySHA256: strings.Repeat("c", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}
	return state, observed, current, subjects, now
}

func TestManagedHistoricalSeedCleanupAcceptsOnlyOriginalExpiredFiveNativeACKs(t *testing.T) {
	original, observation, current, subjects, now := managedHistoricalSeedFixture(t)
	for _, mode := range []string{"original", "finalized-current-no-active-other", "active", "sealed", "future-expiry", "wrong-expiry", "current-session", "wrong-session", "wrong-issuer", "wrong-root", "wrong-fingerprint", "unknown-ack", "duplicate-ack", "different-known-ack", "missing-ack", "changed-allowlist", "duplicate-subject", "changed-observation", "wrong-copy-uid"} {
		t.Run(mode, func(t *testing.T) {
			state := original
			state.Subjects = slices.Clone(original.Subjects)
			state.Installed = slices.Clone(original.Installed)
			observed := observation
			switch mode {
			case "finalized-current-no-active-other":
				state.OtherActive = 0
			case "active":
				state.Status = "active"
			case "sealed":
				state.Status = "sealed"
			case "future-expiry":
				state.Expires = now.Add(time.Hour)
			case "wrong-expiry":
				state.Expires = now.Add(-2 * time.Hour)
			case "current-session":
				state.Session = current.BootstrapSessionID
			case "wrong-session":
				state.Session = "88888888-8888-4888-8888-888888888888"
			case "wrong-issuer":
				state.Issuer = "88888888-8888-4888-8888-888888888888"
			case "wrong-root":
				state.Root = strings.Repeat("d", 64)
			case "wrong-fingerprint":
				state.Fingerprint = strings.Repeat("d", 64)
			case "unknown-ack":
				state.Installed[0] = "service:foreign"
			case "duplicate-ack":
				state.Installed[0] = state.Installed[1]
			case "different-known-ack":
				state.Installed[0] = "service:factory-enroll"
			case "missing-ack":
				state.Installed = state.Installed[:4]
			case "changed-allowlist":
				state.Subjects = state.Subjects[:8]
			case "duplicate-subject":
				state.Subjects[0] = state.Subjects[1]
			case "changed-observation":
				saved := original
				saved.Fingerprint = strings.Repeat("d", 64)
				observed.Session = mustProviderRecoveryJSON(saved)
			case "wrong-copy-uid":
				observed.UIDs = map[string]string{"video-cloud-dev-video-cloud": "unknown", "video-cloud-dev-account-manager": "22222222-2222-4222-8222-222222222222", "video-cloud-dev-secrets": "33333333-3333-4333-8333-333333333333"}
			}
			snapshot, err := validateManagedHistoricalSeedState(state, observed, current, subjects, now)
			pass := mode == "original" || mode == "finalized-current-no-active-other"
			if (err == nil) != pass {
				t.Fatal("unqualified seed or ACK set accepted", mode, err)
			}
			if pass {
				before, _ := validateManagedHistoricalSeedState(original, observation, current, subjects, now)
				if deploymentplan.Digest(snapshot) != deploymentplan.Digest(before) {
					t.Fatal("dynamic current-session count changed immutable old-seed proof")
				}
			}
		})
	}
}

func TestManagedHistoricalSeedCleanupNeverUsesGenericApplicationForward(t *testing.T) {
	_, observed, current, _, _ := managedHistoricalSeedFixture(t)
	r := &createServiceSessionRecovery{ApplicationForward: true}
	known, err := verifyManagedHistoricalSeedCleanup(context.Background(), deploymentConfig{Environment: "dev"}, secretStore{}, environmentCreateBootstrapRuntime{ServiceSessionRecovery: r}, current, observed)
	if err != nil || known {
		t.Fatal("generic forward flag fabricated typed cleanup authority", err)
	}
	r.ManagedHistoricalSeedCleanup = &createManagedHistoricalSeedCleanup{State: managedHistoricalSeedState{Session: observed.Cleanup.SessionID}}
	if known, err := verifyManagedHistoricalSeedCleanup(context.Background(), deploymentConfig{Environment: "dev"}, secretStore{}, environmentCreateBootstrapRuntime{ServiceSessionRecovery: r}, current, observed); err == nil || known {
		t.Fatal("missing original exclusive execution and admission accepted")
	}
}

func TestManagedHistoricalSeedCleanupRuntimeRequiresExactThirdAdmissionAndOriginalObservation(t *testing.T) {
	for _, mode := range []string{"original", "changed-native-ack", "changed-observation", "old-schema"} {
		t.Run(mode, func(t *testing.T) {
			e, b, r, _ := managedApplicationLeaseDepthFixture(t, 3)
			state, observed, current, subjects, now := managedHistoricalSeedFixture(t)
			snapshot, err := validateManagedHistoricalSeedState(state, observed, current, subjects, now)
			if err != nil {
				t.Fatal(err)
			}
			r.Ceremony = current
			r.ManagedApplicationPartial.Interrupted.LeaseSHA = deploymentplan.Digest(deploymentplan.Object(nil))
			r.ManagedHistoricalSeedCleanup = &createManagedHistoricalSeedCleanup{SourceSHA: deploymentplan.Digest(e.Journal), CurrentSession: current.BootstrapSessionID, ObservationSHA: deploymentplan.Digest(observed), ClientsSHA: strings.Repeat("a", 64), State: snapshot}
			if mode == "old-schema" {
				admitManagedApplicationLeaseFixtureWithSchema(t, e, r, "rtk-cloud.create-managed-application-app-role-recovery/v1")
			} else {
				admitManagedApplicationLeaseFixture(t, e, r)
			}
			if mode == "changed-native-ack" {
				state.Installed[0] = "service:factory-enroll"
			}
			if mode == "changed-observation" {
				observed.Cleanup.SecretUID = "88888888-8888-4888-8888-888888888888"
			}
			row := filepath.Join(t.TempDir(), "native-old-row.json")
			if err := os.WriteFile(row, mustProviderRecoveryJSON(state), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "kubectl")
			script := `#!/bin/sh
case "$*" in
*default_transaction_read_only*registry-read*) cat "$RTK_TEST_HISTORICAL_NATIVE_ROW" ;;
*) exit 2 ;;
esac
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_HISTORICAL_NATIVE_ROW", row)
			before := deploymentplan.Digest(e.Journal)
			known, err := verifyManagedHistoricalSeedCleanup(context.Background(), deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}, e.Store, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, current, observed)
			if (err == nil && known) != (mode == "original") {
				t.Fatal("unauthenticated old ACK cleanup admitted", mode, err)
			}
			if before != deploymentplan.Digest(e.Journal) {
				t.Fatal("cleanup proof verification persisted operational outcome")
			}
		})
	}
}

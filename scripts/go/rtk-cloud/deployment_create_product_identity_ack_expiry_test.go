package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductInstallationFactsAllowOnlyExactCompleteExpiredActiveRoster(t *testing.T) {
	now := time.Now().UTC()
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}}
	subjects := []string{"service:mqtt", "service:ota", "service:shadow"}
	ack := productSessionInstallations{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Subjects: slices.Clone(subjects), Installed: []string{subjects[2], subjects[0], subjects[1]}, Status: "active", Expires: now.Add(-time.Second)}
	before := deploymentplan.Digest(ack)
	if done, err := productInstallationsComplete(ack, service, subjects, now); err != nil || !done || deploymentplan.Digest(ack) != before {
		t.Fatal("exact completed installation facts must reach native expiry finalization without rewriting lifetime", err)
	}
	for _, mode := range []string{"incomplete", "duplicate-ack", "unknown-ack", "empty-policy", "duplicate-policy", "bootstrap-policy", "session", "issuer", "root", "subjects", "status", "missing-expiry"} {
		t.Run(mode, func(t *testing.T) {
			bad := ack
			bad.Subjects, bad.Installed = slices.Clone(ack.Subjects), slices.Clone(ack.Installed)
			policy := slices.Clone(subjects)
			switch mode {
			case "incomplete":
				bad.Installed = bad.Installed[:2]
			case "duplicate-ack":
				bad.Installed[2] = bad.Installed[0]
			case "unknown-ack":
				bad.Installed[2] = "service:foreign"
			case "empty-policy":
				policy, bad.Subjects, bad.Installed = nil, nil, nil
			case "duplicate-policy":
				policy = []string{subjects[0], subjects[0]}
				bad.Subjects, bad.Installed = slices.Clone(policy), slices.Clone(policy)
			case "bootstrap-policy":
				policy = []string{"service:deployment-bootstrap"}
				bad.Subjects, bad.Installed = slices.Clone(policy), slices.Clone(policy)
			case "session":
				bad.Session = "foreign"
			case "issuer":
				bad.Issuer = "foreign"
			case "root":
				bad.Root = strings.Repeat("b", 64)
			case "subjects":
				bad.Subjects[2] = "service:foreign"
			case "status":
				bad.Status = "pending"
			case "missing-expiry":
				bad.Expires = time.Time{}
			}
			if done, err := productInstallationsComplete(bad, service, policy, now); err == nil || done {
				t.Fatal("unqualified expired installation roster accepted", mode)
			}
		})
	}
}

// Registry/native-command boundaries are isolated; the actual ACK wrapper,
// finalizer installation guard and native-expiry journal lifecycle run here.
func TestProductExpiredCompleteACKWrapperHandsOffToNativeExpiry(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	installed, err := readProductInstalled(f.store, f.l)
	if err != nil {
		t.Fatal(err)
	}
	service := installed.Ceremony
	f.installed = slices.Clone(f.subjects)
	f.expires = time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	expires := f.expires
	commands := 0
	f.nativeBoundary = func(args []string, _ []byte) ([]byte, int, bool) {
		if strings.Contains(strings.Join(args, " "), "'complete',subjects <@ installed_subjects") {
			return mustProviderRecoveryJSON(completedBootstrapExpiry{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Status: f.status, Expires: f.expires, Complete: true}), 0, true
		}
		if len(args) != 0 && args[len(args)-1] == "expire" {
			commands++
			f.status = "expired"
			return nil, 0, true
		}
		return nil, 0, false
	}
	before, custody := deploymentplan.Digest(f.e.Journal), deploymentplan.Digest(f.bootstrap)
	if err := waitProductSessionInstallations(t.Context(), f.cfg, f.runtime, service, f.subjects); err != nil {
		t.Fatal("completed expired active roster cannot pass the actual wrapper", err)
	}
	if err := verifyEnvironmentCreateFinalizerInstallations(t.Context(), f.cfg, f.runtime, service); err != nil {
		t.Fatal("wrapper and finalizer installation facts disagree", err)
	}
	if commands != 0 || !f.expires.Equal(expires) || deploymentplan.Digest(f.e.Journal) != before || deploymentplan.Digest(f.bootstrap) != custody {
		t.Fatal("fact observation mutated session lifetime, native state or custody")
	}
	runtime := f.runtime
	runtime.ServiceSessionRecovery = &createServiceSessionRecovery{Renewal: true}
	if err := expireCompletedRenewedBootstrapSession(context.Background(), f.cfg, runtime, service); err != nil {
		t.Fatal("complete facts did not reach the existing native expiry producer", err)
	}
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, "bootstrap-completed-expiry-"+service.BootstrapSessionID)
	if commands != 1 || f.status != "expired" || !f.expires.Equal(expires) || entry == nil || entry.State != "completed" || deploymentplan.Digest(f.bootstrap) != custody {
		t.Fatal("native expiry did not retain the original identity/lifetime and record its real outcome")
	}
}

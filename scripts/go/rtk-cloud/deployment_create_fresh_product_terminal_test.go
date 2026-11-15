package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native issuer, five product enrollments, immutable program, closed issuer,
// mirrors, registry and finalizer are real; only external HTTP/CLI are isolated.
func TestFreshFourteenTerminalExpiryRetainsOriginalProgramAndFinalizes(t *testing.T) {
	f := newProductProtocolFixtureForRegistration(t, false, 24*time.Hour, true)
	f.unscoped = true
	f.runtime.ProductIdentityScope, f.runtime.PKIClient.mutationScope = "", ""
	f.subjects, _ = environmentCreatePlannedServiceSubjects(f.cfg, f.p)
	crl, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := createEnvironmentServiceSession(t.Context(), f.cfg, f.p, f.store, f.runtime, environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, RootCRLPEM: crl, RootCRLSecret: "pki-service-root-crl"}, f.subjects, f.apply)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := f.controls(service, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.apply(t.Context(), "managed-complete-issuer", controls); err != nil {
		t.Fatal(err)
	}
	if err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, service, f.apply); err != nil {
		t.Fatal(err)
	}
	program, err := readFreshProductProgram(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := readProductInstalled(f.store, &program.Lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	programPath := filepath.Join(f.store.Root, freshProductProgramPath(f.e.Journal.PlanSHA256))
	beforeProgram, _ := os.ReadFile(programPath)
	beforeCustody := deploymentplan.Digest(f.bootstrap)
	f.installed = slices.Clone(f.subjects)
	f.expires = time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	state := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: f.rows["service:deployment-bootstrap"].Fingerprint, Status: "active", Expires: f.expires, Subjects: slices.Clone(f.subjects), Installed: slices.Clone(f.installed)}
	if err = verifyFreshProductTerminalOutputs(t.Context(), f.cfg, f.runtime, program, installed, state, time.Now().UTC()); err != nil {
		t.Fatal("all14 installed/closed must use terminal cleanup rather than mint another session", err)
	}
	for _, mode := range []string{"incomplete", "foreign-session", "other-active", "foreign-subject", "mirror-uid", "unclosed"} {
		t.Run(mode, func(t *testing.T) {
			changed := state
			changed.Installed = slices.Clone(state.Installed)
			changed.Subjects = slices.Clone(state.Subjects)
			id := installed.Outputs[0].ResourceID
			saved := deploymentplan.Clone(f.b.objects[id])
			closed := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/fresh-product-enrollment-closed")
			savedClosed := *closed
			defer func() { f.b.objects[id] = saved; *closed = savedClosed }()
			switch mode {
			case "incomplete":
				changed.Installed = changed.Installed[:13]
			case "foreign-session":
				changed.Session = "99999999-9999-4999-8999-999999999999"
			case "other-active":
				changed.OtherActive = 1
			case "foreign-subject":
				changed.Subjects[0] = "service:foreign"
			case "mirror-uid":
				deploymentplan.Map(f.b.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "unclosed":
				closed.State = "pending"
			}
			if err := verifyFreshProductTerminalOutputs(t.Context(), f.cfg, f.runtime, program, installed, changed, time.Now().UTC()); err == nil {
				t.Fatal("unverified terminal phase accepted", mode)
			}
		})
	}
	expiryCalls := 0
	f.nativeBoundary = func(args []string, _ []byte) ([]byte, int, bool) {
		if strings.Contains(strings.Join(args, " "), "'complete',subjects <@ installed_subjects") {
			return mustProviderRecoveryJSON(completedBootstrapExpiry{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Status: f.status, Expires: f.expires, Complete: true}), 0, true
		}
		if len(args) > 0 && args[len(args)-1] == "expire" {
			expiryCalls++
			f.status = "expired"
			return nil, 0, true
		}
		return nil, 0, false
	}
	if err = waitProductSessionInstallations(t.Context(), f.cfg, f.runtime, service, f.subjects); err != nil {
		t.Fatal(err)
	}
	expiryRuntime := f.runtime
	expiryRuntime.ServiceSessionRecovery = &createServiceSessionRecovery{Renewal: true}
	if err = expireCompletedRenewedBootstrapSession(context.Background(), f.cfg, expiryRuntime, service); err != nil {
		t.Fatal(err)
	}
	if err = finalizeEnvironmentCreateServiceSession(t.Context(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, service, nil); err != nil {
		t.Fatal("actual completed expired session finalization", err)
	}
	afterProgram, _ := os.ReadFile(programPath)
	if expiryCalls != 1 || string(beforeProgram) != string(afterProgram) || beforeCustody == deploymentplan.Digest(f.bootstrap) || f.final.SessionID != service.BootstrapSessionID {
		t.Fatal("terminal closure changed program/session or missed native expiry/cleanup")
	}
	if len(f.rows) != 6 {
		t.Fatal("terminal continuation re-enrolled permanent products", len(f.rows))
	}
}

// Separate native registry reads can straddle the current session's expiry.
// Once the exact post-enrollment proof exists, its historical predecessors may
// count either the sole current head (1) or its clock-expired state (0).
func TestFreshTerminalHistoricalCounterCrossesCurrentExpiry(t *testing.T) {
	saved := managedSessionState{Session: "original", Issuer: "same-issuer", Root: "same-root", Fingerprint: "same-leaf", Status: "expired", Expires: time.Now().Add(-time.Hour), Subjects: []string{"service:installed"}, Installed: []string{"service:installed"}}
	for _, count := range []int{0, 1} {
		current := saved
		current.OtherActive = count
		if !secondSessionTerminalHistoricalState(saved, current, true) {
			t.Fatalf("authenticated terminal predecessor cannot cross current expiry counter %d", count)
		}
	}
	for _, mode := range []string{"competing", "fingerprint", "subject", "still-active"} {
		current := saved
		switch mode {
		case "competing":
			current.OtherActive = 2
		case "fingerprint":
			current.Fingerprint = "different"
		case "subject":
			current.Subjects = []string{"service:foreign"}
		case "still-active":
			current.Status = "active"
		}
		if secondSessionTerminalHistoricalState(saved, current, true) {
			t.Fatal("terminal history accepted changed native lineage", mode)
		}
	}
	current := saved
	if secondSessionTerminalHistoricalState(saved, current, false) {
		t.Fatal("ordinary live enrollment observer accepted absence of its current active head")
	}
}

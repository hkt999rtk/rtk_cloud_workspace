package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Compose the genuine original Core, three used renewals, Device/App trust and
// native signing-role completion. Temporary expiry is chosen before the final
// DNS renewal is signed; no certificate, source artifact or accepted receipt is
// changed to select this later recovery boundary. Historical admission artifacts
// are synthetic input records supplied by the shared fixture; this test runs
// the strict production source loader, not the public six-gate qualifier.
func newHistoricalManagedTransportSourceExecutionContract(t *testing.T, hooks ...coreExecutionContractHooks) (*deviceTrustExecutionContract, *createServiceSessionRecovery, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer, historicalTransportIdentityObservations) {
	t.Helper()
	if len(hooks) == 0 {
		original := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
		// Select a live original CRL already within the maintained 45-minute
		// renewal window before its first signature or immutable publication.
		original.OriginalServiceCRLLifetime = 30 * time.Minute
		original.OriginalServiceCALifetime = 7 * 24 * time.Hour
		hooks = []coreExecutionContractHooks{original}
	}
	device, _, roots, intermediates := newHistoricalDeviceAppExecutionContract(t, hooks...)
	f := device.acceptedCoreExecutionContract
	identities := historicalTransportIdentities(t, f, roots, intermediates, device.authorityKeys)
	clientsOnly := identities
	clientsOnly.Servers = nil // The server enrollment stage has not executed yet.
	installHistoricalTransportProtocol(t, f, clientsOnly)
	ledger := historicalTransportCaptureVolumes(t, f)
	// At this exact pre-Apps phase the real producers have allocated thirteen
	// bootstrap claims and three foundation claims. The two API trust claims
	// are outputs of the later maintained Apps26 stage, yielding the required
	// final eighteen. Do not preallocate them or relax that later invariant.
	var expected, actual []string
	for _, name := range f.p.Plan.Bootstrap.Claims {
		if name == "video-cloud-api-pki-trust" || name == "video-cloud-api-app-pki-trust" {
			continue
		}
		namespace := f.p.Plan.Stack + "-video-cloud"
		if name == "account-manager-service-state" {
			namespace = f.p.Plan.Stack + "-account-manager"
		}
		if name == "openbao-pki-identity" {
			namespace = f.p.Plan.Stack + "-secrets"
		}
		expected = append(expected, namespace+"/"+name)
	}
	expected = append(expected, f.p.Plan.Stack+"-secrets/data-openbao-0", f.p.Plan.Stack+"-secrets/audit-openbao-0", f.p.Plan.Stack+"-platform/data-postgresql-0")
	for _, owner := range ledger.Volumes {
		actual = append(actual, owner.Namespace+"/"+owner.PVCName)
	}
	slices.Sort(expected)
	slices.Sort(actual)
	if len(expected) != 16 || !slices.Equal(actual, expected) {
		t.Fatalf("pre-Apps original durable owners differ: got %v; want exact %v", actual, expected)
	}
	leaf, err := parseFirstCertificatePEM([]byte(f.certificate))
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(leaf.NotAfter) + 100*time.Millisecond
	if remaining > 3*time.Minute+time.Second {
		t.Fatal("historical native seed lifetime exceeded its selected bound")
	}
	if remaining > 0 {
		t.Logf("waiting %.1fs for the original signed temporary seed to expire", remaining.Seconds())
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	before := deploymentplan.Digest(f.e.Journal)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil {
		t.Fatal("complete original managed transport source", err)
	}
	if r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.SessionRenewal == nil || !r.PostServiceRenewal || r.RenewalCandidate == nil || r.Candidate == nil {
		t.Fatal("full source omitted original-key seed and transport candidates")
	}
	if deploymentplan.Digest(f.e.Journal) != before {
		t.Fatal("source qualification changed original parent history")
	}
	f.p = p
	if len(roots) != 5 || len(intermediates) != 4 || len(identities.Clients) != 11 || len(identities.Servers) != 2 {
		t.Fatal("original native identity cohort differs")
	}
	if r.ManagedTransportRecovery.SessionRenewal.OriginalSession != f.session || r.RenewedIntent.SessionID == f.session || len(r.RenewalCandidate.Resources) != 1 || len(r.Candidate.Resources) != 16 {
		t.Fatal("managed renewal changed original scope")
	}
	for _, domain := range managedPKIDomains {
		var saved map[string]map[string]any
		if err := readExecutionJSON(filepath.Join(f.store.Root, "pki", domain, "issuer.json"), &saved); err != nil {
			t.Fatal(err)
		}
		if deploymentplan.Digest(saved["root"]) != deploymentplan.Digest(roots[domain].Document) {
			t.Fatal("managed source changed native root", domain)
		}
	}
	return device, r, roots, intermediates, identities
}

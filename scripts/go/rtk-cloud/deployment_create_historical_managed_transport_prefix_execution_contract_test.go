package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Historical admission documents are explicit synthetic inputs to this fixture.
// The same-key seed, enrollment, native interruptions and source-history readers
// below are production operations. A short seed selects the historical stage
// boundary; it does not claim the separate current two-hour qualification.
func advanceHistoricalManagedTransportPrefix(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery, roots, intermediates map[string]environmentCreateRegisteredIssuer, identities historicalTransportIdentityObservations, seedLifetime time.Duration) (*createServiceSessionRecovery, []deploymentplan.Definition) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	if seedLifetime <= 0 {
		t.Fatal("native seed lifetime must be selected before issuance")
	}
	// Root CRL publication may have changed the effective public credential
	// body since the earlier source read. Requalify its exact current candidate
	// before the admission fixture freezes this new historical generation.
	p := f.p
	p.Materials = map[string]string{}
	current, err := loadAcceptedServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil || current.ManagedTransportRecovery == nil || current.ManagedTransportRecovery.SessionRenewal == nil {
		t.Fatal("current managed source after Service CRL publication", err)
	}
	r, f.p = current, p
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-application-trust-recovery/v1", f.p.Materials)
	base := filepath.Join("deployment/create/openbao-recovery", r.Review)
	f.write(t, filepath.Join(base, "used-bootstrap-renewal-plan.json"), r.RenewalCandidate)
	f.write(t, filepath.Join(base, "openbao-root-trust-plan.json"), r.Candidate)
	certificate, state := historicalTransportSeedLeaf(t, f, r.RenewedIntent.SessionID, time.Now().UTC().Add(seedLifetime))
	runtime := f.runtime
	runtime.ServiceSessionRecovery, runtime.Backend = r, f.backend
	nativeApply := historicalTransportSessionApply(t, f, r, certificate, state)
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := nativeApply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "service-session-original-key-renewal-"+r.Review {
			sessions := f.protocol["historical_sessions"].(map[string]managedSessionState)
			original := sessions[r.ManagedTransportRecovery.SessionRenewal.OriginalSession]
			original.Status = "expired"
			sessions[original.Session] = original
			f.publish(t)
		}
		return nil
	}
	service, err := executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
	if err != nil {
		t.Fatal("actual managed seed producer", err)
	}
	if service.BootstrapSessionID != state.Session || service.Root.ID != roots["service"].ID || service.Intermediate.ID != intermediates["service"].ID {
		t.Fatal("managed seed changed original authority")
	}
	f.service, f.session, f.certificate = service, state.Session, certificate
	// These server responses have not yet been published. Bind their request
	// IDs to the actual newly issued session before its enrollment Jobs run;
	// the prior DNS session never issued these future transport servers.
	for i := range identities.Servers {
		role := "emqx-pki"
		if identities.Servers[i].Domain == "openbao_tls" {
			role = "openbao"
		}
		identities.Servers[i].Request = service.BootstrapSessionID + ":" + role + ":server"
	}
	r.Ceremony = service
	f.protocol["session"] = map[string]any{"session_id": state.Session, "issuer_id": state.Issuer, "root": state.Root, "fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "installed": state.Installed, "expires": state.Expires, "other_active": state.OtherActive}
	f.publish(t)
	f.runtime = runtime
	var definitions []deploymentplan.Definition
	var partial *createManagedTransportPartialRecovery
	stop := errors.New("isolated original native managed host interruption")
	err = executeEnvironmentCreateManagedTransports(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, runtime, service, roots, intermediates, func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage == "transport-managed-hosts" {
			definitions = defs
			partial = historicalTransportOriginalHostInterruption(t, f, defs)
			return stop
		}
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "transport-server-enrollment" {
			// Expose native issuance only after the two real enrollment Jobs complete.
			f.protocol["historical_servers"] = identities.Servers
			f.publish(t)
		}
		return nil
	})
	if !errors.Is(err, stop) || partial == nil || len(definitions) != 7 {
		t.Fatal("managed coordinator did not preserve original host interruption", err)
	}
	if _, _, review, err := loadManagedTransportPartialSource(f.store, f.e); err != nil || review != r.Review {
		t.Fatal("actual original partial history", err)
	}
	original, journal, err := loadOriginalManagedTransportPartialPlan(f.store, f.e)
	if err != nil || original.SHA256() != partial.Original.SHA256() || deploymentplan.Digest(journal) != deploymentplan.Digest(partial.Journal) {
		t.Fatal("original failed host source differs", err)
	}
	partial.SourceReview = r.Review
	partial.AdditionalOwners, err = loadManagedTransportAdditionalSourceOwners(f.store, f.e, r.Review)
	if err != nil {
		t.Fatal("actual original retained policy owners", err)
	}
	r.ManagedTransportRecovery.Partial = partial
	r.PostServiceRenewal = false
	for generation := 0; generation < 2; generation++ {
		defs := historicalTransportForwardDefinitions(t, f, partial, definitions)
		preview := historicalTransportHostPreview(t, f, defs)
		materials := map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256()}
		review, _ := f.admitRecovery(t, "rtk-cloud.create-managed-transports-partial-recovery/v1", materials)
		f.write(t, filepath.Join("deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), preview)
		forward := historicalTransportHostForwardInterruption(t, f, partial, definitions, review)
		partial.PriorForward = forward
		if generation == 1 {
			for _, resource := range forward.Plan.Resources {
				if _, ok := partial.AdditionalOwners[resource.ID]; ok {
					resource.UID = forward.Journal.Owners[resource.ID]
					partial.AdditionalOwners[resource.ID] = resource
				}
			}
		}
		_, actual, err := loadManagedTransportPartialBase(f.store, f.e)
		if err != nil || actual == nil || actual.Plan.SHA256() != forward.Plan.SHA256() {
			t.Fatal("actual interrupted forward history", generation, err)
		}
	}

	if partial == nil || partial.PriorForward == nil || partial.PriorForward.Previous == nil || partial.PriorForward.Previous.Previous != nil {
		t.Fatal("original native host history lacks exactly two interrupted forwards")
	}
	if _, _, _, err := loadManagedTransportPartialSource(f.store, f.e); err != nil {
		t.Fatal("full original managed history does not authenticate", err)
	}
	return r, definitions
}

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRemainingCleanupFixture(t *testing.T) (*createServiceSessionRecovery, *createManagedHistoricalSeedCleanup) {
	t.Helper()
	state, observed, current, subjects, now := managedHistoricalSeedFixture(t)
	current.BootstrapSessionID = "88888888-8888-4888-8888-888888888888"
	snapshot, err := validateManagedHistoricalSeedState(state, observed, current, subjects, now)
	if err != nil {
		t.Fatal(err)
	}
	late := &createManagedLateSessionRecovery{NewSessionID: current.BootstrapSessionID, OriginalClientsSHA: strings.Repeat("c", 64), SourceRenewedIntent: createServiceSessionIntent{Subjects: subjects}}
	r := &createServiceSessionRecovery{ManagedLateSession: late, Ceremony: current, StaleSeeds: []createServiceSessionObserved{observed}, ManagedLateRemaining: &createManagedLateRemaining{SourceSHA: strings.Repeat("a", 64), SessionID: current.BootstrapSessionID}}
	r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
	proof := &createManagedHistoricalSeedCleanup{SourceSHA: r.ManagedLateRemaining.SourceSHA, CurrentSession: current.BootstrapSessionID, ObservationSHA: deploymentplan.Digest(observed), ClientsSHA: late.OriginalClientsSHA, State: snapshot}
	return r, proof
}

func TestManagedLateRemainingCleanupRestoresOnlyExactQualifiedFiveACKSource(t *testing.T) {
	for _, mode := range []string{"original", "source", "session", "observation", "clients", "four-ack", "different-ack", "active", "fingerprint", "changed-copy", "duplicate-observation", "stale-inventory"} {
		t.Run(mode, func(t *testing.T) {
			r, p := managedLateRemainingCleanupFixture(t)
			before := deploymentplan.Digest(p)
			switch mode {
			case "source":
				p.SourceSHA = strings.Repeat("e", 64)
			case "session":
				p.CurrentSession = "99999999-9999-4999-8999-999999999999"
			case "observation":
				p.ObservationSHA = strings.Repeat("e", 64)
			case "clients":
				p.ClientsSHA = strings.Repeat("e", 64)
			case "four-ack":
				p.State.Installed = slices.Clone(p.State.Installed[:4])
			case "different-ack":
				p.State.Installed = slices.Clone(p.State.Installed)
				p.State.Installed[0] = "service:factory-enroll"
			case "active":
				p.State.Status = "active"
			case "fingerprint":
				p.State.Fingerprint = strings.Repeat("e", 64)
			case "changed-copy":
				r.StaleSeeds[0].UIDs = map[string]string{"foreign": "99999999-9999-4999-8999-999999999999"}
				r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
			case "duplicate-observation":
				r.StaleSeeds = append(r.StaleSeeds, r.StaleSeeds[0])
				r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
			case "stale-inventory":
				r.ManagedLateRemaining.StaleSeedsSHA = strings.Repeat("e", 64)
			}
			err := restoreManagedLateRemainingHistoricalCleanup(r, p)
			if (err == nil) != (mode == "original") {
				t.Fatal("foreign historical cleanup was admitted", mode, err)
			}
			if mode == "original" {
				if r.ManagedHistoricalSeedCleanup == nil || deploymentplan.Digest(r.ManagedHistoricalSeedCleanup) != before || r.ManagedLateRemaining.HistoricalSeedCleanup != r.ManagedHistoricalSeedCleanup {
					t.Fatal("same source proof not hydrated")
				}
				r.ManagedHistoricalSeedCleanup.State.Installed[0] = "caller-only-change"
				if deploymentplan.Digest(p) != before {
					t.Fatal("proof restoration retained caller slices")
				}
			}
		})
	}
}

func TestManagedLateRemainingCleanupArchiveCannotChangePureSource(t *testing.T) {
	r, p := managedLateRemainingCleanupFixture(t)
	current := *r.ManagedLateRemaining
	archived := current
	archived.HistoricalSeedCleanup = p
	archived.Review = strings.Repeat("e", 64)
	if !managedLateRemainingCleanupBaseMatches(&current, &archived) {
		t.Fatal("qualified proof could not be restored alongside same pure source")
	}
	archived.CredentialObservationSHA = strings.Repeat("f", 64)
	if managedLateRemainingCleanupBaseMatches(&current, &archived) {
		t.Fatal("historical proof exempted changed credential source")
	}
}

func TestManagedLateRemainingCleanupRestoresBothE0E1RetirementsOnce(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	_, old, ceremony, _, now := managedHistoricalSeedFixture(t)
	parent, priorReview, originalReview := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: parent}}
	makeLate := func(sid, fp string) *createManagedLateSessionRecovery {
		service := ceremony
		service.BootstrapSessionID = sid
		service.CleanupBinding = old.Cleanup
		service.CleanupBinding.SessionID = sid
		service.CredentialUIDs = old.UIDs
		service.CustodyReader = "original-reader"
		return &createManagedLateSessionRecovery{ParentSHA: parent, OriginalSession: sid, SourceCeremony: service, OriginalSessionState: managedSessionState{Session: sid, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: fp, Expires: now.Add(-time.Hour)}}
	}
	e0 := makeLate("11111111-1111-4111-8111-111111111111", strings.Repeat("d", 64))
	e1 := makeLate("22222222-2222-4222-8222-222222222222", strings.Repeat("e", 64))
	e1.ExpiredRenewal = &createManagedLateExpiredRenewal{PriorReview: priorReview, PriorLate: e0}
	r := &createServiceSessionRecovery{ManagedLateSession: e1, ManagedLateRemaining: &createManagedLateRemaining{}, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{SourceReview: originalReview}}, StaleSeeds: []createServiceSessionObserved{old}}
	for _, item := range []struct {
		late   *createManagedLateSessionRecovery
		review string
	}{{e0, originalReview}, {e1, priorReview}} {
		observed := createServiceSessionObserved{Reader: item.late.SourceCeremony.CustodyReader, UIDs: item.late.SourceCeremony.CredentialUIDs, Cleanup: item.late.SourceCeremony.CleanupBinding, Session: mustProviderRecoveryJSON(item.late.OriginalSessionState)}
		managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create", parent, "bootstrap/service/session-renewal", item.review, "observed.json"), mustProviderRecoveryJSON(observed))
	}
	if err := prepareManagedLateRemainingCopyRetirement(store, e, r); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(r.StaleSeeds)
	if len(r.StaleSeeds) != 3 || r.ManagedLateRemaining.StaleSeedsSHA != before {
		t.Fatal("original fiveACK/e0/e1 copy inventory incomplete")
	}
	if err := prepareManagedLateRemainingCopyRetirement(store, e, r); err != nil || len(r.StaleSeeds) != 3 || deploymentplan.Digest(r.StaleSeeds) != before {
		t.Fatal("repeat hydration duplicated or altered old observations", err)
	}
}

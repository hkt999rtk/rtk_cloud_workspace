package main

import "testing"

func TestAdoptCompletedServiceSessionPreservesOriginalDomainRecovery(t *testing.T) {
	root := environmentCreateRegisteredIssuer{ID: "service-root", RootFingerprint: "root"}
	issuer := environmentCreateRegisteredIssuer{ID: "service-issuer"}
	mqtt := &environmentCreateRegisteredIssuer{ID: "mqtt-root"}
	app := &createAppTrustRecovery{}
	device := &createDeviceTrustRecovery{}
	current := &createServiceSessionRecovery{Ceremony: environmentCreateServiceCeremony{Root: root, Intermediate: issuer}, OriginalKeySHA: "same-key", OriginalCSRSHA: "same-csr", MQTTRoot: mqtt, AppTrustRecovery: app, DeviceTrustRecovery: device, ManagedTransportRecovery: &createManagedTransportRecovery{PartialTrustForward: &createManagedLateSeedForward{}}}
	latest := &createServiceSessionRecovery{Ceremony: environmentCreateServiceCeremony{Root: root, Intermediate: issuer}, OriginalKeySHA: "same-key", OriginalCSRSHA: "same-csr", Renewal: true, SecondSession: true, Review: "latest", Intent: createServiceSessionIntent{SessionID: "previous"}, RenewedIntent: createServiceSessionIntent{SessionID: "latest"}, ManagedTransportRecovery: &createManagedTransportRecovery{SessionRenewal: &createManagedSessionRecovery{}}}
	if err := adoptCompletedServiceSession(current, latest); err != nil {
		t.Fatal(err)
	}
	if current.MQTTRoot != mqtt || current.AppTrustRecovery != app || current.DeviceTrustRecovery != device || current.Review != "latest" || !current.SecondSession || current.RenewedIntent.SessionID != "latest" || current.ManagedTransportRecovery.PartialTrustForward != nil || current.ManagedTransportRecovery.SessionRenewal != latest.ManagedTransportRecovery.SessionRenewal {
		t.Fatal("latest session did not preserve original non-Service recovery")
	}
	foreign := *latest
	foreign.OriginalKeySHA = "other-key"
	if err := adoptCompletedServiceSession(current, &foreign); err == nil {
		t.Fatal("different private key was accepted")
	}
}

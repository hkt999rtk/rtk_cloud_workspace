package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type lateCredentialExecutionContract struct {
	store       secretStore
	e           *environmentCreateExecution
	late        *createManagedLateSessionRecovery
	plan        *deploymentplan.Plan
	journal     deploymentplan.Journal
	observed    createServiceSessionObserved
	objects     map[string]deploymentplan.Object
	certificate string
}

func newLateCredentialExecutionContract(t *testing.T) *lateCredentialExecutionContract {
	t.Helper()
	chain := newManagedLateBootstrapChainFixture(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	sign := func(serial int64, before, after time.Time) (string, *x509.Certificate) {
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, chain.icaCertificate, &key.PublicKey, chain.icaKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + chain.ceremony.Intermediate.Certificate + chain.ceremony.Root.Certificate
		return cert, leaf
	}
	oldCert, oldLeaf := sign(701, chain.now.Add(-9*time.Hour), chain.now.Add(-time.Hour))
	newCert, newLeaf := sign(702, chain.now.Add(-time.Minute), chain.now.Add(8*time.Hour))
	store, e, _, backend := freshStageFixture(t)
	oldSID, nextSID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	ceremony := chain.ceremony
	ceremony.Intermediate.ID = "33333333-3333-4333-8333-333333333333"
	ceremony.BootstrapSessionID = oldSID
	ceremony.BootstrapCredentialNamespaces = []string{e.Journal.Stack + "-video-cloud", e.Journal.Stack + "-secrets", e.Journal.Stack + "-account-manager"}
	ceremony.BootstrapCredentialNamespace = ceremony.BootstrapCredentialNamespaces[0]
	ceremony.CredentialUIDs = map[string]string{}
	ceremony.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: oldSID, NamespaceUID: "44444444-4444-4444-8444-444444444444", SecretUID: "55555555-5555-4555-8555-555555555555", PolicySHA256: strings.Repeat("a", 64), KubernetesCA: base64.StdEncoding.EncodeToString([]byte(ceremony.Root.Certificate))}
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}})
	if err != nil {
		t.Fatal(err)
	}
	late := &createManagedLateSessionRecovery{ParentSHA: e.Journal.PlanSHA256, Review: strings.Repeat("b", 64), OriginalSession: oldSID, OriginalEnrollmentSession: oldSID, NewSessionID: nextSID, OriginalKeySHA: environmentPreflightBytesSHA256([]byte(keyPEM)), SourceCeremony: ceremony, SignerPolicySHA: strings.Repeat("a", 64), Renewal: &createManagedSessionRecovery{OriginalSession: oldSID, OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte(oldCert))}}
	late.OriginalSessionState = managedSessionState{Session: oldSID, Issuer: ceremony.Intermediate.ID, Root: ceremony.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(oldLeaf.Raw), Status: "expired", Expires: oldLeaf.NotAfter, Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects[:8])}
	late.SourceRenewedIntent.Subjects = slices.Clone(subjects)
	late.Custody.Desired = deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "original-custody"}}
	defs := []deploymentplan.Definition{}
	for index, ns := range ceremony.BootstrapCredentialNamespaces {
		old := environmentCreateSecret(ns, "service-bootstrap-"+oldSID, map[string]string{"tls.crt": oldCert, "tls.key": keyPEM, "ca.crt": ceremony.Root.Certificate})
		old.Object["immutable"] = true
		meta := deploymentplan.Map(old.Object["metadata"])
		meta["uid"] = fmt.Sprintf("%08d-5555-4555-8555-555555555555", index+1)
		meta["labels"] = deploymentplan.Object{"rtk.cloud/bootstrap-session": oldSID}
		// Kubernetes stores data, whereas producers provide stringData.
		data := deploymentplan.Object{}
		for name, value := range deploymentplan.Map(old.Object["stringData"]) {
			data[name] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
		}
		delete(old.Object, "stringData")
		old.Object["data"] = data
		ceremony.CredentialUIDs[ns] = deploymentplan.String(meta["uid"])
		backend.objects[deploymentplan.ID(old.Object)] = old.Object
		d := environmentCreateSecret(ns, "service-bootstrap-"+nextSID, map[string]string{"tls.crt": newCert, "tls.key": keyPEM, "ca.crt": ceremony.Root.Certificate})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "rtk.cloud/bootstrap-session": nextSID}
		defs = append(defs, d)
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("c", 64), ReleaseSHA256: strings.Repeat("d", 64), BindingsSHA256: environmentCreateStageBindingsSHA("service-credential-secrets-renewal-"+late.Review, defs)}
	plan, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return backend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
	stage := "service-credential-secrets-renewal-" + late.Review
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, backend, qualify, fence, accept, e.Check, io.Discard); err != nil {
		t.Fatal(err)
	}
	_, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+stage)
	if err != nil {
		t.Fatal(err)
	}
	observed := createServiceSessionObserved{Reader: "original-custody", UIDs: map[string]string{}, Cleanup: ceremony.CleanupBinding}
	observed.Cleanup.SessionID = nextSID
	observed.Cleanup.ConfigMapName = "pki-bootstrap-cleanup-binding-" + nextSID[:12]
	for _, resource := range plan.Resources {
		ns := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["namespace"])
		observed.UIDs[ns] = journal.Owners[resource.ID]
	}
	observed.Cleanup.SecretUID = observed.UIDs[ceremony.BootstrapCredentialNamespace]
	observed.Session = mustProviderRecoveryJSON(managedSessionState{Session: nextSID, Issuer: ceremony.Intermediate.ID, Root: ceremony.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(newLeaf.Raw), Status: "active", Expires: newLeaf.NotAfter, Subjects: slices.Clone(subjects)})
	return &lateCredentialExecutionContract{store, e, late, plan, journal, observed, backend.objects, newCert}
}

func TestManagedLateAllocatedCredentialExecutionContract(t *testing.T) {
	f := newLateCredentialExecutionContract(t)
	if err := validateManagedLateCredentialChild(f.late, f.plan); err != nil {
		t.Fatal("recipe", err)
	}
	if err := validateManagedLateAllocatedCredentialProof(deploymentConfig{Environment: "dev"}, f.late, f.plan, f.journal, f.observed, time.Now().UTC()); err != nil {
		t.Fatal("actual allocation", err)
	}
	backend, _, _ := lateNativeReadContractBackend(t, f.store, f.objects)
	children := map[string]managedLateCurrentChild{"kubernetes/service-credential-secrets-renewal-" + f.late.Review: {Plan: f.plan, Journal: f.journal}}
	before := deploymentplan.Digest([]any{f.e.Journal, f.late, f.observed})
	if err := verifyManagedLateCurrentCredentialCopies(t.Context(), deploymentConfig{Environment: "dev"}, f.store, f.e, backend, f.late, children); err != nil {
		t.Fatal("original expired and new copies", err)
	}
	var state managedSessionState
	if err := decodeEnvironmentJSON(f.observed.Session, &state); err != nil {
		t.Fatal(err)
	}
	if !managedLateNewSessionCurrent(state, f.late, f.certificate, state.Subjects, time.Now().UTC()) {
		t.Fatal("actual newly signed session refused")
	}
	state.Installed = []string{state.Subjects[0], state.Subjects[0]}
	if managedLateNewSessionCurrent(state, f.late, f.certificate, state.Subjects, time.Now().UTC()) {
		t.Fatal("duplicated ACK accepted")
	}
	if deploymentplan.Digest([]any{f.e.Journal, f.late, f.observed}) != before {
		t.Fatal("verification mutated original authority")
	}
}

func TestManagedLateAllocatedCredentialExecutionRejectsTampering(t *testing.T) {
	for _, mode := range []string{"reader", "cleanup-owner", "session-fingerprint", "session-expiry", "allocated-owner", "duplicate-namespace", "mutable-secret", "different-copy", "different-key", "foreign-root"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateCredentialExecutionContract(t)
			switch mode {
			case "reader":
				f.observed.Reader = "foreign"
			case "cleanup-owner":
				f.observed.Cleanup.SecretUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "session-fingerprint":
				var s managedSessionState
				_ = decodeEnvironmentJSON(f.observed.Session, &s)
				s.Fingerprint = strings.Repeat("f", 64)
				f.observed.Session = mustProviderRecoveryJSON(s)
			case "session-expiry":
				var s managedSessionState
				_ = decodeEnvironmentJSON(f.observed.Session, &s)
				s.Expires = s.Expires.Add(time.Minute)
				f.observed.Session = mustProviderRecoveryJSON(s)
			case "allocated-owner":
				f.journal.Owners[f.plan.Resources[2].ID] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "duplicate-namespace":
				f.plan.Resources[2] = f.plan.Resources[0]
			case "mutable-secret":
				f.plan.Resources[2].Desired["immutable"] = false
			case "different-copy":
				deploymentplan.Map(f.plan.Resources[2].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(f.certificate + "\n"))
			case "different-key":
				for i := range f.plan.Resources {
					deploymentplan.Map(f.plan.Resources[i].Desired["data"])["tls.key"] = base64.StdEncoding.EncodeToString([]byte("synthetic-unrelated-key"))
				}
			case "foreign-root":
				for i := range f.plan.Resources {
					deploymentplan.Map(f.plan.Resources[i].Desired["data"])["ca.crt"] = base64.StdEncoding.EncodeToString([]byte("synthetic-unrelated-root"))
				}
			}
			if err := validateManagedLateAllocatedCredentialProof(deploymentConfig{Environment: "dev"}, f.late, f.plan, f.journal, f.observed, time.Now().UTC()); err == nil {
				t.Fatal("changed credential authority accepted")
			}
		})
	}
}

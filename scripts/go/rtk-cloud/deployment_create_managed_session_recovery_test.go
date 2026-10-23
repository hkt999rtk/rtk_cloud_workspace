package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedExpiredSeedRequiresOriginalNativeAllowlist(t *testing.T) {
	now := time.Now().UTC()
	s := environmentCreateServiceCeremony{BootstrapSessionID: "33333333-3333-4333-8333-333333333333", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}}
	subjects := []string{"service:account-manager", "service:openbao"}
	valid := managedSessionState{Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Root: s.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(-time.Second), Subjects: slices.Clone(subjects), Installed: []string{subjects[0]}}
	if !managedSessionMayRenew(valid, s, subjects, now) {
		t.Fatal("native partial installation after genuine expiry rejected")
	}
	for _, mode := range []string{"valid-expired", "not-expired", "missing-time", "foreign-session", "foreign-root", "foreign-issuer", "bad-fingerprint", "sealed", "foreign-subject", "foreign-installation", "duplicate-installation", "other-active"} {
		t.Run(mode, func(t *testing.T) {
			state := valid
			switch mode {
			case "valid-expired":
				state.Status = "expired"
			case "not-expired":
				state.Expires = now.Add(time.Second)
			case "missing-time":
				state.Expires = time.Time{}
			case "foreign-session":
				state.Session = "different"
			case "foreign-root":
				state.Root = "different"
			case "foreign-issuer":
				state.Issuer = "different"
			case "bad-fingerprint":
				state.Fingerprint = "different"
			case "sealed":
				state.Status = "sealed"
			case "foreign-subject":
				state.Subjects = []string{"service:foreign"}
			case "foreign-installation":
				state.Installed = []string{"service:foreign"}
			case "duplicate-installation":
				state.Installed = []string{subjects[0], subjects[0]}
			case "other-active":
				state.OtherActive = 1
			}
			if got := managedSessionMayRenew(state, s, subjects, now); got != (mode == "valid-expired") {
				t.Fatal("unsafe native seed renewal admission", mode, got)
			}
		})
	}
}

func TestManagedSeedRenewalKeepsOriginalKeyCSRClaimAndBoundTTL(t *testing.T) {
	parent, binding := strings.Repeat("a", 64), strings.Repeat("b", 64)
	old, newSession := "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	image := environmentReleaseImage{Reference: "ghcr.io/example/formal-repair@sha256:" + strings.Repeat("c", 64)}
	oldImage := "ghcr.io/example/original@sha256:" + strings.Repeat("d", 64)
	env := map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-older/bootstrap.crt", "PKI_BOOTSTRAP_SESSION_ID": old, "PKI_BOOTSTRAP_EXPIRES_AT": "old-absolute-expiry", "OPENBAO_KUBERNETES_ROLE": "pki-service-bootstrap-dev"}
	object := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "original", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": oldImage, "env": environmentCreateEnv(env), "command": []any{"/app/serviceidentity-bootstrap", "bootstrap"}}}, "initContainers": []any{deploymentplan.Object{"name": "permissions"}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-bootstrap-custody"}}}}}}}
	before := deploymentplan.Digest(object)
	definition, err := managedSessionRenewalDefinition(object, parent, binding, newSession, image)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(definition.Object)
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	got := createRecipeEnv(c)
	if got["PKI_BOOTSTRAP_KEY_PATH"] != env["PKI_BOOTSTRAP_KEY_PATH"] || got["PKI_BOOTSTRAP_CSR_PATH"] != env["PKI_BOOTSTRAP_CSR_PATH"] || got["PKI_BOOTSTRAP_CERT_PATH"] != "/run/pki-bootstrap/renew-"+binding[:12]+"/bootstrap.crt" || got["PKI_BOOTSTRAP_TTL"] != "8h" || got["PKI_BOOTSTRAP_EXPIRES_AT"] != "" || got["PKI_BOOTSTRAP_SESSION_ID"] != newSession || c["image"] != image.Reference || deploymentplan.Digest(pod["volumes"]) != deploymentplan.Digest(environmentPKIPod(object)["volumes"]) || deploymentplan.Digest(object) != before {
		t.Fatal("managed seed renewal replaced original custody or lost exact lifetime/image")
	}
	for _, mode := range []string{"missing-key", "bad-image", "missing-container", "foreign-job"} {
		t.Run(mode, func(t *testing.T) {
			bad := deploymentplan.Clone(object)
			selected := image
			switch mode {
			case "missing-key":
				bc := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bad)["containers"])[0])
				bc["env"] = environmentCreateServiceWithoutEnv(bc["env"], "PKI_BOOTSTRAP_KEY_PATH")
			case "bad-image":
				selected.Reference = "mutable:latest"
			case "missing-container":
				environmentPKIPod(bad)["containers"] = []any{}
			case "foreign-job":
				bad["kind"] = "Deployment"
			}
			if _, err := managedSessionRenewalDefinition(bad, parent, binding, newSession, selected); err == nil {
				t.Fatal("unqualified renewal recipe accepted")
			}
		})
	}
}

func managedSessionClientFixture(t *testing.T) (environmentCreateServiceCeremony, []string, []managedSessionClient, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original-intermediate"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	service := environmentCreateServiceCeremony{Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}}
	subjects := []string{"service:account-manager", "service:certissuer", "service:emqx-pki", "service:factory-enroll", "service:openbao", "service:pki-controller", "service:pkibroker", "service:video-cloud-api", "service:video-cloud-logingester"}
	var rows []managedSessionClient
	for i, subject := range append(slices.Clone(subjects), "service:video-cloud-api", "service:video-cloud-api") {
		clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, clientKey)
		if err != nil {
			t.Fatal(err)
		}
		leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, ca, &clientKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, managedSessionClient{Subject: subject, Caller: "service:deployment-bootstrap", Request: subject + string(rune('a'+i)), Issuer: service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leafDER), Status: "succeeded", CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))})
	}
	return service, subjects, rows, now
}

func TestManagedSeedRequiresEveryOriginalClientWithoutIssuanceReplay(t *testing.T) {
	service, subjects, rows, now := managedSessionClientFixture(t)
	if !validateManagedSessionClients(rows, service, subjects, now) {
		t.Fatal("original eleven native clients rejected")
	}
	for _, mode := range []string{"missing-owner", "extra-owner", "duplicate-fingerprint", "foreign-issuer", "revoked", "expired", "changed-csr", "changed-fingerprint", "partial-issuance", "foreign-subject", "duplicate-subject-policy"} {
		t.Run(mode, func(t *testing.T) {
			clients := slices.Clone(rows)
			policy := slices.Clone(subjects)
			at := now
			switch mode {
			case "missing-owner":
				clients = clients[1:]
			case "extra-owner":
				clients = append(clients, clients[0])
			case "duplicate-fingerprint":
				clients[0].Fingerprint = clients[1].Fingerprint
			case "foreign-issuer":
				clients[0].Issuer = "another"
			case "revoked":
				clients[0].Revoked = &now
			case "expired":
				at = now.Add(2 * time.Hour)
			case "changed-csr":
				clients[0].CSR = clients[1].CSR
			case "changed-fingerprint":
				clients[0].Fingerprint = strings.Repeat("a", 64)
			case "partial-issuance":
				clients[0].Status = "issuing"
			case "foreign-subject":
				clients[0].Subject = "service:foreign"
			case "duplicate-subject-policy":
				policy = append(policy, policy[0])
			}
			if validateManagedSessionClients(clients, service, policy, at) {
				t.Fatal("unverified native identity inventory accepted", mode)
			}
		})
	}
}

func TestManagedClientContinuityAllowsNaturalExpiryAfterAdmission(t *testing.T) {
	service, subjects, rows, now := managedSessionClientFixture(t)
	afterExpiry := now.Add(2 * time.Hour)
	if validateManagedSessionClients(rows, service, subjects, afterExpiry) {
		t.Fatal("initial admission accepted expired workload clients")
	}
	if !validateManagedSessionClientsPreserved(rows, service, subjects) {
		t.Fatal("already admitted continuation rejected unchanged naturally expired clients")
	}
	changed := slices.Clone(rows)
	changed[0].Revoked = &afterExpiry
	if validateManagedSessionClientsPreserved(changed, service, subjects) {
		t.Fatal("continuation accepted a revoked client")
	}
	changed = slices.Clone(rows)
	changed[0].CSR = changed[1].CSR
	if validateManagedSessionClientsPreserved(changed, service, subjects) {
		t.Fatal("continuation accepted changed client key custody")
	}
}

func TestManagedSeedExecutionRequiresItsQualificationBeforeAnyMutation(t *testing.T) {
	called := false
	_, err := executeManagedSessionRecovery(context.Background(), deploymentConfig{}, environmentCreateBootstrapPlan{}, makeIsolatedTestSecretStore(t, "dev"), environmentCreateBootstrapRuntime{ServiceSessionRecovery: &createServiceSessionRecovery{PostServiceRenewal: true}}, func(context.Context, string, []deploymentplan.Definition) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("unqualified managed seed renewal reached writes")
	}
}

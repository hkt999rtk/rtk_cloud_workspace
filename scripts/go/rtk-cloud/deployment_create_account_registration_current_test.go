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
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func currentRegistrationMaterial(t *testing.T, f *productProtocolFixture) (environmentCreateServiceCeremony, string, string) {
	t.Helper()
	service := environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next}
	now := time.Now().UTC()
	expired, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-2 * time.Hour), NextUpdate: now.Add(-time.Hour)}, f.root, f.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	service.RootCRLPEM = productPEM("X509 CRL", expired)
	name := "account-manager." + f.p.Stack + "-account-manager.svc.cluster.local"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1001), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, f.ica, &key.PublicKey, f.icaKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return service, productPEM("CERTIFICATE", der) + f.next.Certificate + f.l.Root.Certificate, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
}

func TestCurrentAccountRegistrationUsesNativeCurrentRootWithoutRewritingCeremony(t *testing.T) {
	_, _, _, _, _, _, core := accountRegistrationFixture(t)
	f := newProductProtocolFixtureForRegistration(t, false, 24*time.Hour, true)
	cfg := f.cfg
	cfg.Values = appendMap(cfg.Values, map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED": "true", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT": "9444"})
	service, certificate, key := currentRegistrationMaterial(t, f)
	before := deploymentplan.Digest(service)
	definitions, err := renderEnvironmentCreateCurrentAccountRegistration(t.Context(), cfg, f.p, f.runtime, service, certificate, key, core)
	if err != nil {
		t.Fatal("expired historical Root CRL must not block current native registration", err)
	}
	secret := createRecipeObject(t, definitions, "Secret/"+f.p.Stack+"-account-manager/account-manager-service-registration-tls")
	data := deploymentplan.Map(secret["stringData"])
	if data["tls.crt"] != certificate || data["tls.key"] != key || data["client-ca.crt"] != service.Root.Certificate || strings.Contains(deploymentplan.String(data["client.crl"]), strings.TrimSpace(service.RootCRLPEM)) {
		t.Fatal("registration replaced original credential/Root or reused expired snapshot")
	}
	if deploymentplan.Digest(service) != before {
		t.Fatal("current public CRL rewrote immutable ceremony")
	}
}

func TestFreshFourteenRegistrationTailUsesCurrentCRLsAfterClosedIssuerAndRetiredExporter(t *testing.T) {
	runFreshFourteenProductProtocol(t, false, func(t *testing.T, f *productProtocolFixture, service environmentCreateServiceCeremony, core []deploymentplan.Definition) {
		t.Helper()
		if f.cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true" {
			t.Fatal("registration not part of original fixture configuration")
		}
		program, err := readFreshProductProgram(f.store, f.e)
		if err != nil {
			t.Fatal("existing five-output program", err)
		}
		installed, err := readProductInstalled(f.store, &program.Lifecycle)
		if err != nil || len(installed.Outputs) != 5 {
			t.Fatal("actual five outputs missing", err)
		}
		if err := verifyProductIdentityOutputs(t.Context(), f.cfg, f.runtime, &program.Lifecycle, installed); err != nil {
			t.Fatal(err)
		}
		if entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/fresh-product-enrollment-closed"); entry == nil || entry.State != "completed" {
			t.Fatal("issuer was not genuinely closed")
		}
		old, certificate, key := currentRegistrationMaterial(t, f)
		service.RootCRLPEM = old.RootCRLPEM
		originalCeremony := deploymentplan.Digest(service)
		material := environmentCreateOwnedServerMaterial{Environment: f.cfg.Environment, Root: service.Root.RootFingerprint, Name: "account-manager." + f.p.Stack + "-account-manager.svc.cluster.local", Certificate: certificate, Key: key}
		originalBoundary := f.nativeBoundary
		defer func() { f.nativeBoundary = originalBoundary }()
		boundary := installCurrentAccountRegistrationExportCheckpoint(t, f.cfg, f.p, f.runtime, service, material, core, f.apply, f.b.objects, func(h func([]string, []byte) ([]byte, int, bool)) { f.nativeBoundary = h })
		readerID := "Deployment/" + f.p.Stack + "-account-manager/pki-export-account-manager-" + f.e.Journal.PlanSHA256[:12]
		beforeExport := boundary.execs
		checkedApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
			if original := f.plans[stage]; original != nil && original.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, defs) {
				return environmentCreateBootstrapBlocker("registration retry changed its frozen original native input")
			}
			return f.apply(ctx, stage, defs)
		}
		if err := configureEnvironmentCreateAccountRegistration(t.Context(), f.cfg, f.p, f.runtime, service, nil, core, checkedApply); err != nil {
			t.Fatal("actual enabled registration tail after original five products", err)
		}
		if boundary.execs != beforeExport+1 || f.b.objects[readerID] != nil || deploymentplan.Digest(service) != originalCeremony {
			t.Fatal("tail replayed retired reader or rewrote historical ceremony")
		}
		registration, _, err := acceptedCreateStage(f.store, f.e, "kubernetes/account-manager-registration")
		if err != nil {
			t.Fatal("registration not genuinely accepted", err)
		}
		certIssuerID := "Deployment/" + f.p.Stack + "-video-cloud/certissuer"
		for _, r := range registration.Resources {
			if r.ID == certIssuerID && (r.Action != "retain" || mqttFoundationEnv(managedNamedField(environmentPKIPod(r.Desired)["containers"], "certissuer"), "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID") != "") {
				t.Fatal("registration reopened closed issuer")
			}
		}
		if err := configureEnvironmentCreateAccountRegistration(t.Context(), f.cfg, f.p, f.runtime, service, nil, core, checkedApply); err != nil {
			t.Fatal("completed registration checkpoint did not reenter", err)
		}
		if f.b.objects[readerID] != nil {
			t.Fatal("completed registration resurrected exporter")
		}
		// A later admitted native session updates this same permanent workload.
		// Its completed registration remains immutable history on the next retry.
		accountID := "Deployment/" + f.p.Stack + "-account-manager/account-manager"
		advanced := deploymentplan.Clone(f.b.objects[accountID])
		app := managedNamedField(environmentPKIPod(advanced)["containers"], "app")
		app["env"] = mergeEnvironmentNamedFields(app["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": "99999999-9999-4999-8999-999999999999"}))
		if err := f.apply(t.Context(), "service-session-forward-registration-retry", []deploymentplan.Definition{{Object: advanced, Phase: "workload"}}); err != nil {
			t.Fatal(err)
		}
		writesBefore := deploymentplan.Digest(f.b.writes)
		if err := configureEnvironmentCreateAccountRegistration(t.Context(), f.cfg, f.p, f.runtime, service, nil, core, checkedApply); err != nil {
			t.Fatal("completed registration after authenticated same-owner successor", err)
		}
		if deploymentplan.Digest(f.b.writes) != writesBefore || f.b.objects[readerID] != nil {
			t.Fatal("completed registration replayed writes or exporter after successor")
		}
		for _, mode := range []string{"foreign-uid", "unrecorded-body"} {
			original := deploymentplan.Clone(f.b.objects[accountID])
			if mode == "foreign-uid" {
				deploymentplan.Map(f.b.objects[accountID]["metadata"])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			} else {
				managedNamedField(environmentPKIPod(f.b.objects[accountID])["containers"], "app")["image"] = "unrecorded-image"
			}
			err := configureEnvironmentCreateAccountRegistration(t.Context(), f.cfg, f.p, f.runtime, service, nil, core, checkedApply)
			f.b.objects[accountID] = original
			if err == nil {
				t.Fatal("completed registration accepted unrecorded current identity", mode)
			}
		}
	})
}

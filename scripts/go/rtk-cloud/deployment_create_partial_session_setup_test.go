package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPartialSessionRenewalPreservesManagedTrustOwners(t *testing.T) {
	id := "33333333-3333-4333-8333-333333333333"
	service := environmentCreateServiceCeremony{BootstrapSessionID: id, BootstrapCredentialSecret: "service-bootstrap-" + id, BootstrapCredentialNamespaces: []string{"video", "account", "secrets"}}
	var definitions []deploymentplan.Definition
	for _, ns := range service.BootstrapCredentialNamespaces {
		d := environmentCreateSecret(ns, service.BootstrapCredentialSecret, map[string]string{"tls.crt": "original"})
		d.Object["immutable"] = true
		definitions = append(definitions, d, environmentCreateSecret(ns, "pki-verifier-database", map[string]string{"password": "original"}), deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-service-trust"}}})
	}
	selected, err := partialSessionCredentialDefinitions(definitions, service)
	if err != nil || len(selected) != len(service.BootstrapCredentialNamespaces) {
		t.Fatal("did not retain only the three successor credentials", err)
	}
	for _, d := range selected {
		if d.Object["kind"] != "Secret" || deploymentplan.Map(d.Object["metadata"])["name"] != service.BootstrapCredentialSecret {
			t.Fatal("renewal would modify a managed trust owner")
		}
	}
	missing := slices.DeleteFunc(slices.Clone(definitions), func(d deploymentplan.Definition) bool {
		return deploymentplan.Map(d.Object["metadata"])["name"] == service.BootstrapCredentialSecret && deploymentplan.Map(d.Object["metadata"])["namespace"] == "account"
	})
	if _, err := partialSessionCredentialDefinitions(missing, service); err == nil {
		t.Fatal("missing original namespace accepted")
	}
}

func TestPartialSessionCustodyRequiresOriginalKeyCSRAndIssuer(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(-time.Minute), KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, key)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), caPEM...)
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	service := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{Certificate: string(caPEM), RootFingerprint: environmentPreflightBytesSHA256(caDER)}, Intermediate: environmentCreateRegisteredIssuer{Certificate: string(caPEM)}}
	state := managedSessionState{Fingerprint: environmentPreflightBytesSHA256(leafDER), Expires: leaf.NotAfter}
	if !partialSessionCustodyValid(keyPEM, certPEM, caPEM, csrPEM, service, state, now) {
		t.Fatal("valid original custody rejected")
	}
	renewedTemplate := *leaf
	renewedTemplate.SerialNumber = big.NewInt(3)
	renewedTemplate.NotAfter = now.Add(time.Hour)
	renewedDER, err := x509.CreateCertificate(rand.Reader, &renewedTemplate, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	renewedCert := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: renewedDER}), caPEM...)
	renewedState := managedSessionState{Fingerprint: environmentPreflightBytesSHA256(renewedDER), Expires: renewedTemplate.NotAfter}
	if !partialSessionSuccessorCustodyValid(keyPEM, renewedCert, caPEM, csrPEM, service, renewedState, now) || partialSessionCustodyValid(keyPEM, renewedCert, caPEM, csrPEM, service, renewedState, now) || partialSessionSuccessorCustodyValid(keyPEM, certPEM, caPEM, csrPEM, service, state, now) {
		t.Fatal("original and successor custody lifetimes were confused")
	}
	foreignCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: otherCSR})
	if partialSessionSuccessorCustodyValid(keyPEM, renewedCert, caPEM, foreignCSR, service, renewedState, now) {
		t.Fatal("successor accepted a different private key CSR")
	}
	for _, mode := range []string{"key", "csr", "different-valid-csr", "root", "fingerprint", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			k, c, root, csr, s := keyPEM, certPEM, caPEM, csrPEM, state
			switch mode {
			case "key":
				k = []byte("foreign")
			case "csr":
				csr = []byte("foreign")
			case "different-valid-csr":
				csr = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: otherCSR})
			case "root":
				root = []byte("foreign")
			case "fingerprint":
				s.Fingerprint = "foreign"
			case "expiry":
				s.Expires = now.Add(time.Hour)
			}
			if partialSessionCustodyValid(k, c, root, csr, service, s, now) {
				t.Fatal("invalid custody accepted")
			}
		})
	}
}

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

func TestManagedCoreOriginalHostDigestRejectsValidReissuedCertificateAndCSR(t *testing.T) {
	now := time.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original-host-authority"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	session := "e0a93344-3c64-4a00-87d5-dc57a4e0d48b"
	keys := []*ecdsa.PrivateKey{}
	rows := []managedTransportServerEvidence{}
	issue := func(domain, role string, key *ecdsa.PrivateKey, serial int64) managedTransportServerEvidence {
		t.Helper()
		dns := role + ".video-cloud-dev-video-cloud.svc"
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}}, key)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return managedTransportServerEvidence{Domain: domain, Caller: "service:" + role, Request: session + ":" + role + ":server", Issuer: "11111111-1111-4111-8111-111111111111", Status: "succeeded", Fingerprint: environmentPreflightBytesSHA256(der), CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), DNS: []string{dns}}
	}
	for index, domain := range []string{"mqtt", "openbao_tls"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		role := []string{"emqx-pki", "openbao"}[index]
		rows = append(rows, issue(domain, role, key, int64(index+10)))
	}
	original := deploymentplan.Digest(rows)
	for _, mode := range []string{"original", "valid-new-certificate-same-key", "valid-new-csr-and-key", "changed-csr-pem", "changed-certificate-pem", "missing-source", "missing-current"} {
		t.Run(mode, func(t *testing.T) {
			current := slices.Clone(rows)
			expected := original
			switch mode {
			case "valid-new-certificate-same-key":
				current[0] = issue("mqtt", "emqx-pki", keys[0], 110)
				current[0].CSR = rows[0].CSR // same original CSR/key, different valid leaf
			case "valid-new-csr-and-key":
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				current[1] = issue("openbao_tls", "openbao", key, 120)
			case "changed-csr-pem":
				current[0].CSR += "\n"
			case "changed-certificate-pem":
				current[0].Certificate += "\n"
			case "missing-source":
				expected = ""
			}
			// Every alternative still passes genuine direct signature, DNS, e0 request,
			// fingerprint and signed CSR/SPKI checks. The immutable original pin is the
			// independent fence that rejects a valid replacement issuance.
			for index, row := range current {
				leaf, err := parseFirstCertificatePEM([]byte(row.Certificate))
				if err != nil || leaf.CheckSignatureFrom(ca) != nil || leaf.Subject.CommonName != row.DNS[0] || !slices.Equal(leaf.DNSNames, row.DNS) || row.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || row.Request != rows[index].Request || row.Issuer != rows[index].Issuer {
					t.Fatal("invalid fixture leaf/request", mode, err)
				}
				block, _ := pem.Decode([]byte(row.CSR))
				if block == nil {
					t.Fatal("invalid fixture CSR")
				}
				csr, err := x509.ParseCertificateRequest(block.Bytes)
				if err != nil || csr.CheckSignature() != nil || !slices.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
					t.Fatal("invalid fixture CSR/signature", mode, err)
				}
			}
			observed := deploymentplan.Digest(current)
			if mode == "missing-current" {
				observed = ""
			}
			if (validateManagedCoreOriginalHostServerDigest(observed, expected) == nil) != (mode == "original") {
				t.Fatal("accepted changed original native host bytes", mode)
			}
			partial := &createManagedTransportPartialRecovery{} // pure derivation leaves ServerSHA empty
			materials := map[string]string{"accepted-service-core-managed-application-current-servers": expected}
			err := bindManagedCoreOriginalHostServerPin(partial, materials, observed)
			if (err == nil) != (mode == "original") {
				t.Fatal("hydration accepted changed native identity", mode, err)
			}
			if mode == "original" && partial.ServerSHA != original {
				t.Fatal("did not hydrate authentic original pin")
			}
			if mode != "original" && partial.ServerSHA != "" {
				t.Fatal("fresh changed bytes became original authority")
			}
		})
	}
}

func TestManagedCoreHostPinHydrationRequiresHistoricalMaterial(t *testing.T) {
	original := deploymentplan.Digest("original two host rows")
	changed := deploymentplan.Digest("changed but valid current host rows")
	for _, test := range []struct {
		name, existing, source, current string
		pass                            bool
	}{
		{"pure-empty-runtime", "", original, original, true},
		{"same-retained-runtime", original, original, original, true},
		{"missing-original-admission", "", "", original, false},
		{"fresh-self-pin-denied", "", "", changed, false},
		{"changed-current-identity", "", original, changed, false},
		{"runtime-conflicts-original", changed, original, original, false},
		{"missing-native-read", "", original, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			partial := &createManagedTransportPartialRecovery{ServerSHA: test.existing}
			err := bindManagedCoreOriginalHostServerPin(partial, map[string]string{"accepted-service-core-managed-application-current-servers": test.source}, test.current)
			if (err == nil) != test.pass {
				t.Fatal("wrong historical/native binding", err)
			}
			if test.pass && partial.ServerSHA != original {
				t.Fatal("source original not retained")
			}
			if !test.pass && partial.ServerSHA != test.existing {
				t.Fatal("failed verification mutated original pin")
			}
		})
	}
}

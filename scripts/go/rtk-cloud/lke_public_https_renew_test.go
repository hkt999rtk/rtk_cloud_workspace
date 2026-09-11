package main

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLKEPublicHTTPSRenewalLeafAndValidation(t *testing.T) {
	caCert, caKey, _, _, err := newLKECertificateAuthority("test-public-renewal-ca", "ed25519")
	if err != nil {
		t.Fatal(err)
	}
	hosts := []string{"api.example.test", "device.example.test"}
	certPEM, keyPEM, err := newLKESignedCertificate(caCert, caKey, hosts[0], hosts, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if err := lkeValidatePublicHTTPSCertificate(certPEM, keyPEM, hosts); err != nil {
		t.Fatalf("valid public TLS pair rejected: %v", err)
	}
	if err := lkeValidatePublicHTTPSCertificate(certPEM, keyPEM, []string{"missing.example.test"}); err == nil {
		t.Fatal("certificate without required hostname was accepted")
	}
	leaf, err := lkePublicHTTPSLeafFromPEM([]byte(certPEM))
	if err != nil {
		t.Fatal(err)
	}
	if leaf.SHA256 == "" || leaf.PublicKeySHA256 == "" || leaf.Serial == "" {
		t.Fatalf("public leaf metadata incomplete: %+v", leaf)
	}
	if len(leaf.DNSNames) != len(hosts) || leaf.NotAfter == "" {
		t.Fatalf("public leaf metadata missing SANs or validity: %+v", leaf)
	}
}

func TestLKEPublicHTTPSRenewalEvidenceIsPublicOnly(t *testing.T) {
	output := filepath.Join(t.TempDir(), "renewal")
	evidence := lkePublicHTTPSRenewalEvidence{
		CompletedAt:          time.Now().UTC().Format(time.RFC3339),
		Environment:          "dev",
		Stack:                "video-cloud-dev",
		Issuer:               "ACME DNS-01 via workspace certbot owner",
		IngressNamespace:     "video-cloud-dev-ingress",
		TLSSecret:            "video-cloud-dev-public-tls",
		Hosts:                []string{"api.example.test"},
		OldSecretVersion:     "100",
		NewSecretVersion:     "101",
		OldLeaf:              lkePublicHTTPSLeaf{SHA256: "old"},
		NewLeaf:              lkePublicHTTPSLeaf{SHA256: "new"},
		InstalledFingerprint: "new",
	}
	if err := lkeWritePublicHTTPSRenewalEvidence(output, evidence); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(output, "renewal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "PRIVATE") || strings.Contains(string(body), "tls.key") {
		t.Fatalf("renewal evidence contains private material: %s", body)
	}
	if err := lkeWritePublicHTTPSRenewalEvidence(output, evidence); err == nil {
		t.Fatal("existing evidence directory was overwritten")
	}
}

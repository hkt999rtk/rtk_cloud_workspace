package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestFactoryClientCertificateLifecycle(t *testing.T) {
	configRoot := t.TempDir()
	dir := filepath.Join(configRoot, "dev", "pki", "factory-client-ca")
	if err := run([]string{"init", "--environment", "dev", "--config-root", configRoot}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init", "--environment", "dev", "--config-root", configRoot}); err == nil {
		t.Fatal("CA was replaced")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "fixture"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPath := filepath.Join(configRoot, "factory.csr")
	if err := os.WriteFile(csrPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(configRoot, "factory.crt")
	cloudID := "1b276960-4e2b-4d64-9422-c2dba64107e6"
	if err := run([]string{"sign", "--environment", "dev", "--config-root", configRoot, "--csr", csrPath, "--cloud-id", cloudID, "--factory-id", "line-a", "--out", certPath}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://rtk-cloud/factory/"+cloudID+"/line-a" {
		t.Fatalf("wrong factory identity: %v", cert.URIs)
	}
	ca, _, err := loadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: rootPool(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"revoke", "--environment", "dev", "--config-root", configRoot, "--serial", cert.SerialNumber.Text(16)}); err != nil {
		t.Fatal(err)
	}
	crlRaw, err := os.ReadFile(filepath.Join(dir, "ca.crl"))
	if err != nil {
		t.Fatal(err)
	}
	crlBlock, _ := pem.Decode(crlRaw)
	crl, err := x509.ParseRevocationList(crlBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatalf("revocation missing: %#v", crl.RevokedCertificateEntries)
	}
}

func rootPool(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCRLMaintenanceCustodyOriginalEncryptedKeyAndMissingState(t *testing.T) {
	item, key, _ := maintenanceFixture(t, "staging", time.Now().UTC().Truncate(time.Second).Add(time.Hour))
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore(root, "staging")
	item.Issuer.Reference = "operator:synthetic-original"
	paths := map[string]string{"key": "pki/custody/roots/" + item.Issuer.ID + "/ca-key.encrypted.pem", "certificate": "pki/custody/roots/" + item.Issuer.ID + "/certificate.pem", "csr": "pki/custody/roots/" + item.Issuer.ID + "/csr.pem", "passphrase": "pki/custody/service-root-v1-key-passphrase"}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "isolated-plain-test-key.pem")
	if err := os.WriteFile(plain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(plain)
	if err := store.write(paths["passphrase"], []byte("isolated synthetic passphrase"), false); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(store.Root, paths["key"])
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("openssl", "pkcs8", "-topk8", "-v2", "aes-256-cbc", "-in", plain, "-out", keyPath, "-passout", "file:"+filepath.Join(store.Root, paths["passphrase"]))
	if err := command.Run(); err != nil {
		t.Fatal("isolated encrypted PKCS8 fixture could not be generated")
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "original synthetic root"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string][]byte{paths["certificate"]: []byte(item.Issuer.Certificate), paths["csr"]: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), "operator/env/PKI_OPERATOR_USER_ID": []byte("synthetic-user"), "operator/env/PKI_OPERATOR_SIGNER_REF": []byte(item.Issuer.Reference)} {
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	hashes := map[string]string{}
	for name, path := range paths {
		raw, err := os.ReadFile(filepath.Join(store.Root, path))
		if err != nil {
			t.Fatal(err)
		}
		hashes[name] = environmentPreflightBytesSHA256(raw)
	}
	manifest := maintenanceCustody{Schema: "rtk-root-custody/v1", Environment: "staging", Domain: "service", Issuer: item.Issuer.ID, Fingerprint: item.Issuer.RootFingerprint, Operator: "synthetic-user", Reference: item.Issuer.Reference, KeyFormat: "encrypted-pkcs8-pem", Paths: paths, Hashes: hashes}
	manifestPath := "pki/custody/roots/" + item.Issuer.ID + "/manifest.json"
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(keyPath)
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(before, after) {
		t.Fatal("verification rewrote original key")
	}
	manifest.Environment = "prod"
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
		t.Fatal("foreign custody accepted")
	}
	manifest.Environment = "staging"
	_ = maintenanceWrite(store, manifestPath, manifest)
	if err := os.WriteFile(keyPath, append(bytes.Clone(before), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
		t.Fatal("changed encrypted key accepted")
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
		t.Fatal("missing original key accepted")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("missing identity was regenerated")
	}
	certificate, err := os.ReadFile(filepath.Join(store.Root, paths["certificate"]))
	if err != nil || string(certificate) != item.Issuer.Certificate {
		t.Fatal("original root certificate changed")
	}
}

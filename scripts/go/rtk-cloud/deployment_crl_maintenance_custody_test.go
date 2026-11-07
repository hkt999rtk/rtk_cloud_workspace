package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceCustodyFixture(t *testing.T) (secretStore, crlMaintenanceIssuer, *ecdsa.PrivateKey, maintenanceCustody) {
	t.Helper()
	item, key, _ := maintenanceFixture(t, "staging", time.Now().UTC().Truncate(time.Second).Add(time.Hour))
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore(root, "staging")
	item.Issuer.Reference = "operator:synthetic-original"
	item.Issuer.Document["created_by"] = "22222222-2222-4222-8222-222222222222"
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
	for path, raw := range map[string][]byte{paths["certificate"]: []byte(item.Issuer.Certificate), paths["csr"]: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), "operator/env/PKI_OPERATOR_USER_ID": []byte("22222222-2222-4222-8222-222222222222"), "operator/env/PKI_OPERATOR_SIGNER_REF": []byte(item.Issuer.Reference)} {
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
	manifest := maintenanceCustody{Schema: "rtk-root-custody/v1", Environment: "staging", Domain: "service", Issuer: item.Issuer.ID, Fingerprint: item.Issuer.RootFingerprint, Operator: "22222222-2222-4222-8222-222222222222", Reference: item.Issuer.Reference, KeyFormat: "encrypted-pkcs8-pem", Paths: paths, Hashes: hashes}
	manifestPath := "pki/custody/roots/" + item.Issuer.ID + "/manifest.json"
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return store, item, key, manifest
}

func TestCRLMaintenanceCustodyOriginalEncryptedKeyAndMissingState(t *testing.T) {
	store, item, _, manifest := maintenanceCustodyFixture(t)
	paths := manifest.Paths
	manifestPath := "pki/custody/roots/" + item.Issuer.ID + "/manifest.json"
	keyPath := filepath.Join(store.Root, paths["key"])
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

func TestCRLMaintenanceSigningResumesPreservedAttempts(t *testing.T) {
	for _, mode := range []string{"empty", "empty-file", "partial", "complete", "wrong-request", "prolonged-outage"} {
		t.Run(mode, func(t *testing.T) {
			store, item, key, custody := maintenanceCustodyFixture(t)
			request, err := buildMaintenanceCRLRequest(store.Environment, item, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			signed := maintenanceSign(t, item.Issuer, key, request)
			candidate := "deployment/crl-maintenance/test-candidate"
			legacy := filepath.Join(store.Root, candidate, "signed")
			if err := os.MkdirAll(legacy, 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("-----BEGIN X509 CRL-----\ninterrupted")
			if mode == "complete" {
				original = []byte(signed.PEM)
			}
			if mode == "empty-file" {
				original = nil
			}
			if mode == "wrong-request" {
				original = []byte(item.Snapshot.Prior.PEM)
			}
			if mode != "empty" {
				if err := os.WriteFile(filepath.Join(legacy, "revocations.pem"), original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "prolonged-outage" {
				for i := 0; i < 129; i++ {
					if err := os.MkdirAll(filepath.Join(store.Root, candidate, "attempts", fmt.Sprintf("attempt-%03d", i), "signed"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			bin := t.TempDir()
			response := filepath.Join(bin, "response.pem")
			if err := os.WriteFile(response, []byte(signed.PEM), 0600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(bin, "calls")
			t.Setenv("CRL_TEST_ROOT", store.Root)
			t.Setenv("CRL_TEST_RESPONSE", response)
			t.Setenv("CRL_TEST_CALLS", calls)
			// Match the native ceremony's exclusive output directory and file creation.
			docker := `#!/usr/bin/env python3
import os,sys
args=sys.argv[1:]
out=args[args.index('--out')+1]
key=args[args.index('--key')+1]
assert key.startswith('/store/pki/custody/roots/')
assert '--expected-request-sha256' in args
out=os.environ['CRL_TEST_ROOT']+out[len('/store'):]
os.mkdir(out,0o700)
fd=os.open(out+'/revocations.pem', os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'wb') as f: f.write(open(os.environ['CRL_TEST_RESPONSE'],'rb').read())
with open(os.environ['CRL_TEST_CALLS'],'a') as f: f.write('signed\n')
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			image := "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)
			for retry := 0; retry < 2; retry++ {
				got, err := signMaintenanceCRL(context.Background(), store, image, item, request, candidate)
				if mode == "wrong-request" {
					if err == nil {
						t.Fatal("unrelated complete signed response accepted")
					}
					continue
				}
				if err != nil || got.Digest != signed.Digest {
					t.Fatalf("resume %d: digest=%s err=%v", retry, got.Digest, err)
				}
			}
			raw, _ := os.ReadFile(calls)
			wantCalls := 1
			if mode == "complete" || mode == "wrong-request" {
				wantCalls = 0
			}
			if strings.Count(string(raw), "signed") != wantCalls {
				t.Fatalf("native signing calls %q", raw)
			}
			if mode != "empty" {
				unchanged, err := os.ReadFile(filepath.Join(legacy, "revocations.pem"))
				if err != nil || !bytes.Equal(unchanged, original) {
					t.Fatal("original interrupted output overwritten")
				}
			}
			after, err := verifyMaintenanceCustody(store, item.Issuer)
			if err != nil || after.Hashes["key"] != custody.Hashes["key"] {
				t.Fatal("original encrypted key changed")
			}
		})
	}
}

func TestCRLMaintenanceCustodyUsesOriginalRegistryCreator(t *testing.T) {
	store, item, _, manifest := maintenanceCustodyFixture(t)
	original := "22222222-2222-4222-8222-222222222222"
	configured := "33333333-3333-4333-8333-333333333333"
	manifest.Operator = original
	item.Issuer.Document["created_by"] = original
	item.Issuer.Reference = "" // Offline issuer records deliberately have no provider mount.
	manifestPath := "pki/custody/roots/" + item.Issuer.ID + "/manifest.json"
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/PKI_OPERATOR_USER_ID", []byte(configured), true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Root, manifestPath))
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err != nil {
		t.Fatalf("original native creator rejected because bootstrap intent differs: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(store.Root, manifestPath))
	if !bytes.Equal(before, after) {
		t.Fatal("original custody manifest rewritten")
	}
	configuredAfter, _ := store.read("operator/env/PKI_OPERATOR_USER_ID")
	if configuredAfter != configured {
		t.Fatal("configured bootstrap identity rewritten")
	}
	for _, creator := range []string{"", configured, "malformed-principal"} {
		item.Issuer.Document["created_by"] = creator
		if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
			t.Fatalf("foreign or missing original creator accepted: %q", creator)
		}
	}
	manifest.Operator = "malformed-principal"
	item.Issuer.Document["created_by"] = manifest.Operator
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
		t.Fatal("malformed matching creator accepted")
	}
	manifest.Operator = original
	if err := maintenanceWrite(store, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	item.Issuer.Document["created_by"] = original
	if err := store.write("operator/env/PKI_OPERATOR_SIGNER_REF", []byte("offline:other-custody"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyMaintenanceCustody(store, item.Issuer); err == nil {
		t.Fatal("foreign selected custody signer accepted")
	}
}

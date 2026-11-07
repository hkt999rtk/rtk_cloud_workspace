package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"
)

func maintenanceFixture(t *testing.T, env string, expiry time.Time) (crlMaintenanceIssuer, *ecdsa.PrivateKey, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: env + " original root"}, NotBefore: now.Add(-30 * 24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3, 4}}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(der)
	root := environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111", Environment: env, Domain: "service", Kind: "root", Status: "active", Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), RootFingerprint: environmentPreflightBytesSHA256(der), Document: map[string]any{"signer_provider": "offline"}}
	root.ChainPEM = root.Certificate
	root.BundleVersion = environmentPreflightBytesSHA256([]byte(root.Certificate))
	root.Document = map[string]any{"issuer_id": root.ID, "environment": env, "trust_domain": "service", "kind": "root", "status": "active", "signer_provider": "offline", "certificate_pem": root.Certificate, "chain_pem": root.Certificate, "certificate_fingerprint_sha256": root.RootFingerprint, "trust_bundle_version": root.BundleVersion}
	prior := maintenanceSign(t, root, key, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "4", ThisUpdate: expiry.Add(-6 * 24 * time.Hour), NextUpdate: expiry, Entries: []createRootCRLRevocation{{Serial: "ab", RevokedAt: now.Add(-20 * 24 * time.Hour), Reason: 1}}})
	return crlMaintenanceIssuer{Issuer: root, Snapshot: createRootCRLNativeSnapshot{Prior: prior, Bindings: []createRootCRLRevocation{{Serial: "cd", RevokedAt: now.Add(-24 * time.Hour)}}}}, key, now
}

func maintenanceSign(t *testing.T, root environmentCreateRegisteredIssuer, key *ecdsa.PrivateKey, r createRootCRLRequest) createRootCRLRecord {
	t.Helper()
	ca, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	number, _ := new(big.Int).SetString(r.Number, 10)
	entries := []x509.RevocationListEntry{}
	for _, e := range r.Entries {
		n, _ := new(big.Int).SetString(e.Serial, 16)
		entries = append(entries, x509.RevocationListEntry{SerialNumber: n, RevocationTime: e.RevokedAt, ReasonCode: e.Reason})
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: number, ThisUpdate: r.ThisUpdate, NextUpdate: r.NextUpdate, RevokedCertificateEntries: entries}, ca, key)
	if err != nil {
		t.Fatal(err)
	}
	return createRootCRLRecord{IssuerID: root.ID, Digest: environmentPreflightBytesSHA256(der), Number: r.Number, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), ThisUpdate: r.ThisUpdate, NextUpdate: r.NextUpdate}
}

func TestCRLMaintenanceRequestAllEnvironmentsExpiredPriorAndRevocations(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			i, key, now := maintenanceFixture(t, env, time.Now().UTC().Truncate(time.Second).Add(-time.Hour))
			request, err := buildMaintenanceCRLRequest(env, i, now)
			if err != nil {
				t.Fatal(err)
			}
			if request.Number != "5" || !request.NextUpdate.Equal(now.Add(6*24*time.Hour)) || len(request.Entries) != 2 {
				t.Fatalf("bad successor: %+v", request)
			}
			signed := maintenanceSign(t, i.Issuer, key, request)
			if _, err := validateMaintenanceCRL(env, i.Issuer, signed, true, now); err != nil {
				t.Fatal(err)
			}
			if _, err := buildMaintenanceCRLRequest("foreign", i, now); err == nil {
				t.Fatal("accepted other environment")
			}
			request.Entries = request.Entries[:1]
			truncated := maintenanceSign(t, i.Issuer, key, request)
			if err := validateMaintenanceSuccessor(env, i, request, truncated, now); err == nil {
				t.Fatal("accepted dropped revocation")
			}
		})
	}
}

func TestCRLMaintenanceResumeDoesNotSignOrPublishTwice(t *testing.T) {
	i, key, now := maintenanceFixture(t, "staging", time.Now().UTC().Truncate(time.Second).Add(time.Hour))
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore(rootDir, "staging")
	if err != nil {
		t.Fatal(err)
	}
	inventory := crlMaintenanceInventory{Issuers: []crlMaintenanceIssuer{i}}
	signs, imports, installs := 0, 0, 0
	fail := true
	deps := crlMaintenanceDependencies{
		Observe: func(context.Context) (crlMaintenanceInventory, error) { return inventory, nil },
		Sign: func(_ context.Context, issuer crlMaintenanceIssuer, r createRootCRLRequest, _ string) (createRootCRLRecord, error) {
			signs++
			return maintenanceSign(t, issuer.Issuer, key, r), nil
		},
		Import: func(_ context.Context, _ crlMaintenanceIssuer, _ createRootCRLRequest, r createRootCRLRecord) error {
			imports++
			inventory.Issuers[0].Snapshot.Prior = r
			return nil
		},
		Converge: func(context.Context, crlMaintenanceInventory, map[string]createRootCRLRecord) error {
			installs++
			if fail {
				return errors.New("interrupted after import")
			}
			return nil
		},
	}
	opts := crlMaintenanceOptions{Environment: "staging", Execute: true, Now: func() time.Time { return now }, ClusterSHA: "cluster"}
	if _, err := executeCRLMaintenance(context.Background(), store, opts, deps); err == nil {
		t.Fatal("interrupted run succeeded")
	} else if err.Error() != "interrupted after import" {
		t.Fatalf("unexpected initial error: %v", err)
	}
	fail = false
	if _, err := executeCRLMaintenance(context.Background(), store, opts, deps); err != nil {
		t.Fatal(err)
	}
	if signs != 1 || imports != 1 || installs != 2 {
		t.Fatalf("sign=%d import=%d installs=%d", signs, imports, installs)
	}
	if _, err := executeCRLMaintenance(context.Background(), store, opts, deps); err != nil {
		t.Fatal(err)
	}
	if signs != 1 || imports != 1 {
		t.Fatal("healthy retry mutated registry")
	}
}

func TestCRLMaintenanceReadOnlyAndExpiredRequireRecovery(t *testing.T) {
	for _, expiry := range []time.Duration{72 * time.Hour, 48 * time.Hour, -time.Hour} {
		t.Run(expiry.String(), func(t *testing.T) {
			i, _, now := maintenanceFixture(t, "prod", time.Now().UTC().Truncate(time.Second).Add(expiry))
			store, _ := newSecretStore(t.TempDir(), "prod")
			deps := crlMaintenanceDependencies{Observe: func(context.Context) (crlMaintenanceInventory, error) {
				return crlMaintenanceInventory{Issuers: []crlMaintenanceIssuer{i}}, nil
			}}
			result, err := executeCRLMaintenance(context.Background(), store, crlMaintenanceOptions{Environment: "prod", Now: func() time.Time { return now }}, deps)
			if err != nil {
				t.Fatal(err)
			}
			if result.Due != (expiry <= 48*time.Hour) {
				t.Fatal("incorrect renewal lead")
			}
			if expiry < 0 {
				_, err = executeCRLMaintenance(context.Background(), store, crlMaintenanceOptions{Environment: "prod", Execute: true, Now: func() time.Time { return now }}, deps)
				if err == nil {
					t.Fatal("expired run requires --recover")
				}
			}
		})
	}
}

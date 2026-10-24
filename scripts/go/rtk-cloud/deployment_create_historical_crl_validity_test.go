package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func historicalAccountCRLFixture(t *testing.T) *createNormalTailAccountRegistrationCRLs {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	s := &createNormalTailAccountRegistrationCRLs{CapturedAt: at, SecretUID: "33333333-3333-4333-8333-333333333333"}
	for i := int64(1); i <= 3; i++ {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(i), Subject: pkix.Name{CommonName: big.NewInt(i).String()}, NotBefore: at.Add(-time.Hour), NotAfter: at.Add(24 * time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		der, err = x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(2), ThisUpdate: at.Add(-time.Minute), NextUpdate: at.Add(time.Hour)}, cert, key)
		if err != nil {
			t.Fatal(err)
		}
		s.Issuers = append(s.Issuers, environmentCreateRegisteredIssuer{ID: big.NewInt(i).String(), Certificate: productPEM("CERTIFICATE", cert.Raw)})
		s.CRLs = append(s.CRLs, productPEM("X509 CRL", der))
	}
	return s
}

func TestHistoricalAccountRegistrationCRLsPreserveCaptureAndRejectLiveExpiry(t *testing.T) {
	s := historicalAccountCRLFixture(t)
	if err := validateAccountRegistrationCRLBundle(s, s.CapturedAt); err != nil {
		t.Fatal("genuine captured proof", err)
	}
	if err := validateAccountRegistrationCRLBundle(s, time.Now().UTC()); err == nil {
		t.Fatal("expired captured CRLs became live trust")
	}
	for _, mode := range []string{"before-publication", "after-expiry", "future-capture", "foreign-signature", "foreign-issuer"} {
		t.Run(mode, func(t *testing.T) {
			var q createNormalTailAccountRegistrationCRLs
			if err := json.Unmarshal(mustProviderRecoveryJSON(s), &q); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "before-publication":
				q.CapturedAt = s.CapturedAt.Add(-time.Hour)
			case "after-expiry":
				q.CapturedAt = s.CapturedAt.Add(2 * time.Hour)
			case "future-capture":
				q.CapturedAt = time.Now().Add(time.Hour)
			case "foreign-signature":
				q.CRLs[2] = q.CRLs[0]
			case "foreign-issuer":
				q.Issuers[2] = q.Issuers[0]
			}
			if validateAccountRegistrationCRLBundle(&q, q.CapturedAt) == nil {
				t.Fatal("tampered historical proof accepted")
			}
		})
	}
}

func historicalServiceRootReaderFixture(t *testing.T) (secretStore, *environmentCreateExecution, string, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	root, key, snapshot, now := rootCRLFixture(t)
	at := now.Add(-3 * time.Hour)
	snapshot.Bindings[0].RevokedAt = at.Add(-time.Hour)
	request, err := buildCreateRootCRLRequest(root, snapshot, at)
	if err != nil {
		t.Fatal(err)
	}
	request.NextUpdate = at.Add(2 * time.Hour)
	signed := signedRootCRLFixture(t, root, key, request)
	_, a, source := rootCRLTailFixture()
	parent := a.Plan.Parent
	source.PlanSHA256, source.OwnerToken = parent, strings.Repeat("e", 64)
	for i := range source.Entries {
		source.Entries[i].CompletedAt = at.Add(-time.Minute)
	}
	journalRel := "deployment/create/executions/" + parent + "/execution-isolated-history/journal.json"
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: source.Stack, PlanSHA256: parent, OwnerToken: source.OwnerToken, Journal: journalRel, StartedAt: at.Add(-time.Hour)}
	a.Plan.Root, a.Plan.Snapshot, a.Plan.Request, a.Plan.Signed = root, snapshot, request, signed
	a.Plan.Parent, a.Plan.OwnerSHA, a.Plan.JournalSHA = parent, deploymentplan.Digest(owner), deploymentplan.Digest(source)
	a.QualifiedAt, a.ValidUntil = at.Add(time.Minute), at.Add(16*time.Minute)
	write := func(path string, value []byte) {
		t.Helper()
		if err := store.write(path, value, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/service-root-v1-key-passphrase", filepath.Join("deployment/create", parent, "bootstrap/service/root-active.json")} {
		write(path, []byte("isolated immutable custody fixture\n"))
	}
	a.Plan.Material, err = createRootCRLMaterial(store, parent, root)
	if err != nil {
		t.Fatal(err)
	}
	review := deploymentplan.Digest(a.Plan)
	a.Schema, a.Qualification = createRootCRLRecoverySchema+"/artifact", deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createRootCRLGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: parent, Review: review, File: a.Plan.Candidate + "/signed/revocations.pem", Digest: signed.Digest, Completed: true}
	current := source
	current.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
	keyName := "service-root-crl-renewal-" + review
	appendStep := func(name string, intent, receipt any) {
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: name, Phase: "bootstrap", State: "completed", StartedAt: at.Add(2 * time.Minute), CompletedAt: at.Add(3 * time.Minute), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(receipt)})
	}
	appendStep(keyName, a.Plan, proof)
	appendStep(keyName+"/native-import", map[string]string{"issuer_id": root.ID, "root_sha256": root.RootFingerprint, "prior_crl_sha256": snapshot.Prior.Digest, "signed_crl_sha256": signed.Digest, "request_sha256": deploymentplan.Digest(request)}, map[string]any{"native_crl": signed, "request_sha256": deploymentplan.Digest(request)})
	appendStep(keyName+"/original-secret", a.Plan.Secret, map[string]string{"resource_id": a.Plan.Secret.ID, "uid": a.Plan.Secret.UID, "desired_sha256": deploymentplan.Digest(a.Plan.Secret.Desired)})
	for _, target := range a.Plan.Targets {
		appendStep(keyName+"/state-"+target.Container, map[string]any{"target": target, "native_crl_sha256": signed.Digest}, map[string]any{"resource_id": target.ID, "uid": target.UID, "crl_sha256": signed.Digest, "paths": target.Paths, "installed_on_disk": true, "runtime_reload_required": true})
	}
	e := &environmentCreateExecution{Store: store, Journal: current, owner: owner}
	write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(owner))
	write(journalRel, mustProviderRecoveryJSON(current))
	base := filepath.Join("deployment/create/root-crl-recovery", review)
	write(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a))
	write(filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(source))
	write(proof.File, []byte(signed.PEM))
	write(filepath.Join("deployment/create/material", parent, "service-root-crl-renewal.json"), mustProviderRecoveryJSON(proof))
	return store, e, review, keyName
}

func TestHistoricalServiceRootCRLReaderAcceptsExpiredHistoryButLiveRequiresCurrent(t *testing.T) {
	store, e, _, _ := historicalServiceRootReaderFixture(t)
	a, proof, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil || a == nil || proof == nil {
		t.Fatal("genuine completed historical reader", err)
	}
	if _, err := parseCreateRootCRL(a.Plan.Root, a.Plan.Signed, true, time.Now().UTC()); err == nil {
		t.Fatal("historical signed record accepted as current")
	}
	// The live helper must fail before it can reach a native command/backend.
	if _, err := verifyManagedServiceRootCRLFreshness(context.Background(), deploymentConfig{}, store, e, environmentKubernetesBackend{}); err == nil {
		t.Fatal("live freshness waived for historical completion")
	}
}

func TestHistoricalServiceRootCRLReaderRejectsTamperedProvenance(t *testing.T) {
	for _, mode := range []string{"qualification-time", "signature", "issuer", "receipt", "admission-time"} {
		t.Run(mode, func(t *testing.T) {
			store, e, review, key := historicalServiceRootReaderFixture(t)
			base := filepath.Join("deployment/create/root-crl-recovery", review)
			var a createRootCRLRecoveryArtifact
			if readExecutionJSON(filepath.Join(store.Root, base, "qualification.json"), &a) != nil {
				t.Fatal("fixture")
			}
			rewrite := func(path string, value []byte) {
				t.Helper()
				if err := store.write(path, value, true); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "qualification-time":
				a.QualifiedAt = a.QualifiedAt.Add(-24 * time.Hour)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
				rewrite(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a))
			case "signature":
				rewrite(a.Plan.Candidate+"/signed/revocations.pem", []byte(a.Plan.Snapshot.Prior.PEM))
			case "issuer":
				a.Plan.Root.ID = "22222222-2222-4222-8222-222222222222"
				rewrite(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a))
			case "receipt", "admission-time":
				for i := range e.Journal.Entries {
					if e.Journal.Entries[i].Key == key {
						if mode == "receipt" {
							e.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(map[string]string{"completed": "foreign"})
						} else {
							e.Journal.Entries[i].StartedAt = a.ValidUntil
						}
					}
				}
				rewrite(e.owner.Journal, mustProviderRecoveryJSON(e.Journal))
			}
			if _, _, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256); err == nil {
				t.Fatal("tampered archived proof accepted")
			}
		})
	}
}

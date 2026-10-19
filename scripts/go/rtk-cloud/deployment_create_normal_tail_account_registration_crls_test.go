package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
	"time"
)

func accountRegistrationCRLFixture(t *testing.T) *createNormalTailAccountRegistrationCRLs {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	s := &createNormalTailAccountRegistrationCRLs{Schema: createNormalTailAccountRegistrationCRLSchema, CapturedAt: now, SecretUID: "33333333-3333-4333-8333-333333333333"}
	for i := int64(1); i <= 3; i++ {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(i), Subject: pkix.Name{CommonName: big.NewInt(i).String()}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		crl, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(7), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, cert, key)
		if err != nil {
			t.Fatal(err)
		}
		s.Issuers = append(s.Issuers, environmentCreateRegisteredIssuer{ID: big.NewInt(i).String(), Certificate: productPEM("CERTIFICATE", der)})
		s.CRLs = append(s.CRLs, productPEM("X509 CRL", crl))
	}
	return s
}
func TestAccountRegistrationCRLsCurrentSignedCoverageAndPrivatePreservation(t *testing.T) {
	s := accountRegistrationCRLFixture(t)
	if err := validateAccountRegistrationCRLBundle(s, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	old := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": deploymentplan.Object{"namespace": "stack-account-manager", "name": "account-manager-service-registration-tls", "uid": s.SecretUID}, "data": deploymentplan.Object{"client-ca.crt": "b3JpZ2luYWwtY2E=", "tls.crt": "b3JpZ2luYWwtc2VydmVy", "tls.key": "b3JpZ2luYWwtcHJpdmF0ZQ==", "client.crl": "b2xk"}}
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Stack: "stack"}, Resources: []deploymentplan.Resource{{ID: createNormalTailAccountRegistrationCRLID("stack"), Action: "retain", UID: s.SecretUID, Baseline: old, Desired: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": "stack-account-manager", "name": "account-manager-service-registration-tls"}}}}}
	before := deploymentplan.Digest(p)
	desired, err := accountRegistrationCRLDesired(p, s)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range deploymentplan.Map(old["data"]) {
		if key != "client.crl" && deploymentplan.Map(desired["data"])[key] != value {
			t.Fatal("private or CA data changed", key)
		}
	}
	if before != deploymentplan.Digest(p) || deploymentplan.Map(desired["metadata"])["uid"] != nil {
		t.Fatal("original changed or server UID copied into Desired")
	}
	raw, _ := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(desired["data"])["client.crl"]))
	if len(raw) == 0 {
		t.Fatal("bundle absent")
	}
	for _, mode := range []string{"missing-new", "foreign-signature", "duplicate-issuer", "expired", "future", "wrong-uid", "unrelated-key"} {
		t.Run(mode, func(t *testing.T) {
			var q createNormalTailAccountRegistrationCRLs
			json.Unmarshal(mustProviderRecoveryJSON(s), &q)
			var pp deploymentplan.Plan
			json.Unmarshal(mustProviderRecoveryJSON(p), &pp)
			at := time.Now().UTC()
			switch mode {
			case "missing-new":
				q.CRLs = q.CRLs[:2]
			case "foreign-signature":
				q.CRLs[2] = q.CRLs[0]
			case "duplicate-issuer":
				q.Issuers[2] = q.Issuers[0]
			case "expired":
				at = at.Add(2 * time.Hour)
			case "future":
				at = at.Add(-2 * time.Hour)
			case "wrong-uid":
				q.SecretUID = "44444444-4444-4444-8444-444444444444"
			case "unrelated-key":
				deploymentplan.Map(pp.Resources[0].Baseline["data"])["extra"] = "ZXh0cmE="
			}
			if mode == "expired" || mode == "future" {
				if validateAccountRegistrationCRLBundle(&q, at) == nil {
					t.Fatal("invalid CRL accepted")
				}
			} else if _, err := accountRegistrationCRLDesired(&pp, &q); err == nil {
				t.Fatal("foreign source accepted")
			}
		})
	}
}
func TestAccountRegistrationCRLRuntimeForwardUsesOnlyAcceptedSecretBaseline(t *testing.T) {
	store, source, inputs, p, j, observed := runtimeBillingPartialFixture(t)
	proof := accountRegistrationCRLFixture(t)
	old := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": p.Stack + "-account-manager", "name": "account-manager-service-registration-tls", "uid": proof.SecretUID}, "data": deploymentplan.Object{"client-ca.crt": "Y2E=", "client.crl": "b2xk", "tls.crt": "c2VydmVy", "tls.key": "cHJpdmF0ZQ=="}, "type": "Opaque"}
	p.Resources = append(p.Resources, deploymentplan.Resource{ID: createNormalTailAccountRegistrationCRLID(p.Stack), Phase: "configuration", Action: "retain", UID: proof.SecretUID, Baseline: old, Desired: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": p.Stack + "-account-manager", "name": "account-manager-service-registration-tls"}}})
	j.PlanSHA256 = p.SHA256()
	source.PreviousPlanSHA256 = p.SHA256()
	source.PreviousJournalSHA256 = deploymentplan.Digest(j)
	source.RegistrationCRLs = proof
	desired, err := accountRegistrationCRLDesired(p, proof)
	if err != nil {
		t.Fatal(err)
	}
	current := deploymentplan.Clone(desired)
	deploymentplan.Map(current["metadata"])["uid"] = proof.SecretUID
	observed[deploymentplan.ID(current)] = current
	next, err := buildCreateNormalTailRuntimeForward(store, source, inputs, p, j, observed, createNormalTailAccountRegistrationCRLProfile)
	if err != nil {
		t.Fatal(err)
	}
	r := next.Resources[len(next.Resources)-1]
	if r.Action != "retain" || r.UID != proof.SecretUID || deploymentplan.Digest(r.Baseline) != deploymentplan.Digest(deploymentplan.Projection(current)) {
		t.Fatal("accepted child not retained exactly")
	}
	for _, mode := range []string{"old-body", "replaced-uid", "foreign-ca", "update-again", "terminating-baseline"} {
		t.Run(mode, func(t *testing.T) {
			var q deploymentplan.Plan
			json.Unmarshal(mustProviderRecoveryJSON(next), &q)
			got := &q.Resources[len(q.Resources)-1]
			switch mode {
			case "old-body":
				got.Baseline = deploymentplan.Clone(old)
			case "replaced-uid":
				got.UID = "44444444-4444-4444-8444-444444444444"
			case "foreign-ca":
				deploymentplan.Map(got.Desired["data"])["client-ca.crt"] = "Zm9yZWlnbg=="
			case "update-again":
				got.Action = "update"
			case "terminating-baseline":
				deploymentplan.Map(got.Baseline["metadata"])["deletionTimestamp"] = "2026-10-19T00:00:00Z"
			}
			if validateCreateNormalTailRuntimeForward(store, source, inputs, p, j, &q, createNormalTailAccountRegistrationCRLProfile) == nil {
				t.Fatal("foreign retained child accepted")
			}
		})
	}
}

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSecondSessionExpiredSessionAcceptsItsValidOrExpiredLeaf(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ca, signer, caPEM := coreContractCertificate(t, "original-root", true, nil, nil)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	service := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{Certificate: caPEM, RootFingerprint: environmentPreflightBytesSHA256(ca.Raw)}, Intermediate: environmentCreateRegisteredIssuer{Certificate: caPEM}}
	for _, tc := range []struct {
		name                   string
		before, after, expires time.Time
		want                   bool
	}{
		{"valid-leaf", now.Add(-time.Hour), now.Add(time.Hour), now.Add(-time.Minute), true},
		{"expired-leaf", now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Minute), true},
		{"future-leaf", now.Add(time.Minute), now.Add(time.Hour), now.Add(-time.Minute), false},
		{"live-session", now.Add(-time.Hour), now.Add(time.Hour), now.Add(time.Minute), false},
		{"missing-expiry", now.Add(-time.Hour), now.Add(time.Hour), time.Time{}, false},
		{"leaf-earlier-than-session", now.Add(-time.Hour), now.Add(-2 * time.Minute), now.Add(-time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: tc.before, NotAfter: tc.after, KeyUsage: x509.KeyUsageDigitalSignature}
			der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, signer)
			if err != nil {
				t.Fatal(err)
			}
			cert := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte(caPEM)...)
			state := managedSessionState{Fingerprint: environmentPreflightBytesSHA256(der), Expires: tc.expires}
			if got := secondSessionExpiredPredecessorCustodyValid(keyPEM, cert, []byte(caPEM), csrPEM, service, state, now); got != tc.want {
				t.Fatalf("custody validation = %v, want %v", got, tc.want)
			}
			state.Fingerprint = strings.Repeat("a", 64)
			if secondSessionExpiredPredecessorCustodyValid(keyPEM, cert, []byte(caPEM), csrPEM, service, state, now) {
				t.Fatal("foreign fingerprint accepted")
			}
		})
	}
}

func TestSecondSessionCredentialRequiresOriginalOwnerAndMaterial(t *testing.T) {
	encode := base64.StdEncoding.EncodeToString
	uid := "11111111-1111-4111-8111-111111111111"
	expected := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "original", "namespace": "video"}, "immutable": true, "data": deploymentplan.Object{"tls.key": encode([]byte("original-key")), "tls.crt": encode([]byte("original-cert")), "ca.crt": encode([]byte("original-root"))}}
	actual := deploymentplan.Clone(expected)
	deploymentplan.Map(actual["metadata"])["uid"] = uid
	keySHA, certSHA := environmentPreflightBytesSHA256([]byte("original-key")), environmentPreflightBytesSHA256([]byte("original-cert"))
	if !secondSessionCredentialValid(uid, expected, actual, keySHA, certSHA, "original-root") {
		t.Fatal("original credential was rejected")
	}
	for _, mode := range []string{"uid", "mutable", "key", "cert", "root", "bad-base64"} {
		t.Run(mode, func(t *testing.T) {
			bad := deploymentplan.Clone(actual)
			switch mode {
			case "uid":
				deploymentplan.Map(bad["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
			case "mutable":
				bad["immutable"] = false
			case "key":
				deploymentplan.Map(bad["data"])["tls.key"] = encode([]byte("other"))
			case "cert":
				deploymentplan.Map(bad["data"])["tls.crt"] = encode([]byte("other"))
			case "root":
				deploymentplan.Map(bad["data"])["ca.crt"] = encode([]byte("other"))
			case "bad-base64":
				deploymentplan.Map(bad["data"])["tls.key"] = "%%%"
			}
			if secondSessionCredentialValid(uid, expected, bad, keySHA, certSHA, "original-root") {
				t.Fatal("altered credential was accepted")
			}
		})
	}
}

func TestSecondSessionCandidateUsesOriginalKeyAndImmutableEvidence(t *testing.T) {
	d := strings.Repeat("a", 64)
	image := environmentReleaseImage{Reference: "ghcr.io/example/api@sha256:" + d}
	plan := environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: d, Bootstrap: environmentCreateBootstrapPlan{Image: image.Reference}, Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}}
	env := map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt", "OPENBAO_ADDR": "https://openbao.video-cloud-dev-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt"}
	source := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "original", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": image.Reference, "env": environmentCreateEnv(env), "volumeMounts": []any{deploymentplan.Object{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "openbao-ca", "configMap": deploymentplan.Object{"name": "pki-openbao-transport-ca"}}, deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-bootstrap-custody"}}}}}}}
	deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(source["spec"])["template"])["spec"])["initContainers"] = []any{deploymentplan.Object{"name": "permissions"}}
	before := deploymentplan.Digest(source)
	q := &secondSessionQualification{ParentSHA: d, OriginalReview: d, SourcePlanSHA: d, SourceJournalSHA: d, SourceJobID: "Job/original", SourceJobUID: "11111111-1111-4111-8111-111111111111", PreviousPlanSHA: d, PreviousJournalSHA: d, PreviousJobUID: "22222222-2222-4222-8222-222222222222", ClientsSHA: d, ClientDigests: []string{d}, KeySHA: d, CSRSHA: d, PreviousCertSHA: d, OpenBaoTrustSHA: d, CustodyOwner: "12345:23456", Intent: createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333"}}
	if err := compileSecondSessionCandidate(deploymentConfig{Environment: "dev"}, plan, q, source, image, nil); err != nil {
		t.Fatal(err)
	}
	if q.BindingSHA != secondSessionBinding(q) || q.RenewedIntent.SessionID != sessionRenewalUUID(q.ParentSHA, q.BindingSHA, q.Intent.SessionID) || q.RenewedIntent.SessionID == q.Intent.SessionID || deploymentplan.Digest(source) != before {
		t.Fatal("candidate lost its original source or evidence binding")
	}
	job := q.Candidate.Resources[0].Desired
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])
	got := createRecipeEnv(c)
	oldVolumes, _ := environmentPKINamedFields(environmentPKIPod(source)["volumes"])
	newVolumes, _ := environmentPKINamedFields(environmentPKIPod(job)["volumes"])
	if got["PKI_BOOTSTRAP_KEY_PATH"] != env["PKI_BOOTSTRAP_KEY_PATH"] || got["PKI_BOOTSTRAP_CSR_PATH"] != env["PKI_BOOTSTRAP_CSR_PATH"] || got["PKI_BOOTSTRAP_CERT_PATH"] != "/run/pki-bootstrap/renew-"+q.BindingSHA[:12]+"/bootstrap.crt" || got["PKI_BOOTSTRAP_SESSION_ID"] != q.RenewedIntent.SessionID || deploymentplan.Digest(newVolumes["state"]) != deploymentplan.Digest(oldVolumes["state"]) {
		t.Fatal("candidate altered key, CSR or original claim")
	}
	if got["PKI_BOOTSTRAP_TTL"] != "" {
		t.Fatal("candidate injected a script-defined TTL")
	}
	init := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["initContainers"])[0])
	command := certIssuerObjectList(init["command"])
	if len(command) != 3 || strings.Count(deploymentplan.String(command[2]), "chown 12345:23456 ") != 2 || strings.Contains(deploymentplan.String(command[2]), "chown 10001:10001 ") {
		t.Fatal("candidate did not preserve actual custody owner")
	}
	changed := *q
	changed.ClientDigests = []string{strings.Repeat("b", 64)}
	if secondSessionBinding(&changed) == q.BindingSHA {
		t.Fatal("candidate failed to bind current permanent identity inventory")
	}
	changed = *q
	changed.PreviousJobUID = "44444444-4444-4444-8444-444444444444"
	if secondSessionBinding(&changed) == q.BindingSHA {
		t.Fatal("candidate failed to bind predecessor Job UID")
	}
	if err := compileSecondSessionCandidate(deploymentConfig{Environment: "dev"}, plan, q, source, image, []deploymentplan.Object{job}); err == nil {
		t.Fatal("unrecorded existing candidate accepted")
	}
	configured := *q
	configured.BootstrapTTL = "4h"
	if err := compileSecondSessionCandidate(deploymentConfig{Environment: "dev", Values: map[string]string{"PKI_BOOTSTRAP_TTL": "4h"}}, plan, &configured, source, image, nil); err != nil {
		t.Fatal(err)
	}
	configuredContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(configured.Candidate.Resources[0].Desired)["containers"])[0])
	if createRecipeEnv(configuredContainer)["PKI_BOOTSTRAP_TTL"] != "4h" || configured.BindingSHA == q.BindingSHA {
		t.Fatal("candidate ignored environment TTL")
	}
}

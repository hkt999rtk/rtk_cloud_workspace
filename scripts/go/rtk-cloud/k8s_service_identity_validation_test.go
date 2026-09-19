package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

type lkePlatformCertificateFixture struct {
	listener   map[string]any
	identities map[string]map[string]any
	issuer     *x509.Certificate
	issuerKey  *ecdsa.PrivateKey
	serials    map[string]*big.Int
	now        time.Time
}

func lkeTestSecret(data map[string][]byte) map[string]any {
	encoded := make(map[string]any, len(data))
	for key, value := range data {
		encoded[key] = base64.StdEncoding.EncodeToString(value)
	}
	return map[string]any{"data": encoded}
}

func lkeTestSecretJSON(t *testing.T, secret map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func lkeCloneSecret(secret map[string]any) map[string]any {
	copyData := map[string]any{}
	for key, value := range secret["data"].(map[string]any) {
		copyData[key] = value
	}
	return map[string]any{"data": copyData}
}

func newLKEPlatformCertificateFixture(t *testing.T, env map[string]string) lkePlatformCertificateFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test Internal Service CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId: []byte{1, 2, 3, 4},
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverKey, serverCert := lkeTestLeaf(t, ca, caKey, big.NewInt(2), pkix.Name{CommonName: "platform-register"}, []string{lkeRegistrationServerDNS(env)}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now)
	crl := lkeTestCRL(t, ca, caKey, now, nil)
	fixture := lkePlatformCertificateFixture{
		listener: lkeTestSecret(map[string][]byte{
			"tls.crt": serverCert, "tls.key": serverKey, "client-ca.crt": caPEM, "client.crl": crl,
		}),
		identities: map[string]map[string]any{}, issuer: ca, issuerKey: caKey, serials: map[string]*big.Int{}, now: now,
	}
	for index, subject := range []string{"service:mqtt", "service:shadow", "service:webrtc", "service:video-storage"} {
		serial := big.NewInt(int64(index + 3))
		key, cert := lkeTestLeaf(t, ca, caKey, serial, pkix.Name{CommonName: subject}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now)
		fixture.identities[subject] = lkeTestSecret(map[string][]byte{"client.crt": cert, "client.key": key, "server-ca.crt": caPEM})
		fixture.serials[subject] = serial
	}
	return fixture
}

func lkeTestLeaf(t *testing.T, issuer *x509.Certificate, issuerKey *ecdsa.PrivateKey, serial *big.Int, subject pkix.Name, dns []string, purposes []x509.ExtKeyUsage, now time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: subject, DNSNames: dns,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: purposes,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

func lkeTestCRL(t *testing.T, issuer *x509.Certificate, key *ecdsa.PrivateKey, now time.Time, revoked []*big.Int) []byte {
	t.Helper()
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, serial := range revoked {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: now.Add(-time.Minute)})
	}
	raw, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour), RevokedCertificateEntries: entries,
	}, issuer, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: raw})
}

func setFakeLKEPlatformIdentitySecrets(t *testing.T, env map[string]string) lkePlatformCertificateFixture {
	t.Helper()
	fixture := newLKEPlatformCertificateFixture(t, env)
	t.Setenv("FAKE_SERVICE_REGISTRATION_SECRET_JSON", lkeTestSecretJSON(t, fixture.listener))
	for key, subject := range map[string]string{
		"FAKE_MQTT_FOUNDATION_IDENTITY_SECRET_JSON":       "service:mqtt",
		"FAKE_SHADOW_WORKER_IDENTITY_SECRET_JSON":         "service:shadow",
		"FAKE_WEBRTC_SERVICE_IDENTITY_SECRET_JSON":        "service:webrtc",
		"FAKE_VIDEO_STORAGE_SERVICE_IDENTITY_SECRET_JSON": "service:video-storage",
	} {
		t.Setenv(key, lkeTestSecretJSON(t, fixture.identities[subject]))
	}
	return fixture
}

func TestLKEPlatformServiceIdentityMaterialRejectsWrongTrustAndRevocation(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	fixture := newLKEPlatformCertificateFixture(t, env)
	dns := lkeRegistrationServerDNS(env)
	if err := validatePlatformServiceIdentityMaterial(fixture.identities["service:shadow"], fixture.listener, "service:shadow", dns, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformServiceIdentityMaterial(fixture.identities["service:shadow"], fixture.listener, "service:mqtt", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "wrong identity") {
		t.Fatalf("wrong service CN accepted: %v", err)
	}
	other := newLKEPlatformCertificateFixture(t, env)
	if err := validatePlatformServiceIdentityMaterial(other.identities["service:shadow"], fixture.listener, "service:shadow", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("foreign service CA accepted: %v", err)
	}
	revoked := lkeTestSecret(map[string][]byte{"client.crl": lkeTestCRL(t, fixture.issuer, fixture.issuerKey, fixture.now, []*big.Int{fixture.serials["service:shadow"]})})
	listener := lkeCloneSecret(fixture.listener)
	listener["data"].(map[string]any)["client.crl"] = revoked["data"].(map[string]any)["client.crl"]
	if err := validatePlatformServiceIdentityMaterial(fixture.identities["service:shadow"], listener, "service:shadow", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked service certificate accepted: %v", err)
	}
	identity := lkeCloneSecret(fixture.identities["service:shadow"])
	identity["data"].(map[string]any)["server-ca.crt"] = other.identities["service:shadow"]["data"].(map[string]any)["server-ca.crt"]
	if err := validatePlatformServiceIdentityMaterial(identity, fixture.listener, "service:shadow", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("foreign server CA accepted: %v", err)
	}
	identity = lkeCloneSecret(fixture.identities["service:shadow"])
	identity["data"].(map[string]any)["client.key"] = other.identities["service:shadow"]["data"].(map[string]any)["client.key"]
	if err := validatePlatformServiceIdentityMaterial(identity, fixture.listener, "service:shadow", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "key pair") {
		t.Fatalf("mismatched private key accepted: %v", err)
	}
	listener = lkeCloneSecret(fixture.listener)
	crlPEM, err := base64.StdEncoding.DecodeString(listener["data"].(map[string]any)["client.crl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(crlPEM)
	if block == nil {
		t.Fatal("fixture has no CRL PEM block")
	}
	listener["data"].(map[string]any)["client.crl"] = base64.StdEncoding.EncodeToString(block.Bytes)
	if err := validatePlatformServiceIdentityMaterial(fixture.identities["service:shadow"], listener, "service:shadow", dns, fixture.now); err != nil {
		t.Fatalf("DER CRL accepted by listener was rejected by preflight: %v", err)
	}
	listener = lkeCloneSecret(fixture.listener)
	staleCRL := lkeTestCRL(t, fixture.issuer, fixture.issuerKey, fixture.now.Add(-2*time.Hour), nil)
	listener["data"].(map[string]any)["client.crl"] = base64.StdEncoding.EncodeToString(staleCRL)
	if err := validatePlatformServiceIdentityMaterial(fixture.identities["service:shadow"], listener, "service:shadow", dns, fixture.now); err == nil || !strings.Contains(err.Error(), "not current") {
		t.Fatalf("stale CRL accepted: %v", err)
	}
}

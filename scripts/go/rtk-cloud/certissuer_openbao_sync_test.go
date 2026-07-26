package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestReconcileCertIssuerOpenBaoCAPreservesAppRoleAndRollsDeployment(t *testing.T) {
	ca := testOpenBaoCAPEM(t)
	encodedCA := base64.StdEncoding.EncodeToString(ca)
	openBao := map[string]any{"data": map[string]any{"ca.crt": encodedCA}}
	authData := map[string]any{
		"ca.crt":    base64.StdEncoding.EncodeToString([]byte("stale")),
		"role_id":   "preserve-role",
		"secret_id": "preserve-secret",
	}
	auth := map[string]any{"data": authData}
	deployment := map[string]any{"spec": map[string]any{"template": map[string]any{}}}

	changed, err := reconcileCertIssuerOpenBaoCA(openBao, auth, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || authData["ca.crt"] != encodedCA {
		t.Fatal("OpenBao CA was not synchronized")
	}
	if authData["role_id"] != "preserve-role" || authData["secret_id"] != "preserve-secret" {
		t.Fatal("AppRole material changed during CA synchronization")
	}
	annotations := deployment["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["rtk.realtek.com/openbao-ca-checksum"] == "" {
		t.Fatal("deployment rollout checksum is missing")
	}

	changed, err = reconcileCertIssuerOpenBaoCA(openBao, auth, deployment)
	if err != nil || changed {
		t.Fatalf("unchanged CA reconciled again: changed=%v err=%v", changed, err)
	}
}

func TestReconcileCertIssuerOpenBaoCARejectsMissingAppRole(t *testing.T) {
	encodedCA := base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
	openBao := map[string]any{"data": map[string]any{"ca.crt": encodedCA}}
	auth := map[string]any{"data": map[string]any{"ca.crt": "stale", "role_id": "role"}}
	deployment := map[string]any{"spec": map[string]any{"template": map[string]any{}}}
	if _, err := reconcileCertIssuerOpenBaoCA(openBao, auth, deployment); err == nil {
		t.Fatal("missing AppRole secret accepted")
	}
}

func testOpenBaoCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-openbao-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-openbao-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

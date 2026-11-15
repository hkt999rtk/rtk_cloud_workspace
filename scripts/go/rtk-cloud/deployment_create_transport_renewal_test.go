package main

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitialTransportRenewalPreservesEveryServingKeyAndName(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if material, proof, err := readInitialTransportRenewal(store, strings.Repeat("a", 64)); err != nil || proof != nil || material != nil {
		t.Fatal("missing renewal pointer did not retain original material", err)
	}
	now := time.Now().UTC()
	old, err := createEnvironmentTransport(store, strings.Repeat("a", 64), "management", map[string][]string{"account-manager": {"account-manager.video-cloud-dev-account-manager.svc"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	renewed, key, err := renewInitialTransportMaterial(old, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(key) == 0 || old.CACert == renewed.CACert || old.Identities["account-manager"].Key != renewed.Identities["account-manager"].Key {
		t.Fatal("serving key changed or temporary authority was not replaced")
	}
	if err := verifyInitialTransportSuccessor(old, renewed, key, now.Add(time.Hour)); err != nil {
		t.Fatal("valid replacement did not preserve the original serving identity", err)
	}
	changed := renewed
	changed.Identities = map[string]environmentCreateTransportIdentity{"account-manager": {Certificate: renewed.Identities["account-manager"].Certificate, Key: old.Identities["account-manager"].Certificate}}
	if err := verifyInitialTransportSuccessor(old, changed, key, now.Add(time.Hour)); err == nil {
		t.Fatal("accepted a replacement serving key")
	}
	if err := verifyInitialTransportSuccessor(old, renewed, []byte("foreign CA key"), now.Add(time.Hour)); err == nil {
		t.Fatal("accepted a foreign authority key")
	}
	if err := verifyInitialTransportSuccessor(old, renewed, key, now.Add(26*time.Hour)); err == nil {
		t.Fatal("accepted expired successor certificates")
	}
	changed.Identities = map[string]environmentCreateTransportIdentity{"foreign": renewed.Identities["account-manager"]}
	if err := verifyInitialTransportSuccessor(old, changed, key, now.Add(time.Hour)); err == nil {
		t.Fatal("accepted a different identity name")
	}
	ca, _ := parseFirstCertificatePEM([]byte(renewed.CACert))
	leaf, _ := parseFirstCertificatePEM([]byte(renewed.Identities["account-manager"].Certificate))
	original, _ := parseFirstCertificatePEM([]byte(old.Identities["account-manager"].Certificate))
	pub, _ := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	oldPub, _ := x509.MarshalPKIXPublicKey(original.PublicKey)
	if string(pub) != string(oldPub) || leaf.CheckSignatureFrom(ca) != nil || deploymentplan.Digest(leaf.DNSNames) != deploymentplan.Digest(original.DNSNames) || !leaf.NotAfter.After(now.Add(20*time.Hour)) {
		t.Fatal("renewal lost original leaf identity, names or bounded lifetime")
	}
	if _, _, err := renewInitialTransportMaterial(environmentCreateTransport{}, now); err == nil {
		t.Fatal("missing original authority was regenerated")
	}
	expiredAt := now.Add(5 * time.Hour)
	renewedExpired, newKey, err := renewInitialTransportMaterial(old, expiredAt)
	if err != nil || verifyInitialTransportSuccessor(old, renewedExpired, newKey, expiredAt) != nil {
		t.Fatal("expired original authority prevented reuse of its serving identities", err)
	}
	broken := old
	broken.Identities = map[string]environmentCreateTransportIdentity{"account-manager": {Certificate: old.Identities["account-manager"].Certificate, Key: "invalid"}}
	if _, _, err := renewInitialTransportMaterial(broken, now); err == nil {
		t.Fatal("missing original serving key was regenerated")
	}
}

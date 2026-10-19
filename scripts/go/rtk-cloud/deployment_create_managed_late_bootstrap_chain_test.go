package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type managedLateBootstrapChainFixture struct {
	ceremony        environmentCreateServiceCeremony
	leaf, ica, root []byte
	icaKey          *ecdsa.PrivateKey
	rootKey         *ecdsa.PrivateKey
	icaCertificate  *x509.Certificate
	rootCertificate *x509.Certificate
	now             time.Time
}

func newManagedLateBootstrapChainFixture(t *testing.T) managedLateBootstrapChainFixture {
	t.Helper()
	root, rootKey, _, now := rootCRLFixture(t)
	rootCertificate, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(501), Subject: pkix.Name{CommonName: "original Service ICA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, NotBefore: now.Add(-12 * time.Hour), NotAfter: now.Add(72 * time.Hour)}
	ica, err := x509.CreateCertificate(rand.Reader, issuer, rootCertificate, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	icaCertificate, err := x509.ParseCertificate(ica)
	if err != nil {
		t.Fatal(err)
	}
	f := managedLateBootstrapChainFixture{ceremony: environmentCreateServiceCeremony{Root: root, Intermediate: environmentCreateRegisteredIssuer{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ica}))}}, ica: ica, root: rootCertificate.Raw, icaKey: key, rootKey: rootKey, icaCertificate: icaCertificate, rootCertificate: rootCertificate, now: now}
	f.leaf = f.signedLeaf(t, "service:deployment-bootstrap", false)
	return f
}

func (f managedLateBootstrapChainFixture) signedLeaf(t *testing.T, subject string, ca bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(502), Subject: pkix.Name{CommonName: subject}, NotBefore: f.now.Add(-9 * time.Hour), NotAfter: f.now.Add(-time.Hour), IsCA: ca, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if ca {
		leaf.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, f.icaCertificate, &key.PublicKey, f.icaKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestManagedLateBootstrapChainPreservesOnlyNativeThreeOrIdenticalICAFour(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	// An old seed is already expired. Its immutable native expiry/hash is checked
	// by the caller; this structural check must not replace retained bytes.
	for _, chain := range [][][]byte{{f.leaf, f.ica, f.root}, {f.leaf, f.ica, slices.Clone(f.ica), f.root}} {
		before := deploymentplan.Digest(chain)
		if !managedLateBootstrapChainMatches(chain, f.ceremony) {
			t.Fatal("genuine signed native chain rejected")
		}
		if deploymentplan.Digest(chain) != before {
			t.Fatal("native certificate bytes normalized or altered")
		}
	}
	// Reissuing the same ICA key under the same Root gives a genuinely valid,
	// different certificate. It cannot impersonate the permitted exact duplicate.
	reissued := *f.icaCertificate
	reissued.SerialNumber = big.NewInt(503)
	differentICA, err := x509.CreateCertificate(rand.Reader, &reissued, f.rootCertificate, &f.icaKey.PublicKey, f.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	foreign := newManagedLateBootstrapChainFixture(t)
	for name, chain := range map[string][][]byte{
		"empty":                        nil,
		"leaf only":                    {f.leaf},
		"missing ICA":                  {f.leaf, f.root},
		"missing Root":                 {f.leaf, f.ica},
		"foreign ICA":                  {f.leaf, foreign.ica, f.root},
		"foreign Root":                 {f.leaf, f.ica, foreign.root},
		"different same-key duplicate": {f.leaf, f.ica, differentICA, f.root},
		"duplicated Root":              {f.leaf, f.ica, f.root, f.root},
		"duplicated leaf":              {f.leaf, f.ica, f.leaf, f.root},
		"reordered":                    {f.leaf, f.root, f.ica},
		"reordered four":               {f.leaf, f.ica, f.root, f.ica},
		"third ICA":                    {f.leaf, f.ica, f.ica, f.ica, f.root},
		"foreign trailing material":    {f.leaf, f.ica, f.root, foreign.root},
		"another leaf subject":         {f.signedLeaf(t, "service:other", false), f.ica, f.root},
		"CA leaf":                      {f.signedLeaf(t, "service:deployment-bootstrap", true), f.ica, f.root},
	} {
		t.Run(name, func(t *testing.T) {
			if managedLateBootstrapChainMatches(chain, f.ceremony) {
				t.Fatal("unexpected certificate composition accepted")
			}
		})
	}
}

func TestManagedLateBootstrapChainRequiresOriginalRootPinAndRealSignatures(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	for _, mode := range []string{"changed Root pin", "missing Root pin", "leaf signature", "ICA signature", "Root signature", "invalid leaf DER", "invalid ICA PEM", "invalid Root PEM"} {
		t.Run(mode, func(t *testing.T) {
			ceremony := f.ceremony
			chain := [][]byte{slices.Clone(f.leaf), slices.Clone(f.ica), slices.Clone(f.root)}
			switch mode {
			case "changed Root pin":
				ceremony.Root.RootFingerprint = strings.Repeat("f", 64)
			case "missing Root pin":
				ceremony.Root.RootFingerprint = ""
			case "leaf signature":
				chain[0][len(chain[0])-1] ^= 1
			case "ICA signature":
				chain[1][len(chain[1])-1] ^= 1
				ceremony.Intermediate.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: chain[1]}))
			case "Root signature":
				chain[2][len(chain[2])-1] ^= 1
				ceremony.Root.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: chain[2]}))
				ceremony.Root.RootFingerprint = environmentPreflightBytesSHA256(chain[2])
			case "invalid leaf DER":
				chain[0] = []byte("invalid")
			case "invalid ICA PEM":
				ceremony.Intermediate.Certificate = "invalid"
			case "invalid Root PEM":
				ceremony.Root.Certificate = "invalid"
			}
			if managedLateBootstrapChainMatches(chain, ceremony) {
				t.Fatal("unverified lineage accepted")
			}
		})
	}
}

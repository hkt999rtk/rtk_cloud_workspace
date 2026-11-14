package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenBaoPublicCAOverlapBootstrapManagedControllerRestart(t *testing.T) {
	now := time.Now().UTC()
	makeAuthority := func(serial int64) (*x509.Certificate, *ecdsa.PrivateKey, string) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "isolated-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	old, oldKey, oldPEM := makeAuthority(1)
	managed, managedKey, managedPEM := makeAuthority(2)
	makeServer := func(ca *x509.Certificate, key *ecdsa.PrivateKey, serial int64) *tls.Certificate {
		t.Helper()
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return &tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: leafKey}
	}
	var selected atomic.Pointer[tls.Certificate]
	selected.Store(makeServer(old, oldKey, 11))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return selected.Load(), nil }}
	server.StartTLS()
	defer server.Close()
	probe := func(ca string) error {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(ca)) {
			t.Fatal("invalid CA")
		}
		client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}, DisableKeepAlives: true}}
		res, err := client.Get(server.URL)
		if err == nil {
			res.Body.Close()
		}
		return err
	}
	if err := probe(oldPEM); err != nil {
		t.Fatal("bootstrap unavailable", err)
	}
	selected.Store(makeServer(managed, managedKey, 12))
	if err := probe(oldPEM); err == nil {
		t.Fatal("old CA accepted managed server")
	}
	bundle, err := openBaoCAOverlap(oldPEM, managedPEM, oldPEM, environmentPreflightBytesSHA256(managed.Raw), now)
	if err != nil {
		t.Fatal(err)
	}
	if err = probe(bundle); err != nil {
		t.Fatal("new Controller transport rejected managed server", err)
	}
	selected.Store(makeServer(old, oldKey, 13))
	if err = probe(bundle); err != nil {
		t.Fatal("overlap rejected bootstrap rollback", err)
	}
}

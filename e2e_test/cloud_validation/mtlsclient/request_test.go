package mtlsclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestUsesMTLSAndPublishesProtectedResponse(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, caPEM := newCertificateAuthority(t)
	clientCert, clientKey := issueCertificate(t, ca, caKey, "device-001", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 || r.TLS.PeerCertificates[0].Subject.CommonName != "device-001" {
			t.Fatalf("client identity was not authenticated: %#v", r.TLS)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"issued"}`))
	}))
	server.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  certPool(t, caPEM),
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()

	serverCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	certPath := writeTestFile(t, dir, "device.crt", clientCert)
	keyPath := writeTestFile(t, dir, "device.key", clientKey)
	caPath := writeTestFile(t, dir, "server-ca.crt", serverCA)
	requestPath := writeTestFile(t, dir, "request.json", []byte(`{"scope":"device"}`))
	outputPath := filepath.Join(dir, "nested", "response.json")

	if err := Request(context.Background(), server.URL, certPath, keyPath, caPath, requestPath, outputPath, time.Second); err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"token":"issued"}` {
		t.Fatalf("response = %q", got)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("response mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRequestRejectsInvalidInputsAndResponses(t *testing.T) {
	dir := t.TempDir()
	requestPath := writeTestFile(t, dir, "request.json", []byte(`{}`))
	missing := filepath.Join(dir, "missing.pem")
	if err := Request(context.Background(), "https://example.test", missing, missing, "", requestPath, filepath.Join(dir, "out"), time.Second); err == nil || !strings.Contains(err.Error(), "load client identity") {
		t.Fatalf("missing identity error = %v", err)
	}

	ca, caKey, caPEM := newCertificateAuthority(t)
	clientCert, clientKey := issueCertificate(t, ca, caKey, "device-002", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	certPath := writeTestFile(t, dir, "device.crt", clientCert)
	keyPath := writeTestFile(t, dir, "device.key", clientKey)
	badCA := writeTestFile(t, dir, "bad-ca.crt", []byte("not a certificate"))
	if err := Request(context.Background(), "https://example.test", certPath, keyPath, badCA, requestPath, filepath.Join(dir, "out"), time.Second); err == nil || !strings.Contains(err.Error(), "contains no certificates") {
		t.Fatalf("invalid CA error = %v", err)
	}

	for _, tc := range []struct {
		name string
		body []byte
		code int
		want string
	}{
		{name: "HTTP failure", body: []byte(`{"error":"denied"}`), code: http.StatusForbidden, want: "HTTP 403"},
		{name: "oversized response", body: make([]byte, maxResponseBytes+1), code: http.StatusOK, want: "response exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write(tc.body)
			}))
			server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: certPool(t, caPEM)}
			server.StartTLS()
			defer server.Close()
			serverCA := writeTestFile(t, dir, strings.ReplaceAll(tc.name, " ", "-")+".crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
			err := Request(context.Background(), server.URL, certPath, keyPath, serverCA, requestPath, filepath.Join(dir, tc.name+".json"), 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Request() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func newCertificateAuthority(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func issueCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func certPool(t *testing.T, pemBytes []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("failed to add test CA")
	}
	return pool
}

func writeTestFile(t *testing.T, dir, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

package factoryenrolltest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignHMACMatchesCanonicalFactoryEnrollmentExample(t *testing.T) {
	body := []byte(`{"request_id":"factory-req-001","devid":"factory-device-001"}`)
	got := SignHMAC([]byte("factory-secret"), http.MethodPost, enrollPath, "2026-05-15T00:00:00Z", "factory-req-001", body)
	want := "v1=f071ab74238e71f4d8c16e1335ef2b20d4716572f99b76054e5bd7f7729d15d7"
	if got != want {
		t.Fatalf("SignHMAC() = %q, want %q", got, want)
	}
}

func TestGenerateDeviceIdentityUsesPublicKeyFingerprintAsDeviceID(t *testing.T) {
	key, deviceID, csrPEM, _, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatalf("GenerateDeviceIdentity() error = %v", err)
	}
	if !strings.HasPrefix(deviceID, "pk-") || len(deviceID) != 67 {
		t.Fatalf("device id = %q", deviceID)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		t.Fatal("missing CSR PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificateRequest() error = %v", err)
	}
	if csr.Subject.CommonName != deviceID {
		t.Fatalf("CSR CN = %q, want %q", csr.Subject.CommonName, deviceID)
	}
	if csr.PublicKey.(*ecdsa.PublicKey).X.Cmp(key.PublicKey.X) != 0 {
		t.Fatal("CSR public key does not match generated key")
	}
}

func TestRunnerEnrollsMultipleDevicesAndValidatesCertificates(t *testing.T) {
	signer := newTestSigner(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != enrollPath {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Video-Cloud-Signature"); !strings.HasPrefix(got, "v1=") {
			t.Fatalf("missing signature: %q", got)
		}
		var in EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		certPEM := signer.signCSR(t, in.DeviceID, in.CSRPem)
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			RequestID:           in.RequestID,
			IssuerRequestID:     in.RequestID,
			DeviceID:            in.DeviceID,
			SerialNumber:        in.SerialNumber,
			NotBefore:           time.Now().UTC(),
			NotAfter:            time.Now().Add(time.Hour).UTC(),
			CertificatePEM:      string(certPEM),
			CertificateChainPEM: string(signer.caPEM),
			IssuedAt:            time.Now().UTC(),
		})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		FactoryURL:  server.URL,
		AuthKey:     "factory-secret",
		Count:       5,
		RunID:       "test-run",
		Concurrency: 2,
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Summary.Successes != 5 || result.Summary.Failures != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	for _, device := range result.Devices {
		if !device.Success || !device.ClientAuthUsable || device.CA {
			t.Fatalf("device result = %+v", device)
		}
	}
}

type testSigner struct {
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
	caPEM []byte
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{ca: cert, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (s *testSigner) signCSR(t *testing.T, deviceID, rawCSR string) []byte {
	t.Helper()
	block, _ := pem.Decode([]byte(rawCSR))
	if block == nil {
		t.Fatal("missing csr")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != deviceID {
		t.Fatalf("csr cn = %q, want %q", csr.Subject.CommonName, deviceID)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: csr.Subject, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, s.ca, csr.PublicKey, s.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

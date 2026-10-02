package cloudmonitor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPExpectedCodeContentAndFallback(t *testing.T) {
	cases := []struct {
		name               string
		code               int
		body, ct, contains string
		want               Status
	}{{"logger", 204, "", "", "", Pass}, {"content", 200, `{"ready":false}`, "application/json", `"ready":true`, Fail}, {"login", 200, `<input type="password">`, "text/html", "", Fail}, {"unauthorized", 401, "DO_NOT_EXPOSE_TOKEN", "text/plain", "", Unknown}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer s.Close()
			c := CollectHTTP(context.Background(), Config{HTTP: []HTTPCheck{{Name: "probe", Service: "service", URL: s.URL, SuccessCodes: []int{200, 204}, Contains: tc.contains, Required: true}}}, Inventory{}, Runtime{}, time.Now())
			r := c.Results[0]
			if r.Status != tc.want {
				t.Fatalf("%s: %s", r.Status, r.Reason)
			}
			if r.Reason == tc.body {
				t.Fatal("body leak")
			}
		})
	}
}
func TestHTTPRedirectNotFollowed(t *testing.T) {
	destinationHit := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			destinationHit = true
			w.Write([]byte("ok"))
			return
		}
		http.Redirect(w, r, "/login", 302)
	}))
	defer s.Close()
	c := CollectHTTP(context.Background(), Config{HTTP: []HTTPCheck{{Name: "probe", Service: "s", URL: s.URL, SuccessCodes: []int{200}, Required: true}}}, Inventory{}, Runtime{}, time.Now())
	if destinationHit || c.Results[0].Status != Fail {
		t.Fatal("redirect must remain failure without follow")
	}
}

func publicFixture(t *testing.T, root, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), b, 0644); err != nil {
		t.Fatal(err)
	}
}
func makePKIFixture(t *testing.T, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, *x509.Certificate, []byte, []byte) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3}}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "service.test"}, DNSNames: []string{"service.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	return ca, caKey, leaf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
}
func TestTLSLocalChainHostnameFingerprintAndRevocation(t *testing.T) {
	now := time.Now().UTC()
	ca, key, leaf, caPEM, leafPEM := makePKIFixture(t, now)
	root := t.TempDir()
	publicFixture(t, root, "ca.pem", caPEM)
	publicFixture(t, root, "leaf.pem", leafPEM)
	crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Hour), NextUpdate: now.Add(72 * time.Hour), RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: leaf.SerialNumber, RevocationTime: now.Add(-time.Minute)}}}, ca, key)
	if err != nil {
		t.Fatal(err)
	}
	publicFixture(t, root, "crl.pem", pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER}))
	cfg := Config{TLS: []TLSCheck{{Name: "s", Service: "s", CertFile: "leaf.pem", CAFile: "ca.pem", ServerName: "service.test", CRLFiles: []string{"crl.pem"}, Required: true}}}
	c := CollectTLS(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "tls/s").Status != Pass {
		t.Fatal("chain should verify")
	}
	if findCollectorResult(t, c, "tls/s/crl/a").Status != Fail {
		t.Fatal("revoked serial must fail")
	}
	cfg.TLS[0].ServerName = "wrong.test"
	c = CollectTLS(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "tls/s").Status != Fail {
		t.Fatal("hostname must fail")
	}
	cfg.TLS[0].ServerName = "service.test"
	cfg.TLS[0].ExpectedFingerprint = "wrong"
	c = CollectTLS(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "tls/s").Status != Fail {
		t.Fatal("installed fingerprint mismatch must fail")
	}
}
func TestManagedCertNeedsRenewalEvidence(t *testing.T) {
	now := time.Now()
	_, _, _, ca, leaf := makePKIFixture(t, now)
	root := t.TempDir()
	publicFixture(t, root, "ca", ca)
	publicFixture(t, root, "leaf", leaf)
	c := CollectTLS(context.Background(), Config{TLS: []TLSCheck{{Name: "m", Service: "s", Role: "managed", CertFile: "leaf", CAFile: "ca", Required: true}}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "tls/m/renewal").Status != Unknown {
		t.Fatal("managed renewal gap must be unknown")
	}
}

func TestCollectorGlobalConcurrencyBudget(t *testing.T) {
	var active, peak atomic.Int32
	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		if r.URL.Path == "/api/v1/query" {
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"scalar","result":[%d,"0"]}}`, now.Unix())
			return
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	cfg := Config{Concurrency: 3}
	warn := 10.0
	for i := 0; i < 6; i++ {
		cfg.HTTP = append(cfg.HTTP, HTTPCheck{Name: fmt.Sprint(i), Service: "s", URL: server.URL, SuccessCodes: []int{200}})
		cfg.Metrics = append(cfg.Metrics, MetricCheck{Name: fmt.Sprint(i), Service: "m", URL: server.URL, Query: "value", WarnAbove: &warn})
	}
	CollectReadOnly(context.Background(), cfg, Inventory{}, Runtime{}, &collectorRunner{}, now)
	if peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("global parallel request budget expected2-3, observed%d", peak.Load())
	}
}

func TestMTLSClientIdentityChecksPurposeExpiryRevocationAndAcceptance(t *testing.T) {
	now := time.Now().UTC()
	ca, key, _, caPEM, _ := makePKIFixture(t, now)
	root := t.TempDir()
	publicFixture(t, root, "ca", caPEM)
	for _, tc := range []struct {
		name    string
		eku     x509.ExtKeyUsage
		expires time.Time
		revoked bool
		want    Status
	}{{"valid", x509.ExtKeyUsageClientAuth, now.Add(60 * 24 * time.Hour), false, Pass}, {"wrong-purpose", x509.ExtKeyUsageServerAuth, now.Add(60 * 24 * time.Hour), false, Fail}, {"expired", x509.ExtKeyUsageClientAuth, now.Add(-time.Minute), false, Fail}, {"revoked", x509.ExtKeyUsageClientAuth, now.Add(60 * 24 * time.Hour), true, Pass}} {
		t.Run(tc.name, func(t *testing.T) {
			clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			template := &x509.Certificate{SerialNumber: big.NewInt(20), Subject: pkix.Name{CommonName: "monitor-client"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: tc.expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{tc.eku}}
			der, err := x509.CreateCertificate(rand.Reader, template, ca, &clientKey.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			publicFixture(t, root, "client", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			crl := &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Hour), NextUpdate: now.Add(72 * time.Hour)}
			if tc.revoked {
				crl.RevokedCertificateEntries = []x509.RevocationListEntry{{SerialNumber: big.NewInt(20), RevocationTime: now.Add(-time.Minute)}}
			}
			crlDER, err := x509.CreateRevocationList(rand.Reader, crl, ca, key)
			if err != nil {
				t.Fatal(err)
			}
			publicFixture(t, root, "client-crl", pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER}))
			c := collectMTLSIdentity(TLSCheck{Name: "m", Service: "s", Address: "service:443", ClientCertFile: "client", ClientCAFile: "ca", ClientCRLFiles: []string{"client-crl"}, Required: true}, Runtime{ConfigRoot: root}, now)
			if r := findCollectorResult(t, c, "tls/m/client-chain"); r.Status != tc.want {
				t.Fatalf("clientchain %s:%s", r.Status, r.Reason)
			}
			if r := findCollectorResult(t, c, "tls/m/mtls-acceptance"); r.Status != Unknown || !r.Required {
				t.Fatal("TLS handshake alone cannot prove client acceptance")
			}
			if tc.revoked && findCollectorResult(t, c, "tls/m/client/crl/a").Status != Fail {
				t.Fatal("revoked client must fail")
			}
			if tc.name == "expired" {
				for _, r := range c.Results {
					if strings.HasPrefix(r.CheckID, "tls/m/client-expiry/") && r.Fingerprint == fingerprintCertMustParse(t, der) && r.Status != Fail {
						t.Fatal("expired client expiry must fail")
					}
				}
			}
		})
	}
}
func fingerprintCertMustParse(t *testing.T, raw []byte) string {
	t.Helper()
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprintCert(cert)
}
func TestStoredRenewalCannotProveLoadedCertificateAndFutureCannotPass(t *testing.T) {
	now := time.Now()
	_, _, leaf, ca, cert := makePKIFixture(t, now)
	root := t.TempDir()
	publicFixture(t, root, "ca", ca)
	publicFixture(t, root, "cert", cert)
	for _, future := range []bool{false, true} {
		last := now.Add(-time.Minute)
		if future {
			last = now.Add(time.Hour)
		}
		meta, _ := json.Marshal(map[string]any{"last_succeeded_at": last, "installed_fingerprint": fingerprintCert(leaf)})
		publicFixture(t, root, "renewal", meta)
		c := CollectTLS(context.Background(), Config{TLS: []TLSCheck{{Name: "stored", Service: "s", CertFile: "cert", CAFile: "ca", RenewalFile: "renewal", Required: true}}}, Runtime{ConfigRoot: root}, now)
		r := findCollectorResult(t, c, "tls/stored")
		if r.Source != "stored-certificate" {
			t.Fatal("must distinguish stored from active")
		}
		if findCollectorResult(t, c, "tls/stored/renewal").Status != Unknown {
			t.Fatal("metadata is not proof of loaded certificate")
		}
	}
}

func TestMTLSApplicationAcceptanceRequiresAuthenticatedPositiveAndNegativeControl(t *testing.T) {
	now := time.Now()
	ca, caKey, _, caPEM, _ := makePKIFixture(t, now)
	root := t.TempDir()
	publicFixture(t, root, "ca", caPEM)
	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(21), Subject: pkix.Name{CommonName: "monitor"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, _ := x509.CreateCertificate(rand.Reader, clientTemplate, ca, &clientKey.PublicKey, caKey)
	publicFixture(t, root, "client", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}))
	clientKeyDER, _ := x509.MarshalPKCS8PrivateKey(clientKey)
	os.WriteFile(filepath.Join(root, "client-key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyDER}), 0600)
	os.WriteFile(filepath.Join(root, "token"), []byte("probe-bearer"), 0600)
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(22), Subject: pkix.Name{CommonName: "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, _ := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	pair := tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, tc := range []struct {
		name                      string
		required, guard, fallback bool
		want                      Status
	}{{"tls13-required", true, false, false, Pass}, {"application-requires-client", false, true, false, Pass}, {"optional-public", false, false, false, Unknown}, {"fallback-body", true, false, true, Fail}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer probe-bearer" {
					t.Error("positive and negative must use same bearer")
				}
				if tc.guard && len(r.TLS.PeerCertificates) == 0 {
					w.WriteHeader(401)
					return
				}
				if tc.fallback {
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte(`<div id="root"></div>`))
					return
				}
				w.Write([]byte(`{"authenticated":true}`))
			}))
			auth := tls.VerifyClientCertIfGiven
			if tc.required {
				auth = tls.RequireAndVerifyClientCert
			}
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: auth, ClientCAs: pool}
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.StartTLS()
			defer server.Close()
			check := TLSCheck{Name: "m", Service: "s", Address: server.Listener.Addr().String(), ServerName: "127.0.0.1", CAFile: "ca", ClientCAFile: "ca", ClientCertFile: "client", ClientKeyFile: "client-key", ApplicationURL: server.URL, ApplicationContains: `"authenticated":true`, ApplicationTokenFile: "token", Required: true}
			r := collectMTLSApplication(context.Background(), Config{TimeoutSeconds: 2}, check, Runtime{ConfigRoot: root}, now)
			if r.Status != tc.want {
				t.Fatalf("acceptance %s:%s", r.Status, r.Reason)
			}
			c := CollectTLS(context.Background(), Config{TimeoutSeconds: 2, TLS: []TLSCheck{check}}, Runtime{ConfigRoot: root}, now)
			if r := findCollectorResult(t, c, "tls/m/mtls-acceptance"); r.Status != tc.want {
				t.Fatalf("collection acceptance %s:%s", r.Status, r.Reason)
			}
		})
	}
}
func TestMTLSNegativeControlNetworkErrorsDoNotProveRejection(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{{tls.AlertError(116), true}, {tls.AlertError(42), true}, {tls.AlertError(48), false}, {context.DeadlineExceeded, false}, {&net.DNSError{Name: "unavailable", IsNotFound: true}, false}, {&net.OpError{Op: "remote error", Err: errors.New("tls: certificate required")}, true}, {errors.New("timeout includes misleading tls: bad certificate"), false}} {
		if explicitClientCertificateRejection(tc.err) != tc.want {
			t.Fatal("only explicit remote certificate alert proves negative control")
		}
	}
}

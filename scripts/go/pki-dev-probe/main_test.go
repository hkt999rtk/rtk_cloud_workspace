package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSProbeReportsActuallyServedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	pair := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(dir, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.CreateTemp(dir, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	beforeIn, beforeOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, output
	defer func() { os.Stdin, os.Stdout = beforeIn, beforeOut }()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err = runTLS([]string{certPath, certPath, keyPath, "example.com", port, "/"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status int    `json:"status"`
		Peer   string `json:"peer_sha256"`
	}
	if err = json.NewDecoder(output).Decode(&result); err != nil || result.Status != 200 || result.Peer != fmt.Sprintf("%x", sha256.Sum256(pair.Certificate[0])) {
		t.Fatalf("actual TLS peer evidence differs: %+v %v", result, err)
	}
}

func TestCertificateDenialRequiresRemoteCertificateAlert(t *testing.T) {
	for _, tc := range []struct {
		err    error
		denied bool
	}{
		{&url.Error{Op: "Post", Err: &net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}}, true},
		{&net.OpError{Op: "remote error", Err: errors.New("tls: internal error")}, false},
		{&net.OpError{Op: "read", Err: io.EOF}, false},
		{errors.New("tls: bad certificate"), false},
		{x509.UnknownAuthorityError{}, false},
	} {
		if certificateRejected(tc.err) != tc.denied {
			t.Fatalf("incorrect rejection evidence for %T", tc.err)
		}
	}
}

func TestMQTTFrameBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"empty", nil, false},
		{"truncated length", []byte{0x30, 0x80}, false},
		{"truncated payload", []byte{0x30, 3, 1}, false},
		{"too many length bytes", []byte{0x30, 0x80, 0x80, 0x80, 0x80, 0}, false},
		{"oversized payload", append([]byte{0x30}, variable((1<<20)+1)...), false},
		{"valid frame", []byte{0x20, 2, 0, 5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := mqttWire{r: bufio.NewReader(bytes.NewReader(tc.raw))}
			_, _, err := w.read()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type shortConn struct {
	net.Conn
	out  bytes.Buffer
	zero bool
}

func (c *shortConn) Write(b []byte) (int, error) {
	if c.zero {
		return 0, nil
	}
	return c.out.Write(b[:1])
}
func (*shortConn) SetWriteDeadline(time.Time) error { return nil }
func TestMQTTHandlesShortWrites(t *testing.T) {
	conn := &shortConn{}
	w := mqttWire{Conn: conn}
	if err := w.send(0x30, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(conn.out.Bytes(), append([]byte{0x30, 7}, []byte("payload")...)) {
		t.Fatal("truncated packet")
	}
	conn.zero = true
	if err := w.send(0, nil); err != io.ErrShortWrite {
		t.Fatalf("zero progress write: %v", err)
	}
}

func TestCRLPreservesSignedRevocations(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(4),
		ThisUpdate: now, NextUpdate: now.Add(time.Hour), RevokedCertificateEntries: []x509.RevocationListEntry{
			{SerialNumber: big.NewInt(0xabcdef), RevocationTime: now.Add(-time.Minute), ReasonCode: 5},
			{SerialNumber: big.NewInt(3), RevocationTime: now, ReasonCode: 0},
		}}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crl})
	var out bytes.Buffer
	if err := readCRL(bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Serial string `json:"serial_hex"`
		At     string `json:"revoked_at"`
		Reason int    `json:"reason_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Serial != "abcdef" || entries[0].Reason != 5 || entries[0].At != now.Add(-time.Minute).Format(time.RFC3339) || entries[1].Reason != 0 {
		t.Fatal("revocation details changed")
	}
	for _, bad := range [][]byte{nil, []byte("invalid"), append(raw, raw...), append(raw, []byte("unexpected")...), bytes.Repeat([]byte{'x'}, (1<<20)+1)} {
		if err := readCRL(bytes.NewReader(bad), io.Discard); err == nil {
			t.Fatal("accepted malformed or multiple CRLs")
		}
	}
}

func TestPublicationRequiresExactTenantTopicPayloadAndQoS(t *testing.T) {
	topic := "_bc/cloud-a/devices/device-a/test"
	packet := append(str(topic), 0, 42)
	packet = append(packet, []byte("expected")...)
	id, err := publication(0x32, packet, topic, "expected")
	if err != nil || !bytes.Equal(id, []byte{0, 42}) {
		t.Fatalf("valid rewritten publication: %v", err)
	}
	for _, tc := range []struct {
		kind           byte
		packet         []byte
		topic, payload string
	}{
		{0x32, packet, "_bc/cloud-b/devices/device-a/test", "expected"},
		{0x32, packet, topic, "different"},
		{0x30, packet, topic, "expected"},
		{0x34, packet, topic, "expected"},
		{0x32, []byte{0, 255}, topic, "expected"},
		{0x32, append(append(str(topic), 0, 0), []byte("expected")...), topic, "expected"},
	} {
		if _, err := publication(tc.kind, tc.packet, tc.topic, tc.payload); err == nil {
			t.Fatal("accepted invalid publication")
		}
	}
}

func TestTLS13CertificateRejectionIsReadBeforeHTTP(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certFile, keyFile := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	for _, deny := range []bool{true, false} {
		t.Run(fmt.Sprint(deny), func(t *testing.T) {
			config := &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS13}
			if deny {
				config.VerifyPeerCertificate = func([][]byte, [][]*x509.Certificate) error { return errors.New("fixture rejects client") }
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if conn.(*tls.Conn).Handshake() == nil {
					io.Copy(io.Discard, conn)
				}
			}()
			_, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			err = runTLS([]string{certFile, certFile, keyFile, "localhost", port, "/unused"}, true)
			if (err == nil) != deny {
				t.Fatalf("denial=%v, result=%v", deny, err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("TLS fixture did not stop")
			}
		})
	}
}

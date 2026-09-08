package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

type mqttWire struct {
	net.Conn
	r  *bufio.Reader
	mu sync.Mutex
}

func variable(n int) []byte {
	b := []byte{}
	for {
		v := byte(n % 128)
		n /= 128
		if n > 0 {
			v |= 128
		}
		b = append(b, v)
		if n == 0 {
			return b
		}
	}
}
func str(s string) []byte {
	b := []byte{byte(len(s) >> 8), byte(len(s))}
	return append(b, []byte(s)...)
}
func (w *mqttWire) send(kind byte, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.SetWriteDeadline(time.Now().Add(5 * time.Second))
	packet := append(append([]byte{kind}, variable(len(p))...), p...)
	for len(packet) > 0 {
		n, err := w.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}
func (w *mqttWire) read() (byte, []byte, error) {
	kind, err := w.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, m := 0, 1
	for i := 0; i < 4; i++ {
		b, err := w.r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(b&127) * m
		if b&128 == 0 {
			if n > 1<<20 {
				return 0, nil, fmt.Errorf("oversized broker packet")
			}
			p := make([]byte, n)
			_, err = io.ReadFull(w.r, p)
			return kind, p, err
		}
		m *= 128
	}
	return 0, nil, fmt.Errorf("invalid remaining length")
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pki-dev-probe mqtt | crl | tls CA CHAIN KEY HOST PORT PATH")
	}
	if args[0] == "tls" || args[0] == "tls-denial" {
		return runTLS(args[1:], args[0] == "tls-denial")
	}
	if len(args) == 1 && args[0] == "crl" {
		return readCRL(os.Stdin, os.Stdout)
	}
	if len(args) != 1 || args[0] != "mqtt" {
		return fmt.Errorf("unknown probe mode")
	}
	return runMQTT()
}
func runMQTT() error {
	var c struct{ CA, Host, Port, Username, Password, ClientID, Topic, DeliveryTopic, Payload, Mode string }
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&c); err != nil {
		return err
	}
	switch c.Mode {
	case "connect", "roundtrip", "hold", "lease":
	default:
		return fmt.Errorf("invalid MQTT probe mode")
	}
	if c.DeliveryTopic == "" {
		c.DeliveryTopic = c.Topic
	}
	if c.Host == "" || c.Port == "" || len(c.ClientID) > 65535 || len(c.Username) > 65535 || len(c.Password) > 65535 || len(c.Topic) > 65535 {
		return fmt.Errorf("invalid MQTT configuration")
	}
	ca, err := os.ReadFile(c.CA)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("invalid MQTT server CA")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", "127.0.0.1:"+c.Port, &tls.Config{RootCAs: roots, ServerName: c.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("verified MQTT TLS failed: %w", err)
	}
	defer conn.Close()
	w := &mqttWire{Conn: conn, r: bufio.NewReader(conn)}
	w.SetReadDeadline(time.Now().Add(15 * time.Second))
	packet := append(str("MQTT"), 4, 0xc2, 0, 20)
	packet = append(packet, str(c.ClientID)...)
	packet = append(packet, str(c.Username)...)
	packet = append(packet, str(c.Password)...)
	if err := w.send(0x10, packet); err != nil {
		return err
	}
	kind, p, err := w.read()
	if err != nil {
		return fmt.Errorf("MQTT CONNACK read failed: %w", err)
	}
	if kind != 0x20 || len(p) != 2 {
		return fmt.Errorf("invalid CONNACK")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.Encode(map[string]any{"event": "connack", "code": p[1], "at": time.Now().UTC()})
	if p[1] != 0 {
		return nil
	}
	if c.Mode == "connect" {
		return w.send(0xe0, nil)
	}
	// Subscribe to the exact test topic, then check forbidden cross-namespace filters.
	for n, topic := range []string{c.Topic, "_bc/other/#", "$aws/#"} {
		id := uint16(n + 1)
		body := append([]byte{byte(id >> 8), byte(id)}, str(topic)...)
		body = append(body, 1)
		if err := w.send(0x82, body); err != nil {
			return err
		}
		kind, p, err := w.read()
		if err != nil || kind != 0x90 || len(p) != 3 || binary.BigEndian.Uint16(p[:2]) != id {
			return fmt.Errorf("invalid SUBACK")
		}
		want := byte(0x80)
		if n == 0 {
			want = 1
		}
		if p[2] != want {
			return fmt.Errorf("unexpected subscription ACL result %d for test %d", p[2], n)
		}
	}
	body := append(str(c.Topic), 0, 10)
	body = append(body, []byte(c.Payload)...)
	if err := w.send(0x32, body); err != nil {
		return err
	}
	acknowledged, received := false, false
	for !acknowledged || !received {
		kind, p, err := w.read()
		if err != nil {
			return err
		}
		switch kind >> 4 {
		case 4:
			if len(p) != 2 || binary.BigEndian.Uint16(p) != 10 {
				return fmt.Errorf("invalid PUBACK")
			}
			acknowledged = true
		case 3:
			id, err := publication(kind, p, c.DeliveryTopic, c.Payload)
			if err != nil {
				return err
			}
			if err := w.send(0x40, id); err != nil {
				return err
			}
			received = true
		default:
			return fmt.Errorf("unexpected MQTT response")
		}
	}
	enc.Encode(map[string]any{"event": "acl_and_roundtrip_passed", "at": time.Now().UTC()})
	if c.Mode == "roundtrip" {
		return w.send(0xe0, nil)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if w.send(0xc0, nil) != nil {
					return
				}
			}
		}
	}()
	w.SetReadDeadline(time.Now().Add(80 * time.Second))
	for {
		kind, _, err := w.read()
		if err != nil {
			if n, ok := err.(net.Error); ok && n.Timeout() {
				return fmt.Errorf("MQTT session was not closed within deadline")
			}
			var networkError *net.OpError
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.As(err, &networkError) {
				return fmt.Errorf("invalid broker response during session: %w", err)
			}
			enc.Encode(map[string]any{"event": "closed", "at": time.Now().UTC()})
			return nil
		}
		if kind == 0xe0 {
			enc.Encode(map[string]any{"event": "closed", "at": time.Now().UTC()})
			return nil
		}
	}
}

// Preserve signed revocation entries when preparing the next offline ceremony.
// The runner must separately compare this CRL with the live registry digest.
func readCRL(in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("invalid CRL size")
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("expected one PEM CRL")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		return fmt.Errorf("invalid CRL")
	}
	entries := []map[string]any{}
	for _, entry := range crl.RevokedCertificateEntries {
		for _, extension := range entry.Extensions {
			if extension.Id.String() != "2.5.29.21" {
				return fmt.Errorf("unsupported revocation extension; preserve for manual ceremony")
			}
		}
		entries = append(entries, map[string]any{
			"serial_hex":  entry.SerialNumber.Text(16),
			"revoked_at":  entry.RevocationTime.UTC().Format(time.RFC3339),
			"reason_code": entry.ReasonCode,
		})
	}
	return json.NewEncoder(out).Encode(entries)
}

func publication(kind byte, p []byte, topic, payload string) ([]byte, error) {
	if kind&6 != 2 {
		return nil, fmt.Errorf("publication did not use QoS 1")
	}
	if len(p) < 2 {
		return nil, fmt.Errorf("invalid publication")
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+n+2 {
		return nil, fmt.Errorf("short publication")
	}
	if string(p[2:2+n]) != topic {
		return nil, fmt.Errorf("publication topic mismatch")
	}
	id := p[2+n : 4+n]
	if binary.BigEndian.Uint16(id) == 0 {
		return nil, fmt.Errorf("invalid publication packet ID")
	}
	if string(p[4+n:]) != payload {
		return nil, fmt.Errorf("publication payload mismatch")
	}
	return id, nil
}

func runTLS(args []string, denialOnly bool) error {
	if len(args) != 6 {
		return fmt.Errorf("TLS probe requires CA, chain, key, host, local port and path")
	}
	ca, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("invalid TLS CA")
	}
	pair, err := tls.LoadX509KeyPair(args[1], args[2])
	if err != nil {
		return fmt.Errorf("load client identity: %w", err)
	}
	if denialOnly {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort("127.0.0.1", args[4]),
			&tls.Config{RootCAs: roots, ServerName: args[3], MinVersion: tls.VersionTLS12,
				// Present the selected identity even after its CA was removed from
				// the server's advertised list. Missing-client alerts prove nothing
				// about rejection of the certificate this probe was asked to test.
				GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }})
		if err == nil {
			defer conn.Close()
			// In TLS 1.3 the server may reject the client certificate after the
			// client's handshake returns. Read its alert before writing HTTP so
			// a racing broken-pipe write cannot hide that specific rejection.
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err = conn.Read(make([]byte, 1))
		}
		if !certificateRejected(err) {
			return fmt.Errorf("peer did not explicitly reject the client certificate")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": 0, "remote_certificate_rejected": true})
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil || len(body) > 65536 {
		return fmt.Errorf("invalid request size")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: args[3], Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", args[4]))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest("POST", "https://"+args[3]+args[5], bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid request")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		if certificateRejected(err) {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": 0, "remote_certificate_rejected": true})
		}
		return fmt.Errorf("verified TLS request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("invalid response size")
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Status     int    `json:"status"`
		Body       string `json:"body"`
		PeerSHA256 string `json:"peer_sha256"`
	}{res.StatusCode, string(raw), fmt.Sprintf("%x", sha256.Sum256(res.TLS.PeerCertificates[0].Raw))})
}

// Only a peer's explicit certificate alert is denial evidence. Local server
// trust errors, network outages and other TLS alerts must fail the probe.
func certificateRejected(err error) bool {
	var remote *net.OpError
	if !errors.As(err, &remote) || remote.Op != "remote error" {
		return false
	}
	switch remote.Err.Error() {
	case "tls: bad certificate", "tls: revoked certificate", "tls: unknown certificate", "tls: unknown certificate authority":
		return true
	}
	return false
}

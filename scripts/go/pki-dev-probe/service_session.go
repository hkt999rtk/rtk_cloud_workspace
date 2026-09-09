package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// Keep one authenticated socket across operator actions. Reconnection is only
// allowed for the explicit denial check, using the original key held in memory.
func serviceSession(args []string, input io.Reader, output io.Writer) error {
	if len(args) != 6 || !strings.HasPrefix(args[4], "/") {
		return fmt.Errorf("service-session requires state, CA, host, port, path and fingerprint")
	}
	state, _, err := loadServiceState(args[0])
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
	if err != nil || len(pair.Certificate) == 0 {
		return fmt.Errorf("invalid stored identity")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(pair.Certificate[0]))
	if state.Pending != nil || fingerprint != args[5] {
		return fmt.Errorf("session identity changed")
	}
	ca, err := os.ReadFile(args[1])
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("session CA unavailable")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: args[2],
		Certificates: []tls.Certificate{pair}, NextProtos: []string{"http/1.1"}}
	dial := func() (*tls.Conn, error) {
		return tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(args[2], args[3]), config)
	}
	exchange := func(c *tls.Conn, reader *bufio.Reader) (int, error) {
		c.SetDeadline(time.Now().Add(10 * time.Second))
		request, err := http.NewRequest(http.MethodGet, "https://"+net.JoinHostPort(args[2], args[3])+args[4], nil)
		if err != nil {
			return 0, err
		}
		if err = request.Write(c); err != nil {
			return 0, err
		}
		response, err := http.ReadResponse(reader, request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		n, err := io.Copy(io.Discard, io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || n > 1<<20 || response.Close {
			return 0, fmt.Errorf("invalid session response")
		}
		c.SetDeadline(time.Time{})
		return response.StatusCode, nil
	}
	c, err := dial()
	if err != nil {
		return fmt.Errorf("initial session TLS failed")
	}
	defer c.Close()
	reader := bufio.NewReader(c)
	status, err := exchange(c, reader)
	if err != nil || status != 200 {
		return fmt.Errorf("initial session not admitted: %d", status)
	}
	enc := json.NewEncoder(output)
	emit := func(event string) error {
		return enc.Encode(map[string]any{
			"event": event, "at": time.Now().UTC(), "fingerprint": fingerprint})
	}
	if err = emit("ready"); err != nil {
		return err
	}
	commands := bufio.NewScanner(input)
	for commands.Scan() {
		switch commands.Text() {
		case "check":
			status, err = exchange(c, reader)
			if err != nil || status != 200 {
				return fmt.Errorf("held session no longer admitted: %d", status)
			}
			err = emit("alive")
		case "closed":
			c.SetReadDeadline(time.Now().Add(25 * time.Second))
			_, readErr := reader.ReadByte()
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, syscall.ECONNRESET) {
				return fmt.Errorf("held socket did not close explicitly")
			}
			err = emit("closed")
		case "denied":
			next, denied := dial()
			code := 0
			if denied == nil {
				code, denied = exchange(next, bufio.NewReader(next))
				next.Close()
			}
			if !certificateRejected(denied) && !(denied == nil && (code == 401 || code == 403)) {
				return fmt.Errorf("old credential not explicitly denied")
			}
			err = emit("denied")
		default:
			return fmt.Errorf("unknown session command")
		}
		if err != nil {
			return err
		}
	}
	return commands.Err()
}

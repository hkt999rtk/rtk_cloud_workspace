package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceSessionKeepsSocketAndRejectsReplacedCredential(t *testing.T) {
	var denied atomic.Bool
	idle := make(chan net.Conn, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateIdle {
			idle <- c
		}
	}
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, VerifyConnection: func(tls.ConnectionState) error {
		if denied.Load() {
			return errors.New("revoked")
		}
		return nil
	}}
	server.StartTLS()
	defer server.Close()
	pair := server.TLS.Certificates[0]
	key, _ := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	raw, _ := json.Marshal(map[string]any{"current": map[string]string{
		"private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})), "certificate_chain_pem": string(cert)}})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "state"), raw, 0600)
	os.WriteFile(filepath.Join(dir, "ca"), cert, 0600)
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	input, commands := io.Pipe()
	output, events := io.Pipe()
	defer commands.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		done <- serviceSession([]string{filepath.Join(dir, "state"), filepath.Join(dir, "ca"), host, port, "/", fmt.Sprintf("%x", sha256.Sum256(pair.Certificate[0]))}, input, events)
		events.Close()
	}()
	lines := make(chan string, 8)
	go func() {
		scan := bufio.NewScanner(output)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case line := <-lines:
			var event struct{ Event string }
			if json.Unmarshal([]byte(line), &event) != nil || event.Event != want {
				t.Fatalf("event: %s", line)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("event deadline")
		}
	}
	expect("ready")
	first := <-idle
	// The installed file changes, but the held socket and denial attempt must
	// continue using the identity loaded before replacement.
	os.WriteFile(filepath.Join(dir, "state"), []byte("replacement"), 0600)
	fmt.Fprintln(commands, "check")
	expect("alive")
	if second := <-idle; second != first {
		t.Fatal("probe silently reconnected")
	}
	denied.Store(true)
	first.Close()
	fmt.Fprintln(commands, "closed")
	expect("closed")
	fmt.Fprintln(commands, "denied")
	expect("denied")
	commands.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

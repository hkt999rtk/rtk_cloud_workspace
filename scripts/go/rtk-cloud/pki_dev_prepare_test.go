package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPKIDevPreparationPersistsUsableSeparateIdentities(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pki-dev-prepare", "--environment", "dev", "--config-root", store.ConfigRoot}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dir, err := preparePKIDevMaterial(store, now)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := map[string][]byte{}
	for _, name := range pkiDevMaterialFiles {
		before[name] = read(name)
	}
	if _, err := preparePKIDevMaterial(store, now); err != nil {
		t.Fatal(err)
	}
	for name, data := range before {
		if !bytes.Equal(data, read(name)) {
			t.Fatalf("retry rotated %s", name)
		}
	}
	serverPair, err := tls.X509KeyPair(read("controller.crt"), read("controller.key"))
	if err != nil {
		t.Fatal(err)
	}
	clientPair, err := tls.X509KeyPair(read("account-manager.crt"), read("account-manager.key"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(read("management-ca.crt"))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "account-manager" {
			t.Error("missing authenticated Account Manager peer")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverPair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
	server.StartTLS()
	defer server.Close()
	for _, scenario := range []string{"valid", "missing-client", "wrong-host", "wrong-purpose"} {
		config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: pkiDevServerName, Certificates: []tls.Certificate{clientPair}}
		switch scenario {
		case "missing-client":
			config.Certificates = nil
		case "wrong-host":
			config.ServerName = "pki-controller.video-cloud-staging-video-cloud.svc"
		case "wrong-purpose":
			config.Certificates = []tls.Certificate{serverPair}
		}
		transport := &http.Transport{TLSClientConfig: config}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, err := client.Get(server.URL)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		transport.CloseIdleConnections()
		if (scenario == "valid") != (err == nil) {
			t.Fatalf("%s authentication outcome: %v", scenario, err)
		}
	}
}

func TestPKIDevPreparationRefusesReplacementAndOtherEnvironments(t *testing.T) {
	for _, env := range []string{"", "staging", "production"} {
		root := t.TempDir()
		if err := run([]string{"pki-dev-prepare", "--environment", env, "--config-root", root}); err == nil {
			t.Fatal("non-dev preparation accepted")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("rejected command changed SecretStore", err)
		}
	}
	now := time.Now().UTC()
	material, err := newPKIDevMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"partial", "mismatched-key", "shared-signer", "permissions", "expired", "symlink", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := newSecretStore(t.TempDir(), "dev")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(store.Root, "pki", "controller-bootstrap")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range material {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			checkTime := now
			var changeErr error
			switch scenario {
			case "partial":
				changeErr = os.Remove(filepath.Join(dir, "controller.key"))
			case "mismatched-key":
				changeErr = os.WriteFile(filepath.Join(dir, "controller.key"), material["account-manager.key"], 0600)
			case "shared-signer":
				for _, suffix := range []string{".key", ".pub"} {
					if err := os.WriteFile(filepath.Join(dir, "jwt-refresh"+suffix), material["jwt-access"+suffix], 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "permissions":
				changeErr = os.Chmod(filepath.Join(dir, "controller.key"), 0644)
			case "expired":
				checkTime = now.Add(91 * 24 * time.Hour)
			case "symlink":
				if err := os.Remove(filepath.Join(dir, "database-password")); err != nil {
					t.Fatal(err)
				}
				changeErr = os.Symlink(filepath.Join(dir, "controller.key"), filepath.Join(dir, "database-password"))
			case "locked":
				changeErr = os.Mkdir(dir+".lock", 0700)
			}
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if _, err := preparePKIDevMaterial(store, checkTime); err == nil {
				t.Fatal("invalid existing material replaced or accepted")
			}
			data, err := os.ReadFile(filepath.Join(dir, "management-ca.crt"))
			if err != nil || !bytes.Equal(data, material["management-ca.crt"]) {
				t.Fatal("failure rotated trust", err)
			}
		})
	}
}

package main

import (
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
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateCustodyServiceCRLPublishesOriginalProviderRecordBeforeManagedCore(t *testing.T) {
	cfg, store, backend, execution, _ := createOpenBaoNativeFixture(t, "valid")
	issuer := environmentCreateRegisteredIssuer{
		ID: "22222222-2222-4222-8222-222222222222", Environment: cfg.Environment,
		Domain: "service", Kind: "intermediate", Status: "active",
	}
	issuer.Reference = "pki-issuers/service/" + issuer.ID + "/v1"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated-signer"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCRLSign | x509.KeyUsageCertSign,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(2 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	issuer.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	this, next := now.Add(-time.Minute), now.Add(time.Hour)
	der, err = x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: this, NextUpdate: next}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	crl := string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
	tokenPath := filepath.Join(store.Root, "openbao/root-token")
	if err := store.write("openbao/root-token", []byte("fixture-only-token\n"), false); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "crl"), crl)
	tool := filepath.Join(dir, "kubectl")
	writeTestFile(t, tool, `#!/bin/sh
set -eu
IFS= read -r token
[ "$token" = fixture-only-token ]
case "$*" in
  *"bao read -field=certificate $RTK_TEST_SERVICE_MOUNT/cert/crl"*) cat "$RTK_TEST_SERVICE_CRL";;
  *) exit 1;;
esac
`)
	if err := os.Chmod(tool, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_SERVICE_MOUNT", issuer.Reference)
	t.Setenv("RTK_TEST_SERVICE_CRL", filepath.Join(dir, "crl"))
	imports := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/platform/pki/issuers/"+issuer.ID+"/crl" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var request map[string]string
			if json.NewDecoder(r.Body).Decode(&request) != nil || request["crl_pem"] != crl {
				http.Error(w, "unexpected original signer record", http.StatusBadRequest)
				return
			}
			imports <- struct{}{}
			http.Error(w, "response lost after committing", http.StatusInternalServerError)
		case http.MethodGet:
			if len(imports) == 0 {
				http.Error(w, "no current CRL", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(createRootCRLRecord{IssuerID: issuer.ID, Digest: environmentPreflightBytesSHA256(der), Number: "1", PEM: crl, ThisUpdate: this, NextUpdate: next})
		}
	}))
	defer server.Close()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := environmentCreateBootstrapRuntime{
		Execution: execution, ExecutionSHA256: execution.Journal.PlanSHA256, Backend: backend,
		Custody:   &environmentCustodyBinding{Generation: execution.Journal.PlanSHA256},
		OpenBao:   environmentCreateOpenBaoBootstrap{RootTokenPath: tokenPath},
		PKIClient: &environmentCreatePKIClient{Environment: cfg.Environment, Origin: origin, HTTP: server.Client()},
	}
	for range 2 {
		if err := publishEnvironmentCreateServiceIntermediateCRL(t.Context(), cfg, runtime, issuer); err != nil {
			t.Fatal(err)
		}
	}
	if len(imports) != 1 {
		t.Fatal("confirmed registry publication was repeated")
	}
}

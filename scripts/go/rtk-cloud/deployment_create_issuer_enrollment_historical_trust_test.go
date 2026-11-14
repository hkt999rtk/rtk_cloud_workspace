package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssuerEnrollmentReconstructsExpiredInitialTrustWithoutRenewingIt(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	past := time.Now().UTC().Add(-48 * time.Hour)
	before := map[string][]byte{}
	for _, role := range []string{"openbao", "management"} {
		material, err := createEnvironmentTransport(store, parent, role, map[string][]string{"original": {"original.video-cloud-dev-secrets.svc"}}, past)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := parseFirstCertificatePEM([]byte(material.CACert))
		if err != nil || time.Now().Before(cert.NotAfter) {
			t.Fatal("fixture authority must actually be expired", err)
		}
		for _, name := range []string{"ca.crt", "identities.json"} {
			path := filepath.Join(store.Root, "deployment/create/material", parent, role+"-transport", name)
			before[path], err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	caPath, management, err := readIssuerEnrollmentInitialTrust(store, parent)
	if err != nil {
		t.Fatal("historical reconstruction must retain expired original trust", err)
	}
	want := filepath.Join(store.Root, "deployment/create/material", parent, "openbao-transport/ca.crt")
	if caPath != want || management != string(before[filepath.Join(store.Root, "deployment/create/material", parent, "management-transport/ca.crt")]) {
		t.Fatal("historical trust selected another authority")
	}
	for path, expected := range before {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(expected) {
			t.Fatal("historical read changed original identity material", err)
		}
	}
	managementPath := filepath.Join(store.Root, "deployment/create/material", parent, "management-transport/ca.crt")
	if err := os.WriteFile(managementPath, []byte("corrupt historical public authority"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readIssuerEnrollmentInitialTrust(store, parent); err == nil {
		t.Fatal("historical reconstruction accepted an invalid authority")
	}
}

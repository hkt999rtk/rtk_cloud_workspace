package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDeviceRootID = "8f4fb04a-1578-4e04-b6f7-1ecdb150cb76"
const testDeviceRootFingerprint = "2f960bf9bd3bdb980394add69a159e9b9b4a89f25bfe3e35ef5c62ee49ff924f"

func productPKITestDeployment(rootID, fingerprint string) liveDeployment {
	var deployment liveDeployment
	raw := map[string]any{"metadata": map[string]any{"name": "pki-controller"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "pki-controller", "env": []any{
				map[string]string{"name": "PKI_ENVIRONMENT", "value": "staging"},
				map[string]string{"name": "PKI_DEVICE_ROOT_ID", "value": rootID},
				map[string]string{"name": "PKI_DEVICE_ROOT_SHA256", "value": fingerprint},
			}}},
		}}}}
	body, _ := json.Marshal(raw)
	if err := json.Unmarshal(body, &deployment); err != nil {
		panic(err)
	}
	return deployment
}

func TestProductPKIRootPin(t *testing.T) {
	for _, tc := range []struct {
		name, id, fingerprint, want string
	}{
		{"ready", testDeviceRootID, testDeviceRootFingerprint, ""},
		{"missing root", "", "", "no complete"},
		{"invalid ID", "not-a-uuid", testDeviceRootFingerprint, "no complete"},
		{"invalid fingerprint", testDeviceRootID, strings.Repeat("z", 64), "fingerprint is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, fingerprint, err := productPKIRootPin(productPKITestDeployment(tc.id, tc.fingerprint), "staging")
			if tc.want == "" {
				if err != nil || id != tc.id || fingerprint != tc.fingerprint {
					t.Fatalf("root pin = %q %q %v", id, fingerprint, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("root pin error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyProductPKIReadiness(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := verifyProductPKIReadiness(store); err == nil || !strings.Contains(err.Error(), "kubeconfig") {
		t.Fatalf("missing kubeconfig: %v", err)
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	deploymentPath := filepath.Join(dir, "deployment.json")
	podsPath := filepath.Join(dir, "pods.json")
	issuerPath := filepath.Join(dir, "issuer.json")
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"  *'get deployment pki-controller'*) cat \"$FAKE_DEPLOYMENT\";;\n" +
		"  *'get pods -l app.kubernetes.io/name=postgresql'*) cat \"$FAKE_PODS\";;\n" +
		"  *'exec postgresql-0'*) cat \"$FAKE_ISSUER\";;\n" +
		"  *) exit 1;;\nesac\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("FAKE_DEPLOYMENT", deploymentPath)
	t.Setenv("FAKE_PODS", podsPath)
	t.Setenv("FAKE_ISSUER", issuerPath)
	writeJSON := func(path string, value any) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(podsPath, map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "postgresql-0"}}}})
	writeJSON(deploymentPath, productPKITestDeployment("", ""))
	if err := verifyProductPKIReadiness(store); err == nil || !strings.Contains(err.Error(), "no complete") {
		t.Fatalf("unbound controller: %v", err)
	}
	writeJSON(deploymentPath, productPKITestDeployment(testDeviceRootID, testDeviceRootFingerprint))
	if err := os.WriteFile(issuerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyProductPKIReadiness(store); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing registry issuer: %v", err)
	}
	writeJSON(issuerPath, map[string]string{"environment": "staging", "trust_domain": "device",
		"kind": "root", "status": "active", "signer_provider": "openbao",
		"certificate_fingerprint_sha256": testDeviceRootFingerprint})
	if err := verifyProductPKIReadiness(store); err != nil {
		t.Fatalf("registered root: %v", err)
	}
	writeJSON(issuerPath, map[string]string{"environment": "staging", "trust_domain": "device",
		"kind": "root", "status": "retired", "signer_provider": "openbao",
		"certificate_fingerprint_sha256": testDeviceRootFingerprint})
	if err := verifyProductPKIReadiness(store); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("retired root: %v", err)
	}
}

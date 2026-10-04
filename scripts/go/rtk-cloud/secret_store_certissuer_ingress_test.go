package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func certIssuerIngressFixture(t *testing.T, namespace, backend, passthrough, host string) certIssuerIngressList {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"namespace": namespace, "annotations": map[string]string{"nginx.ingress.kubernetes.io/ssl-passthrough": passthrough}},
		"spec":     map[string]any{"ingressClassName": "nginx", "rules": []any{map[string]any{"host": host, "http": map[string]any{"paths": []any{map[string]any{"backend": map[string]any{"service": map[string]string{"name": backend}}}}}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var result certIssuerIngressList
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCertIssuerPublicIngressPreservesDirectMTLS(t *testing.T) {
	host := "certissuer.video-cloud-dev.example.test"
	for _, tc := range []struct{ name, namespace, backend, passthrough, names, trusted, failure string }{
		{"current terminating bridge", "video-cloud-dev-ingress", "public-certissuer-video-cloud", "", host, "false", "terminates caller TLS"},
		{"passthrough ExternalName bridge", "video-cloud-dev-ingress", "public-certissuer-video-cloud", "true", host, "false", "own namespace"},
		{"internal-only serving SAN", "video-cloud-dev-video-cloud", "certissuer", "true", "certissuer.video-cloud-dev-video-cloud.svc", "false", "does not cover ingress host"},
		{"forwarded identity workaround", "video-cloud-dev-video-cloud", "certissuer", "true", host, "true", "must not trust"},
		{"approved public and internal names", "video-cloud-dev-video-cloud", "certissuer", "true", "certissuer.video-cloud-dev-video-cloud.svc," + host, "false", ""},
		{"other environment", "video-cloud-staging-ingress", "public-certissuer-video-cloud", "", "", "false", ""},
		{"similar stack prefix", "video-cloud-development-ingress", "public-certissuer-video-cloud", "", "", "false", ""},
		{"other public service", "video-cloud-dev-ingress", "public-video-cloud-api-video-cloud", "", "", "false", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]string{"CERT_ISSUER_HOST_IDENTITY_STATE": "/private/state.json", "CERT_ISSUER_HOST_DNS_NAMES": tc.names, "CERT_ISSUER_TRUSTED_HEADER_ENABLED": tc.trusted}
			err := validateCertIssuerPublicIngress("video-cloud-dev", settings, certIssuerIngressFixture(t, tc.namespace, tc.backend, tc.passthrough, host))
			if tc.failure == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("expected %q, got %v", tc.failure, err)
			}
		})
	}
}

func TestCertIssuerPublicIngressReadFailsClosedAndRedacts(t *testing.T) {
	var deployments liveDeploymentList
	if err := json.Unmarshal([]byte(`{"items":[{"metadata":{"name":"certissuer"},"spec":{"template":{"spec":{"containers":[{"name":"certissuer","env":[{"name":"CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED","value":"true"}]}]}}}}]}`), &deployments); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	for _, tc := range []struct{ name, script, want string }{
		{"API denial", "echo 'Forbidden secret-token-sentinel' >&2; exit 1", "cannot inspect public CertIssuer ingress"},
		{"malformed inventory", "echo '{malformed secret-token-sentinel'", "metadata is invalid"},
		{"known live defect", "cat '" + filepath.Join(dir, "ingresses.json") + "'", "terminates caller TLS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(certIssuerIngressFixture(t, "video-cloud-dev-ingress", "public-certissuer-video-cloud", "", "certissuer.example.test"))
			if err := os.WriteFile(filepath.Join(dir, "ingresses.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(kubectl, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
			err := verifyCertIssuerPublicIngress("selected-dev-config", "video-cloud-dev", deployments, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-token-sentinel") {
				t.Fatalf("unsafe or missing failure: %v", err)
			}
		})
	}
	if err := verifyCertIssuerPublicIngress("missing-config", "video-cloud-dev", liveDeploymentList{}, nil); err != nil {
		t.Fatalf("unconfigured capability should not add external reads: %v", err)
	}
}

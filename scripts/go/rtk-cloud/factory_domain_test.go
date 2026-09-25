package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFactoryClientTrustRequiresCurrentSignedCRL(t *testing.T) {
	now := time.Now().UTC()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte("factory-test")}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Minute)}, ca, key)
	if err != nil {
		t.Fatal(err)
	}
	crlPEM := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER})
	if err := validateFactoryClientTrust(caPEM, crlPEM, now); err != nil {
		t.Fatal(err)
	}
	if err := validateFactoryClientTrust(caPEM, crlPEM, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired CRL accepted")
	}
	if err := validateFactoryClientTrust(caPEM, []byte("invalid"), now); err == nil {
		t.Fatal("invalid CRL accepted")
	}
}

func TestFactoryEnrollmentPublicRouteIsIsolated(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":              "video-cloud-dev",
		"VIDEO_CLOUD_DOMAIN":            "video-cloud-dev.example.test",
		"FACTORY_ENROLL_DOMAIN":         "factory-enroll.video-cloud-dev.example.test",
		"FACTORY_ENROLL_PUBLIC_ENABLED": "true",
	}
	routes := lkePublicHTTPSBaseRoutes(env)
	var factory lkePublicHTTPSRoute
	for _, route := range routes {
		if route.Host == env["FACTORY_ENROLL_DOMAIN"] {
			factory = route
		}
	}
	if factory.Path != "/v1/factory/enroll" || !factory.Exact {
		t.Fatalf("factory route = %#v", factory)
	}
	if factory.Service != "factoryenroll" {
		t.Fatalf("factory route targets wrong service: %s", factory.Service)
	}
	manifests := lkePublicHTTPSIngressManifests(env, routes)
	found := false
	for _, manifest := range manifests {
		if strings.Contains(manifest, "video-cloud-staging-factory-mtls") {
			found = true
			for _, expected := range []string{`auth-tls-verify-client: "on"`, `rewrite-target: "/v1/factory/public-enroll"`, "pathType: Exact"} {
				if !strings.Contains(manifest, expected) {
					t.Fatalf("factory ingress missing %s", expected)
				}
			}
			if strings.Contains(manifest, "path: /\n") {
				t.Fatal("factory ingress exposes root path")
			}
		} else {
			if strings.Contains(manifest, env["FACTORY_ENROLL_DOMAIN"]) {
				t.Fatal("factory domain shares another ingress")
			}
		}
	}
	if !found {
		t.Fatal("factory ingress missing")
	}
	env["FACTORY_ENROLL_PUBLIC_ENABLED"] = "false"
	for _, route := range lkePublicHTTPSBaseRoutes(env) {
		if route.Host == env["FACTORY_ENROLL_DOMAIN"] {
			t.Fatal("disabled factory route remains public")
		}
	}
}

func TestFactoryEnrollmentDNSOnlyWhenEnabled(t *testing.T) {
	cfg := deploymentConfig{Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_DNS_ROOT_DOMAIN": "example.test", "FACTORY_ENROLL_DOMAIN": "factory.example.test", "FACTORY_ENROLL_PUBLIC_ENABLED": "false"}, DNSValues: map[string]string{"DNS_RECORD_TTL": "600"}}
	hasFactory := func() bool {
		for _, record := range buildGenericDNSPlan(cfg).Records {
			if record.Name == "factory.example.test" {
				return true
			}
		}
		return false
	}
	if hasFactory() {
		t.Fatal("disabled factory DNS is present")
	}
	cfg.Values["FACTORY_ENROLL_PUBLIC_ENABLED"] = "true"
	if !hasFactory() {
		t.Fatal("enabled factory DNS is missing")
	}
}

func TestFactoryEnrollmentDomainMustBeIndependent(t *testing.T) {
	const stack, root = "video-cloud-dev", "example.test"
	for _, domain := range []string{"video-cloud-dev.example.test", "admin.video-cloud-dev.example.test", "device.video-cloud-dev.example.test", "billing.video-cloud-dev.example.test", "payment-simulator.video-cloud-dev.example.test"} {
		if err := validateFactoryEnrollmentDomain(domain, stack, root); err == nil {
			t.Fatalf("reserved hostname accepted: %s", domain)
		}
	}
	if err := validateFactoryEnrollmentDomain("factory-enroll.video-cloud-dev.example.test", stack, root); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"factory.elsewhere.test", "Factory.example.test", "-factory.example.test", "factory_.example.test", strings.Repeat("a", 64) + ".example.test"} {
		if err := validateFactoryEnrollmentDomain(domain, stack, root); err == nil {
			t.Fatalf("invalid factory domain accepted: %s", domain)
		}
	}
}

func TestLKEFactoryTrustRejectsMissingAndCorruptFiles(t *testing.T) {
	paths := provisionPaths{EnvRoot: t.TempDir()}
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	if err := lkeApplyFactoryMTLSCASecret(paths, env); err == nil {
		t.Fatal("missing CA accepted")
	}
	dir := sensitiveEnvironmentPath(paths, "factory-client-ca")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lkeApplyFactoryMTLSCASecret(paths, env); err == nil {
		t.Fatal("missing CRL accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crl"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lkeApplyFactoryMTLSCASecret(paths, env); err == nil {
		t.Fatal("corrupt trust accepted")
	}
}

func TestFactoryDomainDeploymentConfigurationRejectsUnsafeExposure(t *testing.T) {
	for _, tc := range []struct{ name, values, want string }{
		{"invalid switch", "FACTORY_ENROLL_PUBLIC_ENABLED=perhaps\n", "FACTORY_ENROLL_PUBLIC_ENABLED"},
		{"reserved host", "FACTORY_ENROLL_PUBLIC_ENABLED=true\nFACTORY_ENROLL_DOMAIN=admin.video-cloud-dev.example.test\n", "independent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), tc.values)
			if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe factory configuration accepted: %v", err)
			}
		})
	}
}

func TestFactoryEnrollmentEnableFlagSurvivesLKECompatibilityWrite(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_DNS_ROOT_DOMAIN": "example.test", "FACTORY_ENROLL_DOMAIN": "factory-enroll.video-cloud-dev.example.test", "FACTORY_ENROLL_PUBLIC_ENABLED": "true"}
	if err := writeLKECompatibilityArtifacts(provisionPaths{EnvRoot: root}, env); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "env", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "FACTORY_ENROLL_PUBLIC_ENABLED=true") {
		t.Fatalf("public enablement lost: %s", raw)
	}
}

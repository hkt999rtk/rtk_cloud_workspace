package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKECertIssuerTLSConfigurationCoversInternalAndPublicConnections(t *testing.T) {
	t.Setenv("LKE_NAMESPACE_VIDEO_CLOUD", "")
	t.Setenv("CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM", "ed25519")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env := map[string]string{
				"CLOUD_STACK_NAME":                       "video-cloud-" + environment,
				"VIDEO_CLOUD_CERTISSUER_DOMAIN":          "certissuer.video-cloud-" + environment + ".example.test",
				"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519",
			}
			config := lkeResolveCertIssuerTLSConfig(env)
			if err := config.Validate(); err != nil {
				t.Fatal(err)
			}
			material, err := newLKECertIssuerMaterial(env)
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode([]byte(material.ServerCert))
			if block == nil {
				t.Fatal("CertIssuer serving certificate is not PEM")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM([]byte(material.ServiceCA)) {
				t.Fatal("CertIssuer Service CA is not PEM")
			}
			internalURL, err := url.Parse(lkeCertIssuerBaseURL(env))
			if err != nil {
				t.Fatal(err)
			}
			for _, host := range []string{internalURL.Hostname(), "certissuer." + config.Namespace + ".svc", config.PublicHost} {
				if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
					t.Fatalf("generated serving certificate does not qualify %s: %v", host, err)
				}
			}
			var service struct {
				Metadata struct{ Namespace string }
				Spec     struct {
					Ports []struct {
						Port       int
						TargetPort int `yaml:"targetPort"`
					}
				}
			}
			if err := yaml.Unmarshal([]byte(lkeCertIssuerServiceManifest(env)), &service); err != nil {
				t.Fatal(err)
			}
			if service.Metadata.Namespace != config.Namespace || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != config.HTTPSPort || service.Spec.Ports[0].TargetPort != config.HTTPSPort {
				t.Fatalf("CertIssuer Service disagrees with TLS policy: %+v", service)
			}
			var deployment struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Ports []struct {
									ContainerPort int `yaml:"containerPort"`
								}
								Env []struct{ Name, Value string }
							}
						}
					}
				}
			}
			if err := yaml.Unmarshal([]byte(lkeCertIssuerDeploymentManifest(env, material, lkeOpenBaoBootstrapResult{})), &deployment); err != nil {
				t.Fatal(err)
			}
			if len(deployment.Spec.Template.Spec.Containers) != 1 {
				t.Fatal("expected the default CertIssuer container")
			}
			container := deployment.Spec.Template.Spec.Containers[0]
			settings := map[string]string{}
			for _, item := range container.Env {
				settings[item.Name] = item.Value
			}
			if len(container.Ports) != 1 || container.Ports[0].ContainerPort != config.HTTPSPort || settings["CERT_ISSUER_LISTEN_ADDR"] != ":"+strconv.Itoa(config.HTTPSPort) {
				t.Fatalf("CertIssuer listener disagrees with Service: %+v", container)
			}
			if settings["CERT_ISSUER_SERVER_CERT"] == "" || settings["CERT_ISSUER_SERVER_KEY"] == "" || settings["CERT_ISSUER_CLIENT_CA"] == "" || settings["CERT_ISSUER_TRUSTED_HEADER_ENABLED"] == "true" {
				t.Fatal("default CertIssuer must terminate and authenticate TLS itself")
			}
			var publicRoute lkePublicHTTPSRoute
			for _, route := range lkePublicHTTPSBaseRoutes(env) {
				if route.Service == "certissuer" {
					publicRoute = route
				}
			}
			if publicRoute != config.PublicRoute() || publicRoute.ServicePort != service.Spec.Ports[0].Port || internalURL.Port() != strconv.Itoa(publicRoute.ServicePort) {
				t.Fatalf("internal URL, Service and public backend disagree: %s, %+v", internalURL, publicRoute)
			}
			helmlog := fakeHelm(t)
			fakeKubectl(t)
			if err := lkeInstallIngressNginx(env); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readTestFile(t, helmlog), "--set controller.extraArgs.enable-ssl-passthrough=true") {
				t.Fatal("public CertIssuer route must enable passthrough on the controller")
			}
			manifest := lkeCertIssuerPassthroughIngressManifest(env, publicRoute)
			if !strings.Contains(manifest, `ssl-passthrough: "true"`) || strings.Contains(manifest, "secretName:") || strings.Contains(manifest, "auth-tls-") {
				t.Fatal("public CertIssuer route must preserve the caller's TLS identity")
			}
		})
	}
}

func TestLKECertIssuerTLSConfigurationDeduplicatesPublicSAN(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	internal := lkeResolveCertIssuerTLSConfig(env).ServerDNSNames()
	env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = internal[2]
	if names := lkeCertIssuerDNSNames(env); !slices.Equal(names, internal) {
		t.Fatalf("duplicate or empty DNS SAN introduced: %v", names)
	}
}

func TestLKECertIssuerTLSConfigurationRejectsMalformedHostsBeforeInstallingController(t *testing.T) {
	t.Setenv("LKE_NAMESPACE_VIDEO_CLOUD", "")
	for _, host := range []string{"https://certissuer.example.test", "certissuer.example.test:443", "*.example.test", "certissuer..example.test", "certissuer.example.test\nother.example.test"} {
		env := map[string]string{"VIDEO_CLOUD_CERTISSUER_DOMAIN": host}
		if err := lkeInstallIngressNginx(env); err == nil || !strings.Contains(err.Error(), "invalid CertIssuer public host") {
			t.Fatalf("malformed host %q did not fail before controller installation: %v", host, err)
		}
	}
	t.Setenv("LKE_NAMESPACE_VIDEO_CLOUD", "invalid.namespace")
	if err := lkeResolveCertIssuerTLSConfig(nil).Validate(); err == nil || !strings.Contains(err.Error(), "invalid CertIssuer namespace") {
		t.Fatalf("invalid namespace accepted: %v", err)
	}
}

func TestLKECertIssuerStoredIdentityIsPreservedWhenPublicHostIsAdded(t *testing.T) {
	t.Setenv("CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM", "ed25519")
	previousRoot := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = ""
	t.Cleanup(func() { activeSecretEnvironmentRoot = previousRoot })
	paths := provisionPaths{EnvRoot: t.TempDir()}
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}
	before, err := loadOrCreateLKECertIssuerMaterial(paths, env)
	if err != nil {
		t.Fatal(err)
	}
	env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "certissuer.video-cloud-dev.example.test"
	after, err := loadOrCreateLKECertIssuerMaterial(paths, env)
	if err != nil {
		t.Fatal(err)
	}
	// Sensitive-file reads trim the trailing PEM newline without changing keys.
	before.ServerKey = strings.TrimSpace(before.ServerKey)
	before.ClientKey = strings.TrimSpace(before.ClientKey)
	before.FactoryKey = strings.TrimSpace(before.FactoryKey)
	after.ServerKey = strings.TrimSpace(after.ServerKey)
	after.ClientKey = strings.TrimSpace(after.ClientKey)
	after.FactoryKey = strings.TrimSpace(after.FactoryKey)
	if before != after {
		t.Fatal("adding a public hostname silently replaced established CA or identity state")
	}
}

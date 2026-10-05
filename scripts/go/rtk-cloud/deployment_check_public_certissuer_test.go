package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deploymentRequiredIngressFixture(t *testing.T) certIssuerIngressList {
	t.Helper()
	var list certIssuerIngressList
	raw := `{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","annotations":{"nginx.ingress.kubernetes.io/ssl-passthrough":"true"}},"spec":{"ingressClassName":"nginx","rules":[{"host":"issuer.example","http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"certissuer","port":{"number":9443}}}}]}}]}}]}`
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestDeploymentPublicCertIssuerRequiresExactExistingRoute(t *testing.T) {
	for _, name := range []string{"valid", "missing", "wrong-host", "foreign-owner", "duplicate", "terminating-TLS", "wrong-path", "wrong-port", "wrong-service", "wrong-class", "wrong-path-type"} {
		t.Run(name, func(t *testing.T) {
			list := deploymentRequiredIngressFixture(t)
			switch name {
			case "missing":
				list.Items = nil
			case "wrong-host":
				list.Items[0].Spec.Rules[0].Host = "other.example"
			case "foreign-owner":
				list.Items[0].Metadata.Namespace = "video-cloud-dev-video-cloud"
			case "duplicate":
				list.Items = append(list.Items, list.Items[0])
			case "terminating-TLS":
				list.Items[0].Metadata.Annotations = nil
			case "wrong-path":
				list.Items[0].Spec.Rules[0].HTTP.Paths[0].Path = "/internal"
			case "wrong-port":
				list.Items[0].Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = 443
			case "wrong-service":
				list.Items[0].Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "external-bridge"
			case "wrong-class":
				list.Items[0].Spec.IngressClassName = "foreign"
			case "wrong-path-type":
				list.Items[0].Spec.Rules[0].HTTP.Paths[0].PathType = "Exact"
			}
			err := validateRequiredCertIssuerPublicIngress("video-cloud-staging", "issuer.example", 9443, list)
			if (err == nil) != (name == "valid") {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

func TestDeploymentPublicCertIssuerProbeRejectsPartialSuccessAndRedacts(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{{"success", "printf '%s\\n' 'PASS: public CertIssuer mTLS validation and anonymous TLS denial verified'", ""}, {"socket-only", "printf '%s\\n' 'PASS: managed CertIssuer socket request'", "no complete qualification"}, {"anonymous accepted", "printf '%s\\n' 'FAIL [ANONYMOUS_DENIAL_UNVERIFIED]: secret-sentinel'; exit 1", "ANONYMOUS_DENIAL_UNVERIFIED"}, {"cancelled", "sleep 30", "deadline"}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "scripts", "check-certissuer-app-mtls.sh"), []byte("#!/bin/sh\n"+tc.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			err := runDeploymentCertIssuerPublicProbe(ctx, root, "staging", "selected-kube", "issuer.example")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("raw probe diagnostic leaked")
			}
		})
	}
}

func TestDeploymentPublicCertIssuerConfigurationAndInventoryFailClosed(t *testing.T) {
	for _, name := range []string{"unconfigured", "invalid-host", "read-denied", "malformed", "missing-route", "valid"} {
		t.Run(name, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "staging")
			cfg := deploymentConfig{Environment: "staging", Workspace: t.TempDir(), Values: map[string]string{"CLOUD_DNS_ROOT_DOMAIN": "example.test"}}
			body := "printf '%s' '{broken'\n"
			switch name {
			case "unconfigured":
				cfg.Values = nil
			case "invalid-host":
				cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] = "bad host"
			case "read-denied":
				body = "printf 'Forbidden secret-sentinel' >&2; exit 1"
			case "missing-route":
				body = "printf '%s' '{\"items\":[]}'"
			case "valid":
				list := deploymentRequiredIngressFixture(t)
				list.Items[0].Spec.Rules[0].Host = "certissuer.video-cloud-staging.example.test"
				raw, _ := json.Marshal(list)
				body = "printf '%s' '" + string(raw) + "'"
				_ = os.MkdirAll(filepath.Join(cfg.Workspace, "scripts"), 0700)
				_ = os.WriteFile(filepath.Join(cfg.Workspace, "scripts", "check-certissuer-app-mtls.sh"), []byte("printf '%s\\n' 'PASS: public CertIssuer mTLS validation and anonymous TLS denial verified'"), 0700)
			}
			deploymentRuntimeTestKubectl(t, body)
			err := verifyDeploymentPublicCertIssuer(context.Background(), cfg, store)
			wantPass := name == "valid"
			if (err == nil) != wantPass {
				t.Fatalf("%s: %v", name, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("private diagnostic leaked")
			}
		})
	}
}

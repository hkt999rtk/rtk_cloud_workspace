package main

import (
	"strings"
	"testing"
)

func TestCertIssuerIngressPreservesCallerTLSAndDirectBackend(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "production"} {
		t.Run(environment, func(t *testing.T) {
			env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-" + environment + ".realtekconnect.com", "VIDEO_CLOUD_DOMAIN": "video-cloud-" + environment + ".realtekconnect.com"}
			route := lkePublicHTTPSRoute{Host: env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], Namespace: lkeNamespaceName(env, "video-cloud"), Service: "certissuer", ServicePort: 9443, Protocol: "HTTPS"}
			other := lkePublicHTTPSRoute{Host: env["VIDEO_CLOUD_DOMAIN"], Namespace: route.Namespace, Service: "video-cloud-api", ServicePort: 80}
			rendered := lkePublicHTTPSIngressManifests(env, []lkePublicHTTPSRoute{route, other})
			var direct string
			for _, m := range rendered {
				if strings.Contains(m, "name: certissuer-public-mtls") {
					direct = m
				}
			}
			for _, want := range []string{"namespace: " + route.Namespace, "ssl-passthrough: \"true\"", "host: " + route.Host, "name: certissuer\n                port:\n                  number: 9443"} {
				if !strings.Contains(direct, want) {
					t.Fatalf("missing %q in direct route", want)
				}
			}
			for _, bad := range []string{"secretName:", "public-certissuer-video-cloud", "X-Client-", "auth-tls-secret"} {
				if strings.Contains(direct, bad) {
					t.Fatalf("passthrough must retain workload authentication: %s", bad)
				}
			}
			bridges := strings.Join(lkePublicHTTPSBridgeServiceManifests(env, []lkePublicHTTPSRoute{route, other}), "\n")
			if strings.Contains(bridges, "public-certissuer") {
				t.Fatal("ExternalName bridge retained for passthrough")
			}
			if !strings.Contains(bridges, "externalName: video-cloud-api.") {
				t.Fatal("unrelated HTTP bridge removed")
			}
			if len(rendered) != 3 {
				t.Fatalf("expected public, OTA-upload and CertIssuer routes, got %d", len(rendered))
			}
		})
	}
}

func TestCertIssuerPassthroughSelectionRequiresExactHostAndOwner(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-dev.realtekconnect.com"}
	route := lkePublicHTTPSRoute{Host: env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], Namespace: lkeNamespaceName(env, "video-cloud"), Service: "certissuer"}
	if !lkeCertIssuerPassthroughRoute(env, route) {
		t.Fatal("canonical route not selected")
	}
	for _, edit := range []func(*lkePublicHTTPSRoute){func(r *lkePublicHTTPSRoute) { r.Host = "" }, func(r *lkePublicHTTPSRoute) { r.Host = "other.example" }, func(r *lkePublicHTTPSRoute) { r.Namespace = "video-cloud-staging-video-cloud" }, func(r *lkePublicHTTPSRoute) { r.Service = "video-cloud-api" }} {
		wrong := route
		edit(&wrong)
		if lkeCertIssuerPassthroughRoute(env, wrong) {
			t.Fatal("unrelated route selected")
		}
	}
}

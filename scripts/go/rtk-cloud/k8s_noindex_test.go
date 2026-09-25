package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKECrawlerPolicy(t *testing.T) {
	for _, stack := range []string{"video-cloud-dev", "video-cloud-staging"} {
		env := k8sWorkloadTestEnv()
		env["CLOUD_STACK_NAME"] = stack
		assertLKECrawlerPolicy(t, env, true)
	}

	prod, err := readStrictEnv(filepath.Join("..", "..", "..", "cloud_env", "prod", "environment.env"))
	if err != nil {
		t.Fatal(err)
	}
	if prod["FRONTEND_DOMAIN"] != "www.realtekconnect.com" || prod["PUBLIC_BASE_URL"] != "https://www.realtekconnect.com" {
		t.Fatalf("unexpected production frontend origin: domain=%q base=%q", prod["FRONTEND_DOMAIN"], prod["PUBLIC_BASE_URL"])
	}
	env := k8sWorkloadTestEnv()
	for key, value := range prod {
		env[key] = value
	}
	if got := lkeFrontendPublicDomain(env); got != "www.realtekconnect.com" {
		t.Fatalf("production frontend route = %q", got)
	}
	assertLKECrawlerPolicy(t, env, false)
	env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "certissuer.video-cloud-prod.realtekconnect.com"
	env["FACTORY_ENROLL_PUBLIC_ENABLED"] = "true"
	env["FACTORY_ENROLL_DOMAIN"] = "factory.video-cloud-prod.realtekconnect.com"
	manifests := lkePublicHTTPSIngressManifests(env, lkePublicHTTPSRoutes(env))
	if len(manifests) != 5 {
		t.Fatalf("production ingress count = %d, want public, frontend, device, factory, and certissuer", len(manifests))
	}
	for _, manifest := range manifests {
		if strings.Contains(manifest, "name: video-cloud-staging-frontend\n") {
			if !strings.Contains(manifest, "host: www.realtekconnect.com") || strings.Contains(manifest, "X-Robots-Tag") {
				t.Fatal("frontend ingress must serve the public host without noindex")
			}
			continue
		}
		for _, want := range []string{
			"nginx.ingress.kubernetes.io/server-snippet: |",
			"nginx.ingress.kubernetes.io/configuration-snippet: |",
			"X-Robots-Tag: noindex",
			"Disallow: /",
			"return 404;",
		} {
			if !strings.Contains(manifest, want) {
				t.Fatalf("non-frontend ingress missing %q", want)
			}
		}
		if strings.Contains(manifest, "host: www.realtekconnect.com") {
			t.Fatal("frontend host must not share a protected ingress")
		}
		if strings.Count(manifest, "nginx.ingress.kubernetes.io/configuration-snippet: |") != 1 {
			t.Fatal("non-frontend ingress has duplicate configuration snippets")
		}
		if strings.Contains(manifest, "name: video-cloud-staging-device-mtls\n") && !strings.Contains(manifest, "proxy_set_header X-Client-Cert $ssl_client_escaped_cert;") {
			t.Fatal("device mTLS ingress lost client-certificate forwarding")
		}
	}
}

func assertLKECrawlerPolicy(t *testing.T, env map[string]string, blocked bool) {
	t.Helper()
	var config map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lkeIngressNoIndexHelmValue(env), "controller.config=")), &config); err != nil {
		t.Fatal(err)
	}
	if config["allow-snippet-annotations"] != "true" || config["annotations-risk-level"] != "Critical" {
		t.Fatal("mTLS forwarding annotations must remain enabled")
	}
	if blocked {
		if config["server-snippet"] != lkeNoIndexServerSnippet || config["location-snippet"] != lkeNoIndexHeader {
			t.Fatal("Helm encoding lost the crawler policy")
		}
	} else if config["server-snippet"] != "" || config["location-snippet"] != "" {
		t.Fatal("production ingress still blocks crawlers or sitemap")
	}
	want := "false"
	if blocked {
		want = "true"
	}
	foundFrontend := false
	for _, workload := range k8sWorkloads(env) {
		if workload.Key != "frontend" {
			continue
		}
		foundFrontend = true
		manifest := lkeDeploymentManifest(env, workload, nil)
		if !strings.Contains(manifest, "- name: DISABLE_SEARCH_INDEXING\n              value: \""+want+"\"") {
			t.Fatalf("%s frontend indexing setting is wrong", env["CLOUD_STACK_NAME"])
		}
		if !blocked && !strings.Contains(manifest, "- name: PUBLIC_BASE_URL\n              value: \"https://www.realtekconnect.com\"") {
			t.Fatal("production sitemap and canonical URLs need the public origin")
		}
	}
	if !foundFrontend {
		t.Fatal("frontend workload not found")
	}
}

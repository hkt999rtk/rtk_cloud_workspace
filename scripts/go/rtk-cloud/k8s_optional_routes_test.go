package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEIngressGroupsExactPluginPathsWithCoreFallback(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	host := "video-cloud-staging.example.test"
	namespace := "video-cloud-staging-video-cloud"
	routes := []lkePublicHTTPSRoute{
		{Host: host, Namespace: namespace, Service: "video-cloud-api", ServicePort: 80},
		{Host: host, Path: "/api/request_webrtc", Exact: true, Namespace: namespace, Service: "video-cloud-webrtcservice", ServicePort: 18082},
		{Host: host, Path: "/api/request_webrtc/ice", Exact: true, Namespace: namespace, Service: "video-cloud-webrtcservice", ServicePort: 18082},
	}
	manifest := lkePublicHTTPSIngressManifest(env, "test-public", routes, "", "")
	var parsed struct {
		Spec struct {
			Rules []struct {
				Host string `yaml:"host"`
				HTTP struct {
					Paths []struct {
						Path     string `yaml:"path"`
						PathType string `yaml:"pathType"`
						Backend  struct {
							Service struct {
								Name string `yaml:"name"`
							} `yaml:"service"`
						} `yaml:"backend"`
					} `yaml:"paths"`
				} `yaml:"http"`
			} `yaml:"rules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Spec.Rules) != 1 || parsed.Spec.Rules[0].Host != host {
		t.Fatalf("expected one host rule, got %#v", parsed.Spec.Rules)
	}
	paths := parsed.Spec.Rules[0].HTTP.Paths
	if len(paths) != 3 || paths[0].Path != "/" || paths[0].PathType != "Prefix" || paths[0].Backend.Service.Name != "public-video-cloud-api-video-cloud" {
		t.Fatalf("core fallback changed: %#v", paths)
	}
	for _, path := range paths[1:] {
		if path.PathType != "Exact" || path.Backend.Service.Name != "public-video-cloud-webrtcservice-video-cloud" {
			t.Fatalf("plugin path is not narrowly routed: %#v", path)
		}
	}
}

func TestLKEDeviceHostPluginPathKeepsMTLSIngress(t *testing.T) {
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.example.test",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video-cloud-staging.example.test",
	}
	namespace := "video-cloud-staging-video-cloud"
	routes := []lkePublicHTTPSRoute{
		{Host: env["VIDEO_CLOUD_DOMAIN"], Namespace: namespace, Service: "video-cloud-api", ServicePort: 80},
		{Host: env["VIDEO_CLOUD_DEVICE_DOMAIN"], Namespace: namespace, Service: "video-cloud-api", ServicePort: 80},
		{Host: env["VIDEO_CLOUD_DEVICE_DOMAIN"], Path: "/api/request_webrtc/answer", Exact: true, Namespace: namespace, Service: "video-cloud-webrtcservice", ServicePort: 18082},
	}
	manifests := lkePublicHTTPSIngressManifests(env, routes)
	if len(manifests) != 2 {
		t.Fatalf("want separate public and device-mTLS ingresses, got %d", len(manifests))
	}
	if strings.Contains(manifests[0], env["VIDEO_CLOUD_DEVICE_DOMAIN"]) {
		t.Fatal("device plugin route bypassed mTLS through public ingress")
	}
	if !strings.Contains(manifests[1], "nginx.ingress.kubernetes.io/auth-tls-verify-client: \"on\"") || !strings.Contains(manifests[1], "path: /api/request_webrtc/answer\n            pathType: Exact") {
		t.Fatal("device plugin route lost mTLS or exact path match")
	}
}

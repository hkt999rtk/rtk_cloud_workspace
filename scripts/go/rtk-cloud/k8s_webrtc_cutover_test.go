package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testWebRTCEdgeEnv() map[string]string {
	return map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.example.test",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video-cloud-staging.example.test",
	}
}

func TestLKEWebRTCEdgeRendersOnlyExactRoutesOnBothHosts(t *testing.T) {
	t.Setenv("LKE_WEBRTC_SERVICE_EDGE_ENABLED", "false")
	env := testWebRTCEdgeEnv()
	for _, route := range lkePublicHTTPSBaseRoutes(env) {
		if route.Service == "video-cloud-webrtcservice" {
			t.Fatal("WebRTC route appeared without opt-in")
		}
	}
	t.Setenv("LKE_WEBRTC_SERVICE_EDGE_ENABLED", "true")
	routes := lkePublicHTTPSBaseRoutes(env)
	count := 0
	for _, route := range routes {
		if route.Service != "video-cloud-webrtcservice" {
			continue
		}
		count++
		if !route.Exact || route.ServicePort != 18082 || route.TargetPort != 18082 {
			t.Fatalf("WebRTC route is not exact and private: %#v", route)
		}
		if route.Host != env["VIDEO_CLOUD_DOMAIN"] && route.Host != env["VIDEO_CLOUD_DEVICE_DOMAIN"] {
			t.Fatalf("WebRTC route has unexpected host: %#v", route)
		}
	}
	if count != 8 {
		t.Fatalf("want four WebRTC paths on each host, got %d", count)
	}
	manifests := lkePublicHTTPSIngressManifests(env, routes)
	if !strings.Contains(manifests[1], "nginx.ingress.kubernetes.io/auth-tls-verify-client: \"on\"") || !strings.Contains(manifests[1], "path: /api/request_webrtc/answer\n            pathType: Exact") {
		t.Fatal("device WebRTC route did not retain exact-match mTLS ingress")
	}
}

func TestLKEWebRTCEdgeRequiresReadyRegisteredEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := testWebRTCEdgeEnv()
	if err := lkeRequireReadyWebRTCServiceEndpoint(env); err == nil {
		t.Fatal("absent WebRTC Service was accepted")
	}
	t.Setenv("FAKE_WEBRTC_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-webrtcservice"},"ports":[{"port":18082,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyWebRTCServiceEndpoint(env); err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("unready WebRTC endpoint was accepted: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18082}],"endpoints":[{"addresses":["10.1.2.3"],"conditions":{"ready":false}}]}]}`)
	if err := lkeRequireReadyWebRTCServiceEndpoint(env); err == nil {
		t.Fatal("EndpointSlice with unready Pod was accepted")
	}
	t.Setenv("FAKE_WEBRTC_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18082}],"endpoints":[{"addresses":["10.1.2.3"],"conditions":{"ready":true}}]}]}`)
	if err := lkeRequireReadyWebRTCServiceEndpoint(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEWebRTCCoreCutoverRequiresObservedEdgeAndDeviceMTLS(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_WEBRTC_SERVICE_EDGE_ENABLED", "true")
	env := testWebRTCEdgeEnv()
	if err := lkeRequireActiveWebRTCEdgeRoutes(env); err == nil {
		t.Fatal("missing live ingress was accepted")
	}
	for _, manifest := range lkePublicHTTPSIngressManifests(env, lkePublicHTTPSBaseRoutes(env))[:2] {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(manifest, "name: video-cloud-staging-device-mtls") {
			t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
		} else {
			t.Setenv("FAKE_WEBRTC_PUBLIC_INGRESS_JSON", string(encoded))
		}
	}
	if err := lkeRequireActiveWebRTCEdgeRoutes(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[]}}`)
	if err := lkeRequireActiveWebRTCEdgeRoutes(env); err == nil || !strings.Contains(err.Error(), "mTLS") {
		t.Fatalf("device ingress without mTLS was accepted: %v", err)
	}
}

func TestLKEWebRTCEdgeRollbackWaitsForCoreHandlerRestoration(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_WEBRTC_CORE_CUTOVER_ENABLED", "false")
	env := testWebRTCEdgeEnv()
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_WEBRTC_SERVICE_CUTOVER_ENABLED","value":"true"}]}]}}}}`)
	if err := lkePreventWebRTCEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "restore core handlers") {
		t.Fatalf("edge rollback bypassed active core cutover: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_WEBRTC_SERVICE_CUTOVER_ENABLED","value":"false"}]}]}}}}`)
	if err := lkePreventWebRTCEdgeRollbackOverlap(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEDeployRejectsWebRTCCoreCutoverBeforeEdgeExists(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_WEBRTC_CORE_CUTOVER_ENABLED", "true")
	t.Setenv("LKE_WEBRTC_SERVICE_EDGE_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires the WebRTC edge route") {
		t.Fatalf("core cutover without edge was accepted: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("core cutover mutated workloads before rejecting missing edge")
	}
}

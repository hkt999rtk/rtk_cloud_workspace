package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEWebRTCServiceRegistrationIsOptIn(t *testing.T) {
	t.Setenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "false")
	if lkeWebRTCServiceRegistrationEnabled(map[string]string{"LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED": "true"}) {
		t.Fatal("process override did not keep WebRTC service disabled")
	}
	t.Setenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "true")
	if !lkeWebRTCServiceRegistrationEnabled(nil) {
		t.Fatal("explicit WebRTC opt-in was ignored")
	}
}

func TestLKEWebRTCServiceRendersIndependentPrivateWorkload(t *testing.T) {
	t.Setenv("ACCOUNT_MANAGER_ENV", "")
	t.Setenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"ACCOUNT_MANAGER_ENV":   "staging",
		"VIDEO_CLOUD_DOMAIN":    "video-cloud-staging.example.test",
		"LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
	}
	deployment := lkeWebRTCServiceDeploymentManifest(env)
	service := lkeWebRTCServiceServiceManifest(env)
	policy := lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service, "turn-policy": policy} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, want := range []string{
		"name: video-cloud-webrtcservice",
		"replicas: 1\n  strategy:\n    type: Recreate",
		"command: [\"/app/webrtcservice\"]",
		"name: VIDEO_CLOUD_MQTT_ENABLED\n              value: \"false\"",
		"name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED\n              value: \"false\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"name: VIDEO_CLOUD_WEBRTC_SERVICE_INSTANCE_ID\n              value: \"webrtc-service-0\"",
		"name: VIDEO_CLOUD_WEBRTC_SERVICE_REGISTRATION_URL\n              value: \"https://account-manager.video-cloud-staging-account-manager.svc.cluster.local:8443\"",
		"secretName: webrtc-service-platform-identity",
		"defaultMode: 0440",
		"path: /readyz",
		"fsGroup: 10001",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("WebRTC service deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "type: LoadBalancer") {
		t.Fatal("WebRTC service must stay private until edge cutover")
	}
	if !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-webrtcservice") || !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "- video-cloud-webrtcservice") {
		t.Fatal("WebRTC service lacks private network access to required services")
	}
}

func TestLKEWebRTCServiceTurnAccessIsDefaultOff(t *testing.T) {
	t.Setenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "false")
	policy := lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if strings.Contains(policy, "video-cloud-webrtcservice") {
		t.Fatal("TURN registry granted WebRTC Pod access without opt-in")
	}
}

func TestLKEWebRTCServiceRequiresOwnIdentitySecret(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireWebRTCServiceIdentitySecret(env); err == nil {
		t.Fatal("missing WebRTC service identity Secret was accepted")
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireWebRTCServiceIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEDeployRejectsWebRTCWithoutFoundationBeforeMutation(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires the MQTT foundation") {
		t.Fatalf("WebRTC dependency gate did not reject rollout: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("WebRTC dependency gate allowed Kubernetes mutations")
	}
}

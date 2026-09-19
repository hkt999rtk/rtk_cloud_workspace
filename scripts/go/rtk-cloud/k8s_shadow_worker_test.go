package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEShadowWorkerRegistrationIsOptIn(t *testing.T) {
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "false")
	if lkeShadowWorkerRegistrationEnabled(map[string]string{"LKE_SHADOW_WORKER_REGISTRATION_ENABLED": "true"}) {
		t.Fatal("process override did not keep Shadow worker disabled")
	}
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "true")
	if !lkeShadowWorkerRegistrationEnabled(nil) {
		t.Fatal("explicit Shadow worker opt-in was ignored")
	}
}

func TestLKEShadowWorkerRendersIndependentPrivateService(t *testing.T) {
	t.Setenv("ACCOUNT_MANAGER_ENV", "")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"ACCOUNT_MANAGER_ENV":   "staging",
		"LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
	}
	deployment := lkeShadowWorkerDeploymentManifest(env)
	service := lkeShadowWorkerServiceManifest(env)
	core := lkeDeploymentManifest(env, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}, nil)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, want := range []string{
		"name: video-cloud-shadowworker",
		"replicas: 1\n  strategy:\n    type: Recreate",
		"command: [\"/app/shadowworker\"]",
		"name: VIDEO_CLOUD_SHADOW_CACHE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"name: VIDEO_CLOUD_SHADOW_MUTATIONS_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_SHADOW_WORKER_INSTANCE_ID\n              value: \"shadow-worker-0\"",
		"name: VIDEO_CLOUD_SHADOW_WORKER_REGISTRATION_URL\n              value: \"https://account-manager.video-cloud-staging-account-manager.svc.cluster.local:8443\"",
		"secretName: shadow-worker-platform-identity",
		"defaultMode: 0440",
		"path: /readyz",
		"fsGroup: 10001",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("Shadow worker deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "type: LoadBalancer") {
		t.Fatal("Shadow worker health Service is not private")
	}
	if !strings.Contains(core, "name: VIDEO_CLOUD_SHADOW_WORKER_ENABLED\n              value: \"true\"") {
		t.Fatal("core API still owns Shadow MQTT subscriptions during worker cutover")
	}
	policy := lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(policy), &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any); len(got) != 5 {
		t.Fatalf("broker ingress has %d allowed sources, want 5", len(got))
	}
	if !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-shadowworker") || !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "video-cloud-shadowworker") {
		t.Fatal("Shadow worker is missing from a private network policy")
	}
}

func TestLKEShadowWorkerRequiresOwnIdentitySecret(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireShadowWorkerIdentitySecret(env); err == nil {
		t.Fatal("missing Shadow worker identity Secret was accepted")
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireShadowWorkerIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEShadowWorkerDoesNotOpenBrokerIngressByDefault(t *testing.T) {
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "false")
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "false")
	policy := lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if strings.Contains(policy, "video-cloud-shadowworker") {
		t.Fatal("Shadow worker broker ingress appeared without opt-in")
	}
	core := lkeDeploymentManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed"}, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: "video-cloud-staging-video-cloud", Port: 8080, Image: "example.test/video-cloud:reviewed"}, nil)
	if !strings.Contains(core, "name: VIDEO_CLOUD_SHADOW_WORKER_ENABLED\n              value: \"false\"") {
		t.Fatal("core API lost Shadow MQTT subscriptions without cutover")
	}
}

func TestLKEShadowWorkerRollbackRequiresExplicitDrain(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkePreventShadowWorkerRollbackOverlap(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SHADOW_WORKER_DEPLOYMENT_PRESENT", "1")
	if err := lkePreventShadowWorkerRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "drain and remove") {
		t.Fatalf("running worker did not block core subscription rollback: %v", err)
	}
}

func TestLKEDeployRejectsShadowRollbackBeforeMutatingWorkloads(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "false")
	t.Setenv("FAKE_SHADOW_WORKER_DEPLOYMENT_PRESENT", "1")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "drain and remove") {
		t.Fatalf("Shadow rollback was not rejected: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("deployment mutated Kubernetes before rejecting Shadow rollback")
	}
}

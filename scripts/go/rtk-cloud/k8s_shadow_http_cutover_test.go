package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEShadowHTTPRequiresReadyPrivateRegisteredEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireReadyShadowWorkerHTTPEndpoint(env); err == nil {
		t.Fatal("missing Shadow worker Service was accepted")
	}
	t.Setenv("FAKE_SHADOW_WORKER_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-shadowworker"},"ports":[{"port":18081,"targetPort":"health"}]}}`)
	if err := lkeRequireReadyShadowWorkerHTTPEndpoint(env); err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("Shadow Service without ready endpoint was accepted: %v", err)
	}
	t.Setenv("FAKE_SHADOW_WORKER_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18081}],"endpoints":[{"addresses":["10.0.0.6"],"conditions":{"ready":true}}]}]}`)
	if err := lkeRequireReadyShadowWorkerHTTPEndpoint(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SHADOW_WORKER_SERVICE_JSON", `{"spec":{"type":"LoadBalancer","selector":{"app.kubernetes.io/name":"video-cloud-shadowworker"},"ports":[{"port":18081,"targetPort":"health"}]}}`)
	if err := lkeRequireReadyShadowWorkerHTTPEndpoint(env); err == nil {
		t.Fatal("public Shadow Service was accepted")
	}
}

func TestLKEShadowHTTPCoreCutoverManifestAndPolicyAreOptIn(t *testing.T) {
	t.Setenv("LKE_SHADOW_HTTP_CORE_CUTOVER_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}
	baseline := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(baseline, "name: VIDEO_CLOUD_SHADOW_HTTP_SERVICE_CUTOVER_ENABLED\n              value: \"false\"") || strings.Contains(baseline, "VIDEO_CLOUD_SHADOW_HTTP_UPSTREAM_URL") {
		t.Fatal("Shadow HTTP gateway was enabled by default")
	}
	if !strings.Contains(lkeShadowWorkerDeploymentManifest(env), "name: VIDEO_CLOUD_AUTH_SECRET") {
		t.Fatal("Shadow worker lacks the shared token/SigV4 verification secret")
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "name: allow-video-cloud-api-shadowworker") {
			t.Fatal("Shadow gateway policy appeared without cutover")
		}
	}
	t.Setenv("LKE_SHADOW_HTTP_CORE_CUTOVER_ENABLED", "true")
	cutover := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(cutover, "name: VIDEO_CLOUD_SHADOW_HTTP_SERVICE_CUTOVER_ENABLED\n              value: \"true\"") || !strings.Contains(cutover, "http://video-cloud-shadowworker.video-cloud-staging-video-cloud.svc.cluster.local:18081") {
		t.Fatal("cutover Deployment lacks the private Shadow origin")
	}
	policy := lkeAllowVideoCloudAPIShadowGatewayNetworkPolicyManifest(env)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(policy), &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-shadowworker") || !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-api") || !strings.Contains(policy, "port: 18081") {
		t.Fatal("Shadow gateway NetworkPolicy has the wrong source, target, or port")
	}
	found := false
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		found = found || strings.Contains(manifest, "name: allow-video-cloud-api-shadowworker")
	}
	if !found {
		t.Fatal("Shadow gateway policy was omitted from public HTTPS policy reconciliation")
	}
}

func TestLKEShadowHTTPCutoverFailsBeforeMutationWithoutReadyEndpoint(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_SHADOW_HTTP_CORE_CUTOVER_ENABLED", "true")
	t.Setenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	setFakeLKEPlatformIdentitySecrets(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "Shadow worker Service is not private") {
		t.Fatalf("missing Shadow endpoint did not stop cutover: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("Shadow cutover preflight allowed Kubernetes mutations")
	}
}

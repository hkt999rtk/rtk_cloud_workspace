package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEVideoStorageServiceRegistrationIsOptIn(t *testing.T) {
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "false")
	if lkeVideoStorageServiceRegistrationEnabled(map[string]string{"LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "true"}) {
		t.Fatal("process override did not keep video storage disabled")
	}
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "true")
	if !lkeVideoStorageServiceRegistrationEnabled(nil) {
		t.Fatal("explicit video storage opt-in was ignored")
	}
}

func TestLKEVideoStorageServiceRendersIndependentPrivateWorkload(t *testing.T) {
	t.Setenv("ACCOUNT_MANAGER_ENV", "")
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":                       "video-cloud-staging",
		"ACCOUNT_MANAGER_ENV":                    "staging",
		"VIDEO_CLOUD_DOMAIN":                     "video-cloud-staging.example.test",
		"LKE_VIDEO_CLOUD_IMAGE":                  "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_BLOB_ENDPOINT":              "https://objects.example.test",
		"VIDEO_CLOUD_BLOB_REGION":                "us-east-1",
		"VIDEO_CLOUD_BLOB_BUCKET":                "clips",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "true",
	}
	deployment := lkeVideoStorageServiceDeploymentManifest(env)
	service := lkeVideoStorageServiceServiceManifest(env)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, want := range []string{
		"name: video-cloud-videostorage",
		"replicas: 1\n  strategy:\n    type: Recreate",
		"command: [\"/app/videostorage\"]",
		"name: VIDEO_CLOUD_MQTT_ENABLED\n              value: \"false\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED\n              value: \"false\"",
		"name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_BLOB_BUCKET\n              value: \"clips\"",
		"name: VIDEO_CLOUD_CLIP_PRIVATE_KEY_PATH\n              value: \"/etc/video_cloud/clip-crypto/clip-private-key.pem\"",
		"name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID\n              value: \"video-storage-service-0\"",
		"name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL\n              value: \"https://account-manager.video-cloud-staging-account-manager.svc.cluster.local:8443\"",
		"secretName: video-storage-service-platform-identity",
		"name: clip-crypto",
		"defaultMode: 0440",
		"path: /readyz",
		"fsGroup: 10001",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("video storage service deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "type: LoadBalancer") {
		t.Fatal("video storage service must stay private until edge cutover")
	}
	if !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "- video-cloud-videostorage") {
		t.Fatal("Platform registration policy does not admit video storage Pod identity")
	}
	for _, route := range lkePublicHTTPSBaseRoutes(env) {
		if route.Service == "video-cloud-videostorage" {
			t.Fatal("private storage deployment flag published an edge route")
		}
	}
}

func TestLKEVideoStorageServiceRequiresOwnIdentitySecret(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireVideoStorageServiceIdentitySecret(env); err == nil {
		t.Fatal("missing video storage identity Secret was accepted")
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireVideoStorageServiceIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEDeployRejectsVideoStorageWithoutFoundationBeforeMutation(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires the MQTT foundation") {
		t.Fatalf("video storage dependency gate did not reject rollout: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("video storage dependency gate allowed Kubernetes mutations")
	}
}

func TestLKEDeployRejectsVideoStorageWithoutDirectS3BeforeMutation(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	setFakeLKEPlatformIdentitySecrets(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires direct S3 clip upload") {
		t.Fatalf("video storage accepted disabled direct S3: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("video storage prerequisite gate allowed Kubernetes mutations")
	}
}

func TestLKEVideoStorageCoreCutoverRequiresReadyPrivateRegisteredEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireReadyVideoStorageServiceEndpoint(env); err == nil {
		t.Fatal("missing storage Service was accepted")
	}
	t.Setenv("FAKE_VIDEO_STORAGE_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-videostorage"},"ports":[{"port":18083,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyVideoStorageServiceEndpoint(env); err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("storage Service without ready endpoint was accepted: %v", err)
	}
	t.Setenv("FAKE_VIDEO_STORAGE_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18083}],"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":true}}]}]}`)
	if err := lkeRequireReadyVideoStorageServiceEndpoint(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_VIDEO_STORAGE_SERVICE_JSON", `{"spec":{"type":"LoadBalancer","selector":{"app.kubernetes.io/name":"video-cloud-videostorage"},"ports":[{"port":18083,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyVideoStorageServiceEndpoint(env); err == nil {
		t.Fatal("public storage Service was accepted")
	}
}

func TestLKEVideoStorageCoreCutoverManifestAndPolicyAreOptIn(t *testing.T) {
	t.Setenv("LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed"}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}
	baseline := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(baseline, "name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CUTOVER_ENABLED\n              value: \"false\"") || strings.Contains(baseline, "VIDEO_CLOUD_VIDEO_STORAGE_UPSTREAM_URL") {
		t.Fatal("storage gateway was enabled in the default core Deployment")
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "name: allow-video-cloud-api-videostorage") {
			t.Fatal("storage gateway NetworkPolicy appeared without cutover")
		}
	}
	t.Setenv("LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED", "true")
	cutover := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(cutover, "name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CUTOVER_ENABLED\n              value: \"true\"") || !strings.Contains(cutover, "http://video-cloud-videostorage.video-cloud-staging-video-cloud.svc.cluster.local:18083") {
		t.Fatal("cutover Deployment lacks its private storage gateway URL")
	}
	policy := lkeAllowVideoCloudAPIStorageGatewayNetworkPolicyManifest(env)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(policy), &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-videostorage") || !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-api") || !strings.Contains(policy, "port: 18083") {
		t.Fatal("gateway NetworkPolicy does not restrict the upstream to core")
	}
	found := false
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		found = found || strings.Contains(manifest, "name: allow-video-cloud-api-videostorage")
	}
	if !found {
		t.Fatal("storage gateway policy was omitted from public HTTPS policy reconciliation")
	}
}

func TestLKEVideoStorageCoreCutoverFailsBeforeMutationWithoutReadyEndpoint(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED", "true")
	t.Setenv("LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	setFakeLKEPlatformIdentitySecrets(t, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "true", "VIDEO_CLOUD_BLOB_ENDPOINT": "https://objects.example.test",
		"VIDEO_CLOUD_BLOB_REGION": "us-east-1", "VIDEO_CLOUD_BLOB_BUCKET": "clips",
		"LINODE_OBJ_ACCESS_KEY_ID": "test-only", "LINODE_OBJ_SECRET_ACCESS_KEY": "test-only",
	}
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "video storage Service is not private") {
		t.Fatalf("missing registered storage endpoint did not stop cutover: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(log), "apply -f") {
		t.Fatal("cutover preflight allowed Kubernetes mutations")
	}
}

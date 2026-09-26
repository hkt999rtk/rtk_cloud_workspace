package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEOTARegistrarDeploymentUsesCoreReadinessAndSeparateIdentity(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-dev",
		"LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
	}
	if lkeOTARegistrarRegistrationEnabled(env) || lkeOTAEntitlementsRequired(env) {
		t.Fatal("OTA registration and strict authorization must start disabled")
	}
	env["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"] = "true"
	env["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
	deployment := lkeOTARegistrarDeploymentManifest(env)
	service := lkeOTARegistrarServiceManifest(env)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service, "registration policy": lkeAllowServiceRegistrationNetworkPolicyManifest(env), "API policy": lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env)} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("invalid %s manifest: %v", name, err)
		}
	}
	for _, want := range []string{
		"replicas: 1", "type: Recreate", "command: [\"/app/otaregistrar\"]",
		"name: VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED\n              value: \"true\"",
		"name: VIDEO_CLOUD_OTA_UPSTREAM_URL\n              value: \"http://video-cloud-api.video-cloud-dev-video-cloud.svc.cluster.local:8080\"",
		"secretName: ota-service-platform-identity",
		"path: /readyz", "value: \"ota-service-0\"",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("OTA registrar deployment lacks %q", want)
		}
	}
	if !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "video-cloud-otaregistrar") || !strings.Contains(lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env), "video-cloud-otaregistrar") {
		t.Fatal("OTA registrar has no private route to both core and registry")
	}
	if !strings.Contains(service, "type: ClusterIP") {
		t.Fatal("OTA registrar Service is public")
	}
	core := lkeDeploymentManifest(env, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}, nil)
	if !strings.Contains(core, "name: VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED\n              value: \"true\"") {
		t.Fatal("core API did not receive the strict OTA entitlement setting")
	}
}

func TestLKEOTARegistrarRequiresIndependentIdentityAndMQTT(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{
		"CLOUD_STACK_NAME":                       "video-cloud-dev",
		"LKE_VIDEO_CLOUD_IMAGE":                  "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false",
		"LKE_OTA_REGISTRAR_REGISTRATION_ENABLED": "true",
	}
	if err := lkeRequireOTARegistrarIdentitySecret(env); err == nil {
		t.Fatal("missing OTA identity was accepted")
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireOTARegistrarIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
	if err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}}); err == nil || !strings.Contains(err.Error(), "requires strict OTA entitlements") {
		t.Fatalf("OTA registrar before strict authorization was accepted: %v", err)
	}
	env["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
	if err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}}); err == nil || !strings.Contains(err.Error(), "requires the MQTT foundation") {
		t.Fatalf("OTA registrar without MQTT was accepted: %v", err)
	}
}

func TestLKEProductWriteGateIsPersistedInAccountManagerRuntime(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_ENV_NAME": "dev"}
	if !strings.Contains(lkeAccountManagerSecretManifest(env), `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES: "false"`) {
		t.Fatal("Product writes must default off in the rendered runtime Secret")
	}
	env["ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES"] = "true"
	if !strings.Contains(lkeAccountManagerSecretManifest(env), `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES: "true"`) {
		t.Fatal("rendered runtime Secret ignored the Product write gate")
	}
}

package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEMQTTFoundationRegistrationIsOptIn(t *testing.T) {
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "false")
	if lkeMQTTFoundationRegistrationEnabled(map[string]string{"LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED": "true"}) {
		t.Fatal("process override did not keep MQTT foundation registration disabled")
	}
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	if !lkeMQTTFoundationRegistrationEnabled(nil) {
		t.Fatal("explicit MQTT foundation registration opt-in was ignored")
	}
}

func TestLKEMQTTEntitlementCutoverIsExplicitInCoreDeployment(t *testing.T) {
	t.Setenv("VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED", "")
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-dev",
		"LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
	}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}
	if lkeMQTTEntitlementsRequired(env) || !strings.Contains(lkeDeploymentManifest(env, workload, nil), "name: VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED\n              value: \"false\"") {
		t.Fatal("core MQTT entitlement gate must default off")
	}
	env["VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED"] = "true"
	if !lkeMQTTEntitlementsRequired(env) || !strings.Contains(lkeDeploymentManifest(env, workload, nil), "name: VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED\n              value: \"true\"") {
		t.Fatal("core deployment ignored strict MQTT entitlement setting")
	}
}

func TestLKEMQTTFoundationRendersStablePrivateRegistrar(t *testing.T) {
	t.Setenv("ACCOUNT_MANAGER_ENV", "")
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"CLOUD_ENV_NAME":        "dev",
		"ACCOUNT_MANAGER_ENV":   "staging",
		"LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed",
	}
	deployment := lkeMQTTFoundationDeploymentManifest(env)
	service := lkeMQTTFoundationServiceManifest(env)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, want := range []string{
		"name: video-cloud-mqttfoundation",
		"replicas: 1\n  strategy:\n    type: Recreate",
		"image: example.test/video-cloud:reviewed",
		"command: [\"/app/mqttfoundation\"]",
		"name: VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID\n              value: \"mqtt-foundation-0\"",
		"name: VIDEO_CLOUD_MQTT_FOUNDATION_REGISTRATION_URL\n              value: \"https://account-manager.video-cloud-staging-account-manager.svc.cluster.local:8443\"",
		"name: VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_CERT",
		"name: VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_KEY",
		"name: VIDEO_CLOUD_MQTT_FOUNDATION_SERVER_CA",
		"secretName: mqtt-foundation-platform-identity",
		"defaultMode: 0440",
		"path: /readyz",
		"fsGroup: 10001",
		"name: VIDEO_CLOUD_ENV\n              value: \"staging\"",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("MQTT foundation deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "type: LoadBalancer") {
		t.Fatal("MQTT foundation health Service is not private")
	}
	if !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "video-cloud-mqttfoundation") {
		t.Fatal("Platform registration policy does not admit the registrar Pod identity")
	}
	var brokerPolicy map[string]any
	if err := yaml.Unmarshal([]byte(lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env)), &brokerPolicy); err != nil {
		t.Fatalf("parse broker ingress policy: %v", err)
	}
	if got := brokerPolicy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any); len(got) != 4 {
		t.Fatalf("enabled broker ingress has %d allowed client identities, want 4", len(got))
	}
	if !strings.Contains(lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env), "app.kubernetes.io/name: video-cloud-mqttfoundation") {
		t.Fatal("broker ingress policy does not admit the registrar Pod identity")
	}
}

func TestLKEMQTTFoundationDoesNotOpenBrokerIngressByDefault(t *testing.T) {
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "false")
	policy := lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if strings.Contains(policy, "video-cloud-mqttfoundation") {
		t.Fatal("broker ingress policy admitted the registrar without opt-in")
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(policy), &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any); len(got) != 3 {
		t.Fatalf("default broker ingress has %d allowed client identities, want 3", len(got))
	}
}

func TestLKEMQTTFoundationRequiresDedicatedIdentitySecret(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireMQTTFoundationIdentitySecret(env); err == nil {
		t.Fatal("missing MQTT foundation identity Secret was accepted")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("test-only"))
	for _, key := range mqttFoundationIdentitySecretKeys {
		data := map[string]any{}
		for _, required := range mqttFoundationIdentitySecretKeys {
			data[required] = encoded
		}
		delete(data, key)
		if err := validateRequiredKubernetesSecretData(map[string]any{"data": data}, "MQTT foundation platform identity Secret", mqttFoundationIdentitySecretKeys); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("missing %s accepted: %v", key, err)
		}
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireMQTTFoundationIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEMQTTFoundationRequiresExistingPrivatePlatformEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireExistingServiceRegistrationEndpoint(env); err == nil {
		t.Fatal("missing Account Manager registration endpoint was accepted")
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"public service", `{"spec":{"type":"LoadBalancer","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[]}}`},
		{"wrong selector", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"other"},"ports":[]}}`},
		{"missing ports", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", tc.body)
			if err := lkeRequireExistingServiceRegistrationEndpoint(env); err == nil {
				t.Fatal("invalid Account Manager registration endpoint was accepted")
			}
		})
	}
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"http","port":80,"targetPort":"http"}]}}`)
	if err := lkeRequireExistingServiceRegistrationEndpoint(env); err == nil {
		t.Fatal("Account Manager HTTP-only Service was accepted")
	}
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","externalIPs":["198.51.100.5"],"selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	if err := lkeRequireExistingServiceRegistrationEndpoint(env); err == nil {
		t.Fatal("externally addressed Account Manager registration Service was accepted")
	}
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"http","port":80,"targetPort":"http"},{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	if err := lkeRequireExistingServiceRegistrationEndpoint(env); err != nil {
		t.Fatal(err)
	}
}

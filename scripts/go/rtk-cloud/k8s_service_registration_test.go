package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEAccountManagerServiceRegistrationIsOptIn(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	workload := lkeWorkload{Key: "account-manager", Name: "account-manager", Namespace: lkeNamespaceName(env, "account-manager"), Port: 8080, Image: "example.test/account-manager:reviewed"}
	for name, manifest := range map[string]string{
		"deployment": lkeDeploymentManifest(env, workload, nil),
		"service":    lkeServiceManifest(env, workload),
	} {
		if strings.Contains(manifest, "service-reg") || strings.Contains(manifest, "account-manager-service-registration-tls") {
			t.Fatalf("%s enabled service registration without opt-in", name)
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "allow-platform-service-registration") {
			t.Fatal("registration NetworkPolicy appeared without opt-in")
		}
	}
}

func TestServiceRegistrationTLSSecretRequiresCompleteData(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("test-only"))
	data := map[string]any{}
	for _, key := range serviceRegistrationTLSSecretKeys {
		data[key] = encoded
	}
	if err := validateServiceRegistrationSecretData(map[string]any{"data": data}); err != nil {
		t.Fatal(err)
	}
	for _, key := range serviceRegistrationTLSSecretKeys {
		missing := map[string]any{}
		for name, value := range data {
			missing[name] = value
		}
		delete(missing, key)
		if err := validateServiceRegistrationSecretData(map[string]any{"data": missing}); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("missing %s was accepted: %v", key, err)
		}
	}
	data["tls.key"] = "not-base64"
	if err := validateServiceRegistrationSecretData(map[string]any{"data": data}); err == nil || !strings.Contains(err.Error(), "tls.key") {
		t.Fatalf("invalid key data was accepted: %v", err)
	}
}

func TestLKERequiresExistingServiceRegistrationSecret(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireServiceRegistrationSecret(env); err == nil {
		t.Fatal("missing service registration Secret was accepted")
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireServiceRegistrationSecret(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKEAccountManagerServiceRegistrationRendersPrivateMTLSBoundary(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	workload := lkeWorkload{Key: "account-manager", Name: "account-manager", Namespace: lkeNamespaceName(env, "account-manager"), Port: 8080, Image: "example.test/account-manager:reviewed"}
	deployment := lkeDeploymentManifest(env, workload, nil)
	service := lkeServiceManifest(env, workload)
	policy := lkeAllowServiceRegistrationNetworkPolicyManifest(env)
	for name, manifest := range map[string]string{"deployment": deployment, "service": service, "policy": policy} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	for _, want := range []string{
		"name: service-reg\n              containerPort: 8443",
		"name: ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT\n              value: \"8443\"",
		"name: ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_CERT",
		"name: ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_KEY",
		"name: ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CA",
		"name: ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL",
		"secretName: account-manager-service-registration-tls",
		"defaultMode: 0440",
		"fsGroup: 10001",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "name: service-reg\n      port: 8443\n      targetPort: service-reg") || strings.Contains(service, "type: LoadBalancer") {
		t.Fatal("registration Service is not internal on port 8443")
	}
	for _, want := range []string{
		"namespace: video-cloud-staging-account-manager",
		"kubernetes.io/metadata.name: video-cloud-staging-video-cloud",
		"app.kubernetes.io/name: account-manager",
		"video-cloud-mqttfoundation",
		"video-cloud-shadowworker",
		"video-cloud-webrtcservice",
		"video-cloud-videostorage",
		"port: service-reg\n",
	} {
		if !strings.Contains(policy, want) {
			t.Fatalf("registration policy lacks %q", want)
		}
	}
	found := false
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		found = found || strings.Contains(manifest, "allow-platform-service-registration")
	}
	if !found {
		t.Fatal("enabled registration NetworkPolicy was not included")
	}
}

func TestRegistrationPolicyUsesPodListenerInsteadOfServicePort(t *testing.T) {
	// The installed staging Service exposes 8443 but resolves service-reg to
	// Pod port 9444. NetworkPolicy evaluates the destination Pod port.
	var policy struct {
		Spec struct {
			Ingress []struct {
				Ports []struct {
					Protocol string `yaml:"protocol"`
					Port     any    `yaml:"port"`
				} `yaml:"ports"`
			} `yaml:"ingress"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(lkeAllowServiceRegistrationNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})), &policy); err != nil {
		t.Fatal(err)
	}
	for _, listener := range []struct {
		name string
		port int
	}{{"service-reg", 9444}, {"service-reg", 8443}} {
		allowed := false
		for _, ingress := range policy.Spec.Ingress {
			for _, port := range ingress.Ports {
				if port.Protocol != "TCP" {
					t.Fatalf("unexpected protocol %q", port.Protocol)
				}
				name, ok := port.Port.(string)
				if !ok {
					t.Fatalf("numeric policy port %v bypasses Pod listener resolution", port.Port)
				}
				if name != "service-reg" || len(name) > 15 {
					t.Fatalf("unrelated listener permitted: %s", name)
				}
				allowed = allowed || name == listener.name
			}
		}
		if !allowed {
			t.Fatalf("Service 8443 -> Pod %s:%d is blocked", listener.name, listener.port)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func TestPaymentSimulatorLKEManifestsUseApprovedIsolatedTopology(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "payment-simulator-test-seed")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":    "account-manager.video-cloud-staging.realtekconnect.com",
		"LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/account-manager:test",
	}
	secret := lkeAccountManagerSecretManifest(env)
	for _, want := range []string{
		`PAYMENT_SIMULATOR_ENABLED: "true"`,
		`PAYMENT_SIMULATOR_RUN_ID: "video-cloud-staging"`,
		`PAYMENT_SIMULATOR_BASE_URL: "http://payment-simulator.video-cloud-staging-account-manager.svc.cluster.local:80"`,
		`PAYMENT_SIMULATOR_PUBLIC_BASE_URL: "https://payment-simulator.video-cloud-staging.realtekconnect.com"`,
		`PAYMENT_SIMULATOR_CALLBACK_URL: "http://account-manager.video-cloud-staging-account-manager.svc.cluster.local:80/v1/internal/payment-simulator/setup-callback"`,
		`PAYMENT_WORKER_ENABLED: "true"`,
		"PAYMENT_REFERENCE_ENCRYPTION_KEY:",
	} {
		if !strings.Contains(secret, want) {
			t.Fatalf("runtime secret missing %q:\n%s", want, secret)
		}
	}
	for name, manifest := range map[string]string{
		"simulator": lkePaymentSimulatorDeploymentManifest(env),
		"worker":    lkeAccountManagerPaymentWorkerManifest(env),
		"service":   lkePaymentSimulatorServiceManifest(env),
		"network":   lkeAllowAccountManagerPaymentSimulatorNetworkPolicyManifest(env),
	} {
		if strings.Contains(manifest, "<no value>") || !strings.Contains(manifest, "video-cloud-staging-account-manager") {
			t.Fatalf("invalid %s manifest:\n%s", name, manifest)
		}
	}
	if manifest := lkePaymentSimulatorDeploymentManifest(env); !strings.Contains(manifest, `/app/rtk-account-manager-payment-simulator`) || !strings.Contains(manifest, `path: /internal/v1/health`) {
		t.Fatalf("simulator deployment is incomplete:\n%s", manifest)
	}
	if manifest := lkeAccountManagerPaymentWorkerManifest(env); !strings.Contains(manifest, `/app/rtk-account-manager-payment-worker`) {
		t.Fatalf("payment worker deployment is incomplete:\n%s", manifest)
	}
}

func TestPaymentSimulatorUsesApprovedPublicTLSRoute(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}
	routes := lkePublicHTTPSBaseRoutes(env)
	found := false
	for _, route := range routes {
		if route.Host == "payment-simulator.video-cloud-staging.realtekconnect.com" {
			found = route.Service == "payment-simulator" && route.ServicePort == 80 && route.TargetPort == 8081
		}
	}
	if !found {
		t.Fatalf("approved payment simulator route missing: %+v", routes)
	}
}

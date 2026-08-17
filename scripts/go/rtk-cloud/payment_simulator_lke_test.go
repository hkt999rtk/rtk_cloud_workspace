package main

import (
	"strings"
	"testing"
)

func TestPaymentSimulatorLKEManifestsUseApprovedIsolatedTopology(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "payment-simulator-test-seed")
	env := map[string]string{
		"CLOUD_STACK_NAME":       "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":     "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN": "account-manager.video-cloud-staging.realtekconnect.com",
		"LKE_BILLING_IMAGE":      "registry.example.test/billing:test",
	}
	secret := lkeBillingSecretManifest(env)
	for _, want := range []string{
		"BILLING_SERVICE_TOKEN:",
		"BILLING_INTERNAL_TOKEN:",
		"BILLING_DEBIT_TOKEN:",
		`BILLING_DEBIT_SOURCE: "rtk_billing"`,
		`PAYMENT_SIMULATOR_ENABLED: "true"`,
		`PAYMENT_SIMULATOR_RUN_ID: "video-cloud-staging"`,
		`PAYMENT_SIMULATOR_BASE_URL: "http://payment-simulator.video-cloud-staging-billing.svc.cluster.local:80"`,
		`PAYMENT_SIMULATOR_PUBLIC_BASE_URL: "https://payment-simulator.video-cloud-staging.realtekconnect.com"`,
		`PAYMENT_SIMULATOR_CALLBACK_URL: "http://billing.video-cloud-staging-billing.svc.cluster.local:80/v1/internal/payment-simulator/setup-callback"`,
		`PAYMENT_WORKER_ENABLED: "true"`,
		"PAYMENT_REFERENCE_ENCRYPTION_KEY:",
	} {
		if !strings.Contains(secret, want) {
			t.Fatalf("runtime secret missing %q:\n%s", want, secret)
		}
	}
	if lkeBillingServiceToken() == lkeBillingInternalToken() || lkeBillingServiceToken() == lkeBillingDebitToken() || lkeBillingInternalToken() == lkeBillingDebitToken() {
		t.Fatal("Billing tenant, internal, and debit credentials must be distinct")
	}
	for name, manifest := range map[string]string{
		"simulator": lkePaymentSimulatorDeploymentManifest(env),
		"worker":    lkeBillingPaymentWorkerManifest(env),
		"service":   lkePaymentSimulatorServiceManifest(env),
		"network":   lkeAllowBillingPaymentSimulatorNetworkPolicyManifest(env),
	} {
		if strings.Contains(manifest, "<no value>") || !strings.Contains(manifest, "video-cloud-staging-billing") {
			t.Fatalf("invalid %s manifest:\n%s", name, manifest)
		}
	}
	if manifest := lkePaymentSimulatorDeploymentManifest(env); !strings.Contains(manifest, `/rtk-billing-payment-simulator`) || !strings.Contains(manifest, `path: /internal/v1/health`) {
		t.Fatalf("simulator deployment is incomplete:\n%s", manifest)
	}
	if manifest := lkeBillingPaymentWorkerManifest(env); !strings.Contains(manifest, `/rtk-billing-payment-worker`) {
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

func TestBillingUsesApprovedPublicTLSRoute(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}
	routes := lkePublicHTTPSBaseRoutes(env)
	found := false
	for _, route := range routes {
		if route.Host == "billing.video-cloud-staging.realtekconnect.com" {
			found = route.Namespace == "video-cloud-staging-billing" && route.Service == "billing" && route.ServicePort == 80 && route.TargetPort == 8080
		}
	}
	if !found {
		t.Fatalf("approved Billing route missing: %+v", routes)
	}
}

func TestLKEBillingRuntimeHelpersResolveExplicitSecretsAndImages(t *testing.T) {
	t.Setenv("PAYMENT_REFERENCE_ENCRYPTION_KEY", "explicit-reference-key")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/account-manager:test",
		"LKE_BILLING_IMAGE":         "registry.example.test/billing:test",
	}
	if got := lkePaymentReferenceEncryptionKey(env); got != "explicit-reference-key" {
		t.Fatalf("reference key = %q", got)
	}
	if got := lkeAccountManagerImage(env); got != env["LKE_ACCOUNT_MANAGER_IMAGE"] {
		t.Fatalf("account-manager image = %q", got)
	}
	if got := lkeBillingImage(env); got != env["LKE_BILLING_IMAGE"] {
		t.Fatalf("billing image = %q", got)
	}
	if got := lkeAccountManagerImage(map[string]string{}); got != "" {
		t.Fatalf("missing account-manager image = %q", got)
	}
	if got := lkeBillingImage(map[string]string{}); got != "" {
		t.Fatalf("missing billing image = %q", got)
	}
}

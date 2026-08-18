package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKEApplyBaseTargetsOnlyRequestedBillingWorkloads(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	opts := provisionOptions{workloads: []string{"billing", "cloud-admin"}}

	if err := lkeApplyBase(env, opts); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{"name: video-cloud-staging-billing", "name: video-cloud-staging-admin"} {
		if !strings.Contains(log, want) {
			t.Fatalf("targeted base apply missing %q:\n%s", want, log)
		}
	}
	for _, unwanted := range []string{"name: video-cloud-staging-video-cloud", "rtk-cloud-runtime", "metrics-server"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("targeted base apply unexpectedly included %q:\n%s", unwanted, log)
		}
	}
}

func TestLKEImportExistingRuntimeSecretReadsClusterWithoutPrintingValues(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	found, err := lkeImportExistingRuntimeSecret(env, "platform", "postgresql-runtime", map[string]string{"POSTGRES_PASSWORD": "postgres"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !found || lkeRuntimeSecretCache["postgres"] != "existing-postgres" {
		t.Fatal("existing PostgreSQL credential was not imported")
	}
	found, err = lkeImportExistingRuntimeSecret(env, "billing", "billing-runtime", map[string]string{"BILLING_SERVICE_TOKEN": "billing-service-token"}, false)
	if err != nil || found {
		t.Fatalf("optional missing Billing secret: found=%t err=%v", found, err)
	}
	if _, err := lkeImportExistingRuntimeSecret(env, "billing", "missing-required", map[string]string{"VALUE": "value"}, true); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("required missing secret error = %v", err)
	}
	if strings.Contains(readTestFile(t, logPath), "existing-postgres") {
		t.Fatal("kubectl command log exposed an imported secret value")
	}
}

func TestLKEApplyTargetedBillingDependenciesAvoidsOpenBao(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "targeted-billing-test-seed")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.realtekconnect.com",
		"LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/account-manager:billing-permissions",
	}
	opts := provisionOptions{workloads: []string{"billing", "cloud-admin"}}

	if err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, opts); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{"name: allow-postgres-clients", "name: allow-cloud-admin-account-manager", "name: allow-cloud-admin-billing", "name: billing-runtime", "name: billing-database-ensure", "job/billing-database-ensure", "name: account-manager-migrate", "job/account-manager-migrate", "registry.example.test/account-manager:billing-permissions", "name: cloud-admin-billing-client"} {
		if !strings.Contains(log, want) {
			t.Fatalf("targeted dependency apply missing %q:\n%s", want, log)
		}
	}
	if strings.Index(log, "name: allow-postgres-clients") > strings.Index(log, "name: billing-database-ensure") {
		t.Fatalf("PostgreSQL network policy must be applied before the Billing database job:\n%s", log)
	}
	if strings.Index(log, "job/billing-database-ensure") > strings.Index(log, "name: account-manager-migrate") {
		t.Fatalf("Billing database ensure must finish before Account Manager migration:\n%s", log)
	}
	if strings.Contains(strings.ToLower(log), "openbao") {
		t.Fatalf("targeted Billing dependency apply touched OpenBao:\n%s", log)
	}
	if lkeRuntimeSecretCache["postgres"] != "existing-postgres" {
		t.Fatal("targeted dependency apply rotated the PostgreSQL credential")
	}
}

func fakeKubectlForTargetedBillingDeploy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"get secret postgresql-runtime"* ]]; then
  printf '{"data":{"POSTGRES_PASSWORD":"ZXhpc3RpbmctcG9zdGdyZXM="}}\n'
  exit 0
fi
if [[ "$*" == *"get secret billing-runtime"* || "$*" == *"get secret missing-required"* ]]; then
  exit 0
fi
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
  if [[ "$*" == *"apply -f -"* ]]; then
    cat
    printf '\n---\n'
  fi
} >> "` + logPath + `"
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	return logPath
}

func TestTargetedBillingDeployImportsExistingRuntimeSecretsWithoutRotation(t *testing.T) {
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	values := map[string]string{
		"POSTGRES_PASSWORD":                 "existing-postgres",
		"BILLING_SERVICE_TOKEN":             "existing-service-token",
		"BILLING_INTERNAL_TOKEN":            "existing-internal-token",
		"BILLING_DEBIT_TOKEN":               "existing-debit-token",
		"PAYMENT_SIMULATOR_SHARED_SECRET":   "existing-simulator-secret",
		"PAYMENT_SIMULATOR_CALLBACK_SECRET": "existing-callback-secret",
		"PAYMENT_REFERENCE_ENCRYPTION_KEY":  "existing-reference-key",
	}
	data := map[string]string{}
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	mappings := map[string]string{
		"POSTGRES_PASSWORD":                 "postgres",
		"BILLING_SERVICE_TOKEN":             "billing-service-token",
		"BILLING_INTERNAL_TOKEN":            "billing-internal-token",
		"BILLING_DEBIT_TOKEN":               "billing-debit-token",
		"PAYMENT_SIMULATOR_SHARED_SECRET":   "payment-simulator-shared",
		"PAYMENT_SIMULATOR_CALLBACK_SECRET": "payment-simulator-callback",
		"PAYMENT_REFERENCE_ENCRYPTION_KEY":  "payment-reference-encryption-key",
	}
	if err := lkeSeedRuntimeSecretCacheFromK8SSecretJSON(raw, mappings); err != nil {
		t.Fatal(err)
	}
	for key, cacheKey := range mappings {
		if lkeRuntimeSecretCache[cacheKey] != values[key] {
			t.Fatalf("runtime secret %s was not preserved", key)
		}
	}
	if got := lkePaymentReferenceEncryptionKey(map[string]string{}); got != "existing-reference-key" {
		t.Fatalf("payment reference key rotated: %q", got)
	}
}

func TestTargetedBillingDeployRejectsMalformedExistingRuntimeSecret(t *testing.T) {
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	if err := lkeSeedRuntimeSecretCacheFromK8SSecretJSON([]byte(`{"data":{}}`), map[string]string{"BILLING_SERVICE_TOKEN": "billing-service-token"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing existing credential error = %v", err)
	}
	if err := lkeSeedRuntimeSecretCacheFromK8SSecretJSON([]byte(`{"data":{"BILLING_SERVICE_TOKEN":"not-base64"}}`), map[string]string{"BILLING_SERVICE_TOKEN": "billing-service-token"}); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed existing credential error = %v", err)
	}
}

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

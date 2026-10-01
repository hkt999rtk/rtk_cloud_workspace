package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	t.Setenv("LKE_BILLING_MIGRATION_JOB_ENABLED", "true")
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
	for _, want := range []string{"name: allow-postgres-clients", "name: allow-cloud-admin-account-manager", "name: allow-cloud-admin-billing", "name: billing-migration-database", "name: billing-runtime", "name: billing-database-ensure", "job/billing-database-ensure", "name: billing-database-migrate", "job/billing-database-migrate", "name: account-manager-migrate", "job/account-manager-migrate", "registry.example.test/account-manager:billing-permissions", "name: cloud-admin-billing-client"} {
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
	if strings.Index(log, "job/billing-database-ensure") > strings.Index(log, "delete job billing-database-migrate") {
		t.Fatal("Billing database ensure must finish before schema migration")
	}
}

func TestLKEBillingDatabaseSeparationFailsBeforeTargetedApply(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "targeted-billing-test-seed")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging"}
	if err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"billing"}}); err == nil || !strings.Contains(err.Error(), "one-shot migration Job") {
		t.Fatalf("expected Billing database role preflight failure, got %v", err)
	}
	if log := readTestFile(t, logPath); log != "" {
		t.Fatalf("Billing preflight failure applied Kubernetes resources: %q", log)
	}
}

func TestLKEBillingDatabaseSeparationFailsBeforeFullApply(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "full-billing-test-seed")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging"}
	if err := lkeApplyRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"billing"}}); err == nil || !strings.Contains(err.Error(), "one-shot migration Job") {
		t.Fatalf("expected full Billing database role preflight failure, got %v", err)
	}
	if log := readTestFile(t, logPath); log != "" {
		t.Fatalf("Billing preflight failure applied Kubernetes resources: %q", log)
	}
}

func TestLKEBillingDatabaseRolesAndManifests(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "billing-role-isolation-test-seed")
	t.Setenv("LKE_BILLING_MIGRATION_JOB_ENABLED", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging"}
	if err := lkeValidateBillingDatabaseRoles(env); err != nil {
		t.Fatal(err)
	}
	runtimeURL, err := url.Parse(lkeBillingDatabaseURL(env))
	if err != nil {
		t.Fatal(err)
	}
	migrationURL, err := url.Parse(lkeBillingMigrationDatabaseURL(env))
	if err != nil {
		t.Fatal(err)
	}
	if runtimeURL.User.Username() != "rtk_billing_runtime_staging" || migrationURL.User.Username() != "postgres" {
		t.Fatalf("unexpected Billing database roles: runtime=%q migration=%q", runtimeURL.User.Username(), migrationURL.User.Username())
	}
	runtimePassword, _ := runtimeURL.User.Password()
	migrationPassword, _ := migrationURL.User.Password()
	if runtimePassword == migrationPassword || len(runtimePassword) < 24 {
		t.Fatal("Billing runtime and migration database passwords must be distinct")
	}
	runtime := lkeBillingSecretManifest(env)
	migration := lkeBillingMigrationSecretManifest(env)
	ensure := lkeBillingDatabaseEnsureJobManifest(env)
	job := lkeBillingMigrationJobManifest(env)
	for _, manifest := range []string{runtime, migration, ensure, job} {
		if strings.Contains(manifest, "%!") {
			t.Fatal("malformed Billing manifest format")
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("invalid Billing manifest YAML: %v", err)
		}
	}
	if strings.Contains(runtime, "POSTGRES_PASSWORD:") || strings.Contains(runtime, migrationPassword) || !strings.Contains(runtime, `BILLING_DB_MIGRATE_ON_STARTUP: "false"`) {
		t.Fatal("Billing runtime Secret contains owner credentials or permits API DDL")
	}
	if !strings.Contains(migration, "POSTGRES_PASSWORD:") || !strings.Contains(migration, "BILLING_RUNTIME_DB_PASSWORD:") || !strings.Contains(job, "name: billing-migration-database, key: DATABASE_URL") || strings.Contains(job, "name: billing-runtime") {
		t.Fatal("Billing migration Job did not use the owner-only Secret")
	}
	for _, grant := range []string{"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES", "GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES", "ALTER DEFAULT PRIVILEGES FOR ROLE postgres"} {
		if !strings.Contains(ensure, grant) {
			t.Fatalf("Billing role ensure Job missing %q", grant)
		}
	}
	if strings.Contains(ensure, "name: billing-runtime") || !strings.Contains(ensure, "name: billing-migration-database") {
		t.Fatal("Billing role ensure Job did not use migration-only credentials")
	}
}

func TestLKEApplyTargetedBillingMigrationBeforeWorkload(t *testing.T) {
	logPath := fakeKubectlForTargetedBillingDeploy(t)
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "targeted-billing-migration-test-seed")
	t.Setenv("LKE_BILLING_MIGRATION_JOB_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":  "video-cloud-staging",
		"LKE_BILLING_IMAGE": "registry.example.test/billing:with-migrate-command",
	}
	if err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"billing"}}); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	for _, want := range []string{
		`BILLING_DB_MIGRATE_ON_STARTUP: "false"`,
		"name: billing-database-migrate",
		"registry.example.test/billing:with-migrate-command",
		`command: ["/rtk-billing-migrate"]`,
		"job/billing-database-migrate",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("targeted Billing migration missing %q:\n%s", want, log)
		}
	}
	if strings.Index(log, "job/billing-database-ensure") > strings.Index(log, "delete job billing-database-migrate") {
		t.Fatalf("Billing database ensure must finish before schema migration:\n%s", log)
	}
}

func TestLKEBillingMigrationJobStopsOnKubectlFailure(t *testing.T) {
	fakeKubectlForTargetedBillingDeploy(t)
	t.Setenv("LKE_BILLING_MIGRATION_JOB_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":  "video-cloud-staging",
		"LKE_BILLING_IMAGE": "registry.example.test/billing:with-migrate-command",
	}
	for _, failure := range []string{"delete", "apply"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("FAKE_KUBECTL_FAIL_MIGRATION_"+strings.ToUpper(failure), "true")
			if err := lkeApplyBillingMigrationJob(env); err == nil {
				t.Fatalf("Billing migration %s failure did not stop rollout", failure)
			}
		})
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
if [[ "${FAKE_KUBECTL_FAIL_MIGRATION_DELETE:-}" == "true" && "$*" == *"delete job billing-database-migrate"* ]]; then
  exit 8
fi
if [[ "${FAKE_KUBECTL_FAIL_MIGRATION_APPLY:-}" == "true" && "$*" == *"apply -f -"* ]]; then
  content="$(cat)"
  if [[ "$content" == *"name: billing-database-migrate"* ]]; then
    exit 9
  fi
fi
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
		"NEWEBPAY_HASH_KEY":                 "existing-newebpay-hash-key-1234",
		"NEWEBPAY_HASH_IV":                  "existing-hash-iv",
		"PAYMENT_SIMULATOR_ADMIN_TOKEN":     "existing-simulator-admin-token-12",
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
		"NEWEBPAY_HASH_KEY":                 "newebpay-hash-key",
		"NEWEBPAY_HASH_IV":                  "newebpay-hash-iv",
		"PAYMENT_SIMULATOR_ADMIN_TOKEN":     "payment-simulator-admin-token",
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
	if err := lkeSeedRuntimeSecretCacheFromK8SSecretJSONWithOptional(
		[]byte(`{"data":{"BILLING_SERVICE_TOKEN":"dmFsaWQ=","NEWEBPAY_HASH_KEY":"not-base64"}}`),
		map[string]string{"BILLING_SERVICE_TOKEN": "billing-service-token"},
		map[string]string{"NEWEBPAY_HASH_KEY": "newebpay-hash-key"},
	); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed optional credential error = %v", err)
	}
}

func TestTargetedBillingDeployAcceptsLegacySecretWithoutNewebPayKeys(t *testing.T) {
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	raw := []byte(`{"data":{"BILLING_SERVICE_TOKEN":"ZXhpc3Rpbmctc2VydmljZS10b2tlbg=="}}`)
	if err := lkeSeedRuntimeSecretCacheFromK8SSecretJSONWithOptional(raw,
		map[string]string{"BILLING_SERVICE_TOKEN": "billing-service-token"},
		map[string]string{"NEWEBPAY_HASH_KEY": "newebpay-hash-key", "NEWEBPAY_HASH_IV": "newebpay-hash-iv"},
	); err != nil {
		t.Fatal(err)
	}
	if lkeRuntimeSecretCache["billing-service-token"] != "existing-service-token" {
		t.Fatal("legacy Billing credential was not preserved")
	}
	if lkeRuntimeSecretCache["newebpay-hash-key"] != "" || lkeRuntimeSecretCache["newebpay-hash-iv"] != "" {
		t.Fatal("missing optional NewebPay credentials must not be fabricated during import")
	}
}

func TestPaymentSimulatorLKEManifestsUseApprovedIsolatedTopology(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "payment-simulator-test-seed")
	env := map[string]string{
		"CLOUD_STACK_NAME":       "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":     "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN": "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":     "admin.video-cloud-staging.realtekconnect.com",
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
		`NEWEBPAY_ENABLED: "true"`,
		`NEWEBPAY_ENVIRONMENT: "sandbox"`,
		`NEWEBPAY_MERCHANT_ID: "RTKSIMULATOR"`,
		`NEWEBPAY_SIMULATOR_BASE_URL: "https://payment-simulator.video-cloud-staging.realtekconnect.com"`,
		`NEWEBPAY_NOTIFY_URL: "http://billing.video-cloud-staging-billing.svc.cluster.local:80/v1/payment-webhooks/newebpay"`,
		`NEWEBPAY_RETURN_URL: "https://admin.video-cloud-staging.realtekconnect.com/console/billing/activity"`,
		`PAYMENT_SIMULATOR_NEWEBPAY_NOTIFY_URL: "http://billing.video-cloud-staging-billing.svc.cluster.local:80/v1/payment-webhooks/newebpay"`,
		"NEWEBPAY_HASH_KEY:",
		"NEWEBPAY_HASH_IV:",
		"PAYMENT_SIMULATOR_ADMIN_TOKEN:",
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
	if len(lkeNewebPayHashKey(env)) != 32 || len(lkeNewebPayHashIV(env)) != 16 || len(lkePaymentSimulatorAdminToken(env)) < 32 {
		t.Fatal("NewebPay simulator credentials have invalid lengths")
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

func TestNewebPayRuntimeChangeRollsBillingSimulatorAndWorker(t *testing.T) {
	t.Setenv("PAYMENT_SIMULATOR_DOMAIN", "")
	base := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN": "admin.video-cloud-staging.realtekconnect.com",
		"LKE_BILLING_IMAGE":  "registry.example.test/billing:test",
		"NEWEBPAY_HASH_KEY":  "11111111111111111111111111111111",
		"NEWEBPAY_HASH_IV":   "1111111111111111",
	}
	workload := lkeWorkload{Key: "billing", Name: "billing", Image: base["LKE_BILLING_IMAGE"]}
	for _, mutation := range []struct {
		name, key, value string
	}{
		{"credential", "NEWEBPAY_HASH_KEY", "22222222222222222222222222222222"},
		{"hosted endpoint", "PAYMENT_SIMULATOR_DOMAIN", "payments-alt.video-cloud-staging.realtekconnect.com"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := mapsClone(base)
			changed[mutation.key] = mutation.value
			for name, manifests := range map[string][2]string{
				"billing":   {lkeDeploymentManifest(base, workload, nil), lkeDeploymentManifest(changed, workload, nil)},
				"simulator": {lkePaymentSimulatorDeploymentManifest(base), lkePaymentSimulatorDeploymentManifest(changed)},
				"worker":    {lkeBillingPaymentWorkerManifest(base), lkeBillingPaymentWorkerManifest(changed)},
			} {
				if manifests[0] == manifests[1] {
					t.Fatalf("%s deployment checksum did not change with NewebPay %s", name, mutation.name)
				}
			}
		})
	}
}

func mapsClone(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
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

func TestLKEBillingPayPalRuntimeUsesSelectedEnvironmentSecrets(t *testing.T) {
	previousStore, previousDir := activeCanonicalSecretStore, lkeRuntimeSecretStateDir
	activeCanonicalSecretStore, lkeRuntimeSecretStateDir = true, t.TempDir()
	t.Cleanup(func() {
		activeCanonicalSecretStore, lkeRuntimeSecretStateDir = previousStore, previousDir
	})
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-dev",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-dev.example.test",
		"PAYPAL_ENABLED":     "true",
		"PAYPAL_ENVIRONMENT": "sandbox",
	}
	if err := lkeValidatePayPalRuntime(env); err == nil || !strings.Contains(err.Error(), "paypal-client-id") {
		t.Fatalf("missing PayPal credentials: %v", err)
	}
	for name, value := range map[string]string{
		"paypal-client-id":     "sandbox-client",
		"paypal-client-secret": "sandbox-secret",
		"paypal-webhook-id":    "sandbox-webhook",
	} {
		if err := os.WriteFile(filepath.Join(lkeRuntimeSecretStateDir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := lkeValidatePayPalRuntime(env); err != nil {
		t.Fatal(err)
	}
	manifest := lkeBillingSecretManifest(env)
	for _, want := range []string{
		"PAYPAL_ENABLED: \"true\"",
		"PAYPAL_ENVIRONMENT: \"sandbox\"",
		"PAYPAL_CLIENT_ID: \"sandbox-client\"",
		"PAYPAL_CLIENT_SECRET: \"sandbox-secret\"",
		"PAYPAL_WEBHOOK_ID: \"sandbox-webhook\"",
		"PAYPAL_RETURN_URL: \"https://billing.video-cloud-dev.example.test/v1/payment-returns/paypal\"",
		"PAYPAL_CANCEL_URL: \"https://billing.video-cloud-dev.example.test/v1/payment-returns/paypal/cancel\"",
		"PAYPAL_AFTER_RETURN_URL: \"https://admin.video-cloud-dev.example.test\"",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("PayPal runtime manifest missing %s", want)
		}
	}
	env["PAYPAL_ENVIRONMENT"] = "production"
	if err := lkeValidatePayPalRuntime(env); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("development production checkout was accepted: %v", err)
	}
	activeCanonicalSecretStore = false
	if err := lkeValidatePayPalRuntime(env); err == nil || !strings.Contains(err.Error(), "SecretStore") {
		t.Fatalf("PayPal checkout without environment SecretStore was accepted: %v", err)
	}
	activeCanonicalSecretStore = true
	env["PAYPAL_ENABLED"] = "false"
	if err := lkeValidatePayPalRuntime(env); err != nil {
		t.Fatal(err)
	}
	if manifest := lkeBillingSecretManifest(env); !strings.Contains(manifest, "PAYPAL_CLIENT_SECRET: \"\"") {
		t.Fatal("disabled PayPal checkout exposed a client secret")
	}
}

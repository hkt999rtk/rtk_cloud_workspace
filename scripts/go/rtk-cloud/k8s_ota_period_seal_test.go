package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEOTAPlatformSealScheduleUsesDedicatedCredentialAndUTCMonth(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{
		"ota-platform-seal-token": strings.Repeat("p", 40),
		"billing-service-token":   strings.Repeat("s", 40),
		"billing-internal-token":  strings.Repeat("i", 40),
	}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging",
		"BILLING_DOMAIN":            "billing.staging.example.test",
		"LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:reviewed",
	}
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "false")
	if lkeOTAPlatformSealScheduleEnabled(env) || !strings.Contains(lkeBillingSecretManifest(env), "BILLING_OTA_PLATFORM_SEAL_TOKEN") {
		t.Fatal("disabling future runs removed the credential needed by historical Jobs")
	}
	if err := lkeRequireOTAPlatformSealSchedule(env); err != nil {
		t.Fatalf("disabled schedule should not require a new rollout: %v", err)
	}
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "true")
	if err := lkeRequireOTAPlatformSealSchedule(env); err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{
		"cronjob": lkeOTAPlatformSealCronJobManifest(env),
		"secret":  lkeOTAPlatformSealRuntimeSecretManifest(env),
		"billing": lkeBillingSecretManifest(env),
	} {
		var decoded map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &decoded); err != nil {
			t.Fatalf("invalid %s YAML: %v", name, err)
		}
	}
	job := lkeOTAPlatformSealCronJobManifest(env)
	for _, want := range []string{"kind: CronJob", "namespace: video-cloud-staging-account-manager", `schedule: "0 3 3 * *"`, "timeZone: Etc/UTC", "concurrencyPolicy: Forbid", "automountServiceAccountToken: false", `args: ["--all-brand-clouds", "--month", "previous", "--submit"]`, "secretRef: { name: ota-platform-seal-runtime }"} {
		if !strings.Contains(job, want) {
			t.Fatalf("Platform seal CronJob lacks %q", want)
		}
	}
	if strings.Contains(job, strings.Repeat("p", 40)) {
		t.Fatal("Platform seal token leaked into CronJob Pod template")
	}
	secret := lkeOTAPlatformSealRuntimeSecretManifest(env)
	if !strings.Contains(secret, `BILLING_OTA_PERIOD_SEAL_BASE_URL: "https://billing.staging.example.test"`) ||
		!strings.Contains(secret, `BILLING_OTA_PLATFORM_SEAL_TOKEN: "`+strings.Repeat("p", 40)+`"`) ||
		!strings.Contains(lkeBillingSecretManifest(env), `BILLING_OTA_PLATFORM_SEAL_TOKEN: "`+strings.Repeat("p", 40)+`"`) {
		t.Fatal("source and Billing runtime do not share the dedicated HTTPS Platform seal credential")
	}
}

func TestLKEOTAPlatformSealScheduleRejectsWeakOrReusedToken(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{
		"ota-platform-seal-token": "short",
		"billing-service-token":   strings.Repeat("s", 40),
		"billing-internal-token":  strings.Repeat("i", 40),
	}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "true")
	env := map[string]string{"BILLING_DOMAIN": "billing.staging.example.test", "LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:reviewed"}
	if err := lkeRequireOTAPlatformSealSchedule(env); err == nil {
		t.Fatal("weak Platform seal token was accepted")
	}
	lkeRuntimeSecretCache["ota-platform-seal-token"] = lkeRuntimeSecretCache["billing-service-token"]
	if err := lkeRequireOTAPlatformSealSchedule(env); err == nil {
		t.Fatal("reused Billing service token was accepted")
	}
	lkeRuntimeSecretCache["ota-platform-seal-token"] = strings.Repeat("p", 40)
	delete(env, "BILLING_DOMAIN")
	if err := lkeRequireOTAPlatformSealSchedule(env); err == nil {
		t.Fatal("missing Billing HTTPS domain was accepted")
	}
	env["BILLING_DOMAIN"] = "billing.staging.example.test"
	delete(env, "LKE_ACCOUNT_MANAGER_IMAGE")
	if err := lkeRequireOTAPlatformSealSchedule(env); err == nil {
		t.Fatal("missing Account Manager image was accepted")
	}
}

func TestLKEOTAPlatformSealScheduleRejectsTargetedDeploymentBeforeMutation(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"ota-platform-seal-token": "short"}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"BILLING_DOMAIN":            "billing.staging.example.test",
		"LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:reviewed",
	}
	err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"account-manager"}})
	if err == nil || !strings.Contains(err.Error(), "distinct canonical token") {
		t.Fatalf("targeted deploy must fail before any Kubernetes mutation for a weak token: %v", err)
	}
}

func TestLKEOTAPlatformSealScheduleRequiresEnvironmentLocalSecret(t *testing.T) {
	oldCanonical, oldDir := activeCanonicalSecretStore, lkeRuntimeSecretStateDir
	activeCanonicalSecretStore = true
	lkeRuntimeSecretStateDir = t.TempDir()
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretStateDir = oldCanonical, oldDir })
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "false")
	if strings.Contains(lkeBillingSecretManifest(map[string]string{}), "BILLING_OTA_PLATFORM_SEAL_TOKEN") {
		t.Fatal("unprovisioned optional token appeared in Billing")
	}
	t.Setenv("LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED", "true")
	env := map[string]string{"BILLING_DOMAIN": "billing.staging.example.test", "LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:reviewed"}
	if err := lkeRequireOTAPlatformSealSchedule(env); err == nil {
		t.Fatal("missing canonical Platform seal token was accepted")
	}
}

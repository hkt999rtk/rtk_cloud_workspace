package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEOTAProducerSealScheduleIsOptInAndUsesDedicatedToken(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{
		"ota-producer-seal-token": strings.Repeat("p", 40),
		"ota-platform-seal-token": strings.Repeat("g", 40),
		"billing-service-token":   strings.Repeat("s", 40),
		"billing-internal-token":  strings.Repeat("i", 40),
	}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging",
		"LKE_VIDEO_CLOUD_IMAGE":   "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_BLOB_REGION": "us-sea", "VIDEO_CLOUD_BLOB_BUCKET": "private-ota",
		"VIDEO_CLOUD_BLOB_ENDPOINT": "https://us-sea-1.linodeobjects.com",
	}
	t.Setenv("LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED", "false")
	if lkeOTAProducerSealScheduleEnabled(env) || lkeRequireOTAProducerSealSchedule(env) != nil {
		t.Fatal("producer seal schedule should remain off by default")
	}
	if !strings.Contains(lkeBillingSecretManifest(env), "BILLING_OTA_PRODUCER_SEAL_TOKEN") {
		t.Fatal("historical Job token was dropped when future scheduling was disabled")
	}
	for name, manifest := range map[string]string{
		"cronjob": lkeOTAProducerSealCronJobManifest(env),
		"secret":  lkeOTAProducerSealRuntimeSecretManifest(env),
		"billing": lkeBillingSecretManifest(env),
		"policy":  lkeAllowOTABillingNetworkPolicyManifest(env),
	} {
		var decoded map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &decoded); err != nil {
			t.Fatalf("invalid %s YAML: %v", name, err)
		}
	}
	job := lkeOTAProducerSealCronJobManifest(env)
	for _, want := range []string{
		"kind: CronJob", "namespace: video-cloud-staging-video-cloud", `schedule: "0 4 3-7 * *"`,
		"timeZone: Etc/UTC", "concurrencyPolicy: Forbid", "automountServiceAccountToken: false",
		`args: ["--all-brand-clouds", "--month", "previous"]`,
		"name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "name: VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN",
	} {
		if !strings.Contains(job, want) {
			t.Fatalf("producer CronJob lacks %q", want)
		}
	}
	if strings.Contains(job, strings.Repeat("p", 40)) {
		t.Fatal("producer token leaked into CronJob template")
	}
	policy := lkeAllowOTABillingNetworkPolicyManifest(env)
	for _, want := range []string{"namespace: video-cloud-staging-billing", "video-cloud-otaservice", "ota-producer-period-seal", "port: 8080"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("OTA Billing ingress lacks %q", want)
		}
	}
	if !strings.Contains(lkeOTAProducerSealRuntimeSecretManifest(env), `VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN: "`+strings.Repeat("p", 40)+`"`) ||
		!strings.Contains(lkeBillingSecretManifest(env), `BILLING_OTA_PRODUCER_SEAL_TOKEN: "`+strings.Repeat("p", 40)+`"`) {
		t.Fatal("source and Billing do not share the dedicated producer seal credential")
	}
}

func TestLKEOTAProducerSealScheduleRejectsMissingServiceAndWeakToken(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"ota-producer-seal-token": "short"}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED", "true")
	env := map[string]string{}
	if err := lkeRequireOTAProducerSealSchedule(env); err == nil || !strings.Contains(err.Error(), "independent registered OTA service") {
		t.Fatalf("schedule without OTA service accepted: %v", err)
	}
	env["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
	if err := lkeRequireOTAProducerSealSchedule(env); err == nil || !strings.Contains(err.Error(), "distinct canonical token") {
		t.Fatalf("weak producer token accepted: %v", err)
	}
	lkeRuntimeSecretCache["ota-producer-seal-token"] = strings.Repeat("s", 40)
	lkeRuntimeSecretCache["billing-service-token"] = strings.Repeat("s", 40)
	if err := lkeRequireOTAProducerSealSchedule(env); err == nil || !strings.Contains(err.Error(), "distinct canonical token") {
		t.Fatalf("reused Billing token accepted: %v", err)
	}
}

func TestLKEOTAProducerSealScheduleRejectsIncompleteDeploymentInputs(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"ota-producer-seal-token": strings.Repeat("p", 40)}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED", "true")
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")

	env := map[string]string{}
	if err := lkeRequireOTAProducerSealSchedule(env); err == nil || !strings.Contains(err.Error(), "selected Video Cloud image") {
		t.Fatalf("schedule without a selected image accepted: %v", err)
	}
	env["LKE_VIDEO_CLOUD_IMAGE"] = "example.test/video-cloud:reviewed"
	if err := lkeRequireOTAProducerSealSchedule(env); err == nil || !strings.Contains(err.Error(), "strict Product OTA entitlements") {
		t.Fatalf("schedule without OTA service prerequisites accepted: %v", err)
	}
	if err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"account-manager", "billing", "video-cloud"}}); err == nil || !strings.Contains(err.Error(), "strict Product OTA entitlements") {
		t.Fatalf("targeted deploy mutated Kubernetes before producer prerequisite validation: %v", err)
	}
}

func TestLKEOTAProducerSealScheduleRejectsPartialAndUnsafeFullDeploy(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"ota-producer-seal-token": "short"}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED", "true")
	env := map[string]string{"LKE_OTA_SERVICE_REGISTRATION_ENABLED": "true"}
	if err := lkeApplyTargetedRuntimeDependencies(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}}); err == nil || !strings.Contains(err.Error(), "coordinated") {
		t.Fatalf("partial producer deployment accepted: %v", err)
	}
	if err := lkeApplyRuntimeDependencies(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "distinct canonical token") {
		t.Fatalf("full deployment mutated Kubernetes before producer token validation: %v", err)
	}
}

func TestLKEOTAProducerTokenRotationChangesBillingPodTemplate(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"ota-producer-seal-token": strings.Repeat("p", 40)}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	workload := lkeWorkload{Key: "billing", Name: "billing", Image: "example.test/billing:reviewed"}
	before := lkeDeploymentManifest(env, workload, nil)
	lkeRuntimeSecretCache["ota-producer-seal-token"] = strings.Repeat("q", 40)
	after := lkeDeploymentManifest(env, workload, nil)
	if before == after || !strings.Contains(after, "rtk.realtek.com/runtime-checksum") || strings.Contains(after, strings.Repeat("q", 40)) {
		t.Fatal("Billing Pod template did not rotate safely with the producer token")
	}
}

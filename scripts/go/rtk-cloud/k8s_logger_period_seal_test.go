package main

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLoggerProducerSealCredentialBindingsAndGate(t *testing.T) {
	previousCache, previousCanonical := lkeRuntimeSecretCache, activeCanonicalSecretStore
	lkeRuntimeSecretCache = map[string]string{"logger-producer-seal-token": strings.Repeat("l", 40)}
	activeCanonicalSecretStore = false
	t.Cleanup(func() { lkeRuntimeSecretCache, activeCanonicalSecretStore = previousCache, previousCanonical })
	t.Setenv("LKE_LOGGER_BILLING_FACTS_ENABLED", "true")
	t.Setenv("LKE_LOGGER_PERIOD_SEALS_ENABLED", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireLoggerProducerSealToken(env); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ body, key string }{
		{lkeBillingSecretManifest(env), "LOGGER_PRODUCER_SEAL_TOKEN"},
		{lkeVideoCloudWorkersSecretManifest(env), "VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN"},
		{lkeLoggerProducerSealWorkerEnv(env), "VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN"},
	} {
		if !strings.Contains(item.body, item.key) {
			t.Fatalf("missing dedicated credential binding %s", item.key)
		}
	}
	lkeRuntimeSecretCache["logger-producer-seal-token"] = lkeBillingInternalToken()
	if err := lkeRequireLoggerProducerSealToken(env); err == nil {
		t.Fatal("shared usage ingestion credential accepted for source seal")
	}
	lkeRuntimeSecretCache["logger-producer-seal-token"] = ""
	if err := lkeRequireLoggerProducerSealToken(env); err == nil {
		t.Fatal("missing producer credential allowed Logger billing")
	}
	t.Setenv("LKE_LOGGER_BILLING_FACTS_ENABLED", "false")
	if err := lkeRequireLoggerProducerSealToken(env); err != nil || lkeLoggerProducerSealWorkerEnv(env) != "" {
		t.Fatal("standby Logger requires an active billing credential")
	}
}

func TestTargetedOTAEnvironmentScope(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		if err := requireTargetedOTAEnvironment(deploymentConfig{Adapter: "lke", Environment: environment}); err != nil {
			t.Fatal(err)
		}
	}
	for _, cfg := range []deploymentConfig{{Adapter: "lke", Environment: "prod"}, {Adapter: "other", Environment: "staging"}} {
		if err := requireTargetedOTAEnvironment(cfg); err == nil {
			t.Fatal("unreviewed target accepted")
		}
	}
}

func TestLoggerPeriodSealJobUTCGraceAndCredentialScope(t *testing.T) {
	before := time.Date(2026, 10, 1, 23, 59, 59, 0, time.UTC)
	if err := validateLoggerSealJobMonth("2026-09", before); err == nil {
		t.Fatal("Taipei's next calendar day bypassed UTC close grace")
	}
	if err := validateLoggerSealJobMonth("2026-09", before.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, month := range []string{"2026-9", "previous", "2026-10", ""} {
		if err := validateLoggerSealJobMonth(month, before); err == nil {
			t.Fatalf("ambiguous or incomplete month accepted: %q", month)
		}
	}
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging", "LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/example/video-cloud@sha256:" + strings.Repeat("a", 64)}
	body := lkeLoggerPeriodSealJobManifest(env, "2026-11", "logger-period-seal-202611-review")
	var job map[string]any
	if err := yaml.Unmarshal([]byte(body), &job); err != nil {
		t.Fatal(err)
	}
	if job["kind"] != "Job" || job["metadata"].(map[string]any)["namespace"] != "video-cloud-staging-video-cloud" {
		t.Fatal("source close installed a schedule or used another environment")
	}
	for _, required := range []string{"/app/loggerperiodseal", "--all-brand-clouds", "2026-11", "VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN", "billing.video-cloud-staging-billing", "video_cloud?sslmode=disable"} {
		if !strings.Contains(body, required) {
			t.Fatalf("source close Job lacks %s", required)
		}
	}
	for _, forbidden := range []string{"CronJob", "BILLING_SERVICE_TOKEN", "OTA_PRODUCER_SEAL_TOKEN", "AWS_ACCESS_KEY_ID", "persistentVolumeClaim"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Logger source close requires unrelated capability %s", forbidden)
		}
	}
}

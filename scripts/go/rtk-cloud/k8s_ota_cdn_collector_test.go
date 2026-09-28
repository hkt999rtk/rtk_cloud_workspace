package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEOTACDNCollectorOptInAndScopedManifest(t *testing.T) {
	t.Setenv("LKE_OTA_CDN_COLLECTOR_ENABLED", "false")
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging",
		"LKE_VIDEO_CLOUD_IMAGE":         "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_OTA_CDN_STREAM_ID": "123", "VIDEO_CLOUD_OTA_CDN_HOST": "ota.example.test",
		"VIDEO_CLOUD_OTA_CDN_PATH_ROOT": "/firmware", "VIDEO_CLOUD_OTA_CDN_LOG_BUCKET": "ota-edge-logs",
		"VIDEO_CLOUD_OTA_CDN_LOG_PREFIX": "datastream/ota-ak-", "VIDEO_CLOUD_OTA_CDN_LOG_REGION": "us-east-1",
		"VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT": "https://objects.example.test",
	}
	if lkeOTACDNCollectorEnabled(env) || lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}) != nil {
		t.Fatal("collector must remain disabled by default")
	}
	job := lkeOTACDNCollectorCronJobManifest(env)
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(job), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: CronJob", "name: ota-cdn-collector", `schedule: "*/5 * * * *"`,
		"timeZone: Etc/UTC", "concurrencyPolicy: Forbid", "automountServiceAccountToken: false",
		`command: ["/app/otacdncollect"]`, "name: ota-cdn-datastream-reader",
		"name: VIDEO_CLOUD_OTA_CDN_LOG_PREFIX", "name: VIDEO_CLOUD_DB_DSN",
	} {
		if !strings.Contains(job, want) {
			t.Fatalf("collector CronJob lacks %q", want)
		}
	}
	if strings.Contains(job, "video-cloud-runtime, key: AWS_ACCESS_KEY_ID") {
		t.Fatal("collector reused firmware-origin credentials")
	}
}

func TestLKEOTACDNCollectorRejectsMissingScopeBeforeDeployment(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_OTA_CDN_COLLECTOR_ENABLED", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed"}
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "STREAM_ID") {
		t.Fatalf("collector accepted missing stream scope: %v", err)
	}
	for key, value := range map[string]string{
		"VIDEO_CLOUD_OTA_CDN_STREAM_ID": "123", "VIDEO_CLOUD_OTA_CDN_HOST": "ota.example.test",
		"VIDEO_CLOUD_OTA_CDN_PATH_ROOT": "/", "VIDEO_CLOUD_OTA_CDN_LOG_BUCKET": "ota-edge-logs",
		"VIDEO_CLOUD_OTA_CDN_LOG_PREFIX": "datastream/ota-ak-", "VIDEO_CLOUD_OTA_CDN_LOG_REGION": "us-east-1",
		"VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT": "http://objects.example.test",
	} {
		env[key] = value
	}
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("collector accepted insecure S3 endpoint: %v", err)
	}
	env["VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT"] = "https://objects.example.test"
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "video-cloud-runtime") {
		t.Fatalf("collector accepted missing database Secret: %v", err)
	}
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"POSTGRES_PASSWORD": "local-test"}))
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "ota-cdn-datastream-reader") {
		t.Fatalf("collector accepted missing dedicated S3 reader: %v", err)
	}
	t.Setenv("FAKE_OTA_DATASTREAM_READER_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"AWS_ACCESS_KEY_ID": "local-test"}))
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("collector accepted incomplete S3 reader: %v", err)
	}
	t.Setenv("FAKE_OTA_DATASTREAM_READER_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"AWS_ACCESS_KEY_ID": "local-test", "AWS_SECRET_ACCESS_KEY": "local-test"}))
	if err := lkeRequireOTACDNCollectorDeployment(env, provisionOptions{}); err != nil {
		t.Fatalf("qualified collector configuration was rejected: %v", err)
	}
}

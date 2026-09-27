package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCoreOTADeploymentReceivesConfiguredCDN(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":               "video-cloud-dev",
		"CLOUD_ENV_NAME":                 "dev",
		"VIDEO_CLOUD_OTA_CDN_BASE_URL":   "https://firmware.example.test",
		"VIDEO_CLOUD_OTA_CDN_TOKEN_NAME": "__token__",
	}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: "example.test/video-cloud:reviewed"}
	manifest := lkeDeploymentManifest(env, workload, nil)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("invalid core deployment manifest: %v", err)
	}
	for _, want := range []string{
		"name: VIDEO_CLOUD_OTA_CDN_BASE_URL\n              value: \"https://firmware.example.test\"",
		"name: VIDEO_CLOUD_OTA_CDN_TOKEN_NAME\n              value: \"__token__\"",
		"name: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX\n              valueFrom:",
		"name: ota-cdn-runtime\n                  key: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("core deployment lacks %q", want)
		}
	}
	delete(env, "VIDEO_CLOUD_OTA_CDN_BASE_URL")
	manifest = lkeDeploymentManifest(env, workload, nil)
	if strings.Contains(manifest, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX") || strings.Contains(manifest, "ota-cdn-runtime") {
		t.Fatal("core deployment without a CDN URL must not reference a CDN key")
	}
}

func TestCoreOTACDNConfigurationRejectsPartialSettings(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	if err := lkeRequireOTACDNConfiguration(env); err != nil {
		t.Fatalf("empty CDN configuration should select Object Storage: %v", err)
	}
	env["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = " \t"
	if err := lkeRequireOTACDNConfiguration(env); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("whitespace-only CDN URL was treated as absent: %v", err)
	}
	env["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = "https://firmware.example.test"
	if err := lkeRequireOTACDNConfiguration(env); err == nil || !strings.Contains(err.Error(), "CDN runtime Secret") {
		t.Fatalf("CDN URL without a key was accepted: %v", err)
	}
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": strings.Repeat("ab", 32)}))
	if err := lkeRequireOTACDNConfiguration(env); err != nil {
		t.Fatalf("complete CDN configuration was rejected: %v", err)
	}
	delete(env, "VIDEO_CLOUD_OTA_CDN_BASE_URL")
	if err := lkeRequireOTACDNConfiguration(env); err == nil || !strings.Contains(err.Error(), "without a CDN base URL") {
		t.Fatalf("CDN key without a URL was accepted: %v", err)
	}
	for _, key := range []map[string]string{
		{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": " \t"},
		{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": ""},
		{},
	} {
		t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, key))
		if err := lkeRequireOTACDNConfiguration(env); err == nil || !strings.Contains(err.Error(), "without a CDN base URL") {
			t.Fatalf("incomplete CDN Secret without URL was accepted: %v", err)
		}
	}
	env["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = "http://firmware.example.test"
	if err := lkeRequireOTACDNConfiguration(env); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("invalid CDN URL was accepted: %v", err)
	}
}

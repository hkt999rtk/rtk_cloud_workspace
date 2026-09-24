package main

import (
	"strings"
	"testing"
)

func TestLKELoggerStagedSubscriptionAndRetentionStorage(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_LOGGER_SERVICE_REGISTRATION_ENABLED": "true"}
	standby := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-logingester", Binary: "logingester", Port: 19300, PortName: "http"})
	if !strings.Contains(standby, "name: VIDEO_CLOUD_LOG_INGESTER_MQTT_SUBSCRIBE_ENABLED\n              value: \"false\"") {
		t.Fatal("registered Logger must stay off the MQTT subscription before cutover")
	}
	if !strings.Contains(standby, "name: VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED\n              value: \"false\"") {
		t.Fatal("customer Billing facts must remain disabled during validation")
	}
	if strings.Contains(lkeLokiDeploymentManifest(env), "claimName: video-cloud-loki-data") || strings.Contains(lkeLokiConfigManifest(env), "retention_stream:") {
		t.Fatal("Loki storage migration must remain opt-in")
	}
	env["LKE_LOGGER_MQTT_CORE_CUTOVER_ENABLED"] = "true"
	env["LKE_LOGGER_RETENTION_STORAGE_ENABLED"] = "true"
	env["LKE_LOGGER_BILLING_FACTS_ENABLED"] = "true"
	active := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-logingester", Binary: "logingester", Port: 19300, PortName: "http"})
	if !strings.Contains(active, "name: VIDEO_CLOUD_LOG_INGESTER_MQTT_SUBSCRIBE_ENABLED\n              value: \"true\"") || !strings.Contains(lkeLokiDeploymentManifest(env), "claimName: video-cloud-loki-data") || !strings.Contains(lkeLokiConfigManifest(env), "retention_stream:") {
		t.Fatal("cutover must enable the sole Logger subscriber and tiered persistent storage")
	}
	if !strings.Contains(active, "name: VIDEO_CLOUD_MQTT_CLEAN_SESSION\n              value: \"false\"") {
		t.Fatal("registered Logger requires a persistent broker session")
	}
	if !strings.Contains(active, "name: VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED\n              value: \"true\"") {
		t.Fatal("Billing facts must honor the separate activation flag")
	}
}

func TestLKELoggerCanReachRegistryAndBilling(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if !strings.Contains(lkeAllowServiceRegistrationNetworkPolicyManifest(env), "- video-cloud-logingester") {
		t.Fatal("platform registry ingress excludes registered Logger")
	}
	if !strings.Contains(lkeAllowAccountManagerHandoffBillingNetworkPolicyManifest(env), "video-cloud-logingester") {
		t.Fatal("Billing usage fact ingress excludes Logger")
	}
}

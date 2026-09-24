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

func TestLKELoggerFlagsAndPrivateGatewayAreOptIn(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	for _, flag := range []string{
		"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "LKE_LOGGER_HTTP_CORE_CUTOVER_ENABLED",
		"LKE_LOGGER_MQTT_CORE_CUTOVER_ENABLED", "LKE_LOGGER_RETENTION_STORAGE_ENABLED",
		"LKE_LOGGER_BILLING_FACTS_ENABLED",
	} {
		t.Setenv(flag, "false")
	}
	if lkeLoggerServiceRegistrationEnabled(env) || lkeLoggerHTTPCoreCutoverEnabled(env) ||
		lkeLoggerMQTTCoreCutoverEnabled(env) || lkeLoggerRetentionStorageEnabled(env) || lkeLoggerBillingFactsEnabled(env) {
		t.Fatal("Logger controls must default off")
	}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}
	if strings.Contains(lkeDeploymentManifest(env, workload, nil), "VIDEO_CLOUD_LOGGER_HTTP_UPSTREAM_URL") {
		t.Fatal("core must not forward to Logger before cutover")
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "allow-video-cloud-api-logger") {
			t.Fatal("Logger gateway policy appeared before cutover")
		}
	}
	for _, flag := range []string{
		"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "LKE_LOGGER_HTTP_CORE_CUTOVER_ENABLED",
		"LKE_LOGGER_MQTT_CORE_CUTOVER_ENABLED", "LKE_LOGGER_RETENTION_STORAGE_ENABLED",
		"LKE_LOGGER_BILLING_FACTS_ENABLED",
	} {
		t.Setenv(flag, "true")
	}
	if !lkeLoggerServiceRegistrationEnabled(env) || !lkeLoggerHTTPCoreCutoverEnabled(env) ||
		!lkeLoggerMQTTCoreCutoverEnabled(env) || !lkeLoggerRetentionStorageEnabled(env) || !lkeLoggerBillingFactsEnabled(env) {
		t.Fatal("Logger controls did not honor opt-in")
	}
	if !strings.Contains(lkeDeploymentManifest(env, workload, nil), "http://video-cloud-logingester.video-cloud-staging-video-cloud.svc.cluster.local:19300") {
		t.Fatal("core deployment lacks private Logger gateway")
	}
	found := false
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		found = found || strings.Contains(manifest, "allow-video-cloud-api-logger")
	}
	if !found || !strings.Contains(lkeAllowVideoCloudAPILoggerNetworkPolicyManifest(env), "port: 19300") {
		t.Fatal("private Logger network policy was omitted")
	}
	if !strings.Contains(lkeLokiPVCManifest(env), "name: video-cloud-loki-data") {
		t.Fatal("Loki retention PVC is absent")
	}
}

func TestLKELoggerCutoverRequiresReadyPrivateEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil {
		t.Fatal("missing Logger Service was accepted")
	}
	t.Setenv("FAKE_LOGGER_SERVICE_JSON", `{"spec":{"type":"LoadBalancer","selector":{"app.kubernetes.io/name":"video-cloud-logingester"},"ports":[{"port":19300}]}}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("public Logger Service was accepted: %v", err)
	}
	t.Setenv("FAKE_LOGGER_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"wrong"},"ports":[{"port":19300}]}}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil || !strings.Contains(err.Error(), "wrong Pods") {
		t.Fatalf("wrong Logger selector was accepted: %v", err)
	}
	t.Setenv("FAKE_LOGGER_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-logingester"},"ports":[{"port":8080}]}}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil || !strings.Contains(err.Error(), "port 19300") {
		t.Fatalf("wrong Logger port was accepted: %v", err)
	}
	t.Setenv("FAKE_LOGGER_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-logingester"},"ports":[{"port":19300}]}}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("Logger without ready endpoints was accepted: %v", err)
	}
	t.Setenv("FAKE_LOGGER_ENDPOINTSLICES_JSON", `{"items":[{"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":false}}]}]}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err == nil {
		t.Fatal("unready Logger Pod was accepted")
	}
	t.Setenv("FAKE_LOGGER_ENDPOINTSLICES_JSON", `{"items":[{"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":true}}]}]}`)
	if err := lkeRequireReadyLoggerEndpoint(env); err != nil {
		t.Fatal(err)
	}
}

func TestLKELoggerIdentityAndCutoverPrerequisites(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireLoggerServiceIdentitySecret(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LKE_LOGGER_HTTP_CORE_CUTOVER_ENABLED", "true")
	err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires the registered Logger") {
		t.Fatalf("unregistered Logger cutover was accepted: %v", err)
	}
	t.Setenv("LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "true")
	err = lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires the MQTT foundation") {
		t.Fatalf("Logger registration without MQTT was accepted: %v", err)
	}
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("FAKE_ACCOUNT_MANAGER_REGISTRATION_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"account-manager"},"ports":[{"name":"service-registry","port":8443,"targetPort":"service-registry"}]}}`)
	err = lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "requires verified Loki retention storage") {
		t.Fatalf("Logger cutover without retention was accepted: %v", err)
	}
	t.Setenv("LKE_LOGGER_RETENTION_STORAGE_ENABLED", "true")
	err = lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "Logger Service is not private") {
		t.Fatalf("Logger cutover without private ready endpoint was accepted: %v", err)
	}
}

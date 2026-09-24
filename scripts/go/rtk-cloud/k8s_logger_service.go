package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const loggerServiceIdentitySecretName = "logger-service-platform-identity"

func lkeLoggerServiceRegistrationEnabled(env map[string]string) bool {
	return lkeLoggerFlag(env, "LKE_LOGGER_SERVICE_REGISTRATION_ENABLED")
}
func lkeLoggerHTTPCoreCutoverEnabled(env map[string]string) bool {
	return lkeLoggerFlag(env, "LKE_LOGGER_HTTP_CORE_CUTOVER_ENABLED")
}
func lkeLoggerMQTTCoreCutoverEnabled(env map[string]string) bool {
	return lkeLoggerFlag(env, "LKE_LOGGER_MQTT_CORE_CUTOVER_ENABLED")
}
func lkeLoggerRetentionStorageEnabled(env map[string]string) bool {
	return lkeLoggerFlag(env, "LKE_LOGGER_RETENTION_STORAGE_ENABLED")
}
func lkeLoggerBillingFactsEnabled(env map[string]string) bool {
	return lkeLoggerFlag(env, "LKE_LOGGER_BILLING_FACTS_ENABLED")
}
func lkeLoggerFlag(env map[string]string, key string) bool {
	raw := firstNonEmpty(os.Getenv(key), env[key], "false")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
func lkeRequireLoggerServiceIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, loggerServiceIdentitySecretName, "Logger platform identity Secret", "service:logger")
}

func lkeAllowVideoCloudAPILoggerNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-api-logger
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-logingester
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
      ports:
        - protocol: TCP
          port: 19300
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeRequireReadyLoggerEndpoint(env map[string]string) error {
	const name = "video-cloud-logingester"
	ns := lkeNamespaceName(env, "video-cloud")
	service, err := kubectlResourceJSON(ns, "service", name)
	if err != nil {
		return fmt.Errorf("Logger Service unavailable: %w", err)
	}
	spec, ok := service["spec"].(map[string]any)
	if !ok || spec["type"] != "ClusterIP" {
		return fmt.Errorf("Logger Service is not private")
	}
	selector, ok := spec["selector"].(map[string]any)
	if !ok || selector["app.kubernetes.io/name"] != name {
		return fmt.Errorf("Logger Service targets the wrong Pods")
	}
	ports, ok := spec["ports"].([]any)
	validPort := false
	if ok {
		for _, raw := range ports {
			if port, ok := raw.(map[string]any); ok && port["port"] == float64(19300) {
				validPort = true
			}
		}
	}
	if !validPort {
		return fmt.Errorf("Logger Service lacks port 19300")
	}
	body, err := kubectlCombinedOutput(nil, "-n", ns, "get", "endpointslices", "-l", "kubernetes.io/service-name="+name, "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect Logger endpoints: %w", err)
	}
	var list struct {
		Items []struct {
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return err
	}
	for _, item := range list.Items {
		for _, endpoint := range item.Endpoints {
			if endpoint.Conditions.Ready && len(endpoint.Addresses) > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("Logger has no ready registered endpoint")
}

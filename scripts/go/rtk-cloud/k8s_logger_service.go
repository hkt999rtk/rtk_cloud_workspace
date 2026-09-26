package main

import (
	"encoding/hex"
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

// A configured retention flag is not proof that the existing Loki Pod has
// switched from emptyDir or that the Compactor is enforcing tiered retention.
func lkeRequireReadyLokiRetentionStorage(env map[string]string) error {
	ns := lkeNamespaceName(env, "observability")
	deployment, err := kubectlResourceJSON(ns, "deployment", "video-cloud-loki")
	if err != nil {
		return fmt.Errorf("Loki retention Deployment is unavailable: %w", err)
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	volumes, _ := podSpec["volumes"].([]any)
	persistent := false
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] != "data" {
			continue
		}
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		persistent = claim["claimName"] == "video-cloud-loki-data"
	}
	if !persistent {
		return fmt.Errorf("Loki retention Deployment still uses nonpersistent storage")
	}
	status, _ := deployment["status"].(map[string]any)
	if ready, _ := status["readyReplicas"].(float64); ready < 1 {
		return fmt.Errorf("Loki retention Deployment has no ready replica")
	}
	pvc, err := kubectlResourceJSON(ns, "pvc", "video-cloud-loki-data")
	if err != nil {
		return fmt.Errorf("Loki retention PVC is unavailable: %w", err)
	}
	pvcStatus, _ := pvc["status"].(map[string]any)
	if pvcStatus["phase"] != "Bound" {
		return fmt.Errorf("Loki retention PVC is not bound")
	}
	config, err := kubectlResourceJSON(ns, "configmap", "video-cloud-loki-config")
	if err != nil {
		return fmt.Errorf("Loki retention ConfigMap is unavailable: %w", err)
	}
	data, _ := config["data"].(map[string]any)
	body, _ := data["config.yaml"].(string)
	for _, required := range []string{"retention_enabled: true", "retention_period: 0s", `selector: '{retention_policy="product-grant-v1",retention_tier="7d"}'`, `selector: '{retention_policy="product-grant-v1",retention_tier="30d"}'`, `selector: '{retention_policy="product-grant-v1",retention_tier="90d"}'`} {
		if !strings.Contains(body, required) {
			return fmt.Errorf("Loki retention ConfigMap lacks %q", required)
		}
	}
	return nil
}

// The current dev Loki stores its index and chunks in an emptyDir. Applying a
// PVC-backed Deployment before copying that Pod's data would discard it.
func lkeRequireLokiDataMigration(env map[string]string) error {
	ns := lkeNamespaceName(env, "observability")
	deployment, err := kubectlResourceJSON(ns, "deployment", "video-cloud-loki")
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("inspect existing Loki Deployment before persistent-storage switch: %w", err)
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	volumes, _ := podSpec["volumes"].([]any)
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] != "data" {
			continue
		}
		if claim, ok := volume["persistentVolumeClaim"].(map[string]any); ok {
			if claim["claimName"] == "video-cloud-loki-data" {
				return nil
			}
			return fmt.Errorf("existing Loki Deployment uses a different data PVC")
		}
		if _, ok := volume["emptyDir"]; !ok {
			return fmt.Errorf("existing Loki data volume has an unknown source")
		}
		podsRaw, err := kubectlCombinedOutput(nil, "-n", ns, "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-loki", "-o", "json")
		if err != nil {
			return fmt.Errorf("inspect Loki source Pod: %w", err)
		}
		var pods struct {
			Items []struct {
				Metadata struct {
					UID string `json:"uid"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if err := json.Unmarshal(podsRaw, &pods); err != nil || len(pods.Items) != 1 || pods.Items[0].Metadata.UID == "" {
			return fmt.Errorf("expected exactly one identifiable Loki source Pod")
		}
		pvc, err := kubectlResourceJSON(ns, "pvc", "video-cloud-loki-data")
		if err != nil {
			return fmt.Errorf("create the Loki migration PVC before switching storage: %w", err)
		}
		metadata, _ := pvc["metadata"].(map[string]any)
		annotations, _ := metadata["annotations"].(map[string]any)
		copyHash, _ := annotations["rtk.realtek.com/loki-copy-sha256"].(string)
		decodedHash, decodeErr := hex.DecodeString(copyHash)
		if annotations["rtk.realtek.com/loki-source-pod-uid"] != pods.Items[0].Metadata.UID || decodeErr != nil || len(decodedHash) != 32 {
			return fmt.Errorf("Loki PVC lacks verified data-copy annotations for the current source Pod")
		}
		status, _ := pvc["status"].(map[string]any)
		if status["phase"] != "Bound" {
			return fmt.Errorf("Loki migration PVC is not bound")
		}
		return nil
	}
	return fmt.Errorf("existing Loki Deployment has no data volume")
}

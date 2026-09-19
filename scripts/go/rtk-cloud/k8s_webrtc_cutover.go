package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

var lkeWebRTCServicePaths = []string{
	"/api/request_webrtc",
	"/api/request_webrtc/ice",
	"/api/request_webrtc/answer",
	"/api/request_webrtc/close",
}

func lkeWebRTCServiceEdgeEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_WEBRTC_SERVICE_EDGE_ENABLED")
}

func lkeWebRTCCoreCutoverEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_WEBRTC_CORE_CUTOVER_ENABLED")
}

func lkeFeatureEnabled(env map[string]string, key string) bool {
	raw := firstNonEmpty(os.Getenv(key), env[key], "false")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Readiness is registered-service readiness: the Pod's /readyz depends on
// its Platform lease, PostgreSQL, and Redis, and Kubernetes only publishes
// ready Pod endpoints. Check the private Service and its ready EndpointSlice
// before adding public ingress paths or disabling the core handlers.
func lkeRequireReadyWebRTCServiceEndpoint(env map[string]string) error {
	service, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "service", "video-cloud-webrtcservice")
	if err != nil {
		return fmt.Errorf("WebRTC service is not deployed: %w", err)
	}
	spec, ok := service["spec"].(map[string]any)
	if !ok || spec["type"] != "ClusterIP" {
		return fmt.Errorf("WebRTC Service is not private")
	}
	if externalIPs, ok := spec["externalIPs"].([]any); ok && len(externalIPs) > 0 {
		return fmt.Errorf("WebRTC Service has external addresses")
	}
	selector, ok := spec["selector"].(map[string]any)
	if !ok || selector["app.kubernetes.io/name"] != "video-cloud-webrtcservice" {
		return fmt.Errorf("WebRTC Service targets the wrong Pods")
	}
	ports, ok := spec["ports"].([]any)
	validPort := false
	if ok {
		for _, raw := range ports {
			port, ok := raw.(map[string]any)
			if ok && port["port"] == float64(18082) && port["targetPort"] == "http" {
				validPort = true
			}
		}
	}
	if !validPort {
		return fmt.Errorf("WebRTC Service lacks port 18082")
	}
	body, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "endpointslices", "-l", "kubernetes.io/service-name=video-cloud-webrtcservice", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect WebRTC ready endpoints: %w", err)
	}
	var list struct {
		Items []struct {
			Ports []struct {
				Port int `json:"port"`
			} `json:"ports"`
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decode WebRTC ready endpoints: %w", err)
	}
	for _, slice := range list.Items {
		validPort := false
		for _, port := range slice.Ports {
			if port.Port == 18082 {
				validPort = true
			}
		}
		if !validPort {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready && len(endpoint.Addresses) > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("WebRTC service has no ready registered endpoint")
}

func lkeRequireActiveWebRTCEdgeRoutes(env map[string]string) error {
	videoHost := env["VIDEO_CLOUD_DOMAIN"]
	deviceHost := firstNonEmpty(os.Getenv("LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+videoHost)
	for _, target := range []struct {
		name string
		host string
	}{
		{name: "video-cloud-staging-public", host: videoHost},
		{name: "video-cloud-staging-device-mtls", host: deviceHost},
	} {
		ingress, err := kubectlResourceJSON(lkeIngressNamespace(env), "ingress", target.name)
		if err != nil {
			return fmt.Errorf("WebRTC core cutover requires existing %s ingress: %w", target.name, err)
		}
		if target.host == deviceHost && !lkeIngressRequiresDeviceMTLS(ingress) {
			return fmt.Errorf("WebRTC core cutover requires device mTLS on %s ingress", target.name)
		}
		if !lkeIngressOwnsWebRTCPaths(ingress, target.host, lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: lkeNamespaceName(env, "video-cloud"), Service: "video-cloud-webrtcservice"})) {
			return fmt.Errorf("WebRTC core cutover requires %s ingress to own every exact WebRTC path", target.name)
		}
	}
	return nil
}

func lkeIngressRequiresDeviceMTLS(ingress map[string]any) bool {
	metadata, ok := ingress["metadata"].(map[string]any)
	if !ok {
		return false
	}
	annotations, ok := metadata["annotations"].(map[string]any)
	return ok && annotations["nginx.ingress.kubernetes.io/auth-tls-verify-client"] == "on"
}

func lkeIngressOwnsWebRTCPaths(ingress map[string]any, host, service string) bool {
	spec, ok := ingress["spec"].(map[string]any)
	if !ok {
		return false
	}
	rules, ok := spec["rules"].([]any)
	if !ok {
		return false
	}
	wanted := make(map[string]bool, len(lkeWebRTCServicePaths))
	for _, path := range lkeWebRTCServicePaths {
		wanted[path] = false
	}
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok || rule["host"] != host {
			continue
		}
		http, ok := rule["http"].(map[string]any)
		if !ok {
			continue
		}
		paths, ok := http["paths"].([]any)
		if !ok {
			continue
		}
		for _, rawPath := range paths {
			path, ok := rawPath.(map[string]any)
			if !ok || path["pathType"] != "Exact" {
				continue
			}
			name, selected := path["path"].(string)
			if !selected {
				continue
			}
			backend, ok := path["backend"].(map[string]any)
			if !ok {
				continue
			}
			backendService, ok := backend["service"].(map[string]any)
			if !ok || backendService["name"] != service {
				continue
			}
			port, ok := backendService["port"].(map[string]any)
			if ok && port["number"] == float64(18082) {
				if _, found := wanted[name]; found {
					wanted[name] = true
				}
			}
		}
	}
	for _, found := range wanted {
		if !found {
			return false
		}
	}
	return true
}

// On rollback restore the core handlers before removing edge paths. The
// desired flag alone is insufficient: inspect the Deployment actually serving.
func lkePreventWebRTCEdgeRollbackOverlap(env map[string]string) error {
	if lkeWebRTCCoreCutoverEnabled(env) {
		return fmt.Errorf("WebRTC edge routes cannot be removed while core cutover remains enabled")
	}
	body, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "deployment", "video-cloud-api", "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect core WebRTC cutover before edge rollback: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	var deployment struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &deployment); err != nil {
		return fmt.Errorf("decode core WebRTC cutover state: %w", err)
	}
	if deployment.Metadata.Name != "video-cloud-api" {
		return fmt.Errorf("inspect core WebRTC cutover: unexpected Deployment")
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "app" {
			continue
		}
		for _, variable := range container.Env {
			if variable.Name == "VIDEO_CLOUD_WEBRTC_SERVICE_CUTOVER_ENABLED" && strings.EqualFold(variable.Value, "true") {
				return fmt.Errorf("core API still has WebRTC handlers disabled; restore core handlers before removing edge routes")
			}
		}
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/video-cloud-api", "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return fmt.Errorf("core API has not completed WebRTC handler restoration: %w", err)
	}
	return nil
}

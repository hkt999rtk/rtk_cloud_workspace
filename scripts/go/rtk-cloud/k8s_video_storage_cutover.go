package main

import (
	"encoding/json"
	"fmt"
)

func lkeVideoStorageCoreCutoverEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED")
}

// A ready EndpointSlice is the observable result of the storage process
// acquiring its Platform registration lease and passing its dependency probes.
func lkeRequireReadyVideoStorageServiceEndpoint(env map[string]string) error {
	const name = "video-cloud-videostorage"
	namespace := lkeNamespaceName(env, "video-cloud")
	service, err := kubectlResourceJSON(namespace, "service", name)
	if err != nil {
		return fmt.Errorf("video storage service is not deployed: %w", err)
	}
	spec, ok := service["spec"].(map[string]any)
	if !ok || spec["type"] != "ClusterIP" {
		return fmt.Errorf("video storage Service is not private")
	}
	if externalIPs, ok := spec["externalIPs"].([]any); ok && len(externalIPs) > 0 {
		return fmt.Errorf("video storage Service has external addresses")
	}
	selector, ok := spec["selector"].(map[string]any)
	if !ok || selector["app.kubernetes.io/name"] != name {
		return fmt.Errorf("video storage Service targets the wrong Pods")
	}
	ports, ok := spec["ports"].([]any)
	validPort := false
	if ok {
		for _, raw := range ports {
			port, ok := raw.(map[string]any)
			if ok && port["port"] == float64(18083) && port["targetPort"] == "http" {
				validPort = true
			}
		}
	}
	if !validPort {
		return fmt.Errorf("video storage Service lacks port 18083")
	}
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "endpointslices", "-l", "kubernetes.io/service-name="+name, "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect video storage ready endpoints: %w", err)
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
		return fmt.Errorf("decode video storage ready endpoints: %w", err)
	}
	for _, slice := range list.Items {
		validPort := false
		for _, port := range slice.Ports {
			if port.Port == 18083 {
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
	return fmt.Errorf("video storage service has no ready registered endpoint")
}

func lkeAllowVideoCloudAPIStorageGatewayNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-api-videostorage
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-videostorage
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
      ports:
        - protocol: TCP
          port: 18083
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

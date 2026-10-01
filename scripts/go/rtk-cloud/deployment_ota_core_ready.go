package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

func otaCoreCutoverPods(namespace string) ([]map[string]any, error) {
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-api", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("inspect live core API Pods: %w", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("core API Pod list is invalid")
	}
	return list.Items, nil
}

// A matching desired template is not evidence that the current API processes
// use it. Qualification only reads the observed revision and every live Pod.
func otaCoreCutoverCurrentReady(deployment map[string]any, pods []map[string]any, image, upstream string) error {
	metadata, _ := deployment["metadata"].(map[string]any)
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	generation, _ := metadata["generation"].(float64)
	observed, _ := status["observedGeneration"].(float64)
	desired, _ := spec["replicas"].(float64)
	if generation < 1 || observed < generation || desired < 1 {
		return errors.New("core API has not observed its current positive replica revision")
	}
	for _, key := range []string{"replicas", "updatedReplicas", "availableReplicas", "readyReplicas"} {
		if count, _ := status[key].(float64); count != desired {
			return fmt.Errorf("core API %s does not match its current desired replicas", key)
		}
	}
	for _, key := range []string{"unavailableReplicas", "terminatingReplicas"} {
		if count, _ := status[key].(float64); count != 0 {
			return fmt.Errorf("core API still has %s", key)
		}
	}
	template, _ := spec["template"].(map[string]any)
	if err := otaCoreCutoverPodSettings(template, image, upstream); err != nil {
		return fmt.Errorf("core API Deployment: %w", err)
	}
	live := 0
	for _, pod := range pods {
		status, _ := pod["status"].(map[string]any)
		phase, _ := status["phase"].(string)
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		live++
		metadata, _ := pod["metadata"].(map[string]any)
		if metadata["deletionTimestamp"] != nil {
			return errors.New("core API has a terminating live Pod; wait for the previous API to stop")
		}
		if err := otaCoreCutoverPodSettings(pod, image, upstream); err != nil {
			return fmt.Errorf("core API live Pod: %w", err)
		}
		ready := false
		conditions, _ := status["conditions"].([]any)
		for _, raw := range conditions {
			condition, _ := raw.(map[string]any)
			if condition["type"] == "Ready" && condition["status"] == "True" {
				ready = true
			}
		}
		if phase != "Running" || !ready {
			return errors.New("core API live Pod is not Running and Ready")
		}
	}
	if float64(live) != desired {
		return errors.New("core API live Pod count differs from its current desired replicas")
	}
	return nil
}

func otaCoreCutoverPodSettings(pod map[string]any, image, upstream string) error {
	spec, _ := pod["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	var app map[string]any
	for _, raw := range containers {
		container, _ := raw.(map[string]any)
		if container["name"] == "app" {
			if app != nil {
				return errors.New("duplicate app containers")
			}
			app = container
		}
	}
	if app == nil {
		return errors.New("app container is missing")
	}
	if app["image"] != image {
		return errors.New("app image differs from the selected immutable release")
	}
	variables, _ := app["env"].([]any)
	for key, wanted := range map[string]string{
		"VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED":   "true",
		"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED": "true",
		"VIDEO_CLOUD_OTA_UPSTREAM_URL":            upstream,
	} {
		matches, valid := 0, false
		for _, raw := range variables {
			variable, _ := raw.(map[string]any)
			if variable["name"] == key {
				matches++
				valid = variable["value"] == wanted && variable["valueFrom"] == nil
			}
		}
		if matches != 1 || !valid {
			return fmt.Errorf("app must declare %s=%s exactly once", key, wanted)
		}
	}
	return nil
}

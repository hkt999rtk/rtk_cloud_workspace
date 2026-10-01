package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// An old ready Logger can still forward mutable monthly facts. Source closing
// requires the selected immutable-period configuration on its only live Pod.
// This guard only reads Kubernetes; its caller binds canonical runtime inputs.
func lkeRequireReadyLoggerPeriodSource(env map[string]string) error {
	if !activeCanonicalSecretStore {
		return errors.New("Logger source readiness requires the selected canonical runtime inputs")
	}
	for _, key := range []string{"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "LKE_LOGGER_BILLING_FACTS_ENABLED", "LKE_LOGGER_PERIOD_SEALS_ENABLED"} {
		if env[key] != "true" {
			return fmt.Errorf("Logger source readiness requires selected %s=true", key)
		}
	}
	image := strings.TrimSpace(env["LKE_VIDEO_CLOUD_IMAGE"])
	if !rolloutImagePattern.MatchString(image) {
		return errors.New("Logger source readiness requires the selected immutable GHCR image")
	}
	checksum := lkeVideoCloudRuntimeChecksum(env)
	ns := lkeNamespaceName(env, "video-cloud")
	deployment, err := kubectlResourceJSON(ns, "deployment", "video-cloud-logingester")
	if err != nil {
		return fmt.Errorf("Logger source Deployment is unavailable: %w", err)
	}
	metadata, _ := deployment["metadata"].(map[string]any)
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	strategy, _ := spec["strategy"].(map[string]any)
	if strategy["type"] != "Recreate" {
		return errors.New("Logger source Deployment requires Recreate rollout")
	}
	generation, _ := metadata["generation"].(float64)
	observed, _ := status["observedGeneration"].(float64)
	desired, _ := spec["replicas"].(float64)
	if generation < 1 || observed < generation || desired != 1 {
		return errors.New("Logger source Deployment has not observed its singleton current revision")
	}
	for _, key := range []string{"replicas", "updatedReplicas", "availableReplicas", "readyReplicas"} {
		if count, _ := status[key].(float64); count != 1 {
			return fmt.Errorf("Logger source Deployment %s is not singleton at its current revision", key)
		}
	}
	for _, key := range []string{"unavailableReplicas", "terminatingReplicas"} {
		if count, _ := status[key].(float64); count != 0 {
			return fmt.Errorf("Logger source Deployment still has %s", key)
		}
	}
	template, _ := spec["template"].(map[string]any)
	if err := loggerSourceTemplateMatches(template, image, checksum); err != nil {
		return fmt.Errorf("Logger source Deployment: %w", err)
	}
	body, err := kubectlCombinedOutput(nil, "-n", ns, "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-logingester", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect live Logger source Pods: %w", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return errors.New("Logger source Pod list is invalid")
	}
	live := 0
	for _, pod := range list.Items {
		status, _ := pod["status"].(map[string]any)
		phase, _ := status["phase"].(string)
		if phase == "Succeeded" || phase == "Failed" {
			continue // A completed process cannot forward an in-flight legacy fact.
		}
		live++
		metadata, _ := pod["metadata"].(map[string]any)
		if metadata["deletionTimestamp"] != nil {
			return errors.New("Logger source has a terminating live Pod; wait for the legacy forwarder to stop")
		}
		if err := loggerSourceTemplateMatches(pod, image, checksum); err != nil {
			return fmt.Errorf("Logger source live Pod: %w", err)
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
			return errors.New("Logger source live Pod is not Running and Ready")
		}
	}
	if live != 1 {
		return errors.New("Logger source must have exactly one live Pod and no overlapping legacy forwarder")
	}
	if err := lkeRequireReadyLoggerEndpoint(env); err != nil {
		return err
	}
	if _, err := kubectlCombinedOutput(nil, "-n", ns, "exec", "deployment/video-cloud-logingester", "-c", "app", "--", "/app/schema-maintenance", "logger-verify"); err != nil {
		return errors.New("Logger immutable source schema verification failed; apply and verify the scoped Logger migration before monthly close")
	}
	return nil
}

func loggerSourceTemplateMatches(template map[string]any, image, checksum string) error {
	metadata, _ := template["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations["rtk.realtek.com/runtime-checksum"] != checksum {
		return errors.New("runtime checksum differs from the canonical selected configuration")
	}
	spec, _ := template["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	for _, raw := range containers {
		container, _ := raw.(map[string]any)
		if container["name"] != "app" {
			continue
		}
		if container["image"] != image {
			return errors.New("app image differs from the selected immutable release")
		}
		variables, _ := container["env"].([]any)
		for _, key := range []string{"VIDEO_CLOUD_LOGGER_SERVICE_ENABLED", "VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED", "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED"} {
			matches := 0
			valid := false
			for _, raw := range variables {
				variable, _ := raw.(map[string]any)
				if variable["name"] == key {
					matches++
					valid = variable["value"] == "true" && variable["valueFrom"] == nil
				}
			}
			if matches != 1 || !valid {
				return fmt.Errorf("app must declare %s=true exactly once", key)
			}
		}
		return nil
	}
	return errors.New("app container is missing")
}

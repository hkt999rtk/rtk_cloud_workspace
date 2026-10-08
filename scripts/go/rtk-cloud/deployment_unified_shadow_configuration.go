package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

var sharedShadowDeploymentKeys = []string{"SHADOW_WORKER_REGISTRATION_ENABLED", "SHADOW_HTTP_CORE_CUTOVER_ENABLED"}

func sharedShadowDeploymentKey(key string) bool {
	for _, candidate := range sharedShadowDeploymentKeys {
		if key == candidate {
			return true
		}
	}
	return false
}
func sharedShadowDeploymentBlocker(key, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.shadow.configuration", Resource: key, Cause: cause, Next: "maintain explicit shared Shadow worker and HTTP forwarding booleans in architecture.env; retain original service identity and repeat full preflight"}
}
func validateSharedShadowDeploymentConfiguration(values map[string]string) error {
	for _, key := range sharedShadowDeploymentKeys {
		if values[key] != "true" && values[key] != "false" {
			return sharedShadowDeploymentBlocker(key, "shared Shadow setting must explicitly be true or false")
		}
	}
	if values["SHADOW_HTTP_CORE_CUTOVER_ENABLED"] == "true" && values["SHADOW_WORKER_REGISTRATION_ENABLED"] != "true" {
		return sharedShadowDeploymentBlocker("SHADOW_HTTP_CORE_CUTOVER_ENABLED", "HTTP forwarding requires the independent Shadow worker")
	}
	if values["SHADOW_WORKER_REGISTRATION_ENABLED"] == "true" && (values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] != "true" || values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true") {
		return sharedShadowDeploymentBlocker("SHADOW_WORKER_REGISTRATION_ENABLED", "Shadow worker requires MQTT Foundation and the Account Manager registration listener")
	}
	return nil
}

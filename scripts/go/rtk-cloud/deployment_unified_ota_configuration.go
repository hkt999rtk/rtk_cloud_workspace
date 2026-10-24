package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

var sharedOTADeploymentKeys = []string{"OTA_SERVICE_REGISTRATION_ENABLED", "OTA_CORE_CUTOVER_ENABLED", "OTA_SERVICE_EDGE_ENABLED"}

func sharedOTADeploymentKey(key string) bool {
	if key == "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED" {
		return true
	}
	for _, candidate := range sharedOTADeploymentKeys {
		if key == candidate {
			return true
		}
	}
	return false
}
func sharedOTADeploymentBlocker(key, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.ota.configuration", Resource: key, Cause: cause, Next: "maintain explicit shared OTA service, core forwarding and edge routing booleans in architecture.env; remove adapter and environment copies, then repeat full preflight"}
}
func validateSharedOTADeploymentConfiguration(values map[string]string) error {
	for _, key := range sharedOTADeploymentKeys {
		if values[key] != "true" && values[key] != "false" {
			return sharedOTADeploymentBlocker(key, "shared OTA topology must explicitly be true or false")
		}
	}
	if (values["OTA_CORE_CUTOVER_ENABLED"] == "true" || values["OTA_SERVICE_EDGE_ENABLED"] == "true") && values["OTA_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return sharedOTADeploymentBlocker("OTA_SERVICE_REGISTRATION_ENABLED", "OTA forwarding and edge routing require the independent OTA service")
	}
	if values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" && values["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] != "true" {
		return sharedOTADeploymentBlocker("VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "independent OTA service requires strict Product/device OTA entitlements")
	}
	return nil
}

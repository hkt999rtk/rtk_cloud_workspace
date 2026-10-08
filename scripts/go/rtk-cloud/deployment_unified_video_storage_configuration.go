package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

func sharedVideoStorageDeploymentKey(key string) bool {
	return key == "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED" || key == "VIDEO_STORAGE_CORE_CUTOVER_ENABLED"
}
func videoStorageConfigurationBlocker(key, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.video-storage.configuration", Resource: key, Cause: cause, Next: "maintain explicit shared VideoStorage registration and gateway settings in architecture.env; retain original identity and repeat preflight"}
}
func validateSharedVideoStorageDeploymentConfiguration(values map[string]string) error {
	for _, key := range []string{"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_CORE_CUTOVER_ENABLED"} {
		if values[key] != "true" && values[key] != "false" {
			return videoStorageConfigurationBlocker(key, "shared VideoStorage setting must explicitly be true or false")
		}
	}
	if values["VIDEO_STORAGE_CORE_CUTOVER_ENABLED"] == "true" && values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return videoStorageConfigurationBlocker("VIDEO_STORAGE_CORE_CUTOVER_ENABLED", "core forwarding requires the independent VideoStorage service")
	}
	if values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" && (values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] != "true" || values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true") {
		return videoStorageConfigurationBlocker("VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "VideoStorage requires MQTT Foundation and the Account Manager registration listener")
	}
	return nil
}

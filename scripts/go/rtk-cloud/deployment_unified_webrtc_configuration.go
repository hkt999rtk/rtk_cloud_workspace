package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

func sharedWebRTCDeploymentKey(key string) bool {
	return key == "WEBRTC_SERVICE_REGISTRATION_ENABLED" || key == "WEBRTC_CORE_CUTOVER_ENABLED" || key == "WEBRTC_SERVICE_EDGE_ENABLED"
}
func webRTCConfigurationBlocker(key, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.webrtc.configuration", Resource: key, Cause: cause, Next: "maintain explicit shared WebRTC registration and gateway settings in architecture.env; retain original identity and repeat preflight"}
}
func validateSharedWebRTCDeploymentConfiguration(values map[string]string) error {
	for _, key := range []string{"WEBRTC_SERVICE_REGISTRATION_ENABLED", "WEBRTC_CORE_CUTOVER_ENABLED", "WEBRTC_SERVICE_EDGE_ENABLED"} {
		if values[key] != "true" && values[key] != "false" {
			return webRTCConfigurationBlocker(key, "shared WebRTC setting must explicitly be true or false")
		}
	}
	if (values["WEBRTC_CORE_CUTOVER_ENABLED"] == "true" || values["WEBRTC_SERVICE_EDGE_ENABLED"] == "true") && values["WEBRTC_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return webRTCConfigurationBlocker("WEBRTC_CORE_CUTOVER_ENABLED", "core forwarding requires the independent WebRTC service")
	}
	if values["WEBRTC_SERVICE_REGISTRATION_ENABLED"] == "true" && (values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] != "true" || values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true") {
		return webRTCConfigurationBlocker("WEBRTC_SERVICE_REGISTRATION_ENABLED", "WebRTC requires MQTT Foundation and the Account Manager registration listener")
	}
	return nil
}

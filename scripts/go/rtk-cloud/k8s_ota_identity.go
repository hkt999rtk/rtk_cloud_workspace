package main

const (
	otaServiceIdentitySecretName = "ota-service-platform-identity"
	otaServiceInstanceID         = "ota-service-0"
)

func lkeOTAEntitlementsRequired(env map[string]string) bool {
	return lkeFeatureEnabled(env, "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED")
}
func lkeRequireOTAServiceIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, otaServiceIdentitySecretName, "OTA platform identity Secret", "service:ota")
}

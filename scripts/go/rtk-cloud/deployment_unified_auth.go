package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

var environmentAuthRoles = map[string]map[string]string{
	"managed-api":     {"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "DEVICE_API_AUTH_APP_PKI_ENABLED", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "DEVICE_API_AUTH_PRODUCT_PKI_ENABLED", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "DEVICE_API_AUTH_MTLS_REQUIRED"},
	"managed-app-api": {"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "APP_API_AUTH_APP_PKI_ENABLED", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "APP_API_AUTH_PRODUCT_PKI_ENABLED", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "APP_API_AUTH_MTLS_REQUIRED"},
	"certissuer":      {"CERT_ISSUER_PRODUCT_PKI_ENABLED": "CERT_ISSUER_PRODUCT_PKI_ENABLED", "CERT_ISSUER_APP_PKI_ENABLED": "CERT_ISSUER_APP_PKI_ENABLED"},
	"factoryenroll":   {"FACTORY_ENROLL_PRODUCT_PKI_ENABLED": "FACTORY_ENROLL_PRODUCT_PKI_ENABLED"},
}

func environmentAuthArchitectureKey(key string) bool {
	for _, role := range environmentAuthRoles {
		for _, value := range role {
			if value == key {
				return true
			}
		}
	}
	return false
}
func environmentAuthRuntimeKey(key string) bool {
	for _, role := range environmentAuthRoles {
		if _, ok := role[key]; ok {
			return true
		}
	}
	return false
}
func environmentAuthBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.auth-role", Resource: id, Cause: cause, Next: "maintain explicit shared Device, App and issuer role booleans in architecture.env; role changes require a separately reviewed authentication migration"}
}
func validateEnvironmentAuthConfiguration(env map[string]string) error {
	for _, role := range []string{"managed-api", "managed-app-api", "certissuer", "factoryenroll"} {
		for _, key := range sortedMapKeys(environmentAuthRoles[role]) {
			name := environmentAuthRoles[role][key]
			if env[name] != "true" && env[name] != "false" {
				return environmentAuthBlocker("", name+" must explicitly be true or false")
			}
		}
	}
	return nil
}
func environmentObjectAuthRole(object deploymentplan.Object) string {
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	if name == "factoryenroll" {
		return name
	}
	return environmentManagedComponents[name]
}
func applyEnvironmentAuthRole(env map[string]string, object deploymentplan.Object) error {
	role := environmentAuthRoles[environmentObjectAuthRole(object)]
	if len(role) == 0 {
		return nil
	}
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) == 0 {
		return environmentAuthBlocker(deploymentplan.ID(object), "authentication role has no primary container")
	}
	var settings []any
	for _, key := range sortedMapKeys(role) {
		value := env[role[key]]
		if value != "true" && value != "false" {
			return environmentAuthBlocker(deploymentplan.ID(object), role[key]+" must explicitly be true or false")
		}
		settings = append(settings, map[string]any{"name": key, "value": value})
	}
	container := deploymentplan.Map(containers[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], settings)
	return nil
}
func validateEnvironmentAuthTransition(component string, old, wanted map[string]any, id string) error {
	// factoryenroll shares the certissuer component catalog, but has its own role.
	if deploymentplan.String(deploymentplan.Map(wanted)["name"]) == "" {
		return environmentAuthBlocker(id, "authentication consumer is absent")
	}
	if strings.HasSuffix(id, "/factoryenroll") {
		component = "factoryenroll"
	}
	for _, key := range sortedMapKeys(environmentAuthRoles[component]) {
		read := func(container map[string]any) (string, bool) {
			result := "false"
			seen := false
			for _, value := range certIssuerObjectList(container["env"]) {
				setting := deploymentplan.Map(value)
				if setting["name"] == key {
					if seen || setting["valueFrom"] != nil {
						return "", false
					}
					seen = true
					result = deploymentplan.String(setting["value"])
				}
			}
			return result, result == "true" || result == "false"
		}
		before, ok := read(old)
		after, valid := read(wanted)
		if !ok || !valid || before != after {
			return environmentAuthBlocker(id, "existing authentication role differs from maintained policy: "+key)
		}
	}
	return nil
}

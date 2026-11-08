package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// These are configuration keys, not defaults. Both creation finalization and
// upgrade rendering project the selected environment's resolved intent.
var environmentMQTTRoles = map[string]string{
	"video-cloud-api":            "CORE_API_MQTT_ENABLED",
	"video-cloud-api-pki":        "DEVICE_API_MQTT_ENABLED",
	"video-cloud-api-app-pki":    "APP_API_MQTT_ENABLED",
	"video-cloud-mqttfoundation": "MQTT_FOUNDATION_MQTT_ENABLED",
	"video-cloud-shadowworker":   "SHADOW_WORKER_MQTT_ENABLED",
}

func environmentMQTTRoleBlocker(key string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.mqtt-role", Resource: key, Cause: "MQTT client role must explicitly be true or false in the selected environment configuration", Next: "maintain the named setting in the selected architecture or environment architecture override, then repeat complete preflight; do not infer runtime policy from live workloads"}
}

func validateEnvironmentMQTTRoles(env map[string]string) error {
	for _, name := range sortedMapKeys(environmentMQTTRoles) {
		key := environmentMQTTRoles[name]
		if env[key] != "true" && env[key] != "false" {
			return environmentMQTTRoleBlocker(key)
		}
	}
	for _, role := range []struct{ enabled, client string }{
		{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "MQTT_FOUNDATION_MQTT_ENABLED"},
		{"SHADOW_WORKER_REGISTRATION_ENABLED", "SHADOW_WORKER_MQTT_ENABLED"},
	} {
		if env[role.enabled] == "true" && env[role.client] != "true" {
			return &deploymentplan.Blocker{ID: "deployment.configuration.mqtt-role", Resource: role.client, Cause: "enabled service requires its MQTT client", Next: "correct the selected environment configuration without disabling the required MQTT service"}
		}
	}
	return nil
}

func applyEnvironmentMQTTRole(env map[string]string, object deploymentplan.Object) error {
	if object["kind"] != "Deployment" {
		return nil
	}
	key := environmentMQTTRoles[deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])]
	if key == "" {
		return nil
	}
	if env[key] != "true" && env[key] != "false" {
		return environmentMQTTRoleBlocker(key)
	}
	container := mqttFoundationPrimary(object)
	if container == nil {
		return environmentMQTTRoleBlocker(key)
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_ENABLED", "value": env[key]}})
	ca, err := disabledEnvironmentMQTTTrustCA(object, container)
	if err != nil {
		return err
	}
	if ca != "" {
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_TLS": "true", "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH": ca}))
	}
	return nil
}

// Native API validation still checks declared server trust when its MQTT client
// is off. Complete that public transport from the original mounted file, keeping
// the client role, independent Root/DNS binding and every identity mount intact.
func disabledEnvironmentMQTTTrustCA(object, container deploymentplan.Object) (string, error) {
	if object["kind"] != "Deployment" || environmentMQTTRoles[deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])] == "" || mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_ENABLED") != "false" {
		return "", nil
	}
	root, name := mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"), mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME")
	if root == "" && name == "" {
		return "", nil
	}
	if !environmentPreflightDigest.MatchString(root) || name == "" {
		return "", coreMQTTTransportBlocker(deploymentplan.ID(object), "disabled MQTT client has an incomplete original server Root/DNS binding")
	}
	file, err := coreMQTTFile(object, container, "mqtt-pki-bundles", "roots.pem")
	return file.file, err
}

package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"path"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func mqttCallbackBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.mqtt-callback", Resource: deploymentplan.ID(object), Cause: cause, Next: "derive the Device API callback CA from its original read-only mqtt-callback mount and retain the emqx-pki client identity, server TLS and broker auth Secret; repeat full preflight"}
}

func environmentDeviceCallbackConsumer(object, container deploymentplan.Object) bool {
	return object["kind"] == "Deployment" && deploymentplan.Map(object["metadata"])["name"] == "video-cloud-api-pki" && container["name"] == "app"
}

// The fresh profile uses the original registry-enrolled Service client, whose
// actual CSR/CN is service:emqx-pki. Admission is explicit in its original
// native trust initializer and same-owner marker; no missing route/CA changes
// a legacy callback's independently provisioned emqx-pki identity.
func environmentDeviceMQTTCallbackClientName(object, container deploymentplan.Object) (string, error) {
	state := mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE")
	marker := mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE")
	native := false
	for _, value := range certIssuerObjectList(environmentPKIPod(object)["initContainers"]) {
		initializer := deploymentplan.Map(value)
		if initializer["name"] != "initialize-trust-state" {
			continue
		}
		command := certIssuerObjectList(initializer["command"])
		if len(command) == 3 && command[0] == "/bin/sh" && command[1] == "-ec" && len(certIssuerObjectList(initializer["args"])) == 0 && deviceTrustStatePath.MatchString(state) && normalizedDevicePreparation(deploymentplan.String(command[2])) == normalizedDevicePreparation(deviceNativeTrustPreparation(state)) {
			if native {
				return "", mqttCallbackBlocker(object, "native callback trust initialization is ambiguous")
			}
			native = true
		}
	}
	if !native && marker == "" {
		return "emqx-pki", nil
	}
	identity := mqttFoundationEnv(container, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE")
	if !native || marker != path.Join(path.Dir(identity), "incoming-root-bootstrap.json") || !managedMQTTPVCFile(object, container, "controller-identity", identity) || !environmentPreflightDigest.MatchString(mqttFoundationEnv(container, "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256")) {
		return "", mqttCallbackBlocker(object, "native callback requires its original native trust initialization and same-owner installed Service identity admission")
	}
	return "service:emqx-pki", nil
}

// The private callback uses the maintained broker client role, independently of
// Device/App trust. Its CA path belongs to the original mount/item mapping.
func deriveEnvironmentDeviceMQTTCallback(env map[string]string, definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentDeviceCallbackConsumer(d.Object, c) {
				continue
			}
			for _, key := range []string{"VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA", "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME"} {
				if env[key] != "" {
					return mqttCallbackBlocker(d.Object, key+" is derived and must not be configured manually")
				}
			}
			ca, err := coreMQTTFile(d.Object, c, "mqtt-callback", "ca.crt")
			if err != nil || ca.kind != "ConfigMap" {
				return mqttCallbackBlocker(d.Object, "broker callback CA lacks its original unique read-only ConfigMap mount")
			}
			client, err := environmentDeviceMQTTCallbackClientName(d.Object, c)
			if err != nil {
				return err
			}
			c["env"] = mergeEnvironmentNamedFields(c["env"], []any{
				deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA", "value": ca.file},
				deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME", "value": client},
			})
		}
	}
	return validateEnvironmentDeviceMQTTCallback(definitions)
}

func validateEnvironmentDeviceMQTTCallback(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentDeviceCallbackConsumer(d.Object, c) {
				continue
			}
			settings, err := environmentPKINamedFields(c["env"])
			if err != nil {
				return mqttCallbackBlocker(d.Object, "candidate callback settings are ambiguous")
			}
			ca, err := coreMQTTFile(d.Object, c, "mqtt-callback", "ca.crt")
			if err != nil || ca.kind != "ConfigMap" {
				return mqttCallbackBlocker(d.Object, "broker callback CA lacks its original unique read-only ConfigMap mount")
			}
			client, err := environmentDeviceMQTTCallbackClientName(d.Object, c)
			if err != nil {
				return err
			}
			values := map[string]string{"VIDEO_CLOUD_MQTT_AUTH_ADDR": ":18447", "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA": ca.file, "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME": client, "VIDEO_CLOUD_AUTH_DISABLE_ACL": "false", "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS": "false"}
			for key, wanted := range values {
				if settings[key]["valueFrom"] != nil || settings[key]["value"] != wanted {
					return mqttCallbackBlocker(d.Object, "candidate callback differs from its complete private mTLS profile: "+key)
				}
			}
			if settings["VIDEO_CLOUD_API_ADDR"]["valueFrom"] != nil || deploymentplan.String(settings["VIDEO_CLOUD_API_ADDR"]["value"]) == "" || settings["VIDEO_CLOUD_API_ADDR"]["value"] == values["VIDEO_CLOUD_MQTT_AUTH_ADDR"] {
				return mqttCallbackBlocker(d.Object, "callback must use a separate address from the Device listener")
			}
			for key, fileKey := range map[string]string{"VIDEO_CLOUD_API_TLS_CERT_PATH": "tls.crt", "VIDEO_CLOUD_API_TLS_KEY_PATH": "tls.key"} {
				file, err := coreMQTTFile(d.Object, c, "pki-server", fileKey)
				if err != nil || file.kind != "Secret" || settings[key]["valueFrom"] != nil || settings[key]["value"] != file.file {
					return mqttCallbackBlocker(d.Object, "callback server TLS must retain its original mounted Secret pair")
				}
			}
			key := settings["VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY"]
			ref := deploymentplan.Map(deploymentplan.Map(key["valueFrom"])["secretKeyRef"])
			if key["value"] != nil || deploymentplan.String(ref["name"]) == "" || deploymentplan.String(ref["key"]) == "" || ref["optional"] == true {
				return mqttCallbackBlocker(d.Object, "callback broker auth key must retain its required Secret reference")
			}
		}
	}
	return validateEnvironmentDeviceTLSPermissions(definitions)
}

// Validate application inputs without requiring a running API or broker. The
// original public CA owner and key are checked through the approved plan.
func checkEnvironmentDeviceMQTTCallback(ctx context.Context, p *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error)) error {
	definitions := []deploymentplan.Definition{}
	for _, r := range p.Resources {
		if r.Action != "retire" {
			definitions = append(definitions, deploymentplan.Definition{Object: r.Desired})
		}
	}
	if err := validateEnvironmentDeviceMQTTCallback(definitions); err != nil {
		return err
	}
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentDeviceCallbackConsumer(d.Object, c) {
				continue
			}
			file, _ := coreMQTTFile(d.Object, c, "mqtt-callback", "ca.crt")
			id := file.kind + "/" + deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"]) + "/" + file.reference
			uid := ""
			for _, r := range p.Resources {
				if r.ID == id && r.Action == "retain" {
					uid = r.UID
				}
			}
			source, err := read(ctx, id)
			if err != nil || source == nil || uid == "" || deploymentplan.Map(source["metadata"])["uid"] != uid {
				return mqttCallbackBlocker(d.Object, "original broker callback CA owner cannot be verified")
			}
			remaining := []byte(deploymentplan.String(deploymentplan.Map(source["data"])[file.key]))
			count := 0
			for len(strings.TrimSpace(string(remaining))) != 0 {
				block, rest := pem.Decode(remaining)
				if block == nil || block.Type != "CERTIFICATE" {
					return mqttCallbackBlocker(d.Object, "original broker callback CA key contains invalid public certificate data")
				}
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil || !cert.IsCA {
					return mqttCallbackBlocker(d.Object, "original broker callback CA key lacks a valid CA certificate")
				}
				count++
				remaining = rest
			}
			if count == 0 {
				return mqttCallbackBlocker(d.Object, "original broker callback CA key is missing")
			}
		}
	}
	return nil
}

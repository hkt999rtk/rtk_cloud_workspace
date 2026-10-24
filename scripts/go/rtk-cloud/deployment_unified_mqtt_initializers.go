package main

import (
	"crypto/sha256"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// Permission preparation never copies, regenerates or removes identity material.
const previousMQTTTrustPreparation = "umask 077; mkdir -p /run/pki-state/device /run/pki-state/app /run/pki-state/broker-identity/private; chmod 700 /run/pki-state/device /run/pki-state/app /run/pki-state/broker-identity /run/pki-state/broker-identity/private; find /run/pki-state -type f -exec chmod 600 {} +"
const previousMQTTHostPreparation = "set -eu; umask 077; mkdir -p /run/emqx-pki/private; chmod 700 /run/emqx-pki/private; if [ -d /var/lib/emqx-pki/identity ]; then chmod 700 /var/lib/emqx-pki/identity; find /var/lib/emqx-pki/identity -type f -exec chmod 600 {} +; fi"

const mqttTrustPreparation = previousMQTTTrustPreparation + "; umask 077; mkdir -p /run/pki-state/private/crl; chmod 700 /run/pki-state/private /run/pki-state/private/crl"
const mqttHostPreparation = previousMQTTHostPreparation + "; umask 077; mkdir -p /var/lib/emqx-pki/private/crl; chmod 700 /var/lib/emqx-pki/private /var/lib/emqx-pki/private/crl"

func mqttInitializerBlocker(id, name string) error {
	return &deploymentplan.Blocker{ID: "deployment.inventory.mqtt-initializer", Resource: id, Cause: "initializer " + name + " is outside the maintained permission-only MQTT profile", Next: "qualify the original initializer image, permission preparation and retained state mounts; do not import arbitrary commands or regenerate identity; repeat preflight"}
}
func maintainedMQTTInitializer(name string) (map[string]any, error) {
	script, uid, mounts := "", float64(10001), []any{}
	switch name {
	case "prepare-pki-state":
		script = mqttTrustPreparation
		mounts = []any{map[string]any{"name": "pki-state", "mountPath": "/run/pki-state"}}
	case "prepare-mqtt-host-state":
		script = mqttHostPreparation
		uid = 1000
		mounts = []any{map[string]any{"name": "mqtt-host-state", "mountPath": "/var/lib/emqx-pki"}, map[string]any{"name": "mqtt-host-runtime", "mountPath": "/run/emqx-pki"}}
	default:
		return nil, mqttInitializerBlocker("", name)
	}
	return map[string]any{"name": name, "command": []any{"/bin/sh", "-ec", script}, "volumeMounts": mounts, "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]any{"memory": "64Mi"}}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": uid, "runAsGroup": float64(1000), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}, nil
}
func validateObservedMQTTInitializer(id string, container map[string]any, release environmentRelease) error {
	name := deploymentplan.String(container["name"])
	maintained, err := maintainedMQTTInitializer(name)
	if err != nil {
		return mqttInitializerBlocker(id, name)
	}
	command, args := certIssuerObjectList(container["command"]), certIssuerObjectList(container["args"])
	if len(command) < 2 || len(command) > 3 || (command[0] != "sh" && command[0] != "/bin/sh") || (command[1] != "-c" && command[1] != "-ec") || len(command)+len(args) != 3 || len(certIssuerObjectList(container["env"])) != 0 || len(certIssuerObjectList(container["envFrom"])) != 0 {
		return mqttInitializerBlocker(id, name)
	}
	parts := []string{}
	for _, v := range append(command[2:], args...) {
		parts = append(parts, deploymentplan.String(v))
	}
	script := strings.Join(parts, "\n")
	// Existing immutable permission-only profiles; these hashes authorize
	// compatibility only. Desired commands always come from the constants above.
	legacy := map[string]string{"prepare-pki-state": "07de769175781fcc1e0a78623519305eb3667483bf4e2fb0e1c464ab013f4dbe", "prepare-mqtt-host-state": "c30c3b2b2dbc1cc506735ab4555b6897f939d6c28c81260665071ef48b4df339"}
	expected := deploymentplan.String(certIssuerObjectList(maintained["command"])[2])
	previous := map[string]string{"prepare-pki-state": previousMQTTTrustPreparation, "prepare-mqtt-host-state": previousMQTTHostPreparation}
	if script != expected && script != previous[name] && fmt.Sprintf("%x", sha256.Sum256([]byte(script))) != legacy[name] {
		return mqttInitializerBlocker(id, name)
	}
	repository := environmentImageRepository(deploymentplan.String(container["image"]))
	if repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && repository != "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api" {
		return mqttInitializerBlocker(id, name)
	}
	return nil
}

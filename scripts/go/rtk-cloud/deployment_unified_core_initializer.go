package main

import (
	"crypto/sha256"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

const coreMQTTStatePreparation = "umask 077; mkdir -p /var/lib/mqtt-pki/crls /var/lib/mqtt-pki/identity; chmod 700 /var/lib/mqtt-pki/crls /var/lib/mqtt-pki/identity"

func coreMQTTInitializerBlocker(id string) error {
	return &deploymentplan.Blocker{ID: "deployment.inventory.api-initializer", Resource: id, Cause: "existing API MQTT state initializer has an unsupported name, permission recipe or image source", Next: "qualify the maintained permission-only MQTT state preparation and original mount; retain trust/identity contents and select the formal API image, then repeat preflight"}
}
func maintainedCoreMQTTInitializer(name string) (map[string]any, error) {
	if name != "mqtt-pki-state-init" {
		return nil, coreMQTTInitializerBlocker(name)
	}
	return map[string]any{"name": name, "command": []any{"/bin/sh", "-ec", coreMQTTStatePreparation}, "volumeMounts": []any{map[string]any{"name": "mqtt-pki-state", "mountPath": "/var/lib/mqtt-pki"}}, "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]any{"memory": "64Mi"}}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}, nil
}
func validateObservedCoreMQTTInitializer(id string, c map[string]any, release environmentRelease) error {
	if _, err := maintainedCoreMQTTInitializer(deploymentplan.String(c["name"])); err != nil {
		return coreMQTTInitializerBlocker(id)
	}
	command, args := certIssuerObjectList(c["command"]), certIssuerObjectList(c["args"])
	if len(command) < 2 || len(command) > 3 || len(command)+len(args) != 3 || (command[0] != "sh" && command[0] != "/bin/sh") || (command[1] != "-c" && command[1] != "-ec") {
		return coreMQTTInitializerBlocker(id)
	}
	parts := []string{}
	for _, v := range append(command[2:], args...) {
		parts = append(parts, deploymentplan.String(v))
	}
	script := strings.Join(parts, "\n")
	// A supported original permission-only recipe; desired commands are maintained.
	if script != coreMQTTStatePreparation && fmt.Sprintf("%x", sha256.Sum256([]byte(script))) != "d1847c03b211a02e36cee912dfc26165ffc654a8630c311896216c7fae390987" {
		return coreMQTTInitializerBlocker(id)
	}
	repository := environmentImageRepository(deploymentplan.String(c["image"]))
	if repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && repository != "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api" {
		return coreMQTTInitializerBlocker(id)
	}
	return nil
}

func existingMQTTStateConsumer(component, name string) bool {
	return component == "existing-workload" && (name == "video-cloud-api" || name == "video-cloud-logingester")
}

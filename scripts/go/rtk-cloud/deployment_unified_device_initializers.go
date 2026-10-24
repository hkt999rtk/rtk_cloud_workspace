package main

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

var deviceTrustStatePath = regexp.MustCompile(`^/run/pki-state/root-trust-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.json$`)
var deviceTrustPathInRecipe = regexp.MustCompile(`/run/pki-state/root-trust-[0-9a-f-]+\.json`)

const deviceControllerPreparation = "umask 077; mkdir -p /state/private; chmod 700 /state/private; if [ -e /state/private/identity.json ]; then chmod 600 /state/private/identity.json; fi"

func deviceInitializerBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.inventory.device-initializer", Resource: id, Cause: cause, Next: "qualify the original Device API trust-state filename, permission-only controller preparation and formal initializer image role; retain existing state and source mounts, then repeat preflight"}
}
func deviceTrustPreparation(file string) string {
	return fmt.Sprintf("if [ ! -e %s ]; then cp /run/pki-initial/root-trust.json %s.new; chmod 600 %s.new; mv %s.new %s; fi", file, file, file, file, file)
}
func deviceNativeTrustPreparation(file string) string {
	return fmt.Sprintf("umask 077; mkdir -p /run/pki-state; chmod 700 /run/pki-state; if [ -e %s ]; then chmod 600 %s; fi", file, file)
}
func normalizedDevicePreparation(script string) string {
	script = strings.ReplaceAll(strings.ReplaceAll(script, "\"", ""), "'", "")
	return strings.Join(strings.Fields(strings.ReplaceAll(script, ";", " ; ")), " ")
}
func observeDeviceInitializer(binding *environmentPodBindings, c map[string]any, release environmentRelease, id string) error {
	command, args := certIssuerObjectList(c["command"]), certIssuerObjectList(c["args"])
	if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-ec" || len(args) != 0 {
		return deviceInitializerBlocker(id, "unsupported Device API initializer entrypoint")
	}
	script := deploymentplan.String(command[2])
	switch c["name"] {
	case "initialize-trust-state":
		paths := deviceTrustPathInRecipe.FindAllString(script, -1)
		if len(paths) == 0 || !deviceTrustStatePath.MatchString(paths[0]) {
			return deviceInitializerBlocker(id, "original trust-state filename cannot be verified")
		}
		for _, file := range paths {
			if file != paths[0] {
				return deviceInitializerBlocker(id, "trust initializer references multiple state files")
			}
		}
		if normalizedDevicePreparation(script) == normalizedDevicePreparation(deviceNativeTrustPreparation(paths[0])) {
			binding.NativeTrustInitialization = true
		} else if normalizedDevicePreparation(script) != normalizedDevicePreparation(deviceTrustPreparation(paths[0])) {
			return deviceInitializerBlocker(id, "trust initializer differs from preserve-on-retry recipe")
		}
		binding.DeviceTrustStateFile = paths[0]
	case "initialize-controller-identity":
		if script != deviceControllerPreparation && fmt.Sprintf("%x", sha256.Sum256([]byte(script))) != "79f3ab174187e67fdf121da5e24f2739626042fb7b4c608571c4efb6dcb6323e" {
			return deviceInitializerBlocker(id, "controller initializer is not a supported permission-only recipe")
		}
	default:
		return deviceInitializerBlocker(id, "unknown Device API initializer name")
	}
	repository := environmentImageRepository(deploymentplan.String(c["image"]))
	if repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && repository != "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api" {
		return deviceInitializerBlocker(id, "initializer has an unknown image source")
	}
	return nil
}
func maintainedDeviceInitializer(name, file string) (map[string]any, error) {
	script, mounts := "", []any{}
	switch name {
	case "initialize-trust-state":
		if !deviceTrustStatePath.MatchString(file) {
			return nil, deviceInitializerBlocker(name, "original trust-state filename is invalid")
		}
		script = deviceTrustPreparation(file)
		mounts = []any{map[string]any{"name": "pki-state", "mountPath": "/run/pki-state"}, map[string]any{"name": "pki-initial-state", "mountPath": "/run/pki-initial", "readOnly": true}}
	case "initialize-controller-identity":
		script = deviceControllerPreparation
		mounts = []any{map[string]any{"name": "controller-identity", "mountPath": "/state"}}
	default:
		return nil, deviceInitializerBlocker(name, "unknown Device API initializer name")
	}
	return map[string]any{"name": name, "command": []any{"/bin/sh", "-ec", script}, "volumeMounts": mounts, "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]any{"memory": "64Mi"}}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}, nil
}

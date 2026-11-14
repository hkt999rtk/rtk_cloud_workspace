package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// Only accept the exact Device native projection. The container-visible file
// path remains unchanged; the private subdirectory is an implementation of the
// memory mount, not part of the OpenBao record key.
func devicePrivateMemoryPlacement(object, container deploymentplan.Object, role string, mount deploymentplan.Object) bool {
	if role != "pki-state" || mount["subPath"] != "private" || mount["mountPath"] != "/run/pki-state" {
		return false
	}
	pod := environmentPKIPod(object)
	if pod == nil {
		return false
	}
	members := 0
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		if c["name"] == container["name"] && deploymentplan.Digest(c) == deploymentplan.Digest(container) {
			members++
		}
	}
	if members != 1 {
		return false
	}
	state := mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE")
	if !deviceTrustStatePath.MatchString(state) {
		return false
	}
	init := managedNamedField(pod["initContainers"], "initialize-trust-state")
	command := certIssuerObjectList(init["command"])
	if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-ec" || command[2] != devicePrivateMemoryTrustPreparation(state) {
		return false
	}
	clone := deploymentplan.Clone(object)
	changed, err := projectDeviceMemoryTrustPrivateSubPath(clone)
	return err == nil && !changed && deploymentplan.Digest(clone) == deploymentplan.Digest(object)
}

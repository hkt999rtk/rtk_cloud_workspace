package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The initializer sees the memory volume root; the application sees only the
// private directory at its unchanged state path. No identity is generated.
func devicePrivateMemoryTrustPreparation(file string) string {
	return fmt.Sprintf(`umask 077; mkdir -p /run/pki-state/private; test ! -L /run/pki-state/private; chmod 700 /run/pki-state/private; state=%s; state=/run/pki-state/private/${state##*/}; if [ -e "$state" ]; then chmod 600 "$state"; fi`, file)
}

// projectDeviceMemoryTrustPrivateSubPath is also used on cloned recorded plans
// to validate an exact same-owner successor. It never changes PVC mounts.
func projectDeviceMemoryTrustPrivateSubPath(object deploymentplan.Object) (bool, error) {
	pod := environmentPKIPod(object)
	if pod == nil {
		return false, nil
	}
	init := managedNamedField(pod["initContainers"], "initialize-trust-state")
	if init == nil {
		return false, nil
	}
	mount := managedNamedField(init["volumeMounts"], "pki-state")
	if mount == nil || mount["mountPath"] != "/run/pki-state" {
		return false, nil
	}
	volume := managedNamedField(pod["volumes"], deploymentplan.String(mount["name"]))
	if deploymentplan.Map(volume["emptyDir"])["medium"] != "Memory" {
		return false, nil
	}
	bad := func() (bool, error) {
		return false, deviceInitializerBlocker(deploymentplan.ID(object), "memory-backed trust state has an unsupported initializer or mount")
	}
	if mount["readOnly"] == true || deploymentplan.String(mount["subPath"]) != "" || deploymentplan.String(mount["subPathExpr"]) != "" {
		return bad()
	}
	cmd := certIssuerObjectList(init["command"])
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-ec" || len(certIssuerObjectList(init["args"])) != 0 {
		return bad()
	}
	script := deploymentplan.String(cmd[2])
	paths := deviceTrustPathInRecipe.FindAllString(script, -1)
	if len(paths) == 0 || !deviceTrustStatePath.MatchString(paths[0]) {
		return bad()
	}
	for _, p := range paths {
		if p != paths[0] {
			return bad()
		}
	}
	if !isDeviceNativeTrustPreparation(script, paths[0]) {
		return bad()
	}
	var mounts []map[string]any
	// The private directory is mode 0700, so its initializer must use the
	// same explicitly configured UID as the application that consumes it.
	var consumerUID, consumerGID any
	podSecurity := deploymentplan.Map(pod["securityContext"])
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		for _, item := range certIssuerObjectList(c["volumeMounts"]) {
			m := deploymentplan.Map(item)
			if m["name"] != mount["name"] {
				continue
			}
			sub := deploymentplan.String(m["subPath"])
			if m["mountPath"] != "/run/pki-state" || m["readOnly"] == true || deploymentplan.String(m["subPathExpr"]) != "" || (sub != "" && sub != "private") {
				return bad()
			}
			mounts = append(mounts, m)
			security := deploymentplan.Map(c["securityContext"])
			uid, gid := security["runAsUser"], security["runAsGroup"]
			if uid == nil {
				uid = podSecurity["runAsUser"]
			}
			if gid == nil {
				gid = podSecurity["runAsGroup"]
			}
			if uid != nil && consumerUID != nil && fmt.Sprint(uid) != fmt.Sprint(consumerUID) {
				return bad()
			}
			if gid != nil && consumerGID != nil && fmt.Sprint(gid) != fmt.Sprint(consumerGID) {
				return bad()
			}
			if uid != nil {
				consumerUID = uid
			}
			if gid != nil {
				consumerGID = gid
			}
		}
	}
	if len(mounts) == 0 {
		return bad()
	}
	desired := devicePrivateMemoryTrustPreparation(paths[0])
	changed := script != desired
	initSecurity := deploymentplan.Map(init["securityContext"])
	if initSecurity == nil {
		initSecurity = deploymentplan.Object{}
	}
	initUID, initGID := initSecurity["runAsUser"], initSecurity["runAsGroup"]
	if initUID == nil {
		initUID = podSecurity["runAsUser"]
	}
	if initGID == nil {
		initGID = podSecurity["runAsGroup"]
	}
	if consumerUID != nil && fmt.Sprint(initUID) != fmt.Sprint(consumerUID) {
		initSecurity["runAsUser"] = consumerUID
		changed = true
	}
	if consumerGID != nil && fmt.Sprint(initGID) != fmt.Sprint(consumerGID) {
		initSecurity["runAsGroup"] = consumerGID
		changed = true
	}
	if len(initSecurity) != 0 {
		init["securityContext"] = initSecurity
	}
	for _, m := range mounts {
		if m["subPath"] != "private" {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	init["command"] = []any{"/bin/sh", "-ec", desired}
	for _, m := range mounts {
		m["subPath"] = "private"
	}
	return true, nil
}

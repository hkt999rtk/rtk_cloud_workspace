package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A private file path is a disposable projection of one original remote owner.
// This checks placement only; admission still verifies committed KV/registry state.
func environmentCustodyMemoryRelativeFile(object, container deploymentplan.Object, role, file string) (string, bool) {
	if mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" || !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n\\") {
		return "", false
	}
	prefix := mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX")
	parts := strings.Split(prefix, "/")
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	if len(parts) != 3 || !secretEnvironmentPattern.MatchString(parts[0]) || !environmentPreflightDigest.MatchString(parts[1]) || !secretEnvironmentPattern.MatchString(parts[2]) || !strings.HasPrefix(ns, "video-cloud-"+parts[0]+"-") || mqttFoundationEnv(container, "PKI_ENVIRONMENT") != parts[0] || mqttFoundationEnv(container, "PKI_STATE_OPENBAO_ROLE") != environmentCustodyRoleName(prefix) {
		return "", false
	}
	pod := environmentPKIPod(object)
	if account := deploymentplan.String(pod["serviceAccountName"]); account == "" || account == "default" {
		return "", false
	}
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return "", false
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return "", false
	}
	volume, mount := volumes[role], mounts[role]
	if volume == nil || mount == nil || len(volume) != 2 || deploymentplan.Map(volume["emptyDir"])["medium"] != "Memory" || mount["readOnly"] == true || mount["subPath"] != nil || mount["subPathExpr"] != nil {
		return "", false
	}
	base := deploymentplan.String(mount["mountPath"])
	if !path.IsAbs(base) || path.Clean(base) != base || base == "/" || !strings.HasPrefix(file, base+"/") {
		return "", false
	}
	for name, other := range mounts {
		otherBase := deploymentplan.String(other["mountPath"])
		if name != role && otherBase != "" && (file == otherBase || strings.HasPrefix(file, strings.TrimSuffix(otherBase, "/")+"/")) && len(otherBase) >= len(base) {
			return "", false
		}
	}
	return strings.TrimPrefix(file, base+"/"), true
}

func managedMQTTStateFile(object, container deploymentplan.Object, role, file string) bool {
	if mqttFoundationEnv(container, "PKI_STATE_BACKEND") == "openbao" {
		_, ok := environmentCustodyMemoryRelativeFile(object, container, role, file)
		return ok
	}
	return managedMQTTPVCFile(object, container, role, file)
}

func controllerCustodyPlacement(object deploymentplan.Object) (bool, error) {
	c := managedNamedField(environmentPKIPod(object)["containers"], "pki-controller")
	if mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" {
		return false, nil
	}
	pod := environmentPKIPod(object)
	uid := deploymentplan.Map(pod["securityContext"])["runAsUser"]
	if selected, ok := deploymentplan.Map(c["securityContext"])["runAsUser"]; ok {
		uid = selected
	}
	runtimeUID, err := strconv.ParseInt(fmt.Sprint(uid), 10, 64)
	if err != nil || runtimeUID <= 0 {
		return true, controllerIdentityPermissionBlocker(deploymentplan.ID(object), "OpenBao private projection requires its explicit non-root runtime owner")
	}
	for _, key := range []string{"PKI_HOST_IDENTITY_STATE", "PKI_SERVICE_CLIENT_IDENTITY_STATE"} {
		if _, ok := environmentCustodyMemoryRelativeFile(object, c, "service-state", mqttFoundationEnv(c, key)); !ok {
			return true, controllerIdentityPermissionBlocker(deploymentplan.ID(object), "original OpenBao identity requires its scoped writable memory projection")
		}
	}
	return true, nil
}

// Storage metadata is separate from signed cache admission. Legacy PVC profiles
// keep their original checks; remote profiles must map to scoped memory only.
func livePKIStateOnStorage(live liveDeployment, containerName, file string) bool {
	raw, err := json.Marshal(live)
	if err != nil {
		return false
	}
	var object deploymentplan.Object
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	pod := environmentPKIPod(object)
	container := managedNamedField(pod["containers"], containerName)
	if mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" {
		return livePKICRLStateOnPVC(live, containerName, file)
	}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		volume := deploymentplan.Map(raw)
		// The legacy typed view has zero-valued Secret/ConfigMap fields. Those
		// are not sources on a real emptyDir; retain every actual storage source.
		if volume["emptyDir"] != nil {
			for _, name := range []string{"persistentVolumeClaim", "secret", "configMap"} {
				if name == "persistentVolumeClaim" && volume[name] == nil {
					delete(volume, name)
				}
				if name == "secret" && deploymentplan.String(deploymentplan.Map(volume[name])["secretName"]) == "" {
					delete(volume, name)
				}
				if name == "configMap" && deploymentplan.String(deploymentplan.Map(volume[name])["name"]) == "" {
					delete(volume, name)
				}
			}
		}
	}
	for name := range normalizedLivePKIMounts(container) {
		if _, ok := environmentCustodyMemoryRelativeFile(object, container, name, file); ok {
			return true
		}
	}
	return false
}

func normalizedLivePKIMounts(container deploymentplan.Object) map[string]deploymentplan.Object {
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return nil
	}
	// Empty optional fields in the typed legacy view mean absent, rather than
	// a configured subPath. The strict common resolver can then be shared.
	for _, mount := range mounts {
		for _, key := range []string{"subPath", "subPathExpr"} {
			if mount[key] == "" {
				delete(mount, key)
			}
		}
	}
	return mounts
}

package main

import (
	"reflect"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceTLSPermissionBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.device-tls-permissions", Resource: deploymentplan.ID(object), Cause: cause, Next: "retain the original read-only TLS Secret, item paths and private identity PVCs; use the maintained non-root UID/GID 10001 with supplementary group 0 for original root-owned 0440 TLS files, without fsGroup or credential copies, then repeat full preflight"}
}

// Secret projections remain root-owned when fsGroup is absent. Qualify the
// candidate's exact reader and projected file modes without starting the API,
// inspecting a future Pod, or changing the original files or identity state.
func validateEnvironmentDeviceTLSPermissions(definitions []deploymentplan.Definition) error {
	for _, definition := range definitions {
		object := definition.Object
		pod := environmentPKIPod(object)
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(raw)
			if !environmentDeviceCallbackConsumer(object, container) {
				continue
			}
			podSecurity := deploymentplan.Map(pod["securityContext"])
			if podSecurity["fsGroup"] != nil || podSecurity["fsGroupChangePolicy"] != nil || !reflect.DeepEqual(podSecurity["supplementalGroups"], []any{float64(0)}) {
				return deviceTLSPermissionBlocker(object, "candidate Device API requires supplementary group 0 without fsGroup mutations of original private identity storage")
			}
			security := deploymentplan.Map(container["securityContext"])
			for _, field := range []string{"runAsUser", "runAsGroup", "runAsNonRoot"} {
				value, explicit := security[field]
				if !explicit {
					value = podSecurity[field]
				}
				wanted := any(float64(10001))
				if field == "runAsNonRoot" {
					wanted = true
				}
				if value != wanted {
					return deviceTLSPermissionBlocker(object, "candidate Device API must explicitly retain non-root runtime UID and primary GID 10001")
				}
			}
			volumes, err := environmentPKINamedFields(pod["volumes"])
			if err != nil {
				return deviceTLSPermissionBlocker(object, "original Device API TLS volume source is ambiguous")
			}
			source := deploymentplan.Map(volumes["pki-server"]["secret"])
			for setting, key := range map[string]string{"VIDEO_CLOUD_API_TLS_CERT_PATH": "tls.crt", "VIDEO_CLOUD_API_TLS_KEY_PATH": "tls.key"} {
				file, err := coreMQTTFile(object, container, "pki-server", key)
				if err != nil || file.kind != "Secret" || mqttFoundationEnv(container, setting) != file.file {
					return deviceTLSPermissionBlocker(object, "original Device API TLS pair lacks its unique read-only Secret mount and item mapping")
				}
				mode := source["defaultMode"]
				for _, raw := range certIssuerObjectList(source["items"]) {
					item := deploymentplan.Map(raw)
					if item["key"] == key && item["mode"] != nil {
						mode = item["mode"]
					}
				}
				permissions, numeric := mode.(float64)
				if !numeric || permissions != float64(0o440) {
					return deviceTLSPermissionBlocker(object, "original Device API TLS projection must retain private group-readable 0440 file modes for both certificate and key")
				}
			}
		}
	}
	return nil
}

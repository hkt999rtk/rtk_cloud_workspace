package main

import (
	"fmt"
	"path"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// State and its independent client Root remain original lifecycle bindings.
// Endpoints and CA/CRL files are derived; no missing key is initialized here.
func coreMQTTManagementSettings(object, container deploymentplan.Object, definitions []deploymentplan.Definition, controller string, controllerPort int, ca string) (map[string]string, coreMQTTMountedFile, error) {
	id := deploymentplan.ID(object)
	fail := func(cause string) (map[string]string, coreMQTTMountedFile, error) {
		return nil, coreMQTTMountedFile{}, coreMQTTTransportBlocker(id, cause)
	}
	state := mqttFoundationEnv(container, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE")
	root := mqttFoundationEnv(container, "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256")
	if !coreMQTTIdentityFile(object, container, state) || len(root) != 64 || strings.Trim(root, "0123456789abcdef") != "" {
		return fail("MQTT management requires the original managed Service identity state on its writable PVC and independent client Root; empty static certificate/key paths cannot qualify")
	}
	for _, key := range []string{"VIDEO_CLOUD_MQTT_MANAGEMENT_CERT", "VIDEO_CLOUD_MQTT_MANAGEMENT_KEY", "VIDEO_CLOUD_CONTROLLER_IDENTITY_BOOTSTRAP_CERT", "VIDEO_CLOUD_CONTROLLER_IDENTITY_BOOTSTRAP_KEY", "VIDEO_CLOUD_CONTROLLER_IDENTITY_MANAGEMENT_CERT", "VIDEO_CLOUD_CONTROLLER_IDENTITY_MANAGEMENT_KEY", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_MANAGEMENT_CERT", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_MANAGEMENT_KEY"} {
		fields, err := environmentPKINamedFields(container["env"])
		if err != nil || fields[key] != nil && (fields[key]["valueFrom"] != nil || fields[key]["value"] != "") {
			return fail("MQTT managed receipts cannot use static or bootstrap client credentials: " + key)
		}
	}
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	issuer, issuerPort, err := coreMQTTService(definitions, ns, "certissuer", "https")
	if err != nil {
		return fail("original CertIssuer HTTPS Service/port is absent or ambiguous")
	}
	pins := map[string]string{}
	for _, role := range []struct{ name, key string }{{"pki-controller", "PKI_HOST_ROOT_SHA256"}, {"certissuer", "CERT_ISSUER_HOST_ROOT_SHA256"}} {
		for _, d := range definitions {
			if deploymentplan.ID(d.Object) != "Deployment/"+ns+"/"+role.name {
				continue
			}
			for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				pin := mqttFoundationEnv(deploymentplan.Map(raw), role.key)
				if pin == "" {
					continue
				}
				if pins[role.name] != "" || len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
					return fail("management endpoint independent Root binding is invalid or ambiguous")
				}
				pins[role.name] = pin
			}
		}
		if pins[role.name] == "" {
			return fail("management endpoint has no original independent host Root binding: " + role.name)
		}
	}
	crl, err := coreMQTTFile(object, container, "account-manager-crls", "crls.json")
	if err != nil {
		return nil, coreMQTTMountedFile{}, err
	}
	origin := fmt.Sprintf("https://%s:%d", controller, controllerPort)
	return map[string]string{
		"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_CONTROLLER_URL":              origin,
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256":         pins["pki-controller"],
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_NAME":                controller,
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA":                         ca,
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL":                    fmt.Sprintf("https://%s:%d", issuer, issuerPort),
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256": pins["certissuer"],
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_NAME":        issuer,
		"VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_TLS_CA":                 ca,
		"VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST":                     crl.file,
	}, crl, nil
}

func coreMQTTIdentityFile(object, container deploymentplan.Object, file string) bool {
	return managedMQTTPVCFile(object, container, "service-identity", file)
}

func managedMQTTPVCFile(object, container deploymentplan.Object, role, file string) bool {
	_, valid := managedMQTTPVCRelativeFile(object, container, role, file)
	return valid
}

// A named role identifies one original PVC. Another full writable mount may
// alias that same claim, but cannot select a different source or hide a file
// behind a read-only/subPath mount. Return the file's actual claim-relative
// path so permission preparation never moves a native cache between aliases.
func managedMQTTPVCRelativeFile(object, container deploymentplan.Object, role, file string) (string, bool) {
	if !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n\\") {
		return "", false
	}
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return "", false
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return "", false
	}
	claim, base, valid := managedMQTTFullPVCMount(volumes[role], mounts[role])
	if !valid {
		return "", false
	}
	selected, depth := "", -1
	ambiguous := false
	for name, mount := range mounts {
		mountPath := deploymentplan.String(mount["mountPath"])
		if file != mountPath && !strings.HasPrefix(file, strings.TrimSuffix(mountPath, "/")+"/") || mountPath == "" {
			continue
		}
		if len(mountPath) == depth {
			ambiguous = true
		}
		if len(mountPath) > depth {
			selected, depth = name, len(mountPath)
			ambiguous = false
		}
	}
	if selected == "" || ambiguous {
		return "", false
	}
	selectedClaim, selectedBase, valid := managedMQTTFullPVCMount(volumes[selected], mounts[selected])
	if !valid || selectedClaim != claim || file == selectedBase {
		return "", false
	}
	if selected == role && selectedBase != base {
		return "", false
	}
	return strings.TrimPrefix(file, strings.TrimSuffix(selectedBase, "/")+"/"), true
}

func managedMQTTFullPVCMount(volume, mount deploymentplan.Object) (string, string, bool) {
	for name := range volume {
		if name != "name" && name != "persistentVolumeClaim" {
			return "", "", false
		}
	}
	pvc := deploymentplan.Map(volume["persistentVolumeClaim"])
	claim, base := deploymentplan.String(pvc["claimName"]), deploymentplan.String(mount["mountPath"])
	if claim == "" || pvc["readOnly"] == true || mount["readOnly"] == true || mount["subPath"] != nil || mount["subPathExpr"] != nil || !path.IsAbs(base) || path.Clean(base) != base || base == "/" || strings.ContainsAny(base, "\x00\r\n\\") {
		return "", "", false
	}
	return claim, base, true
}

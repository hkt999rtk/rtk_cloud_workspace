package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the legacy core API transport affected by the interrupted update is
// changed. Other working MQTT consumers keep their existing lifecycle profile.
func environmentCoreMQTTConsumer(object deploymentplan.Object, container deploymentplan.Object) bool {
	return object["kind"] == "Deployment" && deploymentplan.Map(object["metadata"])["name"] == "video-cloud-api" && container["name"] == "app"
}
func derivedCoreMQTTSetting(name string) bool {
	return keySet("VIDEO_CLOUD_MQTT_TLS", "VIDEO_CLOUD_MQTT_ADDR", "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH", "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL", "VIDEO_CLOUD_MQTT_MANAGEMENT_CA",
		"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_CONTROLLER_URL", "VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256", "VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_NAME", "VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_NAME", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_TLS_CA", "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST")[name]
}
func coreMQTTTransportBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.mqtt-transport", Resource: id, Cause: cause, Next: "retain the original MQTT Root pin and read-only trust/CRL references; derive transport endpoints from the selected Services and file paths from original mounts/items, then repeat full preflight"}
}

type coreMQTTMountedFile struct{ kind, reference, key, file string }

// Relative filenames belong to the existing volume's item mapping. Kubernetes
// mountPath stays absolute; no current directory or host path becomes a fallback.
func coreMQTTFile(object, container deploymentplan.Object, role, key string) (coreMQTTMountedFile, error) {
	fail := func() (coreMQTTMountedFile, error) {
		return coreMQTTMountedFile{}, coreMQTTTransportBlocker(deploymentplan.ID(object), "required "+role+" file has no unique original read-only mount/item mapping")
	}
	pod := environmentPKIPod(object)
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return fail()
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return fail()
	}
	volume, mount := volumes[role], mounts[role]
	if volume == nil || mount == nil || mount["readOnly"] != true || mount["subPathExpr"] != nil {
		return fail()
	}
	result := coreMQTTMountedFile{key: key}
	var source deploymentplan.Object
	for _, kind := range []string{"configMap", "secret"} {
		if s := deploymentplan.Map(volume[kind]); s != nil {
			if source != nil {
				return fail()
			}
			source = s
			result.kind = "ConfigMap"
			name := "name"
			if kind == "secret" {
				result.kind = "Secret"
				name = "secretName"
			}
			result.reference = deploymentplan.String(source[name])
		}
	}
	if source == nil || source["optional"] == true || result.reference == "" {
		return fail()
	}
	relative := key
	if items, declared := source["items"]; declared {
		relative = ""
		for _, raw := range certIssuerObjectList(items) {
			item := deploymentplan.Map(raw)
			if item["key"] == key {
				if relative != "" {
					return fail()
				}
				relative = deploymentplan.String(item["path"])
			}
		}
	}
	base := deploymentplan.String(mount["mountPath"])
	if !path.IsAbs(base) || path.Clean(base) != base || strings.ContainsAny(base, "\x00\r\n\\") || relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") || strings.ContainsAny(relative, "\x00\r\n\\") {
		return fail()
	}
	if sub := deploymentplan.String(mount["subPath"]); sub != "" {
		if sub != relative {
			return fail()
		}
		result.file = base
	} else {
		result.file = path.Join(base, relative)
	}
	for name, other := range mounts {
		if name == role {
			continue
		}
		otherPath := deploymentplan.String(other["mountPath"])
		if otherPath != "" && (result.file == otherPath || strings.HasPrefix(result.file, strings.TrimSuffix(otherPath, "/")+"/")) && len(otherPath) >= len(base) {
			return fail()
		}
	}
	return result, nil
}

func coreMQTTService(definitions []deploymentplan.Definition, namespace, name, portName string) (string, int, error) {
	var found deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == "Service/"+namespace+"/"+name && !d.Retire {
			if found != nil {
				return "", 0, fmt.Errorf("ambiguous Service")
			}
			found = d.Object
		}
	}
	if found == nil {
		return "", 0, fmt.Errorf("required Service unavailable")
	}
	ports, err := environmentPKINamedFields(deploymentplan.Map(found["spec"])["ports"])
	if err != nil {
		return "", 0, err
	}
	port := ports[portName]
	number, err := strconv.Atoi(fmt.Sprint(port["port"]))
	if err != nil || number < 1 || number > 65535 || port["targetPort"] != portName && fmt.Sprint(port["targetPort"]) != fmt.Sprint(port["port"]) || port["protocol"] != nil && port["protocol"] != "TCP" {
		return "", 0, fmt.Errorf("required Service port unavailable")
	}
	return name + "." + namespace + ".svc", number, nil
}

func coreMQTTSettings(object, container deploymentplan.Object, definitions []deploymentplan.Definition) (map[string]string, []coreMQTTMountedFile, error) {
	id := deploymentplan.ID(object)
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return nil, nil, coreMQTTTransportBlocker(id, "original MQTT settings are ambiguous")
	}
	if settings["VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"]["valueFrom"] != nil {
		return nil, nil, coreMQTTTransportBlocker(id, "original independent MQTT Root fingerprint must be explicitly observed before derivation")
	}
	pin := mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")
	if pin == "" {
		if mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME") != "" || mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_ID") != "" {
			return nil, nil, coreMQTTTransportBlocker(id, "core MQTT PKI requires its original independent Root binding; no trust may be inferred from mounted certificates")
		}
		return nil, nil, nil
	} // No original adopted pin: do not enable PKI.
	if len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
		return nil, nil, coreMQTTTransportBlocker(id, "original MQTT Root fingerprint is invalid")
	}
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	host, port, err := coreMQTTService(definitions, ns, "mqtt-pki", "mqtts")
	if err != nil {
		return nil, nil, coreMQTTTransportBlocker(id, "managed MQTT TLS Service/port is unavailable or ambiguous")
	}
	controller, controllerPort, err := coreMQTTService(definitions, ns, "pki-controller", "https")
	if err != nil {
		return nil, nil, coreMQTTTransportBlocker(id, "original PKI Controller Service/port is unavailable or ambiguous")
	}
	roles := [][2]string{{"mqtt-pki-bundles", "roots.pem"}, {"mqtt-pki-crls", "crls.json"}, {"service-root", "root.pem"}}
	files := []coreMQTTMountedFile{}
	for _, role := range roles {
		file, err := coreMQTTFile(object, container, role[0], role[1])
		if err != nil {
			return nil, nil, err
		}
		files = append(files, file)
	}
	management, managementFile, err := coreMQTTManagementSettings(object, container, definitions, controller, controllerPort, files[2].file)
	if err != nil {
		return nil, nil, err
	}
	files = append(files, managementFile)
	values := map[string]string{"VIDEO_CLOUD_MQTT_TLS": "true", "VIDEO_CLOUD_MQTT_ADDR": fmt.Sprintf("%s:%d", host, port), "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH": files[0].file, "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME": host, "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": files[1].file, "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL": fmt.Sprintf("https://%s:%d", controller, controllerPort), "VIDEO_CLOUD_MQTT_MANAGEMENT_CA": files[2].file}
	for key, value := range management {
		values[key] = value
	}
	return values, files, nil
}

func deriveEnvironmentCoreMQTTTransport(env map[string]string, definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentCoreMQTTConsumer(d.Object, c) {
				continue
			}
			values, _, err := coreMQTTSettings(d.Object, c, definitions)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				continue
			}
			for key := range values {
				if env[key] != "" {
					return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), key+" is derived and must not be configured manually")
				}
			}
			settings := []any{}
			for _, raw := range certIssuerObjectList(c["env"]) {
				if !derivedCoreMQTTSetting(deploymentplan.String(deploymentplan.Map(raw)["name"])) {
					settings = append(settings, raw)
				}
			}
			for _, key := range sortedMapKeys(values) {
				settings = append(settings, deploymentplan.Object{"name": key, "value": values[key]})
			}
			c["env"] = settings
		}
	}
	return validateEnvironmentCoreMQTTTransport(definitions)
}

func validateEnvironmentCoreMQTTTransport(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentCoreMQTTConsumer(d.Object, c) {
				continue
			}
			values, _, err := coreMQTTSettings(d.Object, c, definitions)
			if err != nil {
				return err
			}
			settings, err := environmentPKINamedFields(c["env"])
			if err != nil {
				return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "candidate MQTT settings are ambiguous")
			}
			for key, value := range values {
				if settings[key]["valueFrom"] != nil || settings[key]["value"] != value {
					return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "candidate MQTT transport differs from its derived TLS endpoint or mounted file profile: "+key)
				}
			}
		}
	}
	return nil
}

// Confirm the role-key files exist in their original, UID-fenced resources.
// The independent Root pin is retained; never infer trust from provided PEM.
func checkEnvironmentCoreMQTTTransport(ctx context.Context, p *deploymentplan.Plan, permissionImage string, read func(context.Context, string) (deploymentplan.Object, error)) error {
	definitions := []deploymentplan.Definition{}
	for _, r := range p.Resources {
		if r.Action != "retire" {
			definitions = append(definitions, deploymentplan.Definition{Object: r.Desired})
		}
	}
	if err := validateEnvironmentCoreMQTTTransport(definitions); err != nil {
		return err
	}
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentCoreMQTTConsumer(d.Object, c) {
				continue
			}
			values, files, err := coreMQTTSettings(d.Object, c, definitions)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				continue
			}
			if err := validateCoreIdentityInitializer(d.Object, permissionImage); err != nil {
				return err
			}
			namespace := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
			for index, file := range files {
				sourceID := file.kind + "/" + namespace + "/" + file.reference
				source, err := read(ctx, sourceID)
				var uid string
				for _, r := range p.Resources {
					if r.ID == sourceID && r.Action == "retain" {
						uid = r.UID
					}
				}
				if err != nil || source == nil || uid == "" || deploymentplan.Map(source["metadata"])["uid"] != uid {
					return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "original MQTT transport file source owner cannot be verified")
				}
				data := deploymentplan.String(deploymentplan.Map(source["data"])[file.key])
				if file.kind == "Secret" {
					decoded, err := base64.StdEncoding.DecodeString(data)
					if err != nil {
						data = ""
					} else {
						data = string(decoded)
					}
				}
				if strings.TrimSpace(data) == "" {
					return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "original MQTT transport file role key is unavailable")
				}
				if index == 0 && !coreMQTTRootMatches(data, mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")) {
					return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "mounted MQTT trust does not contain the independent original Root")
				}
				if index == 2 {
					for _, key := range []string{"VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256", "VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256"} {
						if !coreMQTTRootMatches(data, mqttFoundationEnv(c, key)) {
							return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "Service trust does not contain an independently bound management Root: "+key)
						}
					}
				}
				if index == 3 {
					var entries []struct {
						StatePath string `json:"state_path"`
					}
					if json.Unmarshal([]byte(data), &entries) != nil || len(entries) == 0 {
						return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "Service client CRL manifest is empty or malformed")
					}
					for _, entry := range entries {
						if !coreMQTTIdentityFile(d.Object, c, entry.StatePath) {
							return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "Service client CRL state is outside the original writable identity PVC")
						}
						if !coreCRLParentPrepared(d.Object, c, entry.StatePath) {
							return coreIdentityPermissionBlocker(d.Object, "Service CRL persistence parent is not covered by the reviewed directory permission initializer")
						}
					}
				}
				if index == 1 {
					var entries []map[string]any
					if json.Unmarshal([]byte(data), &entries) != nil || len(entries) == 0 {
						return coreMQTTTransportBlocker(deploymentplan.ID(d.Object), "original MQTT CRL manifest is empty or malformed")
					}
				}
			}
		}
	}
	return nil
}
func coreMQTTRootMatches(data, pin string) bool {
	for {
		block, rest := pem.Decode([]byte(data))
		if block == nil {
			return false
		}
		data = string(rest)
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil && cert.IsCA && cert.CheckSignatureFrom(cert) == nil && fmt.Sprintf("%x", sha256.Sum256(cert.Raw)) == pin {
				return true
			}
		}
	}
}

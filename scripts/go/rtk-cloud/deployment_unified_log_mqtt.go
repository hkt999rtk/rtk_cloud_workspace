package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const logMQTTIdentityPrefix = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_"

func environmentLogMQTTConsumer(object, container deploymentplan.Object) bool {
	return object["kind"] == "Deployment" && deploymentplan.Map(object["metadata"])["name"] == "video-cloud-logingester" && container["name"] == "app"
}

func logMQTTBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.log-mqtt-transport", Resource: deploymentplan.ID(object), Cause: cause, Next: "retain the original LogIngester managed Service identity, independent Root pins and writable MQTT state PVC; derive MQTT transport from the selected Services and original read-only trust/CRL mounts, then repeat full preflight"}
}

func logMQTTRolloutBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.log-mqtt-rollout", Resource: deploymentplan.ID(object), Cause: cause, Next: "retain the original LogIngester MQTT identity/state PVC and paths; use the maintained one-replica Recreate update so the original state has one writer and no rolling surge, then repeat full preflight"}
}

func logMQTTScopeBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.log-mqtt-scope", Resource: deploymentplan.ID(object), Cause: cause, Next: "derive the LogIngester runtime environment from the selected maintained environment; retain both original UID-bound Service/MQTT CRL manifests, issuer domains, authority pins and state paths, then repeat full preflight without rewriting identities or waiting for the application to run"}
}

func validateEnvironmentLogMQTTScope(object, container deploymentplan.Object, environment string) error {
	if environment == "" || !secretEnvironmentPattern.MatchString(environment) || deploymentplan.Map(object["metadata"])["namespace"] != "video-cloud-"+environment+"-video-cloud" {
		return logMQTTScopeBlocker(object, "managed LogIngester has no matching selected environment and namespace")
	}
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil || settings["VIDEO_CLOUD_ENV"]["valueFrom"] != nil || settings["VIDEO_CLOUD_ENV"]["value"] != environment {
		return logMQTTScopeBlocker(object, "candidate LogIngester runtime environment differs from the selected environment and original CRL consumer scope")
	}
	return nil
}

func validateEnvironmentLogMQTTRollout(object, container deploymentplan.Object) error {
	if !environmentLogMQTTConsumer(object, container) || mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") == "" && mqttFoundationEnv(container, logMQTTIdentityPrefix+"STATE") == "" {
		return nil
	}
	spec := deploymentplan.Map(object["spec"])
	if spec["replicas"] != float64(1) {
		return logMQTTRolloutBlocker(object, "managed LogIngester requires exactly one original MQTT identity/state writer")
	}
	if !managedMQTTPVCFile(object, container, "mqtt-pki-state", mqttFoundationEnv(container, logMQTTIdentityPrefix+"STATE")) {
		return logMQTTRolloutBlocker(object, "managed LogIngester state must remain on its original writable MQTT PVC and canonical path")
	}
	strategy := deploymentplan.Map(spec["strategy"])
	if strategy["type"] != "Recreate" || strategy["rollingUpdate"] != nil {
		return logMQTTRolloutBlocker(object, "managed LogIngester requires Recreate to prevent a second state writer and an unqualified rolling-surge replica")
	}
	return nil
}

func logMQTTTransportSettings(object, c deploymentplan.Object, definitions []deploymentplan.Definition) (map[string]string, []coreMQTTMountedFile, error) {
	settings, err := environmentPKINamedFields(c["env"])
	if err != nil {
		return nil, nil, logMQTTBlocker(object, "original LogIngester MQTT settings are ambiguous")
	}
	if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") == "" && mqttFoundationEnv(c, logMQTTIdentityPrefix+"STATE") == "" {
		return nil, nil, nil
	}
	for _, key := range []string{"VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", logMQTTIdentityPrefix + "ROOT_SHA256", logMQTTIdentityPrefix + "SERVER_PKI_ROOT_SHA256", logMQTTIdentityPrefix + "RENEWAL_SERVER_PKI_ROOT_SHA256"} {
		pin := mqttFoundationEnv(c, key)
		if settings[key]["valueFrom"] != nil || len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
			return nil, nil, logMQTTBlocker(object, "managed LogIngester MQTT requires its original independent Root binding: "+key)
		}
	}
	if !managedMQTTPVCFile(object, c, "mqtt-pki-state", mqttFoundationEnv(c, logMQTTIdentityPrefix+"STATE")) {
		return nil, nil, logMQTTBlocker(object, "original managed Service identity state is outside its writable MQTT PVC")
	}
	for _, key := range []string{"VIDEO_CLOUD_MQTT_MANAGEMENT_CERT", "VIDEO_CLOUD_MQTT_MANAGEMENT_KEY", logMQTTIdentityPrefix + "BOOTSTRAP_CERT", logMQTTIdentityPrefix + "BOOTSTRAP_KEY", logMQTTIdentityPrefix + "TRANSITION_FROM_STATE", logMQTTIdentityPrefix + "TRANSITION_FROM_ROOT_SHA256"} {
		if s := settings[key]; s["valueFrom"] != nil || deploymentplan.String(s["value"]) != "" {
			return nil, nil, logMQTTBlocker(object, "ordinary managed LogIngester updates cannot use static, bootstrap or transition credentials")
		}
	}
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	host, port, err := coreMQTTService(definitions, ns, "mqtt-pki", "mqtts")
	if err != nil {
		return nil, nil, logMQTTBlocker(object, "managed MQTT TLS Service is unavailable or ambiguous")
	}
	controller, cp, err := coreMQTTService(definitions, ns, "pki-controller", "https")
	if err != nil {
		return nil, nil, logMQTTBlocker(object, "original Controller Service is unavailable or ambiguous")
	}
	issuer, ip, err := coreMQTTService(definitions, ns, "certissuer", "https")
	if err != nil {
		return nil, nil, logMQTTBlocker(object, "original renewal Service is unavailable or ambiguous")
	}
	files := []coreMQTTMountedFile{}
	for _, role := range [][2]string{{"mqtt-pki-bundles", "roots.pem"}, {"mqtt-pki-crls", "crls.json"}, {"service-root", "root.pem"}, {"service-crls", "crls.json"}} {
		file, err := coreMQTTFile(object, c, role[0], role[1])
		if err != nil || file.kind != "ConfigMap" {
			return nil, nil, logMQTTBlocker(object, "required original LogIngester trust/CRL role has no unique read-only ConfigMap mount")
		}
		files = append(files, file)
	}
	controllerURL := fmt.Sprintf("https://%s:%d", controller, cp)
	return map[string]string{"VIDEO_CLOUD_MQTT_TLS": "true", "VIDEO_CLOUD_MQTT_ADDR": fmt.Sprintf("%s:%d", host, port), "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH": files[0].file, "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME": host, "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": files[1].file, "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL": controllerURL, "VIDEO_CLOUD_MQTT_MANAGEMENT_CA": files[2].file,
		logMQTTIdentityPrefix + "CONTROLLER_URL": controllerURL, logMQTTIdentityPrefix + "SERVER_PKI_NAME": controller, logMQTTIdentityPrefix + "TLS_CA": files[2].file, logMQTTIdentityPrefix + "SERVER_CRL_MANIFEST": files[3].file, logMQTTIdentityPrefix + "PKI_CONTROLLER_URL": controllerURL, logMQTTIdentityPrefix + "MANAGEMENT_CA": files[2].file,
		logMQTTIdentityPrefix + "RENEWAL_URL": fmt.Sprintf("https://%s:%d", issuer, ip), logMQTTIdentityPrefix + "RENEWAL_SERVER_PKI_NAME": issuer, logMQTTIdentityPrefix + "RENEWAL_TLS_CA": files[2].file}, files, nil
}

func deriveEnvironmentLogMQTTTransport(env map[string]string, definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentLogMQTTConsumer(d.Object, c) {
				continue
			}
			values, _, err := logMQTTTransportSettings(d.Object, c, definitions)
			if err != nil {
				return err
			}
			if len(values) > 0 {
				// The legacy auxiliary template says staging. Environment is
				// maintained target intent, never an identity or manual path setting.
				c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_ENV", "value": env["CLOUD_ENV_NAME"]}})
				if err := validateEnvironmentLogMQTTScope(d.Object, c, env["CLOUD_ENV_NAME"]); err != nil {
					return err
				}
				// This consumer owns one original state PVC. Stop its old writer
				// before creating the replacement; never reserve a second replica.
				deploymentplan.Map(d.Object["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
				if err := bindEnvironmentLogIngesterPVCWriter(d.Object, c); err != nil {
					return err
				}
			}
			var entries []any
			for _, key := range sortedMapKeys(values) {
				if env[key] != "" {
					return logMQTTBlocker(d.Object, key+" is derived and must not be configured manually")
				}
				entries = append(entries, deploymentplan.Object{"name": key, "value": values[key]})
			}
			c["env"] = mergeEnvironmentNamedFields(c["env"], entries)
		}
	}
	return validateEnvironmentLogMQTTTransport(definitions, env["CLOUD_ENV_NAME"])
}

func validateEnvironmentLogMQTTTransport(definitions []deploymentplan.Definition, environment string) error {
	for _, d := range definitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentLogMQTTConsumer(d.Object, c) {
				continue
			}
			if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != "" || mqttFoundationEnv(c, logMQTTIdentityPrefix+"STATE") != "" {
				if err := validateEnvironmentLogMQTTScope(d.Object, c, environment); err != nil {
					return err
				}
			}
			values, _, err := logMQTTTransportSettings(d.Object, c, definitions)
			if err != nil {
				return err
			}
			settings, _ := environmentPKINamedFields(c["env"])
			for _, key := range sortedMapKeys(values) {
				if settings[key]["valueFrom"] != nil || settings[key]["value"] != values[key] {
					return logMQTTBlocker(d.Object, "candidate LogIngester MQTT differs from its derived transport: "+key)
				}
			}
			if err := validateEnvironmentLogMQTTRollout(d.Object, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkEnvironmentLogMQTTTransport(ctx context.Context, p *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error)) error {
	defs := []deploymentplan.Definition{}
	for _, r := range p.Resources {
		if r.Action != "retire" {
			defs = append(defs, deploymentplan.Definition{Object: r.Desired})
		}
	}
	if err := validateEnvironmentLogMQTTTransport(defs, p.Environment); err != nil {
		return err
	}
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentLogMQTTConsumer(d.Object, c) {
				continue
			}
			_, files, _ := logMQTTTransportSettings(d.Object, c, defs)
			for index, file := range files {
				id := file.kind + "/" + deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"]) + "/" + file.reference
				uid := ""
				for _, r := range p.Resources {
					if r.ID == id && r.Action == "retain" {
						uid = r.UID
					}
				}
				source, err := read(ctx, id)
				if err != nil || source == nil || uid == "" || deploymentplan.Map(source["metadata"])["uid"] != uid {
					return logMQTTBlocker(d.Object, "original LogIngester transport file source owner cannot be verified")
				}
				data := deploymentplan.String(deploymentplan.Map(source["data"])[file.key])
				pins := []string{}
				if index == 0 {
					pins = []string{"VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"}
				}
				if index == 2 {
					pins = []string{logMQTTIdentityPrefix + "ROOT_SHA256", logMQTTIdentityPrefix + "SERVER_PKI_ROOT_SHA256", logMQTTIdentityPrefix + "RENEWAL_SERVER_PKI_ROOT_SHA256"}
				}
				for _, key := range pins {
					if !coreMQTTRootMatches(data, mqttFoundationEnv(c, key)) {
						return logMQTTBlocker(d.Object, "mounted LogIngester trust lacks its original independent Root: "+key)
					}
				}
				if index == 1 || index == 3 {
					domain := "mqtt"
					if index == 3 {
						domain = "service"
					}
					if err := validateEnvironmentLogCRLManifest(d.Object, c, data, p.Environment, domain); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// Native NewCRLConsumer requires matching environment/domain, an original state
// path, active/retiring authority and a pinned certificate chain. Its runtime
// installer is supplied by the formal consumer code, not a preflight operation.
func validateEnvironmentLogCRLManifest(object, container deploymentplan.Object, raw, environment, domain string) error {
	var entries []struct {
		Issuer struct {
			ID          string `json:"issuer_id"`
			Environment string `json:"environment"`
			Domain      string `json:"trust_domain"`
			Status      string `json:"status"`
			Chain       string `json:"chain_pem"`
			Fingerprint string `json:"certificate_fingerprint_sha256"`
		} `json:"issuer"`
		StatePath string `json:"state_path"`
	}
	if len(raw) == 0 || len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &entries) != nil || len(entries) == 0 || len(entries) > 128 {
		return logMQTTBlocker(object, "original LogIngester CRL manifest is empty, oversized or malformed")
	}
	ids, paths, fingerprints := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if !managedMQTTPVCFile(object, container, "mqtt-pki-state", entry.StatePath) {
			return logMQTTBlocker(object, "LogIngester CRL state is outside its original writable MQTT PVC")
		}
		issuer := entry.Issuer
		if issuer.Environment != environment || issuer.Domain != domain || !livePKIIssuerIDPattern.MatchString(issuer.ID) || issuer.Status != "active" && issuer.Status != "retiring" || ids[issuer.ID] || paths[entry.StatePath] || fingerprints[issuer.Fingerprint] {
			return logMQTTScopeBlocker(object, "original LogIngester CRL authority scope, lifecycle or state mapping differs from its selected native consumer role")
		}
		remaining := bytes.TrimSpace([]byte(issuer.Chain))
		certificates := 0
		for len(remaining) > 0 {
			if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
				return logMQTTScopeBlocker(object, "original LogIngester CRL authority chain is invalid or lacks its pinned signing certificate")
			}
			block, rest := pem.Decode(remaining)
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return logMQTTScopeBlocker(object, "original LogIngester CRL authority chain is invalid or lacks its pinned signing certificate")
			}
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return logMQTTScopeBlocker(object, "original LogIngester CRL authority chain is invalid or lacks its pinned signing certificate")
			}
			if certificates == 0 {
				fingerprint := sha256.Sum256(certificate.Raw)
				if !certificate.IsCA || hex.EncodeToString(fingerprint[:]) != issuer.Fingerprint {
					return logMQTTScopeBlocker(object, "original LogIngester CRL authority chain differs from its independent manifest fingerprint")
				}
			}
			certificates++
			remaining = bytes.TrimSpace(rest)
		}
		if certificates == 0 {
			return logMQTTScopeBlocker(object, "original LogIngester CRL authority chain is missing")
		}
		ids[issuer.ID], paths[entry.StatePath], fingerprints[issuer.Fingerprint] = true, true, true
	}
	return nil
}

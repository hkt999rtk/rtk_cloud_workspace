package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Finalization changes commands and public trust configuration, while every
// private managed client remains on the very claim used by native enrollment.
// Initial Root policies are fetched by the native consumers; no policy file is
// manufactured by a renderer or copied from a different identity generation.
func finalizeEnvironmentCreateApplicationDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || release.Environment != p.Environment || release.Stack != p.Stack || validateEnvironmentCreateDeviceRoot(cfg, roots["device"]) != nil || roots["device"].Status != "active" || roots["app"].Environment != p.Environment || roots["app"].Domain != "app" || roots["app"].Status != "active" || intermediates["app"].Status != "active" || intermediates["app"].RootFingerprint != roots["app"].RootFingerprint {
		return nil, environmentCreateBootstrapBlocker("final applications require the exact formal release and actual active Device/App native lineages")
	}
	transport, err := renderEnvironmentCreateTransportDefinitions(ctx, cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		return nil, err
	}
	var broker deploymentplan.Object
	for _, definition := range transport {
		if deploymentplan.Map(definition.Object["metadata"])["name"] == "mqtt-pki" && definition.Object["kind"] == "Deployment" {
			broker = definition.Object
		}
	}
	if broker == nil {
		return nil, environmentCreateBootstrapBlocker("normal native MQTT owner cannot be rendered from its enrolled transport identities")
	}
	if roots["mqtt"].Status != "active" || intermediates["mqtt"].Status != "active" {
		return nil, environmentCreateBootstrapBlocker("normal MQTT consumers require actual active native Root and intermediate metadata")
	}
	definitions, err = configureEnvironmentCreateMQTTConsumers(p, service, roots["mqtt"], []environmentCreateRegisteredIssuer{intermediates["mqtt"]}, definitions)
	if err != nil {
		return nil, err
	}
	values := environmentCreateProviderValues(cfg)
	values["LKE_VIDEO_CLOUD_IMAGE"] = p.Image
	catalog := map[string]deploymentplan.Object{}
	for _, workload := range k8sWorkloads(values) {
		if workload.Name != "video-cloud-api" {
			continue
		}
		parsed, err := decodeEnvironmentCreateManifests(lkeDeploymentManifest(values, workload, nil))
		if err != nil || len(parsed) != 1 {
			return nil, environmentCreateBootstrapBlocker("maintained ordinary API/log runtime cannot be restored after native trust bootstrap")
		}
		catalog[workload.Name] = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(parsed[0].Object)["containers"])[0])
	}
	for _, auxiliary := range lkeVideoCloudAuxiliaryServices() {
		if auxiliary.Name != "video-cloud-logingester" {
			continue
		}
		parsed, err := decodeEnvironmentCreateManifests(lkeVideoCloudAuxiliaryDeploymentManifest(values, auxiliary))
		if err != nil || len(parsed) != 1 {
			return nil, environmentCreateBootstrapBlocker("maintained native LogIngester runtime cannot be restored after trust bootstrap")
		}
		catalog[auxiliary.Name] = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(parsed[0].Object)["containers"])[0])
	}
	owners := map[string]environmentCreateApplicationOwner{}
	for _, owner := range environmentCreateApplicationOwners() {
		owners[owner.Name] = owner
	}
	seen := map[string]bool{}
	var out []deploymentplan.Definition
	for _, definition := range definitions {
		definition.Object = deploymentplan.Clone(definition.Object)
		object := definition.Object
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if object["kind"] == "ConfigMap" && slices.Contains([]string{"pki-create-device-trust", "pki-create-app-trust"}, name) {
			continue
		}
		owner, selected := owners[name]
		if object["kind"] != "Deployment" || !selected {
			out = append(out, definition)
			continue
		}
		if seen[name] || deploymentplan.Map(object["metadata"])["namespace"] != p.Stack+"-video-cloud" {
			return nil, environmentCreateBootstrapBlocker("final application catalog contains a duplicate or foreign enrolled owner")
		}
		seen[name] = true
		if name == "mqtt-pki" {
			object = deploymentplan.Clone(broker)
			definition.Object = object
		}
		pod := environmentPKIPod(object)
		var container deploymentplan.Object
		for _, value := range certIssuerObjectList(pod["containers"]) {
			if deploymentplan.Map(value)["name"] == owner.Container {
				container = deploymentplan.Map(value)
			}
		}
		if container == nil {
			return nil, environmentCreateBootstrapBlocker("final application lost its original native enrolled primary consumer")
		}
		container["env"] = environmentCreateServiceWithoutEnv(container["env"], "RTK_DEPLOYMENT_BOOTSTRAP_TRUST_ADDR", "PKI_BROKER_BOOTSTRAP_TRUST_ADDR")
		// Retained clients attest the current session from their running native
		// owner. This also replaces an expired seed after same-key renewal.
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID}))
		if owner.Role == "video-cloud-api" || owner.Role == "video-cloud-logingester" {
			base := catalog[owner.Role]
			if base != nil {
				var workerEnv []any
				for _, value := range certIssuerObjectList(base["env"]) {
					v := deploymentplan.Map(value)
					ref := deploymentplan.Map(deploymentplan.Map(v["valueFrom"])["secretKeyRef"])
					if ref["name"] == "video-cloud-workers-runtime" {
						workerEnv = append(workerEnv, value)
					}
				}
				container["env"] = mergeEnvironmentNamedFields(container["env"], workerEnv)
			}
			if base == nil {
				return nil, environmentCreateBootstrapBlocker("maintained ordinary business database configuration is unavailable")
			}
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], "VIDEO_CLOUD_DB_DSN", "POSTGRES_PASSWORD")
			for _, value := range certIssuerObjectList(base["env"]) {
				setting := deploymentplan.Map(value)
				if setting["name"] == "VIDEO_CLOUD_DB_DSN" || setting["name"] == "POSTGRES_PASSWORD" {
					container["env"] = append(container["env"].([]any), setting)
				}
			}
		}
		delete(container, "args")
		if name == "video-cloud-api" || name == "video-cloud-logingester" {
			base := catalog[name]
			if base == nil {
				return nil, environmentCreateBootstrapBlocker("normal API/log catalog image role is unavailable")
			}
			for _, field := range []string{"command", "args", "ports", "readinessProbe", "livenessProbe"} {
				if value, ok := base[field]; ok {
					container[field] = value
				} else {
					delete(container, field)
				}
			}
			if name == "video-cloud-logingester" {
				container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "service-identity")
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("mqtt-pki-state", owner.StateRoot, false)})
				pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "service-identity")
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("mqtt-pki-state", "persistentVolumeClaim", owner.Claim)})
			}
			// Keep canonical roles on sibling paths. A ConfigMap directory uses
			// symlink projections, so a nested subPath mount over one of its files
			// fails before the native process starts.
			container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "mqtt-trust", "service-trust")
			pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "mqtt-trust", "service-trust")
			serviceCRLRole := "account-manager-crls"
			if name == "video-cloud-logingester" {
				serviceCRLRole = "service-crls"
			}
			for _, source := range []struct{ role, config, key, path string }{
				{"mqtt-pki-bundles", "pki-create-mqtt-consumer-trust", "", "/run/pki-mqtt"},
				{"mqtt-pki-crls", "pki-create-mqtt-consumer-trust", "crls.json", "/run/pki-mqtt-crls/crls.json"},
				{"service-root", "pki-create-service-trust", "", environmentCreateServiceTrust},
				{serviceCRLRole, "pki-create-service-consumer-crls", "crls.json", "/run/pki-service-crls/outbound-crls.json"},
			} {
				mount := deploymentplan.Map(environmentCreateServiceMount(source.role, source.path, true))
				if source.key != "" {
					mount["subPath"] = source.key
				}
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{mount})
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume(source.role, "configMap", source.config)})
			}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "/run/pki-mqtt-crls/crls.json"}))
			for _, value := range certIssuerObjectList(container["env"]) {
				setting := deploymentplan.Map(value)
				if strings.HasSuffix(deploymentplan.String(setting["name"]), "_SERVER_CRL_MANIFEST") && setting["value"] == environmentCreateServiceTrust+"/outbound-crls.json" {
					setting["value"] = "/run/pki-service-crls/outbound-crls.json"
				}
			}
		} else if name == "factoryenroll" {
			container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "service-identity")
			pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "service-identity")
			container["command"] = []any{"/app/factoryenroll"}
			container["envFrom"] = []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "factoryenroll-runtime"}}}
			container["env"] = append(environmentCreateServiceWithoutEnv(container["env"], "POSTGRES_PASSWORD", "VIDEO_CLOUD_DB_DSN"), deploymentplan.Object{"name": "POSTGRES_PASSWORD", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "factoryenroll-runtime", "key": "POSTGRES_PASSWORD"}}}, deploymentplan.Object{"name": "VIDEO_CLOUD_DB_DSN", "value": "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql." + p.Stack + "-platform.svc.cluster.local:5432/video_cloud?sslmode=disable"}, deploymentplan.Object{"name": "FACTORY_ENROLL_ADDR", "value": ":18443"})
			container["ports"] = []any{deploymentplan.Object{"name": "http", "containerPort": float64(18443)}}
			container["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": "http"}, "periodSeconds": float64(5)}
		} else if name == "mqtt-pki" {
			container["command"] = []any{"/app/pkibroker", "watch"}
			container["env"] = mergeEnvironmentNamedFields(container["env"], []any{
				deploymentplan.Object{"name": "PKI_BROKER_API_KEY", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-workers-runtime", "key": "VIDEO_CLOUD_EMQX_API_KEY"}}},
				deploymentplan.Object{"name": "PKI_BROKER_API_SECRET", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-workers-runtime", "key": "VIDEO_CLOUD_EMQX_API_SECRET"}}},
			})
			delete(container, "readinessProbe")
			delete(container, "ports")
			mqtt := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			mqtt["env"] = mergeEnvironmentNamedFields(mqtt["env"], environmentCreateEnv(map[string]string{"EMQX_PKI_CALLBACK_URL": "https://video-cloud-api-pki." + p.Stack + "-video-cloud.svc:18447/v1/internal/mqtt/authenticate", "EMQX_PKI_CALLBACK_CA": environmentCreateServiceTrust + "/root.pem"}))
			mqtt["env"] = mergeEnvironmentNamedFields(mqtt["env"], []any{deploymentplan.Object{"name": "EMQX_PKI_CALLBACK_AUTH_KEY", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-runtime", "key": "VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY"}}}})
			device, app := roots["device"], roots["app"]
			settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID, "PKI_BROKER_REQUIRE_CRLS": "true", "PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_DEVICE_ROOT_ID": device.ID, "PKI_BROKER_DEVICE_ROOT_STATE": "/run/pki-state/device-root.json", "PKI_BROKER_DEVICE_ROOTS": "/run/pki-device/roots.pem", "PKI_BROKER_DEVICE_CRL_MANIFEST": "/run/pki-device/broker-crls.json", "PKI_BROKER_APP_PKI_ENABLED": "true", "PKI_BROKER_APP_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_APP_ROOT_ID": app.ID, "PKI_BROKER_APP_ROOT_STATE": "/run/pki-state/app-root.json", "PKI_BROKER_APP_ROOTS": "/run/pki-app/roots.pem", "PKI_BROKER_APP_CRL_MANIFEST": "/run/pki-app/broker-crls.json"}
			settings["PKI_BROKER_DEVICE_AUTOMATIC_STATE_DIR"] = "/run/pki-state/device/automatic"
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
			container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "device-trust", "app-trust")
			container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("pki-device", "/run/pki-device", true), environmentCreateServiceMount("pki-app", "/run/pki-app", true)})
			pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "device-trust", "app-trust")
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("pki-device", "configMap", "pki-create-device-trust"), environmentCreateServiceVolume("pki-app", "configMap", "pki-create-app-trust")})
		} else {
			domain, trustName, trustPath, claim := "device", "pki-create-device-trust", "/run/pki-device", "video-cloud-api-pki-trust"
			if name == "video-cloud-api-app-pki" {
				domain, trustName, trustPath, claim = "app", "pki-create-app-trust", "/run/pki-app", "video-cloud-api-app-pki-trust"
			}
			root := roots[domain]
			settings := map[string]string{"VIDEO_CLOUD_API_ADDR": "0.0.0.0:8443", "VIDEO_CLOUD_API_BASE_URL": "https://" + name + "." + p.Stack + "-video-cloud.svc:8443", "VIDEO_CLOUD_API_TLS_CERT_PATH": "/run/pki-server/tls.crt", "VIDEO_CLOUD_API_TLS_KEY_PATH": "/run/pki-server/tls.key", "VIDEO_CLOUD_AUTH_CRL_MANIFEST": trustPath + "/api-crls.json", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": trustPath + "/bundles.json", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_MQTT_ENABLED": "false", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS": "true"}
			settings["VIDEO_CLOUD_DB_ENSURE_SCHEMA"] = "false"
			settings["VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"] = filepath.Join(owner.StateRoot, "private", "incoming-root-bootstrap.json")
			prefix, state := "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST", "/run/pki-state/root-trust-"+root.ID+".json"
			if domain == "app" {
				prefix, state = "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST", "/run/pki-state/app/root-policy.json"
				settings["VIDEO_CLOUD_TURN_CREDENTIAL_TTL"] = "1m"
				settings["VIDEO_CLOUD_MQTT_AUTH_ADDR"] = ""
			} else {
				settings["VIDEO_CLOUD_MQTT_AUTH_ADDR"] = ":18447"
				settings["VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA"] = "/run/mqtt-callback/ca.crt"
				settings["VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME"] = "service:emqx-pki"
				settings["VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR"] = "/run/pki-state/device-automatic"
			}
			settings[prefix+"_ROOT_ID"], settings[prefix+"_STATE"], settings[prefix+"_CONTROLLER_URL"] = root.ID, state, "https://pki-controller."+p.Stack+"-video-cloud.svc:18446"
			settings["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"], settings["VIDEO_CLOUD_AUTH_APP_CA_CERT"] = "", ""
			if domain == "app" {
				settings[prefix+"_ROOTS"] = trustPath + "/roots.pem"
			}
			container["command"] = []any{"/app/api"}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
			container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "device-trust", "app-trust", "pki-create-transport")
			container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("pki-server", "/run/pki-server", true), environmentCreateServiceMount("pki-"+domain, trustPath, true), environmentCreateServiceMount("pki-state", "/run/pki-state", false)})
			pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "device-trust", "app-trust", "pki-create-transport")
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("pki-server", "secret", name+"-tls"), environmentCreateServiceVolume("pki-"+domain, "configMap", trustName), environmentCreateServiceVolume("pki-state", "persistentVolumeClaim", claim)})
			pod["securityContext"] = deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "supplementalGroups": []any{float64(0)}, "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}
			container["ports"] = []any{deploymentplan.Object{"name": "https", "containerPort": float64(8443)}}
			if domain == "device" {
				container["ports"] = append(container["ports"].([]any), deploymentplan.Object{"name": "mqtt-auth", "containerPort": float64(18447)})
			}
			container["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": "https"}, "periodSeconds": float64(5)}
			if domain == "device" {
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("mqtt-callback", "/run/mqtt-callback", true)})
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("mqtt-callback", "configMap", "pki-create-mqtt-callback-ca")})
				trust, _ := maintainedDeviceInitializer("initialize-trust-state", state)
				trust["command"], trust["image"], trust["volumeMounts"] = []any{"/bin/sh", "-ec", deviceNativeTrustPreparation(state)}, p.Image, []any{environmentCreateServiceMount("pki-state", "/run/pki-state", false)}
				identity, _ := maintainedDeviceInitializer("initialize-controller-identity", "")
				identity["image"] = p.Image
				pod["initContainers"] = []any{trust, identity}
				container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "service-identity")
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("controller-identity", owner.StateRoot, false)})
				pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "service-identity")
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("controller-identity", "persistentVolumeClaim", owner.Claim)})
			} else {
				pod["initContainers"] = []any{deploymentplan.Object{"name": "initialize-app-trust-state", "image": p.Image, "command": []any{"/bin/sh", "-ec", environmentAppNativeTrustInitialize}, "volumeMounts": []any{environmentCreateServiceMount("pki-state", "/run/pki-state", false), environmentCreateServiceMount("pki-app", "/run/pki-app", true)}}}
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("pki-management", "persistentVolumeClaim", owner.Claim)})
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("pki-management", "/run/pki-management", true)})
			}
			out = append(out, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", claim))
			preparation, err := environmentCreateApplicationTrustPreparation(p, claim)
			if err != nil {
				return nil, err
			}
			out = append(out, preparation)
		}
		// Shared public Service CRL manifests use this alias of the same enrolled
		// claim. The alias never changes, exports or regenerates private material.
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", owner.Claim)})
		container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false)})
		if err := applyEnvironmentAuthRole(values, object); err != nil {
			return nil, err
		}
		if owner.Role == "video-cloud-api" {
			object, err = managedApplicationServiceCRLConfigurationRepair(object)
			if err != nil {
				return nil, err
			}
			definition.Object = object
		}
		if name == "video-cloud-api-pki" || name == "video-cloud-api-app-pki" {
			object, err = managedApplicationIncomingManifestRepair(object)
			if err != nil {
				return nil, err
			}
			definition.Object = object
		}
		if name == "video-cloud-api-app-pki" {
			object, err = managedApplicationAppCRLRoleRepair(object)
			if err != nil {
				return nil, err
			}
			definition.Object = object
		}
		definition.Phase = "workload"
		out = append(out, definition)
	}
	for name := range owners {
		if !seen[name] {
			return nil, environmentCreateBootstrapBlocker("final native application owner is missing: " + name)
		}
	}
	for _, domain := range []string{"device", "app"} {
		root := roots[domain]
		additional := []environmentCreateRegisteredIssuer{}
		if domain == "app" {
			additional = append(additional, intermediates[domain])
		}
		refs := []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}
		for _, issuer := range additional {
			refs = append(refs, map[string]string{"issuer_id": issuer.ID, "trust_bundle_version": issuer.BundleVersion})
		}
		manifest, _ := json.Marshal(refs)
		out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-" + domain + "-trust", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"roots.pem": root.Certificate, "bundles.json": string(manifest), "api-crls.json": environmentCreateServiceCRLManifest(root, filepath.Join("/run/pki-state", domain+"-root-crl.json"), additional...), "broker-crls.json": environmentCreateServiceCRLManifest(root, filepath.Join("/run/pki-state", domain+"-crl.json"), additional...)}}})
	}
	out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-consumer-crls", "namespace": p.Stack + "-video-cloud"},
		"data": deploymentplan.Object{"crls.json": environmentCreateServiceCRLManifest(service.Root, environmentCreateServiceState+"/private/crl/outbound-root.json", service.Intermediate)},
	}})
	out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-mqtt-callback-ca", "namespace": p.Stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}},
		"data": deploymentplan.Object{"ca.crt": service.Root.Certificate},
	}})
	// These native endpoints must exist before the finalized API/log processes
	// start. Numeric listener targets preserve availability while the final
	// ordinary compiler normalizes each container's named-port profile.
	out = append(out, environmentManagedConsumerDependencies(values)...)
	if p.AppRoutingMode == "internal-service" {
		out = append(out, environmentAppInternalServiceDependencies(values)...)
	}
	for _, endpoint := range []struct {
		name string
		port float64
	}{{"pki-controller", 18446}, {"certissuer", 18445}} {
		out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
			"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": endpoint.name, "namespace": p.Stack + "-video-cloud"},
			"spec": deploymentplan.Object{"type": "ClusterIP", "selector": deploymentplan.Object{"app.kubernetes.io/name": endpoint.name}, "ports": []any{deploymentplan.Object{"name": "https", "port": endpoint.port, "targetPort": endpoint.port}}},
		}})
	}
	// Parent plans may already carry the exact enrolled claims. Deduplicate only
	// identical definitions; changed ownership/reference bodies remain blockers.
	unique, byID := []deploymentplan.Definition{}, map[string]string{}
	for _, definition := range out {
		id, digest := deploymentplan.ID(definition.Object), deploymentplan.Digest(definition.Object)
		if previous, exists := byID[id]; exists {
			if previous != digest {
				return nil, environmentCreateBootstrapBlocker("final application definitions contain conflicting original references")
			}
			continue
		}
		byID[id] = digest
		unique = append(unique, definition)
	}
	return unique, nil
}

// The API's standalone Service-client CRL sync reads the controller base
// prefix, separately from its configured IDENTITY and renewal HTTP policies.
// Repair only that missing public reference using its exact declared mount.
func managedApplicationServiceCRLConfigurationRepair(original deploymentplan.Object) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("API Service CRL configuration requires its exact original read-only native manifest role")
	}
	metadata := deploymentplan.Map(original["metadata"])
	name, namespace := deploymentplan.String(metadata["name"]), deploymentplan.String(metadata["namespace"])
	environment := strings.TrimSuffix(strings.TrimPrefix(namespace, "video-cloud-"), "-video-cloud")
	if original["kind"] != "Deployment" || !secretEnvironmentPattern.MatchString(environment) || namespace != "video-cloud-"+environment+"-video-cloud" || !slices.Contains([]string{"video-cloud-api", "video-cloud-api-pki", "video-cloud-api-app-pki"}, name) {
		return bad()
	}
	object := deploymentplan.Clone(original)
	containers, err := environmentPKINamedFields(environmentPKIPod(object)["containers"])
	container := containers["app"]
	if err != nil || container == nil {
		return bad()
	}
	role, key, source, expected := "service-trust", "outbound-crls.json", "pki-create-service-trust", environmentCreateServiceTrust+"/outbound-crls.json"
	if name == "video-cloud-api" {
		role, key, source, expected = "account-manager-crls", "crls.json", "pki-create-service-consumer-crls", "/run/pki-service-crls/outbound-crls.json"
	}
	file, err := coreMQTTFile(object, container, role, key)
	if err != nil || file.kind != "ConfigMap" || file.reference != source || file.key != key || file.file != expected {
		return bad()
	}
	settings, err := environmentPKINamedFields(container["env"])
	const field = "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST"
	if err != nil || settings[field] != nil && (settings[field]["valueFrom"] != nil || settings[field]["value"] != "" && settings[field]["value"] != expected) {
		return bad()
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{field: file.file}))
	return object, nil
}

// Native incoming Root admission requires a regular public JSON file. Preserve
// its original ConfigMap bytes through the same read-only single-file projection
// used by bootstrap, rather than the AtomicWriter symlink in the whole CM mount.
func managedApplicationIncomingManifestRepair(original deploymentplan.Object) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("incoming manifest repair requires its exact original Device/App API role and read-only public bundle source")
	}
	metadata := deploymentplan.Map(original["metadata"])
	namespace, name := deploymentplan.String(metadata["namespace"]), deploymentplan.String(metadata["name"])
	environment := strings.TrimSuffix(strings.TrimPrefix(namespace, "video-cloud-"), "-video-cloud")
	if original["kind"] != "Deployment" || !secretEnvironmentPattern.MatchString(environment) || namespace != "video-cloud-"+environment+"-video-cloud" || name != "video-cloud-api-pki" && name != "video-cloud-api-app-pki" {
		return bad()
	}
	domain, product, app := "device", "true", "false"
	if name == "video-cloud-api-app-pki" {
		domain, product, app = "app", "false", "true"
	}
	object := deploymentplan.Clone(original)
	containers, err := environmentPKINamedFields(environmentPKIPod(object)["containers"])
	container := containers["app"]
	if err != nil || container == nil {
		return bad()
	}
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return bad()
	}
	for key, value := range map[string]string{"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": product, "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": app, "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true"} {
		if settings[key] == nil || settings[key]["valueFrom"] != nil || settings[key]["value"] != value {
			return bad()
		}
	}
	file, err := coreMQTTFile(object, container, "pki-"+domain, "bundles.json")
	if err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-"+domain+"-trust" || file.file != "/run/pki-"+domain+"/bundles.json" {
		return bad()
	}
	field := settings["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"]
	projected := "/run/pki-" + domain + "-manifest/bundles.json"
	if field == nil || field["valueFrom"] != nil || field["value"] != file.file && field["value"] != projected || !managedApplicationIncomingManifestAlias(object, container, domain, field["value"] == projected) {
		return bad()
	}
	if err := projectIncomingBootstrapManifest(object, domain); err != nil {
		return nil, err
	}
	return object, nil
}

// Existing projections must be this exact original key; the repair cannot
// overwrite a foreign role, source, mount or path under the maintained name.
func managedApplicationIncomingManifestAlias(object, container deploymentplan.Object, domain string, required bool) bool {
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return false
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return false
	}
	role := domain + "-trust-manifest"
	volume, mount := volumes[role], mounts[role]
	if volume == nil && mount == nil {
		return !required
	}
	wantedVolume := deploymentplan.Object{"name": role, "configMap": deploymentplan.Object{"name": "pki-create-" + domain + "-trust", "items": []any{deploymentplan.Object{"key": "bundles.json", "path": "bundles.json"}}}}
	wantedMount := deploymentplan.Object{"name": role, "mountPath": "/run/pki-" + domain + "-manifest/bundles.json", "subPath": "bundles.json", "readOnly": true}
	return deploymentplan.Digest(volume) == deploymentplan.Digest(wantedVolume) && deploymentplan.Digest(mount) == deploymentplan.Digest(wantedMount)
}

// The mandatory Product CRL switch belongs to Device authentication. App-only
// incoming TLS still uses its exact existing native App CRL and Root policy.
func managedApplicationAppCRLRoleRepair(original deploymentplan.Object) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("App API CRL role repair requires its exact original App-only mTLS, manifest and private Root policy")
	}
	metadata := deploymentplan.Map(original["metadata"])
	namespace := deploymentplan.String(metadata["namespace"])
	environment := strings.TrimSuffix(strings.TrimPrefix(namespace, "video-cloud-"), "-video-cloud")
	if original["kind"] != "Deployment" || metadata["name"] != "video-cloud-api-app-pki" || !secretEnvironmentPattern.MatchString(environment) || namespace != "video-cloud-"+environment+"-video-cloud" {
		return bad()
	}
	object := deploymentplan.Clone(original)
	containers, err := environmentPKINamedFields(environmentPKIPod(object)["containers"])
	container := containers["app"]
	if err != nil || container == nil {
		return bad()
	}
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return bad()
	}
	for key, expected := range map[string]string{
		"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "false", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true",
		"VIDEO_CLOUD_AUTH_CRL_MANIFEST":         "/run/pki-app/api-crls.json",
		"VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOTS": "/run/pki-app/roots.pem", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE": "/run/pki-state/app/root-policy.json",
	} {
		if settings[key] == nil || settings[key]["valueFrom"] != nil || settings[key]["value"] != expected {
			return bad()
		}
	}
	issuer := settings["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"]
	if issuer == nil || issuer["valueFrom"] != nil || issuer["value"] != "/run/pki-app/bundles.json" && issuer["value"] != "/run/pki-app-manifest/bundles.json" || !managedApplicationIncomingManifestAlias(object, container, "app", issuer["value"] == "/run/pki-app-manifest/bundles.json") {
		return bad()
	}
	root := mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID")
	if !environmentBootstrapOwnerUUID.MatchString(root) || mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE") != "" {
		return bad()
	}
	for _, key := range []string{"api-crls.json", "bundles.json", "roots.pem"} {
		file, err := coreMQTTFile(object, container, "pki-app", key)
		if err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-app-trust" || file.file != "/run/pki-app/"+key {
			return bad()
		}
	}
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil || deploymentplan.Map(volumes["pki-state"]["persistentVolumeClaim"])["claimName"] != "video-cloud-api-app-pki-trust" {
		return bad()
	}
	if relative, ok := managedMQTTPVCRelativeFile(object, container, "pki-state", "/run/pki-state/app/root-policy.json"); !ok || relative != "app/root-policy.json" {
		return bad()
	}
	const flag = "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"
	if settings[flag] == nil || settings[flag]["valueFrom"] != nil || settings[flag]["value"] != "true" && settings[flag]["value"] != "false" {
		return bad()
	}
	settings[flag]["value"] = "false"
	return object, nil
}

// This Job is admitted by the parent against the new claim's actual UID. It
// refuses all prior trust state before changing only the mount-root ownership;
// it never creates policy/key files or recursively changes an enrolled owner.
func environmentCreateApplicationTrustPreparation(p environmentCreateBootstrapPlan, claim string) (deploymentplan.Definition, error) {
	if (p.Environment != "dev" && p.Environment != "staging") || p.Stack != "video-cloud-"+p.Environment || !immutableEnvironmentImage.MatchString(p.Image) || !slices.Contains([]string{"video-cloud-api-pki-trust", "video-cloud-api-app-pki-trust"}, claim) {
		return deploymentplan.Definition{}, environmentCreateBootstrapBlocker("trust ownership preparation requires one exact new Device/App claim and formal native image")
	}
	script := `set -eu
test -z "$(find /state -mindepth 1 -maxdepth 1 ! -name lost+found -print -quit)"
if [ -e /state/lost+found ]; then
  test -d /state/lost+found
  test -z "$(find /state/lost+found -mindepth 1 -maxdepth 1 -print -quit)"
fi
chown 10001:10001 /state
chmod 0700 /state`
	security := deploymentplan.Object{"runAsNonRoot": false, "runAsUser": float64(0), "runAsGroup": float64(0), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "FOWNER"}}}
	container := deploymentplan.Object{"name": "prepare", "image": p.Image, "command": []any{"/bin/sh", "-ec", script}, "securityContext": security, "volumeMounts": []any{environmentCreateServiceMount("trust-state", "/state", false)}}
	return deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "prepare-" + claim, "namespace": p.Stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(120), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}}, "spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "imagePullSecrets": []any{deploymentplan.Object{"name": "ghcr-pull"}}, "securityContext": deploymentplan.Object{"seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}, "containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("trust-state", "persistentVolumeClaim", claim)}}}}}}, nil
}

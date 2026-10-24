package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateApplicationOwner struct {
	Name, Container, Role, Claim, StateRoot, ClientState string
}

func environmentCreateApplicationOwners() []environmentCreateApplicationOwner {
	return []environmentCreateApplicationOwner{
		{"video-cloud-api", "app", "video-cloud-api", "video-cloud-api-service-state", "/var/lib/video-cloud-service", "/var/lib/video-cloud-service/private/client.json"},
		{"video-cloud-api-pki", "app", "video-cloud-api", "video-cloud-api-pki-controller-identity", "/var/lib/video-cloud-api-pki-controller-identity", "/var/lib/video-cloud-api-pki-controller-identity/private/identity.json"},
		{"video-cloud-api-app-pki", "app", "video-cloud-api", "video-cloud-api-app-service-state", "/var/lib/video-cloud-app-service", "/var/lib/video-cloud-app-service/private/client.json"},
		{"video-cloud-logingester", "app", "video-cloud-logingester", "video-cloud-logingester-service-state", "/var/lib/video-cloud-log-service", "/var/lib/video-cloud-log-service/private/client.json"},
		{"factoryenroll", "factoryenroll", "factory-enroll", "factory-service-state", environmentCreateServiceState, environmentCreateServiceState + "/private/client.json"},
		{"mqtt-pki", "pkibroker", "pkibroker", "mqtt-pki-trust", "/run/pki-state", environmentCreateBrokerIdentity},
	}
}

// Every native installation Job writes only its final owner's claim. Separate
// API instances have separate keys even when they use the same Service role.
func renderEnvironmentCreateApplicationEnrollment(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	var out []deploymentplan.Definition
	namespace := p.Stack + "-video-cloud"
	for _, owner := range environmentCreateApplicationOwners() {
		job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, service, namespace, owner.Role, owner.Claim)
		if err != nil {
			return nil, err
		}
		deploymentplan.Map(job.Object["metadata"])["name"] = "pki-enroll-" + owner.Name + "-client-" + strings.ReplaceAll(service.BootstrapSessionID, "-", "")[:12]
		environmentCreateTransportJobOwner(job.Object, p.Image, owner.StateRoot, owner.ClientState, 10001)
		out = append(out, environmentCreateServiceClaimDefinition(cfg, namespace, owner.Claim), job)
	}
	return out, nil
}

func environmentCreateApplicationClientSettings(p environmentCreateBootstrapPlan, service environmentCreateServiceCeremony, owner environmentCreateApplicationOwner) map[string]string {
	controller := "pki-controller." + p.Stack + "-video-cloud.svc"
	issuer := "certissuer." + p.Stack + "-video-cloud.svc"
	prefix := "VIDEO_CLOUD_CONTROLLER_IDENTITY"
	if owner.Role == "video-cloud-logingester" {
		prefix = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY"
	}
	settings := map[string]string{
		"VIDEO_CLOUD_ENV": p.Environment, "PKI_ENVIRONMENT": p.Environment,
		"PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID,
		prefix + "_STATE":          owner.ClientState, prefix + "_ROOT_SHA256": service.Root.RootFingerprint,
		prefix + "_CONTROLLER_URL":                          "https://" + controller + ":18446",
		prefix + "_RENEWAL_URL":                             "https://" + issuer + ":18445",
		"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_CONTROLLER_URL": "https://" + controller + ":18446",
	}
	if owner.Role == "video-cloud-api" {
		settings["VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"] = ""
		if owner.Name == "video-cloud-api-pki" || owner.Name == "video-cloud-api-app-pki" {
			settings["VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"] = filepath.Join(filepath.Dir(owner.ClientState), "incoming-root-bootstrap.json")
		}
		// Each owned API installs one selected initial pool. Explicitly clear
		// inherited settings before that pool is supplied by the native stage.
		for key, value := range map[string]string{
			"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "false", "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "false",
			"VIDEO_CLOUD_AUTH_DEVICE_CA_CERT": "", "VIDEO_CLOUD_AUTH_APP_CA_CERT": "", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": "",
			"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE": "",
			"VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE": "",
			"VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_CONTROLLER_URL": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOTS": "",
			"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_CA": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_CERT": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_KEY": "",
			"VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_CA": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_CERT": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_KEY": "",
			"VIDEO_CLOUD_MQTT_ENABLED": "false", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST": "",
			"VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256": "", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_PKI_CONTROLLER_URL": "", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANAGEMENT_CA": "",
		} {
			settings[key] = value
		}
	}
	for _, endpoint := range []struct{ Prefix, Name string }{{prefix, controller}, {prefix + "_RENEWAL", issuer}} {
		environmentCreateServiceHTTPSettings(settings, endpoint.Prefix, endpoint.Name, service.Root.RootFingerprint, true, p)
		settings[endpoint.Prefix+"_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/outbound-crls.json"
	}
	if owner.Role == "factory-enroll" {
		settings = map[string]string{"VIDEO_CLOUD_ENV": p.Environment, "PKI_ENVIRONMENT": p.Environment, "PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID, "FACTORY_ENROLL_SERVICE_IDENTITY_STATE": owner.ClientState, "FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256": service.Root.RootFingerprint, "FACTORY_ENROLL_CERT_ISSUER_URL": "https://" + issuer + ":18445", "FACTORY_ENROLL_ACCOUNT_MANAGER_URL": "https://account-manager." + p.Stack + "-account-manager.svc.cluster.local:18443"}
		for _, endpoint := range []struct{ Prefix, Name string }{{"FACTORY_ENROLL_CERT_ISSUER", issuer}, {"FACTORY_ENROLL_ACCOUNT_MANAGER", "account-manager." + p.Stack + "-account-manager.svc.cluster.local"}} {
			environmentCreateServiceHTTPSettings(settings, endpoint.Prefix, endpoint.Name, service.Root.RootFingerprint, true, p)
			settings[endpoint.Prefix+"_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/outbound-crls.json"
		}
	}
	if owner.Role == "pkibroker" {
		settings = map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID, "PKI_BROKER_IDENTITY_STATE": owner.ClientState, "PKI_BROKER_ROOT_SHA256": service.Root.RootFingerprint, "PKI_BROKER_PKI_CONTROLLER_URL": "https://" + controller + ":18446", "PKI_BROKER_IDENTITY_RENEWAL_URL": "https://" + issuer + ":18445"}
		for _, endpoint := range []struct{ Prefix, Name string }{{"PKI_BROKER_IDENTITY", controller}, {"PKI_BROKER_IDENTITY_RENEWAL", issuer}} {
			environmentCreateServiceHTTPSettings(settings, endpoint.Prefix, endpoint.Name, service.Root.RootFingerprint, true, p)
			settings[endpoint.Prefix+"_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/outbound-crls.json"
		}
	}
	return settings
}

// Render from the maintained workload catalog and selected private runtime,
// rather than observing a Deployment and treating its settings as intent.
func renderEnvironmentCreateApplicationOwners(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}
	if err := readEnvironmentCreateStorageRenderSnapshot(cfg, store, &snapshot); err != nil {
		return nil, err
	}
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
		var api, log deploymentplan.Object
		for _, workload := range k8sWorkloads(values) {
			if workload.Name != "video-cloud-api" {
				continue
			}
			definitions, err := decodeEnvironmentCreateManifests(lkeDeploymentManifest(values, workload, nil))
			if err != nil || len(definitions) != 1 {
				return environmentCreateBootstrapBlocker("maintained native API or log consumer recipe cannot be rendered")
			}
			if workload.Name == "video-cloud-api" {
				api = definitions[0].Object
			} else {
				log = definitions[0].Object
			}
		}
		for _, workload := range lkeVideoCloudAuxiliaryServices() {
			if workload.Name == "video-cloud-logingester" {
				definitions, err := decodeEnvironmentCreateManifests(lkeVideoCloudAuxiliaryDeploymentManifest(values, workload))
				if err != nil || len(definitions) != 1 {
					return environmentCreateBootstrapBlocker("maintained log consumer recipe cannot be rendered")
				}
				log = definitions[0].Object
			}
		}
		if api == nil || log == nil {
			return environmentCreateBootstrapBlocker("fresh deployment requires the maintained API and log trust owners")
		}
		for _, owner := range environmentCreateApplicationOwners() {
			object := deploymentplan.Clone(api)
			if owner.Role == "video-cloud-logingester" {
				object = deploymentplan.Clone(log)
			}
			if owner.Role == "factory-enroll" || owner.Role == "pkibroker" {
				labels := deploymentplan.Object{"app.kubernetes.io/name": owner.Name}
				object = deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": owner.Container, "image": p.Image, "command": []any{"/app/" + owner.Role}}}}}}}
			}
			metadata := deploymentplan.Map(object["metadata"])
			metadata["name"], metadata["namespace"] = owner.Name, p.Stack+"-video-cloud"
			metadata["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
			spec := deploymentplan.Map(object["spec"])
			labels := deploymentplan.Object{"app.kubernetes.io/name": owner.Name, "rtk.realtek.com/stack": p.Stack}
			spec["selector"], spec["strategy"] = deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": owner.Name}}, deploymentplan.Object{"type": "Recreate"}
			deploymentplan.Map(deploymentplan.Map(spec["template"])["metadata"])["labels"] = labels
			pod := environmentPKIPod(object)
			pod["automountServiceAccountToken"] = false
			pod["securityContext"] = deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}
			pod["imagePullSecrets"] = []any{deploymentplan.Object{"name": "ghcr-pull"}}
			container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			container["name"], container["image"] = owner.Container, p.Image
			settings := environmentCreateApplicationClientSettings(p, service, owner)
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
			database := deploymentplan.Map(environmentCreateServiceDatabaseEnv("pki-verifier-database"))
			if owner.Role == "video-cloud-api" || owner.Role == "video-cloud-logingester" {
				// Native trust-only startup uses the registry grant. Normal runtime
				// restores its maintained business DSN during finalization.
				database["name"] = "VIDEO_CLOUD_DB_DSN"
			}
			container["env"] = append(environmentCreateServiceWithoutEnv(container["env"], deploymentplan.String(database["name"])), database)
			if owner.Name == "video-cloud-api-pki" || owner.Name == "video-cloud-api-app-pki" {
				// Read the original root-owned 0440 serving Secret without changing PVC
				// metadata or copying TLS credentials; keep the native runtime non-root.
				deploymentplan.Map(pod["securityContext"])["supplementalGroups"] = []any{float64(0)}
				transport := "pki-create-video-cloud-api-transport"
				if owner.Name == "video-cloud-api-app-pki" {
					transport = "pki-create-video-cloud-api-app-pki-transport"
				}
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_API_TLS_CERT_PATH": "/run/pki-server/tls.crt", "VIDEO_CLOUD_API_TLS_KEY_PATH": "/run/pki-server/tls.key", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_AUTH_DISABLE_ACL": "false", "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS": "false", "VIDEO_CLOUD_AUTH_ENABLE_LEGACY_CERT": "false"}))
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("pki-server", "secret", transport)})
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("pki-server", "/run/pki-server", true)})
			}
			container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("service-identity", owner.StateRoot, false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
			if owner.StateRoot != environmentCreateServiceState {
				// Shared public CRL manifests name this path. Both aliases resolve
				// only to this owner's already enrolled claim.
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false)})
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", owner.Claim)})
			}
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-identity", "persistentVolumeClaim", owner.Claim), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
			if owner.Role != "factory-enroll" {
				binary := "api"
				if owner.Role == "video-cloud-logingester" {
					binary = "logingester"
				}
				if owner.Role == "pkibroker" {
					binary = "pkibroker"
				}
				container["command"] = []any{"/app/" + binary, "--deployment-bootstrap-trust"}
				removeCreateBootstrapWorkerSecretRefs(container)
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"RTK_DEPLOYMENT_BOOTSTRAP_TRUST_ADDR": "0.0.0.0:19310", "PKI_BROKER_BOOTSTRAP_TRUST_ADDR": "0.0.0.0:19310"}))
				container["ports"] = []any{deploymentplan.Object{"name": "bootstrap", "containerPort": float64(19310)}}
				container["readinessProbe"] = deploymentplan.Object{"httpGet": deploymentplan.Object{"path": "/readyz", "port": "bootstrap"}, "periodSeconds": float64(3)}
				delete(container, "livenessProbe")
			}
			out = append(out, deploymentplan.Definition{Phase: "workload", Object: object})
		}
		base, err := decodeEnvironmentCreateManifests(lkeVideoCloudRuntimeSecretManifest(values), lkeFactoryEnrollRuntimeSecretManifest(values), lkeVideoCloudWorkersSecretManifest(values))
		if err != nil {
			return err
		}
		out = append(out, base...)
		return nil
	})
	return out, err
}

func configureEnvironmentCreateMQTTConsumers(p environmentCreateBootstrapPlan, service environmentCreateServiceCeremony, root environmentCreateRegisteredIssuer, additional []environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if root.Domain != "mqtt" || root.Environment != p.Environment || root.Kind != "root" || root.Status != "ready" && root.Status != "active" {
		return nil, environmentCreateBootstrapBlocker("MQTT bootstrap trust requires the actual registered ready lineage")
	}
	refs := []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}
	for _, issuer := range additional {
		if issuer.Domain != root.Domain || issuer.RootFingerprint != root.RootFingerprint {
			return nil, environmentCreateBootstrapBlocker("MQTT bootstrap bundle changed its native Root lineage")
		}
		refs = append(refs, map[string]string{"issuer_id": issuer.ID, "trust_bundle_version": issuer.BundleVersion})
	}
	manifest, _ := json.Marshal(refs)
	namespace, name, trustPath := p.Stack+"-video-cloud", "pki-create-mqtt-consumer-trust", "/run/pki-mqtt"
	seen := map[string]bool{}
	var out []deploymentplan.Definition
	for _, definition := range definitions {
		definition.Object = deploymentplan.Clone(definition.Object)
		object := definition.Object
		if object["kind"] == "ConfigMap" && deploymentplan.Map(object["metadata"])["name"] == name {
			continue
		}
		owner := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if object["kind"] == "Deployment" && (owner == "video-cloud-api" || owner == "video-cloud-logingester") {
			if seen[owner] || deploymentplan.Map(object["metadata"])["namespace"] != namespace {
				return nil, environmentCreateBootstrapBlocker("MQTT bootstrap distribution has duplicate or foreign native owners")
			}
			seen[owner] = true
			setCreateBundleRollout(object, "mqtt", string(manifest))
			pod := environmentPKIPod(object)
			container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			controller := "https://pki-controller." + namespace + ".svc:18446"
			// The managed Service identity supplies receipt authentication. Do
			// not emit unused static credential EnvVars: canonical API empty
			// literals are omitted/null and are intentionally rejected as a
			// configured static credential by transport qualification.
			settings := map[string]string{"VIDEO_CLOUD_MQTT_ENABLED": "true", "VIDEO_CLOUD_MQTT_TLS": "true", "VIDEO_CLOUD_MQTT_ADDR": "mqtt-pki." + namespace + ".svc:8883", "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME": "mqtt-pki." + namespace + ".svc", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256": root.RootFingerprint, "VIDEO_CLOUD_MQTT_ROOT_ID": "", "VIDEO_CLOUD_MQTT_ROOT_STATE": "", "VIDEO_CLOUD_MQTT_ROOTS": "", "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH": trustPath + "/roots.pem", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST": trustPath + "/bundles.json", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256": root.RootFingerprint, "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_PKI_CONTROLLER_URL": controller, "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem", "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL": controller, "VIDEO_CLOUD_MQTT_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
			for _, enrolled := range environmentCreateApplicationOwners() {
				if enrolled.Name == owner {
					container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("mqtt-bundle-state", "/var/lib/mqtt-trust", false)})
					pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("mqtt-bundle-state", "persistentVolumeClaim", enrolled.Claim)})
				}
			}
			container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("mqtt-trust", trustPath, true)})
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("mqtt-trust", "configMap", name)})
		}
		out = append(out, definition)
	}
	if !seen["video-cloud-api"] || !seen["video-cloud-logingester"] {
		return nil, environmentCreateBootstrapBlocker("both maintained MQTT trust consumers are required")
	}
	out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "data": deploymentplan.Object{"roots.pem": root.Certificate, "bundles.json": string(manifest), "crls.json": environmentCreateServiceCRLManifest(root, "/var/lib/mqtt-trust/root-crl.json", additional...)}}})
	return out, nil
}

func configureEnvironmentCreateAppConsumers(p environmentCreateBootstrapPlan, root environmentCreateRegisteredIssuer, additional []environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if root.Domain != "app" || root.Environment != p.Environment || root.Kind != "root" || root.Status != "ready" && root.Status != "active" {
		return nil, environmentCreateBootstrapBlocker("App bootstrap trust requires its actual registered ready Root")
	}
	refs := []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}
	for _, issuer := range additional {
		if issuer.Domain != root.Domain || issuer.RootFingerprint != root.RootFingerprint {
			return nil, environmentCreateBootstrapBlocker("App bootstrap bundle changed its native Root lineage")
		}
		refs = append(refs, map[string]string{"issuer_id": issuer.ID, "trust_bundle_version": issuer.BundleVersion})
	}
	manifest, _ := json.Marshal(refs)
	namespace, name, trustPath := p.Stack+"-video-cloud", "pki-create-app-trust", "/run/pki-app"
	seen := map[string]bool{}
	var out []deploymentplan.Definition
	for _, definition := range definitions {
		definition.Object = deploymentplan.Clone(definition.Object)
		object := definition.Object
		if object["kind"] == "ConfigMap" && deploymentplan.Map(object["metadata"])["name"] == name {
			continue
		}
		owner := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if object["kind"] == "Deployment" && (owner == "video-cloud-api-app-pki" || owner == "mqtt-pki") {
			pod := environmentPKIPod(object)
			for _, value := range certIssuerObjectList(pod["containers"]) {
				container := deploymentplan.Map(value)
				if container["name"] != "app" && container["name"] != "pkibroker" {
					continue
				}
				if seen[owner] || deploymentplan.Map(object["metadata"])["namespace"] != namespace {
					return nil, environmentCreateBootstrapBlocker("App bootstrap distribution has duplicate or foreign native owners")
				}
				seen[owner] = true
				setCreateBundleRollout(object, "app", string(manifest))
				settings := map[string]string{"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "false", "VIDEO_CLOUD_AUTH_APP_CA_CERT": trustPath + "/roots.pem", "VIDEO_CLOUD_AUTH_DEVICE_CA_CERT": "", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": trustPath + "/bundles.json", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID": "", "VIDEO_CLOUD_AUTH_CRL_MANIFEST": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_CONTROLLER_URL": "https://pki-controller." + namespace + ".svc:18446"}
				if container["name"] == "pkibroker" {
					settings = map[string]string{"PKI_BROKER_APP_PKI_ENABLED": "true", "PKI_BROKER_APP_ROOT_ID": root.ID, "PKI_BROKER_APP_ROOT_STATE": "/run/pki-state/app-root.json", "PKI_BROKER_APP_ROOTS": trustPath + "/roots.pem", "PKI_BROKER_APP_CRL_MANIFEST": trustPath + "/broker-crls.json", "PKI_BROKER_APP_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_REQUIRE_CRLS": "true"}
				}
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("app-trust", trustPath, true)})
			}
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("app-trust", "configMap", name)})
			if owner == "video-cloud-api-app-pki" {
				if err := projectIncomingBootstrapManifest(object, "app"); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, definition)
	}
	if !seen["video-cloud-api-app-pki"] || !seen["mqtt-pki"] {
		return nil, environmentCreateBootstrapBlocker("actual App API and broker trust owners are required")
	}
	out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "data": deploymentplan.Object{"roots.pem": root.Certificate, "bundles.json": string(manifest), "broker-crls.json": environmentCreateServiceCRLManifest(root, "/run/pki-state/app-crl.json", additional...)}}})
	return out, nil
}

func environmentCreateApplicationDefinitionsFor(definitions []deploymentplan.Definition, names ...string) []deploymentplan.Definition {
	var out []deploymentplan.Definition
	for _, definition := range definitions {
		if definition.Object["kind"] != "Deployment" || slices.Contains(names, deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["name"])) {
			if definition.Object["kind"] == "Deployment" {
				definition.Phase = "workload"
			}
			out = append(out, definition)
		}
	}
	return out
}

func readEnvironmentCreateStorageRenderSnapshot(cfg deploymentConfig, store secretStore, snapshot *environmentDeploymentSnapshot) error {
	var err error
	if cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
		snapshot.OTAStorage, err = readOTAStorageObservation(cfg, store)
		if err != nil {
			return err
		}
	}
	if cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
		snapshot.MediaStorage, err = readMediaStorageObservation(cfg, store)
		if err != nil {
			return err
		}
	}
	return nil
}

func saveEnvironmentCreateRegisteredIssuers(store secretStore, roots, intermediates map[string]environmentCreateRegisteredIssuer) error {
	for _, domain := range managedPKIDomains {
		root := roots[domain]
		if root.Status != "active" || root.Environment != store.Environment || root.Domain != domain || root.Document == nil {
			return environmentCreateBootstrapBlocker("only native verified active initial issuers may be recorded")
		}
		value := deploymentplan.Object{"root": root.Document}
		if intermediate, ok := intermediates[domain]; ok {
			if intermediate.Status != "active" || intermediate.RootFingerprint != root.RootFingerprint {
				return environmentCreateBootstrapBlocker("initial recorded intermediate differs from its native active lineage")
			}
			value["intermediate"] = intermediate.Document
		}
		if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("pki", domain, "issuer.json"), value); err != nil {
			return err
		}
	}
	return nil
}

// Trust-only programs do not consume business worker credentials. Finalization
// restores the maintained runtime environment after bootstrap acceptance.
func removeCreateBootstrapWorkerSecretRefs(container deploymentplan.Object) {
	var env []any
	for _, value := range certIssuerObjectList(container["env"]) {
		v := deploymentplan.Map(value)
		ref := deploymentplan.Map(deploymentplan.Map(v["valueFrom"])["secretKeyRef"])
		if ref["name"] != "video-cloud-workers-runtime" {
			env = append(env, value)
		}
	}
	container["env"] = env
}

func setCreateBundleRollout(object deploymentplan.Object, domain, manifest string) {
	meta := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["metadata"])
	annotations := deploymentplan.Map(meta["annotations"])
	if annotations == nil {
		annotations = deploymentplan.Object{}
		meta["annotations"] = annotations
	}
	annotations["rtk.realtek.com/pki-create-"+domain+"-bundle-sha256"] = environmentPreflightBytesSHA256([]byte(manifest))
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var environmentManagedComponents = map[string]string{
	"certissuer": "certissuer", "factoryenroll": "certissuer", "certissuer-service-pki": "service-issuer",
	"pki-controller": "pki-controller", "video-cloud-api-pki": "managed-api", "mqtt-pki": "managed-mqtt", "openbao": "managed-openbao",
	"video-cloud-mqttfoundation": "managed-mqtt-foundation", "pkiturn": "managed-turn", "video-cloud-api-app-pki": "managed-app-api",
}

// The catalog describes desired configuration, not copies of live workloads.
// Identity references are owned by the independent managed PKI lifecycle.
func renderEnvironmentResources(env map[string]string, bindings environmentIdentityBindings, release environmentRelease, support ...[]deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	return renderEnvironmentResourceCatalog(env, bindings, release, nil, support...)
}

func renderEnvironmentResourceCatalog(env map[string]string, bindings environmentIdentityBindings, release environmentRelease, intent *environmentCreateDependencyRender, support ...[]deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if env["SUPPORT_TICKETS_ENABLED"] == "true" && (len(support) != 1 || len(support[0]) == 0) {
		return nil, zammadBlocker("deployment.target.support", "", "enabled Zammad chart target is unavailable")
	}
	needed := map[string]bool{"certissuer": true, "factoryenroll": true, "pki-controller": true, "video-cloud-api-pki": true, "mqtt-pki": true, "openbao": true}
	if strings.EqualFold(env["PKI_SERVICE_ISSUER_ENABLED"], "true") {
		needed["certissuer-service-pki"] = true
	}
	if intent != nil {
		for name := range needed {
			if intent.managed[name] {
				delete(needed, name)
			}
		}
	}
	for _, binding := range bindings.Pods {
		if binding.Name == "mqtt" && (binding.Kind == "StatefulSet" || binding.Kind == "Deployment") {
			return nil, &deploymentplan.Blocker{ID: "deployment.migration.mqtt", Resource: binding.Kind + "/" + bindings.Stack + "-" + binding.NamespaceSuffix + "/mqtt", Cause: "an installed unmanaged MQTT owner requires its explicit retirement or migration before the native managed target", Next: "preserve its original data and identity; qualify its exact lifecycle instead of creating a second broker or silently dropping the owner"}
		}
		// Existing TURN consumers use the same maintained renderer in every
		// environment. Installing a new identity is a separate bootstrap stage.
		if binding.Component == "managed-turn" {
			needed["pkiturn"] = true
		}
		if binding.Component == "managed-mqtt-foundation" && lkeMQTTFoundationRegistrationEnabled(env) {
			needed[binding.Name] = true
		}
		if binding.Component == "managed-app-api" {
			needed[binding.Name] = true
		}
		if binding.Component != "existing-workload" {
			if !needed[binding.Name] {
				return nil, &deploymentplan.Blocker{ID: "deployment.inventory.disabled", Resource: binding.Kind + "/" + bindings.Stack + "-" + binding.NamespaceSuffix + "/" + binding.Name, Cause: "an existing managed consumer is disabled in the maintained target", Next: "align its maintained enablement with the existing environment, or qualify an explicit controlled retirement; repeat preflight"}
			}
			delete(needed, binding.Name)
		}
	}
	if len(needed) > 0 {
		names := []string{}
		for name := range needed {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, &deploymentplan.Blocker{ID: "deployment.target.identity", Cause: "target managed consumers have no existing verifiable identity references: " + strings.Join(names, ","), Next: "qualify the required new managed identity initialization as a deployment stage; preflight cannot invent future identity or resource UIDs"}
	}
	var definitions []deploymentplan.Definition
	seen := map[string]bool{}
	seenObjects := map[string]deploymentplan.Object{}
	add := func(manifest string) error {
		decoder := yaml.NewDecoder(strings.NewReader(manifest))
		for {
			var object map[string]any
			if err := decoder.Decode(&object); err != nil {
				if err == io.EOF {
					return nil
				}
				return &deploymentplan.Blocker{ID: "deployment.renderer.yaml", Cause: "shared component renderer produced invalid Kubernetes YAML", Next: "repair the maintained renderer output; do not include raw YAML or private values in diagnostics; repeat preflight"}
			}
			if len(object) == 0 {
				continue
			}
			// Normalize YAML integers and nested objects to the API JSON shape.
			raw, err := json.Marshal(object)
			if err != nil || json.Unmarshal(raw, &object) != nil {
				return &deploymentplan.Blocker{ID: "deployment.renderer.resource", Cause: "shared renderer produced invalid Kubernetes resource values", Next: "repair the maintained renderer's resource structure, then repeat preflight"}
			}
			id := deploymentplan.ID(object)
			if intent != nil && intent.outputs[id] != "" {
				continue // A separately qualified bootstrap producer owns this object.
			}
			if object["kind"] == "Ingress" {
				deploymentplan.Map(object["spec"])["ingressClassName"] = environmentIngressClass(env)
			}
			if err := applyEnvironmentPodBindings(object, bindings, release); err != nil {
				return err
			}
			if seen[id] {
				if deploymentplan.Digest(seenObjects[id]) == deploymentplan.Digest(object) {
					continue
				}
				return &deploymentplan.Blocker{ID: "deployment.renderer.duplicate", Resource: id, Cause: "shared renderers produced conflicting definitions for the same resource", Next: "remove the conflicting shared definition and keep one maintained renderer for this resource; repeat preflight"}
			}
			seen[id] = true
			phase := environmentResourcePhase(deploymentplan.String(object["kind"]))
			seenObjects[id] = object
			definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: phase})
		}
	}
	for _, namespace := range lkeNamespaces(env) {
		if err := add(fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace.Name)); err != nil {
			return nil, err
		}
	}
	for _, workload := range k8sWorkloads(env) {
		var manifest string
		if workload.Key == "cloud-logger" {
			manifest = lkeCloudLoggerDeploymentManifest(env)
		} else {
			manifest = lkeDeploymentManifest(env, workload, nil)
		}
		if err := add(manifest); err != nil {
			return nil, err
		}
		if workload.ServiceEnabled {
			if err := add(lkeServiceManifest(env, workload)); err != nil {
				return nil, err
			}
		}
		if lkeSQLitePVCEnabled(env, workload.Key) {
			if err := add(lkeSQLitePVCManifest(env, workload)); err != nil {
				return nil, err
			}
		}
	}
	for _, worker := range k8sAuxiliaryWorkloads() {
		if err := add(lkeVideoCloudAuxiliaryDeploymentManifest(env, worker)); err != nil {
			return nil, err
		}
		if worker.Port > 0 {
			if err := add(lkeVideoCloudAuxiliaryServiceManifest(env, worker)); err != nil {
				return nil, err
			}
		}
	}
	manifests := []string{
		lkePostgresInitManifest(env), lkePostgresServiceManifest(env), lkePostgresStatefulSetManifest(env),
		lkeRedisDeploymentManifest(env), lkeRedisServiceManifest(env), lkeRedisExporterDeploymentManifest(env), lkeRedisExporterServiceManifest(env),
		lkeFleetValkeyStatefulSetManifest(env), lkeFleetValkeyServiceManifest(env), lkeFleetValkeyExporterDeploymentManifest(env), lkeFleetValkeyExporterServiceManifest(env),
		lkeCertIssuerServiceManifest(env), lkeFactoryEnrollServiceManifest(env),
		lkeVideoCloudPrometheusConfigManifest(env, provisionOptions{}), lkeVideoCloudPrometheusDeploymentManifest(env, provisionOptions{}), lkeVideoCloudPrometheusServiceManifest(env),
		lkeLokiConfigManifest(env), lkeLokiDeploymentManifest(env), lkeLokiServiceManifest(env),
		lkeLogCollectorServiceAccountManifest(env), lkeLogCollectorClusterRoleManifest(env), lkeLogCollectorClusterRoleBindingManifest(env), lkeLogCollectorConfigManifest(env), lkeLogCollectorDaemonSetManifest(env),
		lkeGrafanaDatasourcesConfigManifest(env), lkeGrafanaDashboardProvidersConfigManifest(env), lkeGrafanaDashboardsConfigManifest(env), lkeGrafanaDeploymentManifest(env), lkeGrafanaServiceManifest(env),
		lkeBillingPaymentWorkerManifest(env), lkeAccountManagerEmailWorkerManifest(env), lkeAccountManagerOutboxWorkerManifest(env),
		lkeAllowPostgresClientsNetworkPolicyManifest(env), lkeAllowPostgresOpenBaoNetworkPolicyManifest(env), lkeAllowOpenBaoClientsNetworkPolicyManifest(env), lkeAllowAccountManagerCertIssuerNetworkPolicyManifest(env),
		lkeAllowCloudAdminFactoryEnrollNetworkPolicyManifest(env), lkeAllowVideoCloudAccountManagerNetworkPolicyManifest(env), lkeAllowServiceRegistrationNetworkPolicyManifest(env),
		lkeAllowCloudAdminAccountManagerNetworkPolicyManifest(env), lkeAllowCloudAdminBillingNetworkPolicyManifest(env), lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env),
		lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env), lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env), lkeAllowEMQXMQTTUsageNetworkPolicyManifest(env), lkeAllowEMQXClusterNetworkPolicyManifest(env),
		lkeAllowVideoCloudLoggerNetworkPolicyManifest(env), lkeAllowCloudLoggerLokiNetworkPolicyManifest(env), lkeAllowLogCollectorLokiNetworkPolicyManifest(env),
		lkeAllowRedisClientsNetworkPolicyManifest(env), lkeAllowFleetValkeyClientsNetworkPolicyManifest(env), lkeAllowPrometheusScrapeNetworkPolicyManifest(env),
		lkeAllowCloudAdminGrafanaNetworkPolicyManifest(env), lkeAllowGrafanaPrometheusNetworkPolicyManifest(env), lkeAllowPrometheusGrafanaNetworkPolicyManifest(env),
	}
	if lkeAccountManagerHandoffWorkerEnabled(env) {
		manifests = append(manifests, lkeAccountManagerHandoffWorkerManifest(env), lkeAccountManagerCloudDeletionWorkerManifest(env), lkeBillingSettlementCollectorManifest(env))
		manifests = append(manifests, lkeAccountManagerHandoffNetworkPolicyManifests(env)...)
	}
	if lkeMQTTFoundationRegistrationEnabled(env) {
		manifests = append(manifests, lkeMQTTFoundationDeploymentManifest(env), lkeMQTTFoundationServiceManifest(env))
	}
	if lkeShadowWorkerRegistrationEnabled(env) {
		manifests = append(manifests, lkeShadowWorkerDeploymentManifest(env), lkeShadowWorkerServiceManifest(env))
	}
	if lkeWebRTCServiceRegistrationEnabled(env) {
		manifests = append(manifests, lkeWebRTCServiceDeploymentManifest(env), lkeWebRTCServiceServiceManifest(env))
	}
	if lkeVideoStorageServiceRegistrationEnabled(env) {
		manifests = append(manifests, lkeVideoStorageServiceDeploymentManifest(env), lkeVideoStorageServiceServiceManifest(env))
	}
	if lkeOTAServiceRegistrationEnabled(env) {
		manifests = append(manifests, lkeOTAServiceDeploymentManifest(env), lkeOTAServiceServiceManifest(env))
	}
	if lkeShadowHTTPCoreCutoverEnabled(env) {
		manifests = append(manifests, lkeAllowVideoCloudAPIShadowGatewayNetworkPolicyManifest(env))
	}
	if lkeLoggerHTTPCoreCutoverEnabled(env) {
		manifests = append(manifests, lkeAllowVideoCloudAPILoggerNetworkPolicyManifest(env))
	}
	if lkeVideoStorageCoreCutoverEnabled(env) {
		manifests = append(manifests, lkeAllowVideoCloudAPIStorageGatewayNetworkPolicyManifest(env))
	}
	if lkeOTAServiceRegistrationEnabled(env) || lkeOTACoreCutoverEnabled(env) {
		manifests = append(manifests, lkeAllowVideoCloudAPIOTAGatewayNetworkPolicyManifest(env))
	}
	if lkeLoggerRetentionStorageEnabled(env) {
		manifests = append(manifests, lkeLokiPVCManifest(env))
	}
	if lkeGrafanaPersistenceEnabled(env) {
		manifests = append(manifests, lkeGrafanaPVCManifest(env))
	}
	if strings.EqualFold(env["TEST_LAB_ENABLED"], "true") {
		manifests = append(manifests, lkePaymentSimulatorDeploymentManifest(env), lkePaymentSimulatorServiceManifest(env), lkeAllowBillingPaymentSimulatorNetworkPolicyManifest(env))
	}
	for _, namespace := range lkeNamespaces(env) {
		manifests = append(manifests, lkeDefaultDenyIngressNetworkPolicyManifest(env, namespace.Name))
	}
	routes := lkePublicHTTPSBaseRoutes(env)
	// Planned routes must not depend on the availability of the old Service.
	if logger := lkeCloudLoggerPlannedRoute(env); logger.Host != "" {
		routes = append(routes, logger)
	}
	manifests = append(manifests, lkePublicHTTPSBridgeServiceManifests(env, routes)...)
	manifests = append(manifests, lkePublicHTTPSIngressManifests(env, routes)...)
	manifests = append(manifests, lkePublicHTTPSNetworkPolicyManifests(env, routes)...)
	for _, manifest := range manifests {
		if err := add(manifest); err != nil {
			return nil, err
		}
	}
	// PKI components use the same maintained skeleton in every environment.
	// A reference bundle cannot introduce an arbitrary new workload.
	definitions = append(definitions, environmentFullManagedConsumerDependencies(env)...)
	definitions = append(definitions, environmentNativeConnectivityDefinitions(env)...)
	// The unified compiler and scoped create stages share the same managed
	// MQTT producer; neither can invent TLS keys or absent process credentials.
	mqttSecrets := activeRuntimeSecretSnapshot
	if mqttSecrets == nil {
		mqttSecrets = map[string]string{}
		for _, name := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
			mqttSecrets[lkeSecretFileName(name)] = lkeRuntimeSecretValue(name)
		}
	}
	mqttDependencies, err := renderManagedMQTTDependencyDefinitions(env, mqttSecrets)
	if err != nil {
		return nil, err
	}
	definitions = append(definitions, mqttDependencies...)
	for _, binding := range bindings.Pods {
		id := binding.Kind + "/" + bindings.Stack + "-" + binding.NamespaceSuffix + "/" + binding.Name
		if seen[id] {
			continue
		}
		if binding.Component == "existing-workload" {
			return nil, &deploymentplan.Blocker{ID: "deployment.inventory.renderer", Resource: id, Cause: "existing workload has no enabled maintained renderer", Next: "add this workload to the common component catalog with its formal image roles and retained lifecycle references, or qualify explicit retirement; repeat preflight"}
		}
		if binding.Component == "managed-openbao" {
			if err := validateOpenBaoTargetProfile(env, binding); err != nil {
				return nil, err
			}
			for _, dependency := range environmentOpenBaoDependencies(env) {
				definitions = append(definitions, deploymentplan.Definition{Object: dependency, Phase: "configuration"})
			}
		}
		if binding.Component == "pki-controller" {
			definitions = append(definitions, environmentManagedControllerDependencies(env)...)
		}
		object, err := renderManagedEnvironmentComponent(env, binding)
		if err != nil {
			return nil, err
		}
		if err := applyEnvironmentPodBindings(object, bindings, release); err != nil {
			return nil, err
		}
		if err := applyEnvironmentAuthRole(env, object); err != nil {
			return nil, err
		}
		if err := applyEnvironmentIssuerRuntime(env, object, binding); err != nil {
			return nil, err
		}
		if binding.Component == "managed-app-api" {
			dependencies, err := environmentAppDependencies(env, bindings)
			if err != nil {
				return nil, err
			}
			definitions = append(definitions, dependencies...)
		}
		if binding.Component == "pki-controller" {
			if err := applyControllerIdentityPreparation(object, release); err != nil {
				return nil, err
			}
			controllerValues := env
			for _, managed := range bindings.Pods {
				if managed.Component == "managed-mqtt" || managed.Component == "managed-openbao" {
					ids, err := managedTransportServiceConsumerIDs(strings.Split(env["PKI_REQUIRED_CONSUMERS_SERVICE"], ","))
					if err != nil {
						return nil, err
					}
					controllerValues = appendMap(env, map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": strings.Join(ids, ",")})
					break
				}
			}
			if err := applyEnvironmentControllerPolicy(controllerValues, object); err != nil {
				return nil, err
			}
			if bindings.BootstrapCleanup != nil {
				dependencies, err := applyEnvironmentBootstrapCleanup(env, object, *bindings.BootstrapCleanup)
				if err != nil {
					return nil, err
				}
				definitions = append(definitions, dependencies...)
			}
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "workload"})
	}
	if intent == nil {
		ingress, err := renderEnvironmentIngress(env, bindings.IngressAdmission, release)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, ingress...)
	}
	for _, definition := range definitions {
		if err := applyAccountManagerServicePorts(definition.Object, bindings); err != nil {
			return nil, err
		}
		if err := applyEnvironmentRegistryIssuerService(definition.Object, bindings); err != nil {
			return nil, err
		}
		canonicalEnvironmentEmptyEnvLiterals(definition.Object)
	}
	if intent == nil {
		definitions, err = bindEnvironmentCertIssuerPublicTransport(env, definitions)
	} else {
		definitions, err = bindEnvironmentCreateDependencyPublicTransport(env, definitions, intent)
	}
	if err != nil {
		return nil, err
	}
	definitions, err = applyAccountRegistrationCRLRefreshProfile(env, definitions)
	if err != nil {
		return nil, err
	}
	if len(support) == 1 {
		definitions = append(definitions, support[0]...)
	}
	if lkeMQTTFoundationRegistrationEnabled(env) && intent == nil {
		if err := validateMQTTFoundationTarget(env, definitions); err != nil {
			return nil, err
		}
	}
	definitions, err = applyEnvironmentDurableStorage(definitions, env)
	if err != nil {
		return nil, err
	}
	if err := bindEnvironmentNewStorageClasses(definitions, env["DURABLE_DATA_STORAGE_CLASS"], bindings.Owners); err != nil {
		return nil, err
	}
	if err := deriveEnvironmentCoreMQTTTransport(env, definitions); err != nil {
		return nil, err
	}
	if err := bindEnvironmentManagedMQTTConsumersForRender(definitions, bindings.ProductIdentities); err != nil {
		return nil, err
	}
	if err := bindEnvironmentCurrentMQTTConsumers(definitions, bindings); err != nil {
		return nil, err
	}
	if err := applyEnvironmentBillingRuntimeConfiguration(env, definitions); err != nil {
		return nil, err
	}
	if err := applyCoreIdentityPreparation(definitions, release.Images["LKE_ALPINE_IMAGE"].Reference); err != nil {
		return nil, err
	}
	if err := validateEnvironmentAppTURNPolicy(definitions); err != nil {
		return nil, err
	}
	if err := deriveEnvironmentDeviceMQTTCallback(env, definitions); err != nil {
		return nil, err
	}
	if err := deriveEnvironmentLogMQTTTransport(env, definitions); err != nil {
		return nil, err
	}
	bindEnvironmentVideoStorageObjectStorage(definitions)
	if intent == nil {
		// Native endpoint projection belongs to execution after allocation; the
		// fresh intent pass checks references without inventing those outputs.
		if err := deriveEnvironmentOTAStartup(definitions); err != nil {
			return nil, err
		}
		if err := deriveEnvironmentVideoStorageStartup(env, definitions); err != nil {
			return nil, err
		}
	}
	// The native host consumes the same explicit listener intent before it binds
	// the registered HTTPS callback. A missing flag must not create a WS listener.
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) != "Deployment/"+env["CLOUD_STACK_NAME"]+"-video-cloud/mqtt-pki" {
			continue
		}
		container := managedNamedField(environmentPKIPod(d.Object)["containers"], "mqtt")
		if container != nil {
			enabled := "false"
			if lkeTestLabEnabled(env) {
				enabled = "true"
			}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"EMQX_LISTENERS__WS__TESTLAB__ENABLE": enabled, "EMQX_LISTENERS__WS__TESTLAB__ENABLE_AUTHN": "true"}))
		}
	}
	if err := pinEnvironmentResourceImages(definitions, release); err != nil {
		return nil, err
	}
	if err := validateEnvironmentOTARegistrationRelease(definitions, release); err != nil {
		return nil, err
	}
	sort.Slice(definitions, func(i, j int) bool {
		return deploymentplan.ID(definitions[i].Object) < deploymentplan.ID(definitions[j].Object)
	})
	return definitions, nil
}

func canonicalEnvironmentEmptyEnvLiterals(object deploymentplan.Object) {
	switch object["kind"] {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
	default:
		return
	}
	for _, group := range []string{"containers", "initContainers"} {
		for _, value := range certIssuerObjectList(environmentZammadPod(object)[group]) {
			container := deploymentplan.Map(value)
			settings := append([]any(nil), certIssuerObjectList(container["env"])...)
			changed := false
			for index, setting := range settings {
				entry := deploymentplan.Map(setting)
				literal, explicit := entry["value"].(string)
				if !explicit || literal != "" || entry["valueFrom"] != nil {
					continue
				}
				// The typed API omits an empty literal. Keep both keys so admission
				// cannot replace the empty override with a value or credential source.
				entry = maps.Clone(entry)
				entry["value"] = nil
				if _, declared := entry["valueFrom"]; !declared {
					entry["valueFrom"] = nil
				}
				settings[index], changed = entry, true
			}
			if changed {
				container["env"] = settings
			}
		}
	}
}

func environmentResourcePhase(kind string) string {
	switch kind {
	case "Namespace":
		return "namespace"
	case "PersistentVolumeClaim":
		return "storage"
	case "Deployment", "StatefulSet", "DaemonSet":
		return "workload"
	case "Job":
		return "migration"
	case "Ingress":
		return "routing"
	case "CronJob":
		return "workload"
	default:
		return "configuration"
	}
}

func renderManagedEnvironmentComponent(env map[string]string, binding environmentPodBindings) (deploymentplan.Object, error) {
	if environmentManagedComponents[binding.Name] == "" || environmentManagedComponents[binding.Name] != binding.Component {
		return nil, errors.New("deployment.inventory: unknown managed PKI component")
	}
	if binding.Name == "openbao" {
		return renderEnvironmentOpenBao(env, binding)
	}
	if binding.Kind != "Deployment" || binding.NamespaceSuffix != "video-cloud" {
		return nil, errors.New("deployment.bindings: managed service has an invalid resource identity")
	}
	if binding.Component == "managed-api" || binding.Component == "managed-app-api" {
		return renderManagedEnvironmentAPI(env, binding)
	}
	if binding.Component == "managed-turn" {
		return renderEnvironmentTurn(env, binding)
	}
	labels := map[string]any{"app.kubernetes.io/name": binding.Name, "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}
	containers := []any{}
	if len(binding.Containers) == 0 {
		return nil, errors.New("deployment.bindings: managed component has no primary consumer")
	}
	for i, container := range binding.Containers {
		var desired map[string]any
		if i > 0 {
			var err error
			desired, err = environmentManagedSidecar(container.Name)
			if err != nil {
				return nil, err
			}
		} else {
			binary, port := binding.Name, 0
			switch binding.Component {
			case "certissuer", "service-issuer":
				binary, port = "certissuer", lkeResolveCertIssuerTLSConfig(env).HTTPSPort
				if binding.Name == "factoryenroll" {
					binary, port = "factoryenroll", 18443
				}
			case "pki-controller":
				binary, port = "pkicontroller", 18446
			case "managed-api":
				binary, port = "api", 8443
			case "managed-mqtt":
				binary, port = "", 8883
			}
			desired = map[string]any{"name": container.Name, "resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]any{"memory": "512Mi"}}, "ports": []any{map[string]any{"name": "tls", "containerPort": float64(port)}}, "readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": "tls"}, "periodSeconds": float64(5)}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
			if binary != "" {
				desired["command"] = []any{"/app/" + binary}
			}
		}
		containers = append(containers, desired)
	}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": lkeNamespaceName(env, "video-cloud"), "name": binding.Name}, "spec": map[string]any{"replicas": float64(1), "strategy": map[string]any{"type": "Recreate"}, "selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": binding.Name}}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"containers": containers}}}}
	if binding.Component == "pki-controller" {
		applyManagedControllerDefaults(env, object)
	}
	if binding.Component == "managed-mqtt" {
		if err := applyManagedBrokerDefaults(env, object); err != nil {
			return nil, err
		}
		var initializers []any
		for _, bound := range binding.InitContainers {
			c, err := maintainedMQTTInitializer(bound.Name)
			if err != nil {
				return nil, mqttInitializerBlocker(deploymentplan.ID(object), bound.Name)
			}
			c["image"] = env["LKE_VIDEO_CLOUD_IMAGE"]
			initializers = append(initializers, c)
		}
		if len(initializers) > 0 {
			environmentPKIPod(object)["initContainers"] = initializers
		}
	}
	return object, nil
}

// Every container, including provider and init containers, must be release pinned.
func pinEnvironmentResourceImages(definitions []deploymentplan.Definition, release environmentRelease) error {
	refs := map[string]string{}
	ambiguous := map[string]bool{}
	selectedRefs := map[string]bool{}
	for key, image := range release.Images {
		selectedRefs[image.Reference] = true
		if strings.HasPrefix(key, "LKE_ZAMMAD_") {
			continue
		}
		name := environmentImageRepository(image.Reference)
		if previous := refs[name]; previous != "" && previous != image.Reference {
			ambiguous[name] = true
		}
		refs[name] = image.Reference
	}
	for _, definition := range definitions {
		spec := deploymentplan.Map(definition.Object["spec"])
		if definition.Object["kind"] == "CronJob" {
			spec = deploymentplan.Map(deploymentplan.Map(spec["jobTemplate"])["spec"])
		}
		pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
		for _, field := range []string{"containers", "initContainers"} {
			for _, value := range certIssuerObjectList(pod[field]) {
				container := certIssuerObjectMap(value)
				image := deploymentplan.String(container["image"])
				name := environmentImageRepository(image)
				selected := refs[name]
				if deploymentplan.Map(definition.Object["metadata"])["namespace"] == release.Stack+"-support" {
					selected = release.Images[zammadImageRole(image)].Reference
				} else if selectedRefs[image] {
					selected = image
				} else if ambiguous[name] {
					return &deploymentplan.Blocker{ID: "deployment.release.image-role", Resource: deploymentplan.ID(definition.Object), Cause: "container " + deploymentplan.String(container["name"]) + " matches multiple selected image roles with different digests", Next: "assign one maintained release image role to this container; repeat preflight"}
				}
				if selected == "" {
					return &deploymentplan.Blocker{ID: "deployment.release.image-role", Resource: deploymentplan.ID(definition.Object), Cause: "container " + deploymentplan.String(container["name"]) + " lacks an immutable selected image", Next: "publish and select the exact formal CI image for this container's maintained role; repeat preflight"}
				}
				container["image"] = selected
			}
		}
	}
	return nil
}

// Tags follow the last slash; registry ports must never be stripped. Docker
// Hub's implicit library and registry spellings denote the same repository.
func environmentImageRepository(reference string) string {
	name, _, _ := strings.Cut(reference, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name = name[:colon]
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "index.docker.io/"), "docker.io/")
	if !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return name
}

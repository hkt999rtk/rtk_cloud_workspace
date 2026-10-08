package main

import (
	"encoding/json"
	"errors"
	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"unicode"
)

var managedPKIDomains = []string{"device", "app", "service", "mqtt", "openbao_tls"}

func environmentControllerPolicySetting(name string) bool {
	return environmentAuthRuntimeKey(name) || name == "PKI_REQUIRED_CONSUMERS" || strings.HasPrefix(name, "PKI_REQUIRED_CONSUMERS_") || strings.HasPrefix(name, "PKI_REQUIRED_BUNDLE_CONSUMERS_") || name == "PKI_APP_CRL_WORKER_ENABLED" || name == "PKI_SERVER_CRL_DOMAIN" || name == "PKI_REQUIRE_USER_MFA" || strings.HasPrefix(name, "PKI_BOOTSTRAP_CLEANUP") || strings.HasPrefix(name, "PKI_BOOTSTRAP_KUBERNETES")
}

// Process policies belong to maintained settings, not live workload adoption.
// These existing runtime settings keep all managed trust domains refreshable.
func applyEnvironmentControllerPolicy(env map[string]string, object deploymentplan.Object) error {
	block := func(cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.pki.controller-policy", Resource: deploymentplan.ID(object), Cause: cause, Next: "maintain exact domain consumer and bundle-consumer receipt IDs in cloud_deploy/architectures/kubernetes/architecture.env; maintain explicit MFA and CRL worker policies, then validate actual consumer acknowledgments before full preflight"}
	}
	if env["PKI_REQUIRED_CONSUMERS"] != "" {
		return block("global controller consumer policy is retired for complete deployment; explicit domain policies are required")
	}
	values := map[string]string{"PKI_REQUIRED_CONSUMERS": "", "PKI_APP_CRL_WORKER_ENABLED": env["PKI_APP_CRL_WORKER_ENABLED"], "PKI_SERVER_CRL_DOMAIN": "service,mqtt,openbao_tls", "PKI_REQUIRE_USER_MFA": env["PKI_REQUIRE_USER_MFA"]}
	domains := map[string]bool{}
	for _, domain := range strings.Split(env["PKI_SERVER_CRL_DOMAIN"], ",") {
		domain = strings.TrimSpace(domain)
		if domain != "service" && domain != "mqtt" && domain != "openbao_tls" || domains[domain] {
			return block("server CRL refresh must select distinct Service, MQTT and OpenBao transport domains")
		}
		domains[domain] = true
	}
	if values["PKI_APP_CRL_WORKER_ENABLED"] != "true" || len(domains) != 3 {
		return block("complete managed PKI requires the App CRL worker and Service, MQTT and OpenBao transport CRL refresh workers")
	}
	if values["PKI_REQUIRE_USER_MFA"] != "true" && values["PKI_REQUIRE_USER_MFA"] != "false" || (env["DEPLOYMENT_PROTECTION_PROFILE"] == "production" || env["CLOUD_ENV_NAME"] == "prod") && values["PKI_REQUIRE_USER_MFA"] != "true" {
		return block("controller authentication policy must be explicit; production requires MFA")
	}
	for _, domain := range managedPKIDomains {
		for _, prefix := range []string{"PKI_REQUIRED_CONSUMERS_", "PKI_REQUIRED_BUNDLE_CONSUMERS_"} {
			name := prefix + strings.ToUpper(domain)
			raw := env[name]
			if raw == "" && prefix == "PKI_REQUIRED_BUNDLE_CONSUMERS_" {
				values[name] = ""
				continue // Native controller uses the domain consumer list.
			}
			ids, seen := strings.Split(raw, ","), map[string]bool{}
			if len(ids) > 128 {
				return block(name + " exceeds the native consumer policy bound")
			}
			for i, id := range ids {
				id = strings.TrimSpace(id)
				if id == "" || len(id) > 128 || seen[id] || strings.HasPrefix(id, "service:") || strings.ContainsAny(id, ",/\\") || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
					return block(name + " requires distinct unprefixed receipt IDs")
				}
				ids[i], seen[id] = id, true
			}
			values[name] = strings.Join(ids, ",")
		}
	}
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	settings := []any{}
	for _, name := range sortedMapKeys(values) {
		settings = append(settings, deploymentplan.Object{"name": name, "value": values[name]})
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], settings)
	return nil
}

// Reuse maintained ordinary API configuration. Managed identity references are
// applied separately; no online Pod or old private desired spec is a template.
func renderManagedEnvironmentAPI(env map[string]string, binding environmentPodBindings) (deploymentplan.Object, error) {
	if len(binding.Containers) == 0 || binding.Containers[0].Name != "app" {
		return nil, errors.New("deployment.bindings: managed API primary container must keep its catalog app identity")
	}
	workload := k8sWorkloads(env)[0]
	var object deploymentplan.Object
	if yaml.Unmarshal([]byte(lkeDeploymentManifest(env, workload, nil)), &object) != nil {
		return nil, errors.New("deployment.renderer: maintained API configuration is invalid")
	}
	raw, err := json.Marshal(object)
	if err != nil || json.Unmarshal(raw, &object) != nil {
		return nil, errors.New("deployment.renderer: maintained API configuration cannot be normalized")
	}
	deploymentplan.Map(object["metadata"])["name"] = binding.Name
	spec := deploymentplan.Map(object["spec"])
	labels := map[string]any{"app.kubernetes.io/name": binding.Name, "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}
	spec["selector"] = map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": binding.Name}}
	template := deploymentplan.Map(spec["template"])
	deploymentplan.Map(template["metadata"])["labels"] = labels
	pod := deploymentplan.Map(template["spec"])
	pod["automountServiceAccountToken"] = false
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	container["ports"] = []any{map[string]any{"name": "https", "containerPort": float64(8443)}, map[string]any{"name": "mqtt-auth", "containerPort": float64(18447)}}
	container["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": "https"}, "periodSeconds": float64(5)}
	delete(container, "livenessProbe")
	container["env"] = mergeEnvironmentNamedFields(container["env"], []any{
		map[string]any{"name": "VIDEO_CLOUD_ENV", "value": env["CLOUD_ENV_NAME"]},
		map[string]any{"name": "VIDEO_CLOUD_API_ADDR", "value": "0.0.0.0:8443"},
		map[string]any{"name": "VIDEO_CLOUD_API_BASE_URL", "value": "https://" + binding.Name + "." + lkeNamespaceName(env, "video-cloud") + ".svc:8443"},
		map[string]any{"name": "VIDEO_CLOUD_AUTH_DISABLE_ACL", "value": "false"},
		map[string]any{"name": "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS", "value": "false"},
		map[string]any{"name": "VIDEO_CLOUD_AUTH_ENABLE_LEGACY_CERT", "value": "false"},
		map[string]any{"name": "VIDEO_CLOUD_DB_ENSURE_SCHEMA", "value": "false"},
		map[string]any{"name": "VIDEO_CLOUD_MQTT_AUTH_ADDR", "value": ":18447"},
		map[string]any{"name": "VIDEO_CLOUD_API_TLS_CERT_PATH", "value": "/run/pki-server/tls.crt"},
		map[string]any{"name": "VIDEO_CLOUD_API_TLS_KEY_PATH", "value": "/run/pki-server/tls.key"},
	})
	pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{map[string]any{"name": "pki-server", "secret": map[string]any{"secretName": "video-cloud-api-pki-tls", "defaultMode": float64(288)}}})
	container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{map[string]any{"name": "pki-server", "mountPath": "/run/pki-server", "readOnly": true}})
	for _, value := range certIssuerObjectList(pod["topologySpreadConstraints"]) {
		deploymentplan.Map(value)["labelSelector"] = map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": binding.Name}}
	}
	if binding.Component == "managed-api" {
		var initializers []any
		for _, bound := range binding.InitContainers {
			c, err := maintainedDeviceInitializer(bound.Name, binding.DeviceTrustStateFile)
			if err != nil {
				return nil, err
			}
			c["image"] = env["LKE_VIDEO_CLOUD_IMAGE"]
			initializers = append(initializers, c)
		}
		if len(initializers) > 0 {
			pod["initContainers"] = initializers
		}
	}
	if binding.Component == "managed-app-api" {
		if err := validateEnvironmentAppBinding(binding); err != nil {
			return nil, err
		}
		pod["securityContext"] = map[string]any{"fsGroup": float64(10001), "fsGroupChangePolicy": "OnRootMismatch"}
		spec["strategy"] = map[string]any{"type": "Recreate"}
		container["ports"] = []any{map[string]any{"name": "https", "containerPort": float64(8443)}}
		container["env"] = mergeEnvironmentNamedFields(container["env"], []any{map[string]any{"name": "VIDEO_CLOUD_MQTT_ENABLED", "value": "false"}, map[string]any{"name": "VIDEO_CLOUD_MQTT_AUTH_ADDR", "value": ""}})
		pod["initContainers"] = []any{map[string]any{"name": "initialize-app-trust-state", "command": []any{"/bin/sh", "-ec", environmentAppTrustInitialize}, "volumeMounts": []any{map[string]any{"name": "pki-state", "mountPath": "/run/pki-state"}, map[string]any{"name": "pki-app", "mountPath": "/run/pki-app", "readOnly": true}}}}
	}
	if err := applyEnvironmentAuthRole(env, object); err != nil {
		return nil, err
	}
	// Optional maintained sidecars are added by the typed binding adapter.
	return object, nil
}

func applyManagedBrokerDefaults(env map[string]string, object deploymentplan.Object) error {
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 2 || deploymentplan.Map(containers[0])["name"] != "mqtt" || deploymentplan.Map(containers[1])["name"] != "pkibroker" {
		return errors.New("deployment.inventory: managed MQTT requires its maintained mqtt and pkibroker consumers")
	}
	pod["automountServiceAccountToken"] = false
	pod["securityContext"] = map[string]any{"fsGroup": float64(1000), "fsGroupChangePolicy": "OnRootMismatch"}
	pod["imagePullSecrets"] = []any{map[string]any{"name": "ghcr-pull"}}
	mqtt, broker := deploymentplan.Map(containers[0]), deploymentplan.Map(containers[1])
	mqtt["ports"] = []any{map[string]any{"name": "mqtts", "containerPort": float64(8883)}}
	mqtt["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": "mqtts"}, "initialDelaySeconds": float64(10), "periodSeconds": float64(5)}
	mqtt["env"] = []any{
		map[string]any{"name": "PKI_ENVIRONMENT", "value": env["CLOUD_ENV_NAME"]},
		map[string]any{"name": "EMQX_NODE__NAME", "value": "emqx@127.0.0.1"},
		map[string]any{"name": "EMQX_PKI_EXECUTABLE", "value": "/usr/local/bin/emqx"},
		map[string]any{"name": "EMQX_PKI_RUNTIME_DIR", "value": "/run/emqx-pki/private"},
		map[string]any{"name": "EMQX_PKI_HOST_IDENTITY_STATE", "value": "/var/lib/emqx-pki/identity/state.json"},
		map[string]any{"name": "EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE", "value": "/var/lib/emqx-pki/identity/service-client.json"},
	}
	credentials := map[string]string{"EMQX_NODE__COOKIE": "cookie", "EMQX_DASHBOARD__DEFAULT_PASSWORD": "dashboard-password", "EMQX_LISTENERS__SSL__DEFAULT__AUTHENTICATION": "authentication"}
	for _, name := range sortedMapKeys(credentials) {
		mqtt["env"] = append(mqtt["env"].([]any), map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "mqtt-pki-runtime", "key": credentials[name]}}})
	}
	database := map[string]any{"name": "PKI_DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "mqtt-pki-database", "key": "url"}}}
	mqtt["env"] = append(mqtt["env"].([]any), database)
	broker["env"] = []any{database, map[string]any{"name": "PKI_ENVIRONMENT", "value": env["CLOUD_ENV_NAME"]}, map[string]any{"name": "PKI_BROKER_API_URL", "value": "http://127.0.0.1:18083/api/v5"}}
	broker["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": "mqtt-pki-worker"}}}
	volumes := []any{
		map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "mqtt-pki-data"}},
		map[string]any{"name": "pki-state", "persistentVolumeClaim": map[string]any{"claimName": "mqtt-pki-trust"}},
		map[string]any{"name": "mqtt-host-state", "persistentVolumeClaim": map[string]any{"claimName": "mqtt-pki-host-identity"}},
		map[string]any{"name": "config", "configMap": map[string]any{"name": "mqtt-pki-config"}},
		map[string]any{"name": "runtime", "secret": map[string]any{"secretName": "mqtt-pki-runtime"}},
		map[string]any{"name": "mqtt-host-runtime", "emptyDir": map[string]any{}},
	}
	pod["volumes"] = volumes
	mqtt["volumeMounts"] = []any{map[string]any{"name": "data", "mountPath": "/opt/emqx/data"}, map[string]any{"name": "config", "mountPath": "/opt/emqx/etc/base.hocon", "readOnly": true, "subPath": "base.hocon"}, map[string]any{"name": "runtime", "mountPath": "/run/mqtt-pki", "readOnly": true}, map[string]any{"name": "mqtt-host-state", "mountPath": "/var/lib/emqx-pki"}, map[string]any{"name": "mqtt-host-runtime", "mountPath": "/run/emqx-pki"}}
	broker["volumeMounts"] = []any{map[string]any{"name": "pki-state", "mountPath": "/run/pki-state"}}
	return nil
}

func applyManagedControllerDefaults(env map[string]string, object deploymentplan.Object) {
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	values := map[string]string{"PKI_ENVIRONMENT": env["CLOUD_ENV_NAME"], "PKI_LISTEN_ADDR": "0.0.0.0:18446", "PKI_SERVER_CERT": "/run/pki-tls/tls.crt", "PKI_SERVER_KEY": "/run/pki-tls/tls.key", "PKI_CLIENT_CA": "/run/pki-tls/ca.crt", "PKI_ACCOUNT_MANAGER_PUBLIC_KEY": "/run/account-key/public-key.pem", "OPENBAO_ADDR": "https://openbao." + lkeNamespaceName(env, "secrets") + ".svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt", "OPENBAO_AUTH_METHOD": "kubernetes", "OPENBAO_KUBERNETES_ROLE": "pki-controller-" + env["CLOUD_ENV_NAME"], "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": "/run/openbao-auth/token"}
	settings := []any{map[string]any{"name": "PKI_DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "pki-controller-database", "key": "url"}}}}
	// Preserve deterministic API input ordering and plan hashes.
	for _, name := range sortedMapKeys(values) {
		settings = append(settings, map[string]any{"name": name, "value": values[name]})
	}
	container["env"] = settings
	container["ports"] = []any{map[string]any{"name": "https", "containerPort": float64(18446)}}
	container["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": "https"}, "periodSeconds": float64(10)}
	pod["serviceAccountName"], pod["automountServiceAccountToken"] = "pki-controller", false
	pod["imagePullSecrets"] = []any{map[string]any{"name": "ghcr-pull"}}
	pod["securityContext"] = map[string]any{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001), "seccompProfile": map[string]any{"type": "RuntimeDefault"}}
	volumes := []any{
		map[string]any{"name": "pki-tls", "secret": map[string]any{"secretName": "pki-controller-tls", "defaultMode": float64(288)}},
		map[string]any{"name": "account-key", "configMap": map[string]any{"name": "pki-account-manager-public-key"}},
		map[string]any{"name": "openbao-ca", "configMap": map[string]any{"name": "pki-openbao-transport-ca"}},
		map[string]any{"name": "openbao-auth", "projected": map[string]any{"defaultMode": float64(288), "sources": []any{map[string]any{"serviceAccountToken": map[string]any{"audience": "openbao", "expirationSeconds": float64(3600), "path": "token"}}}}},
	}
	pod["volumes"] = volumes
	container["volumeMounts"] = []any{map[string]any{"name": "pki-tls", "mountPath": "/run/pki-tls", "readOnly": true}, map[string]any{"name": "account-key", "mountPath": "/run/account-key", "readOnly": true}, map[string]any{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}, map[string]any{"name": "openbao-auth", "mountPath": "/run/openbao-auth", "readOnly": true}}
}

func environmentManagedControllerDependencies(env map[string]string) []deploymentplan.Definition {
	namespace := lkeNamespaceName(env, "video-cloud")
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": namespace} }
	selector := map[string]any{"app.kubernetes.io/name": "pki-controller"}
	return []deploymentplan.Definition{
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta("pki-controller"), "automountServiceAccountToken": false}, Phase: "configuration"},
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": meta("pki-controller"), "spec": map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "https", "port": float64(18446), "targetPort": "https"}}}}, Phase: "configuration"},
		{Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("allow-pki-controller"), "spec": map[string]any{"podSelector": map[string]any{"matchLabels": selector}, "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": lkeNamespaceName(env, "account-manager")}}, "podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "account-manager"}}}, map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}}, "podSelector": map[string]any{"matchLabels": map[string]any{"rtk.cloud/pki-consumer": "true"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(18446)}}}}}}, Phase: "configuration"},
	}
}

// These are desired network resources shared by all environments. In particular,
// the private MQTT authentication listener is reachable only from the managed
// broker Pod, never from the public HTTPS ingress namespace.
func environmentManagedConsumerDependencies(env map[string]string) []deploymentplan.Definition {
	namespace := lkeNamespaceName(env, "video-cloud")
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": namespace} }
	selector := func(name string) map[string]any {
		return map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": name}}
	}
	ingress := map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": lkeNamespaceName(env, "ingress")}}}
	broker := map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": namespace}}, "podSelector": selector("mqtt-pki")}
	var definitions []deploymentplan.Definition
	for _, consumer := range []struct {
		name  string
		ports []any
		rules []any
	}{
		{"video-cloud-api-pki", []any{map[string]any{"name": "https", "port": float64(8443), "targetPort": "https"}, map[string]any{"name": "mqtt-auth", "port": float64(18447), "targetPort": "mqtt-auth"}}, []any{map[string]any{"from": []any{ingress}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(8443)}}}, map[string]any{"from": []any{broker}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(18447)}}}}},
		{"mqtt-pki", []any{map[string]any{"name": "mqtts", "port": float64(8883), "targetPort": "mqtts"}}, []any{map[string]any{"from": []any{ingress}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(8883)}}}}},
	} {
		definitions = append(definitions,
			deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": meta(consumer.name), "spec": map[string]any{"type": "ClusterIP", "selector": selector(consumer.name)["matchLabels"], "ports": consumer.ports}}, Phase: "configuration"},
			deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("allow-" + consumer.name), "spec": map[string]any{"podSelector": selector(consumer.name), "policyTypes": []any{"Ingress"}, "ingress": consumer.rules}}, Phase: "configuration"},
		)
	}
	return definitions
}

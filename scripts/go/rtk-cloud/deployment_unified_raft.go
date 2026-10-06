package main

import (
	"errors"
	"fmt"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Raft is a common profile; production capacity and qualification require three
// distinct nodes. File-storage adoption needs a matched, independent migration.
func renderEnvironmentOpenBaoRaft(env map[string]string, binding environmentPodBindings) (deploymentplan.Object, error) {
	if binding.Kind != "StatefulSet" || binding.NamespaceSuffix != "secrets" {
		return nil, errors.New("deployment.bindings: OpenBao requires its secrets StatefulSet identity")
	}
	replicas, err := positiveIntValue("OPENBAO_EFFECTIVE_REPLICAS", env["OPENBAO_EFFECTIVE_REPLICAS"])
	if err != nil || replicas != 1 && replicas != 3 || (env["DEPLOYMENT_PROTECTION_PROFILE"] == "production" || env["CLOUD_ENV_NAME"] == "prod") && replicas != 3 {
		return nil, &deploymentplan.Blocker{ID: "deployment.target.openbao", Cause: "OpenBao capacity must explicitly select one or three Raft members; production requires three", Next: "maintain OPENBAO_MIN_REPLICAS and the production protection profile, then recompute shared node capacity; no renderer may silently override it"}
	}
	name, namespace := "openbao", lkeNamespaceName(env, "secrets")
	labels := map[string]any{"app.kubernetes.io/name": name, "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}
	match := map[string]any{"app.kubernetes.io/name": name}
	claim := func(name, size string) any {
		return map[string]any{"metadata": map[string]any{"name": name}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": size}}}}
	}
	container := map[string]any{"name": "openbao", "image": env["LKE_OPENBAO_IMAGE"], "command": []any{"bao", "server", "-config=/openbao/config/raft.hcl"}, "env": []any{map[string]any{"name": "BAO_CACERT", "value": "/openbao/transport/ca.crt"}, map[string]any{"name": "BAO_TLS_SERVER_NAME", "value": "openbao-internal"}}, "ports": []any{map[string]any{"name": "api", "containerPort": float64(8200)}, map[string]any{"name": "cluster", "containerPort": float64(8201)}}, "resources": map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "512Mi"}, "limits": map[string]any{"memory": "2Gi"}}, "volumeMounts": []any{map[string]any{"name": "data", "mountPath": "/openbao/data"}, map[string]any{"name": "audit", "mountPath": "/openbao/audit"}, map[string]any{"name": "raft-config", "mountPath": "/openbao/config", "readOnly": true}, map[string]any{"name": "transport-tls", "mountPath": "/openbao/transport", "readOnly": true}}, "readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"bao", "status", "-address=https://127.0.0.1:8200"}}, "periodSeconds": float64(10)}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
	// Kubernetes expands only earlier environment entries. Stable pod DNS and
	// node identity prevent address autodetection and accidental peer identity
	// changes when a StatefulSet pod moves to a different node.
	container["env"] = append([]any{
		map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}},
		map[string]any{"name": "BAO_RAFT_NODE_ID", "value": "$(POD_NAME)"},
		map[string]any{"name": "BAO_API_ADDR", "value": "https://$(POD_NAME).openbao-internal." + namespace + ".svc:8200"},
		map[string]any{"name": "BAO_CLUSTER_ADDR", "value": "https://$(POD_NAME).openbao-internal." + namespace + ".svc:8201"},
	}, container["env"].([]any)...)
	for field, value := range map[string]string{"cpu": env["OPENBAO_REQUEST_CPU"], "memory": env["OPENBAO_REQUEST_MEMORY"]} {
		if value == "" {
			return nil, errors.New("deployment.target.openbao: shared CPU and memory requests are required")
		}
		container["resources"].(map[string]any)["requests"].(map[string]any)[field] = value
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": map[string]any{"replicas": float64(replicas), "serviceName": "openbao-internal", "podManagementPolicy": "Parallel", "selector": map[string]any{"matchLabels": match}, "updateStrategy": map[string]any{"type": "RollingUpdate"}, "volumeClaimTemplates": []any{claim("data", "20Gi"), claim("audit", "10Gi")}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"serviceAccountName": "openbao", "automountServiceAccountToken": false, "nodeSelector": map[string]any{firstNonEmpty(env["NODE_CLASS_LABEL_KEY"], "rtk.io/node-class"): env["OPENBAO_NODE_CLASS"]}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": float64(100), "runAsGroup": float64(1000), "fsGroup": float64(1000)}, "affinity": map[string]any{"podAntiAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"labelSelector": map[string]any{"matchLabels": match}, "topologyKey": "kubernetes.io/hostname"}}}}, "containers": []any{container}, "volumes": []any{map[string]any{"name": "raft-config", "configMap": map[string]any{"name": "openbao-raft"}}, map[string]any{"name": "transport-tls", "secret": map[string]any{"secretName": "openbao-transport-tls", "defaultMode": float64(288)}}}}}}}, nil
}

func environmentOpenBaoRaftDependencies(env map[string]string) []deploymentplan.Object {
	namespace := lkeNamespaceName(env, "secrets")
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": namespace} }
	config := `ui = false
listener "tcp" {
  address = "[::]:8200"
  cluster_address = "[::]:8201"
  tls_cert_file = "/openbao/transport/tls.crt"
  tls_key_file = "/openbao/transport/tls.key"
  tls_min_version = "tls12"
}
storage "raft" {
  path = "/openbao/data"
`
	for i := 0; i < envIntFrom(env, "OPENBAO_EFFECTIVE_REPLICAS", 1); i++ {
		config += fmt.Sprintf("  retry_join {\n    leader_api_addr = \"https://openbao-%d.openbao-internal.%s.svc:8200\"\n    leader_ca_cert_file = \"/openbao/transport/ca.crt\"\n    leader_tls_servername = \"openbao-internal\"\n  }\n", i, namespace)
	}
	config += "}\ndisable_mlock = true\n"
	// Declarative audit configuration is identical on every peer and is restored
	// on startup. Audit records stay on their own persistent claim; raw secret
	// logging and API-created audit devices are not enabled.
	config += `audit "file" "deployment-audit" {
  options {
    file_path = "/openbao/audit/audit.jsonl"
    log_raw = "false"
  }
}
`
	match := map[string]any{"app.kubernetes.io/name": "openbao"}
	service := func(name string, headless bool) deploymentplan.Object {
		spec := map[string]any{"selector": match, "ports": []any{map[string]any{"name": "api", "port": float64(8200)}, map[string]any{"name": "cluster", "port": float64(8201)}}}
		if headless {
			spec["clusterIP"] = "None"
			spec["publishNotReadyAddresses"] = true
		}
		return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": meta(name), "spec": spec}
	}
	return []deploymentplan.Object{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("openbao-raft"), "data": map[string]any{"raft.hcl": config}},
		{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta("openbao"), "automountServiceAccountToken": false},
		service("openbao", false), service("openbao-internal", true),
		{"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": meta("openbao"), "spec": map[string]any{"maxUnavailable": float64(envIntFrom(env, "OPENBAO_EFFECTIVE_REPLICAS", 1) / 3), "selector": map[string]any{"matchLabels": match}}},
		{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("allow-openbao-raft-peers"), "spec": map[string]any{"podSelector": map[string]any{"matchLabels": match}, "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": match}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(8200)}, map[string]any{"protocol": "TCP", "port": float64(8201)}}}}}},
	}
}

func validateOpenBaoBindingProfile(binding environmentPodBindings) error {
	// Binding adoption must not restore the old file-storage command over Raft.
	for _, container := range binding.Containers {
		for _, value := range certIssuerObjectList(container.Fields["env"]) {
			name := deploymentplan.String(deploymentplan.Map(value)["name"])
			if name == "POD_NAME" || strings.HasPrefix(name, "BAO_") || strings.HasPrefix(name, "VAULT_") {
				return errors.New("deployment.migration.openbao: Raft node identity, transport and address settings are renderer-owned")
			}
		}
		for _, value := range certIssuerObjectList(container.Fields["volumeMounts"]) {
			mount := deploymentplan.Map(value)
			if strings.HasPrefix(deploymentplan.String(mount["mountPath"]), "/openbao") || slicesContainsRaftMount(deploymentplan.String(mount["name"])) {
				return errors.New("deployment.migration.openbao: Raft storage, audit, configuration and transport mounts are renderer-owned")
			}
		}
		for _, field := range []string{"command", "args"} {
			if len(certIssuerObjectList(container.Fields[field])) > 0 {
				return errors.New("deployment.migration.openbao: retire file-storage launch overrides after a verified matched Raft migration")
			}
		}
	}
	for _, volume := range certIssuerObjectList(binding.PodFields["volumes"]) {
		name := deploymentplan.String(deploymentplan.Map(volume)["name"])
		if strings.HasPrefix(name, "data") || name == "audit" || name == "raft-config" || name == "transport-tls" {
			return errors.New("deployment.migration.openbao: Raft-owned storage and transport references cannot be overridden")
		}
	}
	return nil
}

func slicesContainsRaftMount(name string) bool {
	return name == "data" || name == "audit" || name == "raft-config" || name == "transport-tls"
}

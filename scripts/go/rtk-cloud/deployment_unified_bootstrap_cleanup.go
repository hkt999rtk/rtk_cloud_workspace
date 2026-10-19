package main

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentBootstrapCleanupBinding struct {
	Version       int    `json:"schema_version"`
	Environment   string `json:"environment"`
	SessionID     string `json:"session_id"`
	NamespaceUID  string `json:"namespace_uid"`
	SecretUID     string `json:"secret_uid"`
	PolicySHA256  string `json:"policy_sha256"`
	KubernetesCA  string `json:"kubernetes_ca_bundle"`
	ConfigMapName string `json:"config_map_name,omitempty"`
}

var environmentBootstrapOwnerUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// Only fixed validation messages cross the sanitized report/log boundary.
func environmentBootstrapCleanupBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.bindings.bootstrap", Cause: cause, Next: "restore the selected environment and original bootstrap session, resource UIDs and Kubernetes CA binding; preserve existing identities and execution receipts"}
}

func validateEnvironmentBootstrapCleanupBinding(b *environmentBootstrapCleanupBinding, environment string) error {
	if b == nil {
		return nil
	}
	if b.Version != 1 || b.Environment != environment || len(b.PolicySHA256) != 64 || strings.Trim(b.PolicySHA256, "0123456789abcdef") != "" {
		return environmentBootstrapCleanupBlocker("exact bootstrap authorization owner required")
	}
	for _, id := range []string{b.SessionID, b.NamespaceUID, b.SecretUID} {
		if !environmentBootstrapOwnerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return environmentBootstrapCleanupBlocker("original canonical session and resource UIDs required")
		}
	}
	if b.ConfigMapName != "" && b.ConfigMapName != "pki-bootstrap-cleanup-binding-"+b.SessionID[:12] {
		return environmentBootstrapCleanupBlocker("cleanup map must belong to the exact renewed session")
	}
	if err := validateEnvironmentKubernetesCertificates(b.KubernetesCA, "", "", time.Now()); err != nil {
		return environmentBootstrapCleanupBlocker("verified Kubernetes CA binding required")
	}
	return nil
}

// The cleanup identity is separate from the OpenBao audience. RBAC permits
// only the recorded bootstrap Secret and the selected namespace; no listing,
// updates, general Secret deletion or persistent issuer policy is granted.
func applyEnvironmentBootstrapCleanup(env map[string]string, object deploymentplan.Object, binding environmentBootstrapCleanupBinding) ([]deploymentplan.Definition, error) {
	if err := validateEnvironmentBootstrapCleanupBinding(&binding, env["CLOUD_ENV_NAME"]); err != nil {
		return nil, err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	if deploymentplan.ID(object) != "Deployment/"+namespace+"/pki-controller" {
		return nil, environmentBootstrapCleanupBlocker("cleanup belongs only to the environment controller")
	}
	// Strip the separate API trust bundle from the runtime ownership record.
	record := map[string]any{"schema_version": binding.Version, "environment": binding.Environment, "session_id": binding.SessionID, "namespace_uid": binding.NamespaceUID, "secret_uid": binding.SecretUID, "policy_sha256": binding.PolicySHA256}
	raw, _ := json.Marshal(record)
	ca, _ := base64.StdEncoding.Strict().DecodeString(binding.KubernetesCA)
	name := env["CLOUD_STACK_NAME"] + "-bootstrap-cleanup"
	mapName := binding.ConfigMapName
	if mapName == "" {
		mapName = "pki-bootstrap-cleanup-binding"
	}
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": namespace} }
	clusterMeta := map[string]any{"name": name}
	definitions := []deploymentplan.Definition{
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta(mapName), "immutable": true, "data": map[string]any{"binding.json": string(raw), "ca.crt": string(ca)}}},
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta("bootstrap-cleanup"), "rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"service-bootstrap-" + binding.SessionID}, "verbs": []any{"get", "delete"}}}}},
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": meta("bootstrap-cleanup"), "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "bootstrap-cleanup"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "pki-controller", "namespace": namespace}}}},
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": clusterMeta, "rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"namespaces"}, "resourceNames": []any{namespace}, "verbs": []any{"get"}}}}},
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": clusterMeta, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "pki-controller", "namespace": namespace}}}},
	}
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
	if pod["serviceAccountName"] != "pki-controller" || pod["automountServiceAccountToken"] != false {
		return nil, environmentBootstrapCleanupBlocker("controller cleanup requires its exact dedicated ServiceAccount and projected token")
	}
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], []any{
		map[string]any{"name": "PKI_BOOTSTRAP_CLEANUP_BINDING", "value": "/run/bootstrap-cleanup/binding.json"},
		map[string]any{"name": "PKI_BOOTSTRAP_KUBERNETES_CA_FILE", "value": "/run/bootstrap-cleanup/ca.crt"},
		map[string]any{"name": "PKI_BOOTSTRAP_KUBERNETES_TOKEN_FILE", "value": "/run/bootstrap-cleanup-api/token"},
	})
	pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{
		map[string]any{"name": "bootstrap-cleanup", "configMap": map[string]any{"name": mapName, "defaultMode": float64(288)}},
		map[string]any{"name": "bootstrap-cleanup-api", "projected": map[string]any{"defaultMode": float64(288), "sources": []any{map[string]any{"serviceAccountToken": map[string]any{"expirationSeconds": float64(3600), "path": "token"}}}}},
	})
	container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{
		map[string]any{"name": "bootstrap-cleanup", "mountPath": "/run/bootstrap-cleanup", "readOnly": true},
		map[string]any{"name": "bootstrap-cleanup-api", "mountPath": "/run/bootstrap-cleanup-api", "readOnly": true},
	})
	return definitions, nil
}

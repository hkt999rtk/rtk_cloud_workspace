package main

import (
	"context"
	"regexp"
	"strings"
)

// Only native Kubernetes IDs are safe in ordinary reports; filesystem paths,
// arbitrary process diagnostics and secrets must never appear here.
var deploymentDiagnosticResourcePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]{0,62}/([a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?)?/[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func safeDeploymentDiagnosticResource(value string) string {
	if len(value) > 512 || !deploymentDiagnosticResourcePattern.MatchString(value) {
		return ""
	}
	kind, _, _ := strings.Cut(value, "/")
	switch kind {
	case "Namespace", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Pod", "Job", "CronJob", "Service", "ConfigMap", "Secret", "PersistentVolumeClaim", "PersistentVolume", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "NetworkPolicy", "Ingress", "IngressClass", "PodDisruptionBudget", "HorizontalPodAutoscaler", "Lease", "StorageClass", "CustomResourceDefinition", "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "Node":
		return value
	default:
		return ""
	}
}

// Classify dry-run diagnostics to a fixed code and discard their original text.
func classifyDeploymentAdmissionError(ctx context.Context, raw []byte, err error) *deploymentRuntimeError {
	base := classifyDeploymentRuntimeError(ctx, raw, err, false)
	if base.code != "KUBE_COMMAND_FAILED" && base.code != "KUBE_FORBIDDEN" {
		return base
	}
	diagnostic := strings.ToLower(string(raw))
	switch {
	case strings.Contains(diagnostic, "exceeded quota"), strings.Contains(diagnostic, "exceeded resource quota"):
		return &deploymentRuntimeError{"KUBE_QUOTA", "Kubernetes resource quota rejected the request"}
	case strings.Contains(diagnostic, "field is immutable"), strings.Contains(diagnostic, "immutable field"):
		return &deploymentRuntimeError{"KUBE_IMMUTABLE", "Kubernetes rejected an immutable field change"}
	case strings.Contains(diagnostic, "admission webhook") && strings.Contains(diagnostic, "denied"):
		return &deploymentRuntimeError{"KUBE_ADMISSION_DENIED", "Kubernetes admission denied the request"}
	case strings.Contains(diagnostic, " is invalid:"), strings.Contains(diagnostic, "invalid value:"), strings.Contains(diagnostic, "unprocessable entity"):
		return &deploymentRuntimeError{"KUBE_INVALID", "Kubernetes rejected an invalid resource"}
	case strings.Contains(diagnostic, "already exists"), strings.Contains(diagnostic, "the object has been modified"), strings.Contains(diagnostic, "conflict"):
		return &deploymentRuntimeError{"KUBE_CONFLICT", "Kubernetes resource conflict"}
	}
	return base
}

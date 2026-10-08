package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Ingress is an ordinary catalog component, including its admission transport.
// The bootstrap lifecycle owns that transport Secret and its public CA binding;
// a full upgrade never generates a replacement admission key or disables it.
func renderEnvironmentIngress(env map[string]string, binding *environmentAdmissionBinding, release environmentRelease) ([]deploymentplan.Definition, error) {
	if binding == nil || !secretEnvironmentPattern.MatchString(binding.SecretName) {
		return nil, ingressAdmissionBlocker(env["CLOUD_STACK_NAME"], "existing admission Secret reference or authoritative webhook CA could not be observed")
	}
	raw, err := base64.StdEncoding.DecodeString(binding.CABundle)
	if err != nil {
		return nil, ingressAdmissionBlocker(env["CLOUD_STACK_NAME"], "invalid public admission CA bundle")
	}
	count := 0
	for len(strings.TrimSpace(string(raw))) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, ingressAdmissionBlocker(env["CLOUD_STACK_NAME"], "only public CA certificates are accepted")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, ingressAdmissionBlocker(env["CLOUD_STACK_NAME"], "invalid admission CA certificate")
		}
		count++
		raw = rest
	}
	if count == 0 {
		return nil, ingressAdmissionBlocker(env["CLOUD_STACK_NAME"], "empty admission CA bundle")
	}
	image := release.Images["LKE_INGRESS_IMAGE"].Reference
	if !immutableEnvironmentImage.MatchString(image) {
		return nil, errors.New("deployment.release.ingress: select the controller's immutable INGRESS_CONTROLLER_IMAGE")
	}
	namespace, stack := lkeIngressNamespace(env), env["CLOUD_STACK_NAME"]
	webhookName := firstNonEmpty(binding.WebhookName, stack+"-ingress-admission")
	if !secretEnvironmentPattern.MatchString(webhookName) {
		return nil, ingressAdmissionBlocker(stack, "observed admission webhook name is invalid")
	}
	class, roleName := stack+"-nginx", stack+"-ingress-nginx"
	controllerClass := "rtk.realtek.com/" + class
	selector := map[string]any{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/instance": "ingress-nginx", "app.kubernetes.io/component": "controller"}
	meta := func(name, ns string) map[string]any {
		m := map[string]any{"name": name}
		if ns != "" {
			m["namespace"] = ns
		}
		return m
	}
	object := func(version, kind, name, ns string, fields map[string]any) deploymentplan.Object {
		fields["apiVersion"], fields["kind"], fields["metadata"] = version, kind, meta(name, ns)
		return fields
	}
	var definitions []deploymentplan.Definition
	add := func(o deploymentplan.Object, phase string, dependencies ...string) {
		definitions = append(definitions, deploymentplan.Definition{Object: o, Phase: phase, Dependencies: dependencies})
	}
	serviceAccount := "ingress-nginx"
	saID := "ServiceAccount/" + namespace + "/" + serviceAccount
	configID := "ConfigMap/" + namespace + "/ingress-nginx-controller"
	roleID := "Role/" + namespace + "/ingress-nginx"
	clusterRoleID := "ClusterRole//" + roleName
	roleBindingID := "RoleBinding/" + namespace + "/ingress-nginx"
	clusterBindingID := "ClusterRoleBinding//" + roleName
	add(object("v1", "ServiceAccount", serviceAccount, namespace, map[string]any{}), "configuration")
	readRule := func(group string, resources ...string) any {
		return map[string]any{"apiGroups": []any{group}, "resources": resources, "verbs": []any{"get", "list", "watch"}}
	}
	clusterRules := []any{
		readRule("", "configmaps", "endpoints", "nodes", "pods", "secrets", "namespaces", "services"),
		readRule("discovery.k8s.io", "endpointslices"),
		readRule("networking.k8s.io", "ingresses", "ingressclasses"),
		map[string]any{"apiGroups": []any{"networking.k8s.io"}, "resources": []any{"ingresses/status"}, "verbs": []any{"update"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"events"}, "verbs": []any{"create", "patch"}},
	}
	add(object("rbac.authorization.k8s.io/v1", "ClusterRole", roleName, "", map[string]any{"rules": clusterRules}), "configuration")
	add(object("rbac.authorization.k8s.io/v1", "Role", "ingress-nginx", namespace, map[string]any{"rules": []any{
		readRule("", "configmaps", "pods", "secrets", "services"),
		map[string]any{"apiGroups": []any{"coordination.k8s.io"}, "resources": []any{"leases"}, "verbs": []any{"get", "create", "update"}},
	}}), "configuration")
	subjects := []any{map[string]any{"kind": "ServiceAccount", "name": serviceAccount, "namespace": namespace}}
	roleRef := func(kind, name string) map[string]any {
		return map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": kind, "name": name}
	}
	add(object("rbac.authorization.k8s.io/v1", "RoleBinding", "ingress-nginx", namespace, map[string]any{"subjects": subjects, "roleRef": roleRef("Role", "ingress-nginx")}), "configuration", saID, roleID)
	add(object("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", roleName, "", map[string]any{"subjects": subjects, "roleRef": roleRef("ClusterRole", roleName)}), "configuration", saID, clusterRoleID)
	config := map[string]any{"allow-snippet-annotations": "true", "annotations-risk-level": "Critical"}
	if lkeDisableSearchIndexing(env) {
		config["server-snippet"], config["location-snippet"] = lkeNoIndexServerSnippet, lkeNoIndexHeader
	}
	add(object("v1", "ConfigMap", "ingress-nginx-controller", namespace, map[string]any{"data": config}), "configuration")
	add(object("networking.k8s.io/v1", "IngressClass", class, "", map[string]any{"spec": map[string]any{"controller": controllerClass}}), "configuration")
	service := func(name, typ string, ports []any) deploymentplan.Object {
		return object("v1", "Service", name, namespace, map[string]any{"spec": map[string]any{"type": typ, "selector": selector, "ports": ports}})
	}
	add(service("ingress-nginx-controller", "NodePort", []any{map[string]any{"name": "https", "port": float64(443), "targetPort": "https", "protocol": "TCP", "nodePort": float64(lkeIngressHTTPSNodePort(env))}}), "configuration")
	add(service("ingress-nginx-controller-admission", "ClusterIP", []any{map[string]any{"name": "https-webhook", "port": float64(443), "targetPort": "webhook", "protocol": "TCP"}}), "configuration")
	replicas, _ := strconv.Atoi(lkeIngressReplicas(env))
	args := []any{"/nginx-ingress-controller", "--election-id=ingress-nginx-leader", "--controller-class=" + controllerClass, "--ingress-class=" + class, "--watch-ingress-without-class=false", "--configmap=$(POD_NAMESPACE)/ingress-nginx-controller", "--publish-service=$(POD_NAMESPACE)/ingress-nginx-controller", "--watch-namespace-selector=rtk.realtek.com/stack=" + stack, "--enable-ssl-passthrough", "--validating-webhook=:8443", "--validating-webhook-certificate=/usr/local/certificates/cert", "--validating-webhook-key=/usr/local/certificates/key"}
	probe := map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": float64(10254)}, "initialDelaySeconds": float64(10), "periodSeconds": float64(10)}
	container := map[string]any{
		"name": "controller", "image": image, "args": args,
		"env":            []any{map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}, map[string]any{"name": "POD_NAMESPACE", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.namespace"}}}},
		"ports":          []any{map[string]any{"name": "https", "containerPort": float64(443)}, map[string]any{"name": "http", "containerPort": float64(80)}, map[string]any{"name": "webhook", "containerPort": float64(8443)}},
		"readinessProbe": probe, "livenessProbe": probe,
		"resources":       map[string]any{"requests": map[string]any{"cpu": firstNonEmpty(env["LKE_INGRESS_REQUEST_CPU"], "500m"), "memory": firstNonEmpty(env["LKE_INGRESS_REQUEST_MEMORY"], "512Mi")}, "limits": map[string]any{"memory": firstNonEmpty(env["LKE_INGRESS_LIMIT_MEMORY"], "1Gi")}},
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": float64(101), "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}, "add": []any{"NET_BIND_SERVICE"}}},
		"volumeMounts":    []any{map[string]any{"name": "admission", "mountPath": "/usr/local/certificates", "readOnly": true}},
	}
	pod := map[string]any{"serviceAccountName": serviceAccount, "securityContext": map[string]any{"fsGroup": float64(101)}, "containers": []any{container}, "volumes": []any{map[string]any{"name": "admission", "secret": map[string]any{"secretName": binding.SecretName, "defaultMode": float64(288)}}}, "nodeSelector": map[string]any{firstNonEmpty(env["NODE_CLASS_LABEL_KEY"], "rtk.io/node-class"): firstNonEmpty(env["INGRESS_NODE_CLASS"], env["DEFAULT_WORKLOAD_NODE_CLASS"], "general")}, "topologySpreadConstraints": []any{map[string]any{"maxSkew": float64(1), "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway", "labelSelector": map[string]any{"matchLabels": selector}}}}
	deploy := object("apps/v1", "Deployment", "ingress-nginx-controller", namespace, map[string]any{"spec": map[string]any{"replicas": float64(replicas), "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{"metadata": map[string]any{"labels": selector}, "spec": pod}}})
	add(deploy, "workload", saID, configID, roleBindingID, clusterBindingID, "Secret/"+namespace+"/"+binding.SecretName)
	add(object("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", webhookName, "", map[string]any{"webhooks": []any{map[string]any{
		"name": "validate.nginx.ingress.kubernetes.io", "admissionReviewVersions": []any{"v1"}, "sideEffects": "None", "failurePolicy": "Fail", "matchPolicy": "Equivalent", "timeoutSeconds": float64(10),
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"rtk.realtek.com/stack": stack}},
		"clientConfig":      map[string]any{"caBundle": binding.CABundle, "service": map[string]any{"namespace": namespace, "name": "ingress-nginx-controller-admission", "path": "/networking/v1/ingresses", "port": float64(443)}},
		"rules":             []any{map[string]any{"apiGroups": []any{"networking.k8s.io"}, "apiVersions": []any{"v1"}, "operations": []any{"CREATE", "UPDATE"}, "resources": []any{"ingresses"}, "scope": "Namespaced"}},
	}}}), "routing", deploymentplan.ID(deploy), "Service/"+namespace+"/ingress-nginx-controller-admission")
	add(object("networking.k8s.io/v1", "NetworkPolicy", "ingress-nginx-admission", namespace, map[string]any{"spec": map[string]any{"podSelector": map[string]any{"matchLabels": selector}, "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{"ports": []any{map[string]any{"protocol": "TCP", "port": float64(8443)}}}, map[string]any{"ports": []any{map[string]any{"protocol": "TCP", "port": float64(443)}}}}}}), "configuration")
	return definitions, nil
}

func environmentIngressClass(env map[string]string) string {
	return fmt.Sprintf("%s-nginx", env["CLOUD_STACK_NAME"])
}

func ingressAdmissionBlocker(stack, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.ingress.admission", Resource: "Service/" + stack + "-ingress/ingress-nginx-controller-admission", Cause: cause, Next: "verify the original controller Secret mount and authoritative webhook CA for this environment; preserve its owner and credentials, then repeat preflight"}
}

// A non-prefixed Helm owner is accepted only when every webhook targets this
// exact private admission Service. Mixed/foreign endpoints are never adopted.
func environmentIngressAdmissionOwner(object deploymentplan.Object, stack string) bool {
	if object["kind"] != "ValidatingWebhookConfiguration" {
		return false
	}
	hooks := certIssuerObjectList(object["webhooks"])
	if len(hooks) == 0 {
		return false
	}
	for _, v := range hooks {
		client := deploymentplan.Map(deploymentplan.Map(v)["clientConfig"])
		service := deploymentplan.Map(client["service"])
		if client["url"] != nil || service["namespace"] != stack+"-ingress" || service["name"] != "ingress-nginx-controller-admission" {
			return false
		}
	}
	return true
}

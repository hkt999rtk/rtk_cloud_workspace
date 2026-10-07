package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentAccessRequirement struct {
	Group, Resource, Namespace, Name, Verb string
}

// These lists are shared with the phase inventory reader. A narrower list would
// miss an unplanned owner; label selectors do not restrict RBAC list permission.
const environmentNamespacedInventoryKinds = "deployment,statefulset,daemonset,replicaset,pod,job,cronjob,service,configmap,secret,persistentvolumeclaim,serviceaccount,role,rolebinding,networkpolicy,ingress,poddisruptionbudget,horizontalpodautoscaler"
const environmentClusterInventoryKinds = "clusterrole,clusterrolebinding,ingressclass,validatingwebhookconfiguration"

var environmentAPIResources = map[string]environmentAccessRequirement{
	"namespace": {Resource: "namespaces"}, "deployment": {Group: "apps", Resource: "deployments"},
	"statefulset": {Group: "apps", Resource: "statefulsets"}, "daemonset": {Group: "apps", Resource: "daemonsets"},
	"replicaset": {Group: "apps", Resource: "replicasets"}, "pod": {Resource: "pods"},
	"job": {Group: "batch", Resource: "jobs"}, "cronjob": {Group: "batch", Resource: "cronjobs"},
	"service": {Resource: "services"}, "configmap": {Resource: "configmaps"}, "secret": {Resource: "secrets"},
	"persistentvolumeclaim": {Resource: "persistentvolumeclaims"}, "serviceaccount": {Resource: "serviceaccounts"},
	"role": {Group: "rbac.authorization.k8s.io", Resource: "roles"}, "rolebinding": {Group: "rbac.authorization.k8s.io", Resource: "rolebindings"},
	"clusterrole": {Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, "clusterrolebinding": {Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
	"networkpolicy": {Group: "networking.k8s.io", Resource: "networkpolicies"}, "ingress": {Group: "networking.k8s.io", Resource: "ingresses"},
	"ingressclass":                   {Group: "networking.k8s.io", Resource: "ingressclasses"},
	"validatingwebhookconfiguration": {Group: "admissionregistration.k8s.io", Resource: "validatingwebhookconfigurations"},
	"poddisruptionbudget":            {Group: "policy", Resource: "poddisruptionbudgets"}, "horizontalpodautoscaler": {Group: "autoscaling", Resource: "horizontalpodautoscalers"},
}

// Kubernetes uses collection-level create authorization; an existing object
// uses name-scoped update/delete rights. Waiting additionally needs watch.
func environmentPlanAccessRequirements(plan *deploymentplan.Plan) ([]environmentAccessRequirement, error) {
	if plan == nil || len(plan.Resources) == 0 {
		return nil, errors.New("selected complete resource plan required")
	}
	seen := map[environmentAccessRequirement]bool{}
	namespaces := map[string]bool{}
	for _, resource := range plan.Resources {
		parts := strings.Split(resource.ID, "/")
		if len(parts) != 3 || environmentAPIResources[strings.ToLower(parts[0])].Resource == "" {
			return nil, errors.New("resource lacks an explicit Kubernetes permission mapping")
		}
		version := deploymentplan.String(resource.Desired["apiVersion"])
		group := ""
		if version != "v1" {
			var found bool
			group, _, found = strings.Cut(version, "/")
			if !found || group == "" {
				return nil, errors.New("resource API group unavailable")
			}
		}
		base := environmentAPIResources[strings.ToLower(parts[0])]
		if group != base.Group {
			return nil, errors.New("planned resource API group does not match its Kubernetes permission mapping")
		}
		base.Namespace, base.Name, base.Verb = parts[1], parts[2], "get"
		if parts[1] != "" {
			namespaces[parts[1]] = true
		}
		if parts[0] == "Namespace" {
			namespaces[parts[2]] = true
		}
		seen[base] = true
		write := base
		switch resource.Action {
		case "create":
			write.Verb, write.Name = "create", ""
		case "update":
			write.Verb = "update"
		case "retire":
			write.Verb = "delete"
		case "retain":
		default:
			return nil, errors.New("unknown planned permission action")
		}
		if resource.Action != "retain" {
			seen[write] = true
		}
		if environmentOnDeleteUpdate(resource) {
			for ordinal := 0; ordinal < envObjectReplicas(resource.Desired); ordinal++ {
				for _, verb := range []string{"get", "delete", "watch"} {
					seen[environmentAccessRequirement{Resource: "pods", Namespace: parts[1], Name: fmt.Sprintf("%s-%d", parts[2], ordinal), Verb: verb}] = true
				}
			}
		}
		if parts[0] == "PersistentVolumeClaim" && resource.Action != "create" {
			volume := deploymentplan.String(deploymentplan.Map(resource.Baseline["spec"])["volumeName"])
			if volume != "" {
				seen[environmentAccessRequirement{Resource: "persistentvolumes", Name: volume, Verb: "get"}] = true
			}
		}
		if resource.Action == "retire" || parts[0] == "Deployment" || parts[0] == "StatefulSet" || parts[0] == "DaemonSet" || parts[0] == "Job" || parts[0] == "PersistentVolumeClaim" {
			watch := base
			watch.Verb = "watch"
			seen[watch] = true
			// kubectl wait/rollout uses a ListWatch with metadata.name selected.
			watch.Verb = "list"
			seen[watch] = true
		}
	}
	// Inventory rechecks observe new/unplanned owners at every phase.
	seen[environmentAccessRequirement{Resource: "namespaces", Verb: "list"}] = true
	seen[environmentAccessRequirement{Resource: "nodes", Verb: "list"}] = true
	for namespace := range namespaces {
		for _, kind := range strings.Split(environmentNamespacedInventoryKinds, ",") {
			permission := environmentAPIResources[kind]
			permission.Namespace, permission.Verb = namespace, "list"
			seen[permission] = true
		}
	}
	for _, kind := range strings.Split(environmentClusterInventoryKinds, ",") {
		permission := environmentAPIResources[kind]
		permission.Verb = "list"
		seen[permission] = true
	}
	result := make([]environmentAccessRequirement, 0, len(seen))
	for request := range seen {
		result = append(result, request)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		return strings.Join([]string{a.Group, a.Resource, a.Namespace, a.Name, a.Verb}, "/") < strings.Join([]string{b.Group, b.Resource, b.Namespace, b.Name, b.Verb}, "/")
	})
	return result, nil
}

func (b environmentKubernetesBackend) CheckPlanAccess(ctx context.Context, plan *deploymentplan.Plan) error {
	requests, err := environmentPlanAccessRequirements(plan)
	if err != nil {
		return err
	}
	for _, permission := range requests {
		if err := b.checkEnvironmentAccess(ctx, permission, ""); err != nil {
			return err
		}
	}
	return b.checkOpenBaoRestartAdmission(ctx, plan)
}

func (b environmentKubernetesBackend) checkEnvironmentAccess(ctx context.Context, permission environmentAccessRequirement, subresource string) error {
	attributes := map[string]string{"group": permission.Group, "resource": permission.Resource, "namespace": permission.Namespace, "name": permission.Name, "verb": permission.Verb, "subresource": subresource}
	body, _ := json.Marshal(map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": attributes}})
	raw, err := b.command(ctx, body, "create", "--raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "-f", "-")
	var response struct {
		Status struct {
			Allowed, Denied bool
			EvaluationError string `json:"evaluationError"`
		} `json:"status"`
	}
	if err != nil || json.Unmarshal(raw, &response) != nil || !response.Status.Allowed || response.Status.Denied || response.Status.EvaluationError != "" {
		name := permission.Name
		if name == "" {
			name = "collection"
		}
		resource := permission.Resource
		if subresource != "" {
			resource += "/" + subresource
		}
		return &deploymentplan.Blocker{ID: "deployment.execution.permissions", Resource: resource + "/" + permission.Namespace + "/" + name, Cause: "selected deployment identity lacks verified " + permission.Verb + " permission for the planned resource", Next: "grant only the selected environment operation's resource/name-scoped RBAC permissions, including rollout watch access, and repeat full preflight; access reviews persist no workload resources"}
	}
	return nil
}

package main

import (
	"errors"
	"regexp"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var ciRolloutKubernetesName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ciRolloutRBAC derives named resource permissions from the dev plan. Kubernetes
// RBAC restricts resources, not individual patch fields; runtime also uses a
// UID/resourceVersion-guarded image-only patch.
func ciRolloutRBAC(plan *deploymentplan.Plan, repo string, targets []ciRolloutTarget) ([]deploymentplan.Object, error) {
	if plan == nil || plan.Environment != "dev" || !ciRolloutKubernetesName.MatchString(plan.Stack) || !ciRepositoryName.MatchString(repo) || len(targets) == 0 {
		return nil, errors.New("CI rollout: valid dev plan and targets required")
	}
	parts := strings.Split(repo, "/")
	name := "rtk-ci-" + strings.ReplaceAll(strings.ToLower(parts[1]), "_", "-")
	if len(name) > 63 || !ciRolloutKubernetesName.MatchString(name) {
		return nil, errors.New("CI rollout: repository cannot name a dedicated Kubernetes identity")
	}
	stateName, err := ciRolloutStateName(repo)
	if err != nil {
		return nil, err
	}
	allowed := map[ciRolloutTarget]bool{}
	for _, repository := range environmentVersionRoles() {
		if !strings.HasPrefix(repository, "ghcr.io/"+repo+"/") {
			continue
		}
		matches, err := ciRolloutTargetsFromPlan(plan, repository)
		if err != nil {
			continue
		}
		for _, target := range matches {
			allowed[target] = true
		}
	}
	identityNamespace := plan.Stack + "-video-cloud"
	byNamespace := map[string]map[string]bool{identityNamespace: {}}
	for _, target := range targets {
		if !allowed[target] || !strings.HasPrefix(target.Namespace, plan.Stack+"-") || !ciRolloutKubernetesName.MatchString(target.Namespace) || !ciRolloutKubernetesName.MatchString(target.Name) || target.UID == "" || target.Container == "" {
			return nil, errors.New("CI rollout: invalid target in dev plan")
		}
		if byNamespace[target.Namespace] == nil {
			byNamespace[target.Namespace] = map[string]bool{}
		}
		byNamespace[target.Namespace][target.Name] = true
	}
	result := []deploymentplan.Object{{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"namespace": identityNamespace, "name": name}}}
	namespaces := make([]string, 0, len(byNamespace))
	for namespace := range byNamespace {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		names := make([]string, 0, len(byNamespace[namespace]))
		for n := range byNamespace[namespace] {
			names = append(names, n)
		}
		sort.Strings(names)
		var rules []any
		if len(names) > 0 {
			rules = append(rules, map[string]any{"apiGroups": []string{"apps"}, "resources": []string{"deployments"}, "resourceNames": names, "verbs": []string{"get", "list", "watch", "patch"}})
		}
		if namespace == identityNamespace {
			// RBAC cannot restrict create by resourceNames. Delete and read are
			// restricted to the existing deployment execution Lease name.
			rules = append(rules,
				map[string]any{"apiGroups": []string{"coordination.k8s.io"}, "resources": []string{"leases"}, "verbs": []string{"create"}},
				map[string]any{"apiGroups": []string{"coordination.k8s.io"}, "resources": []string{"leases"}, "resourceNames": []string{"rtk-deployment-execution"}, "verbs": []string{"get", "delete"}},
				map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "resourceNames": []string{stateName}, "verbs": []string{"get", "patch"}},
			)
		}
		result = append(result, deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": map[string]any{"namespace": namespace, "name": name}, "rules": rules}, deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": map[string]any{"namespace": namespace, "name": name}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": name, "namespace": identityNamespace}}})
	}
	for _, object := range result {
		metadata := deploymentplan.Map(object["metadata"])
		metadata["annotations"] = map[string]any{"rtk.realtek.com/ci-rollout-repository": repo}
	}
	return result, nil
}

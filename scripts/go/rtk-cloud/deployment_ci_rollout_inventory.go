package main

import (
	"encoding/json"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The provisioner records only observed workload identities and image names;
// pod configuration, environment variables and credentials never enter CI.
func ciRolloutPlanFromInventory(cfg deploymentConfig, policy ciBranchSelection, repo string, inventory []deploymentplan.Object) (*deploymentplan.Plan, error) {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if cfg.Environment != "dev" || !ciRolloutKubernetesName.MatchString(stack) || !policy.matches(repo, policy[repo]) {
		return nil, errors.New("CI rollout: selected dev environment and repository required")
	}
	repositories := map[string]bool{}
	for _, image := range environmentVersionRoles() {
		if strings.HasPrefix(image, "ghcr.io/"+repo+"/") {
			repositories[image] = true
		}
	}
	if len(repositories) == 0 {
		return nil, errors.New("CI rollout: repository has no maintained service images")
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: stack, Operation: "scoped"}}
	seen := map[string]bool{}
	for _, object := range inventory {
		metadata := deploymentplan.Map(object["metadata"])
		labels := deploymentplan.Map(metadata["labels"])
		namespace, name, uid := deploymentplan.String(metadata["namespace"]), deploymentplan.String(metadata["name"]), deploymentplan.String(metadata["uid"])
		if deploymentplan.String(object["apiVersion"]) != "apps/v1" || deploymentplan.String(object["kind"]) != "Deployment" || deploymentplan.String(labels["rtk.realtek.com/stack"]) != stack || !strings.HasPrefix(namespace, stack+"-") {
			continue
		}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		selected := []any{}
		for _, entry := range containers {
			c := deploymentplan.Map(entry)
			image := deploymentplan.String(c["image"])
			name := deploymentplan.String(c["name"])
			if !repositories[environmentImageRepository(image)] {
				continue
			}
			if name == "" || !immutableEnvironmentImage.MatchString(image) {
				return nil, errors.New("CI rollout: existing target container lacks a recoverable immutable image")
			}
			selected = append(selected, map[string]any{"name": name, "image": image})
		}
		if len(selected) == 0 {
			continue
		}
		if !ciRolloutKubernetesName.MatchString(namespace) || !ciRolloutKubernetesName.MatchString(name) || uid == "" || seen[namespace+"/"+name] {
			return nil, errors.New("CI rollout: target inventory has missing or duplicate identity")
		}
		seen[namespace+"/"+name] = true
		desired := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": selected}}}}
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: "Deployment/" + namespace + "/" + name, UID: uid, Desired: desired})
	}
	if len(plan.Resources) == 0 {
		return nil, errors.New("CI rollout: selected repository has no existing dev deployment targets")
	}
	return plan, nil
}

// ciRolloutPlanFromEnvironment intersects observed owners with the ordered
// environment plan. Live inventory supplies UIDs, never new desired services.
func ciRolloutPlanFromEnvironment(cfg deploymentConfig, policy ciBranchSelection, repo string, desired *deploymentplan.Plan, inventory []deploymentplan.Object) (*deploymentplan.Plan, error) {
	if desired == nil || desired.Environment != cfg.Environment || desired.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return nil, errors.New("CI rollout: selected environment plan required")
	}
	observed, err := ciRolloutPlanFromInventory(cfg, policy, repo, inventory)
	if err != nil {
		return nil, err
	}
	byID := map[string]deploymentplan.Resource{}
	for _, resource := range observed.Resources {
		byID[resource.ID] = resource
	}
	result := &deploymentplan.Plan{Inputs: observed.Inputs}
	for _, resource := range desired.Resources {
		if resource.Action == "retire" {
			continue
		}
		id := deploymentplan.ID(resource.Desired)
		live, ok := byID[id]
		if !ok {
			continue
		}
		if resource.UID != "" && resource.UID != live.UID {
			return nil, errors.New("CI rollout: observed workload identity differs from environment plan")
		}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(resource.Desired["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		expected := map[string]string{}
		for _, entry := range containers {
			c := deploymentplan.Map(entry)
			image := deploymentplan.String(c["image"])
			if strings.HasPrefix(environmentImageRepository(image), "ghcr.io/"+repo+"/") {
				expected[deploymentplan.String(c["name"])] = environmentImageRepository(image)
			}
		}
		livePod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(live.Desired["spec"])["template"])["spec"])
		actual, _ := livePod["containers"].([]any)
		selected := []any{}
		for _, entry := range actual {
			c := deploymentplan.Map(entry)
			if expected[deploymentplan.String(c["name"])] == environmentImageRepository(deploymentplan.String(c["image"])) {
				selected = append(selected, c)
			}
		}
		if len(selected) == 0 {
			continue
		}
		livePod["containers"] = selected
		result.Resources = append(result.Resources, live)
	}
	if len(result.Resources) == 0 {
		return nil, errors.New("CI rollout: environment plan has no matching existing service workloads")
	}
	return result, nil
}

// Decode only a Kubernetes Deployment collection. The result is held in memory
// and the caller strips environment values before persisting any target.
func ciRolloutDecodeDeploymentInventory(raw []byte) ([]deploymentplan.Object, error) {
	var list struct {
		Kind  string                  `json:"kind"`
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || (list.Kind != "List" && list.Kind != "DeploymentList") || list.Items == nil {
		return nil, errors.New("CI rollout: invalid Kubernetes Deployment inventory")
	}
	for _, item := range list.Items {
		if deploymentplan.String(item["apiVersion"]) != "apps/v1" || deploymentplan.String(item["kind"]) != "Deployment" {
			return nil, errors.New("CI rollout: unexpected object in Deployment inventory")
		}
	}
	return list.Items, nil
}

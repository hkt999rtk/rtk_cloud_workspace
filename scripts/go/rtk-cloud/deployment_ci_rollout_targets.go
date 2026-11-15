package main

import (
	"errors"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutTarget struct{ Namespace, Name, Container, UID string }

// ciRolloutTargetsFromPlan selects only workloads maintained by the selected
// environment plan. The retained UID prevents adopting a replacement workload.
func ciRolloutTargetsFromPlan(plan *deploymentplan.Plan, repository string) ([]ciRolloutTarget, error) {
	if plan == nil || plan.Environment != "dev" || repository == "" {
		return nil, errors.New("CI rollout: dev environment plan and image repository required")
	}
	var targets []ciRolloutTarget
	seen := map[ciRolloutTarget]bool{}
	for _, resource := range plan.Resources {
		object := resource.Desired
		if deploymentplan.String(object["kind"]) != "Deployment" || deploymentplan.String(object["apiVersion"]) != "apps/v1" || resource.Action == "retire" {
			continue
		}
		metadata := deploymentplan.Map(object["metadata"])
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		for _, item := range containers {
			c := deploymentplan.Map(item)
			if environmentImageRepository(deploymentplan.String(c["image"])) != repository {
				continue
			}
			target := ciRolloutTarget{Namespace: deploymentplan.String(metadata["namespace"]), Name: deploymentplan.String(metadata["name"]), Container: deploymentplan.String(c["name"]), UID: resource.UID}
			if target.Namespace == "" || target.Name == "" || target.Container == "" || target.UID == "" || seen[target] {
				return nil, errors.New("CI rollout: selected plan has missing or duplicate workload identity")
			}
			seen[target] = true
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("CI rollout: selected image has no existing environment workload")
	}
	return targets, nil
}

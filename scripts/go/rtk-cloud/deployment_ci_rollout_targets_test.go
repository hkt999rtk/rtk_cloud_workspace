package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutTargetsUseEnvironmentPlan(t *testing.T) {
	repo := "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend"
	image := repo + "@sha256:" + strings.Repeat("a", 64)
	workload := func(namespace, name, uid string, containers ...deploymentplan.Object) deploymentplan.Resource {
		all := make([]any, len(containers))
		for i, c := range containers {
			all[i] = c
		}
		return deploymentplan.Resource{UID: uid, Desired: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": all}}}}}
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev"}, Resources: []deploymentplan.Resource{
		workload("dev-frontend", "frontend", "uid-1", deploymentplan.Object{"name": "frontend", "image": image}, deploymentplan.Object{"name": "sidecar", "image": "other/sidecar:v1"}),
		workload("dev-other", "other", "uid-2", deploymentplan.Object{"name": "other", "image": "other/image:v1"}),
	}}
	targets, err := ciRolloutTargetsFromPlan(plan, repo)
	if err != nil || len(targets) != 1 || targets[0].Namespace != "dev-frontend" || targets[0].Name != "frontend" || targets[0].Container != "frontend" || targets[0].UID != "uid-1" {
		t.Fatalf("unexpected targets: %#v %v", targets, err)
	}
	plan.Resources[0].UID = ""
	if _, err = ciRolloutTargetsFromPlan(plan, repo); err == nil {
		t.Fatal("target without existing workload identity accepted")
	}
}

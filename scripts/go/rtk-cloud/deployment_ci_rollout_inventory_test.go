package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutInventoryUsesSelectedStackAndImageSources(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	image := "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("a", 64)
	object := func(namespace, name, stack, uid string) deploymentplan.Object {
		return deploymentplan.Object{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"namespace": namespace, "name": name, "uid": uid, "labels": map[string]any{"rtk.realtek.com/stack": stack}},
			"spec":     map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "frontend", "image": image, "env": []any{map[string]any{"name": "SECRET", "value": "private-value"}}}}}}},
		}
	}
	inventory := []deploymentplan.Object{object("video-cloud-dev-frontend", "frontend", "video-cloud-dev", "uid-1"), object("video-cloud-staging-frontend", "frontend", "video-cloud-staging", "uid-2")}
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	plan, err := ciRolloutPlanFromInventory(cfg, ciBranchSelection{repo: "main"}, repo, inventory)
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].UID != "uid-1" {
		t.Fatalf("unexpected inventory plan %#v: %v", plan, err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatal("runtime environment values leaked to CI target inventory")
	}
	targets, err := ciRolloutTargetsFromPlan(plan, "ghcr.io/"+repo+"/frontend")
	if err != nil || len(targets) != 1 || targets[0].Name != "frontend" {
		t.Fatalf("plan cannot select frontend: %#v %v", targets, err)
	}
	if _, err = ciRolloutPlanFromInventory(cfg, ciBranchSelection{}, repo, inventory); err == nil {
		t.Fatal("unselected repository accepted")
	}
}

func TestCIRolloutTargetsFollowEnvironmentPlanOrder(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	image := "ghcr.io/" + repo + "/video-cloud-api@sha256:" + strings.Repeat("a", 64)
	workload := func(name, uid string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": name, "uid": uid, "labels": map[string]any{"rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}}}}}
	}
	controller, api, unrelated := workload("pki-controller", "uid-1"), workload("video-cloud-api", "uid-2"), workload("unplanned", "uid-3")
	desired := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(controller), UID: "uid-1", Desired: controller}, {ID: deploymentplan.ID(api), UID: "uid-2", Desired: api}}}
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	got, err := ciRolloutPlanFromEnvironment(cfg, ciBranchSelection{repo: "main"}, repo, desired, []deploymentplan.Object{api, unrelated, controller})
	if err != nil || len(got.Resources) != 2 || got.Resources[0].ID != deploymentplan.ID(controller) || got.Resources[1].ID != deploymentplan.ID(api) {
		t.Fatalf("environment order not retained: %#v %v", got, err)
	}
	desired.Resources[0].UID = "replaced"
	if _, err = ciRolloutPlanFromEnvironment(cfg, ciBranchSelection{repo: "main"}, repo, desired, []deploymentplan.Object{api, controller}); err == nil {
		t.Fatal("replacement identity accepted")
	}
}

func TestCIRolloutDecodeDeploymentInventory(t *testing.T) {
	items, err := ciRolloutDecodeDeploymentInventory([]byte(`{"apiVersion":"apps/v1","kind":"DeploymentList","items":[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"frontend"}}]}`))
	if err != nil || len(items) != 1 || deploymentplan.String(deploymentplan.Map(items[0]["metadata"])["name"]) != "frontend" {
		t.Fatalf("deployment inventory: %#v %v", items, err)
	}
	for _, bad := range []string{`{}`, `{"kind":"SecretList","items":[]}`, `{"kind":"DeploymentList","items":null}`, `invalid`} {
		if _, err := ciRolloutDecodeDeploymentInventory([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid inventory %s", bad)
		}
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutRBACIsDevScopedToNamedDeploymentsAndLease(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	image := "ghcr.io/" + repo + "/video-cloud-api@sha256:" + strings.Repeat("a", 64)
	target := func(namespace, name, container, uid string) deploymentplan.Resource {
		return deploymentplan.Resource{UID: uid, Desired: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": container, "image": image}}}}}}}
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{target("video-cloud-dev-account-manager", "account-manager", "pkimanagement", "uid"), target("video-cloud-dev-video-cloud", "video-cloud-api", "app", "uid2")}}
	targets, err := ciRolloutTargetsFromPlan(plan, "ghcr.io/"+repo+"/video-cloud-api")
	if err != nil {
		t.Fatal(err)
	}
	objects, err := ciRolloutRBAC(plan, repo, targets)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	value := string(raw)
	for _, required := range []string{`"kind":"ServiceAccount"`, `"name":"rtk-ci-rtk-video-cloud"`, `"kind":"Role"`, `"kind":"RoleBinding"`, `"resourceNames":["account-manager"]`, `"resourceNames":["video-cloud-api"]`, `"resourceNames":["rtk-deployment-execution"]`, `"resourceNames":["rtk-ci-state-rtk-video-cloud"]`} {
		if !strings.Contains(value, required) {
			t.Fatalf("missing %s in %s", required, value)
		}
	}
	for _, forbidden := range []string{`"resources":["secrets"]`, `"resources":["pods"]`, `"resources":["persistentvolumeclaims"]`, `"kind":"ClusterRole"`, `"verbs":["*"]`} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("unexpected permission %s", forbidden)
		}
	}
	if !strings.Contains(value, `"resources":["deployments"],"verbs":["get","list","watch","patch"]`) {
		t.Fatal("named deployment rollout status watch permission is missing")
	}
	if !strings.Contains(value, `"resources":["leases"],"verbs":["create"]`) || !strings.Contains(value, `"resourceNames":["rtk-deployment-execution"],"resources":["leases"],"verbs":["get","delete"]`) {
		t.Fatal("lease permissions do not match the shared execution protocol")
	}
	if _, err = ciRolloutRBAC(plan, "hkt999rtk/rtk_cloud_frontend", targets); err == nil {
		t.Fatal("repository identity granted another repository's deployment")
	}
	plan.Environment = "prod"
	if _, err = ciRolloutRBAC(plan, repo, targets); err == nil {
		t.Fatal("prod identity accepted")
	}
	plan.Environment = "dev"
	targets[0].Namespace = "video-cloud-staging-frontend"
	if _, err = ciRolloutRBAC(plan, repo, targets); err == nil {
		t.Fatal("other environment target accepted")
	}
}

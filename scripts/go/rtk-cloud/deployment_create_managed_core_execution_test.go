package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestManagedCoreAliasAcceptsOnlyQualifiedPlanAndStageBinding(t *testing.T) {
	expected := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("configuration"), ReleaseSHA256: deploymentplan.Digest("original-business-release"), BindingsSHA256: deploymentplan.Digest("preview-core")}, Resources: []deploymentplan.Resource{{ID: "Deployment/video-cloud-dev-video-cloud/pki-controller", Phase: "workload", Action: "update", UID: "11111111-1111-4111-8111-111111111111", Baseline: deploymentplan.Object{"original": "same-controller"}, Desired: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"fixedRoot": "true", "rootID": "original-root", "rootSHA": deploymentplan.Digest("original-root")}}, Dependencies: []string{"ServiceAccount/video-cloud-dev-video-cloud/pki-controller"}}}}
	clone := func() *deploymentplan.Plan {
		raw, _ := json.Marshal(expected)
		var p deploymentplan.Plan
		if json.Unmarshal(raw, &p) != nil {
			t.Fatal("fixture decode")
		}
		return &p
	}
	actual := clone()
	actual.BindingsSHA256 = deploymentplan.Digest("genuine-forward-stage")
	if !managedCoreSameQualifiedPlan(actual, expected) {
		t.Fatal("same qualified resource and original UID must admit its distinct stage binding")
	}
	cases := map[string]func(*deploymentplan.Plan){"business-release": func(p *deploymentplan.Plan) { p.ReleaseSHA256 = deploymentplan.Digest("different") }, "configuration": func(p *deploymentplan.Plan) { p.ConfigurationSHA256 = deploymentplan.Digest("different") }, "environment": func(p *deploymentplan.Plan) { p.Environment = "staging" }, "operation": func(p *deploymentplan.Plan) { p.Operation = "create" }, "original-owner": func(p *deploymentplan.Plan) { p.Resources[0].UID = "22222222-2222-4222-8222-222222222222" }, "new-allocation": func(p *deploymentplan.Plan) { p.Resources[0].Action = "create" }, "root-identity": func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["spec"])["rootID"] = "replacement-root"
	}, "unsafe-controller-flag": func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["spec"])["fixedRoot"] = "false"
	}, "baseline": func(p *deploymentplan.Plan) { p.Resources[0].Baseline["original"] = "different" }, "dependency": func(p *deploymentplan.Plan) { p.Resources[0].Dependencies = nil }, "extra-resource": func(p *deploymentplan.Plan) { p.Resources = append(p.Resources, p.Resources[0]) }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := clone()
			p.BindingsSHA256 = actual.BindingsSHA256
			mutate(p)
			if managedCoreSameQualifiedPlan(p, expected) {
				t.Fatal("unqualified drift accepted")
			}
		})
	}
	if managedCoreSameQualifiedPlan(nil, expected) || managedCoreSameQualifiedPlan(actual, nil) {
		t.Fatal("missing plan accepted")
	}
}

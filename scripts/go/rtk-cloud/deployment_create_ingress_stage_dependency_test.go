package main

import (
	"context"
	"slices"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func ingressStageDependencyFixture(t *testing.T) (environmentCreateBootstrapPlan, []deploymentplan.Definition, deploymentplan.Object, deploymentplan.Inputs) {
	t.Helper()
	cfg, p, release, store, e := createIngressAdmissionFixture(t, "dev")
	material, err := prepareEnvironmentCreateIngressAdmissionMaterial(context.Background(), cfg, p, release, store, e)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := renderEnvironmentCreateIngressAdmission(cfg, p, release, material)
	if err != nil {
		t.Fatal(err)
	}
	inputs := deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(definitions)}
	identity, err := deploymentplan.Compile(inputs, definitions[:1], nil)
	if err != nil || len(identity.Resources) != 1 {
		t.Fatal("real admission identity stage cannot compile", err)
	}
	secret := deploymentplan.Clone(identity.Resources[0].Desired)
	deploymentplan.Map(secret["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	return p, definitions, secret, inputs
}

func TestCreateIngressStageDependencyKeepsOriginalSecretInControllerDAG(t *testing.T) {
	p, definitions, secret, inputs := ingressStageDependencyFixture(t)
	before := deploymentplan.Digest(definitions)
	if _, err := deploymentplan.Compile(inputs, definitions[1:], []deploymentplan.Object{secret}); err == nil {
		t.Fatal("legacy controller-only slice hid its absent admission Secret dependency")
	}
	stage := slices.Clone(definitions)
	stage[0].Retain = true
	plan, err := deploymentplan.Compile(inputs, stage, []deploymentplan.Object{secret})
	if err != nil {
		t.Fatal("maintained complete ingress controller DAG still refuses exact accepted identity", err)
	}
	indices := map[string]int{}
	var retained *deploymentplan.Resource
	for i := range plan.Resources {
		resource := &plan.Resources[i]
		indices[resource.ID] = i
		if resource.ID == deploymentplan.ID(secret) {
			retained = resource
		}
	}
	if retained == nil || retained.Action != "retain" || retained.UID != deploymentplan.Map(secret["metadata"])["uid"] || retained.Desired["immutable"] != true || !deploymentplan.Matches(retained.Desired, secret) || deploymentplan.Digest(retained.Baseline) != deploymentplan.Digest(deploymentplan.Projection(secret)) {
		t.Fatal("controller stage replaced or rewrote the original private admission Secret")
	}
	ns := p.Stack + "-ingress"
	controller := "Deployment/" + ns + "/ingress-nginx-controller"
	webhook := "ValidatingWebhookConfiguration//" + p.Stack + "-ingress-admission"
	for _, dependency := range []string{deploymentplan.ID(secret), "ServiceAccount/" + ns + "/ingress-nginx", "Role/" + ns + "/ingress-nginx", "ClusterRole//" + p.Stack + "-ingress-nginx", "RoleBinding/" + ns + "/ingress-nginx", "ClusterRoleBinding//" + p.Stack + "-ingress-nginx"} {
		i, exists := indices[dependency]
		if !exists || i >= indices[controller] {
			t.Fatal("controller can precede an actual identity/RBAC prerequisite", dependency)
		}
	}
	if indices[webhook] <= indices[controller] {
		t.Fatal("fail-closed admission webhook can precede its native controller")
	}
	body, err := retained.Body(secret)
	if err != nil || body != nil {
		t.Fatal("retained Secret generated a write", err)
	}
	replaced := deploymentplan.Clone(secret)
	deploymentplan.Map(replaced["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
	if _, err := retained.Body(replaced); err == nil {
		t.Fatal("admitted controller plan accepted a replacement identity owner")
	}
	if deploymentplan.Digest(definitions) != before {
		t.Fatal("retained stage planning modified the original identity recipe")
	}
}

func TestCreateIngressStageDependencyRejectsMissingChangedOrInvalidPrerequisites(t *testing.T) {
	for _, mode := range []string{"missing-secret", "changed-secret", "not-immutable", "foreign-owner", "cycle", "later-phase"} {
		t.Run(mode, func(t *testing.T) {
			p, definitions, secret, inputs := ingressStageDependencyFixture(t)
			stage := slices.Clone(definitions)
			stage[0].Retain = true
			live := []deploymentplan.Object{secret}
			switch mode {
			case "missing-secret":
				live = nil
			case "changed-secret":
				deploymentplan.Map(secret["data"])["cert"] = "Zm9yZWlnbi1jZXJ0aWZpY2F0ZQ=="
			case "not-immutable":
				secret["immutable"] = false
			case "foreign-owner":
				deploymentplan.Map(deploymentplan.Map(secret["metadata"])["labels"])["rtk.realtek.com/stack"] = "foreign-stack"
			case "cycle":
				stage[0].Dependencies = []string{"Deployment/" + p.Stack + "-ingress/ingress-nginx-controller"}
			case "later-phase":
				stage[0].Phase = "routing"
			}
			if _, err := deploymentplan.Compile(inputs, stage, live); err == nil {
				t.Fatal("unqualified dependency was hidden or reordered past its consumer", mode)
			}
		})
	}
}

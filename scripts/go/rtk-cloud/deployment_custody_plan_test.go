package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCustodyBootstrapPlanOrdersAccessBeforeNativeOwnerAndRetiresWithoutRecreatingIt(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
	ns := "video-cloud-dev-account-manager"
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "account-manager", Namespace: ns, ServiceAccount: "account-manager", StorageAliases: []string{"account-state"}}}}
	owner := deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": ns}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "account", "image": "fixture", "env": environmentCreateEnv(map[string]string{"PKI_CLIENT_IDENTITY_STATE": "/identity/private/client.json"}), "volumeMounts": []any{environmentCreateServiceMount("identity", "/identity", false)}}}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", "account-state")}}}}}}
	account := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": ns}, "automountServiceAccountToken": false}}
	projected, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, "", []deploymentplan.Definition{owner, account})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", ConfigurationSHA256: strings.Repeat("c", 64), ReleaseSHA256: strings.Repeat("d", 64), BindingsSHA256: strings.Repeat("e", 64)}, projected, nil)
	if err != nil {
		t.Fatal("formal bootstrap cannot order selected native custody prerequisites", err)
	}
	if _, err := environmentPlanAccessRequirements(plan); err != nil {
		t.Fatal("formal access check cannot qualify custody resources", err)
	}
	seen := map[string]bool{}
	for _, r := range plan.Resources {
		if r.ID == deploymentplan.ID(owner.Object) {
			if len(r.Dependencies) < 5 {
				t.Fatal("native owner omitted scoped access prerequisites")
			}
			for _, id := range r.Dependencies {
				if !seen[id] {
					t.Fatal("native owner starts before", id)
				}
			}
			retired := deploymentplan.Definition{Object: r.Desired, Phase: "retirement", Retire: true}
			result, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, "", []deploymentplan.Definition{retired})
			if err != nil || len(result) != 1 || deploymentplan.Digest(result[0]) != deploymentplan.Digest(retired) {
				t.Fatal("retiring native owner recreated access or changed accepted body", err)
			}
		}
		seen[r.ID] = true
	}
}

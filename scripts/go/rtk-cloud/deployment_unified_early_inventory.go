package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These checks have no image dependency. Their failures reuse the actual
// renderer/retirement blockers; success never publishes bindings or skips gates.
func checkEnvironmentEarlyInventory(ctx context.Context, env map[string]string, live []deploymentplan.Object) error {
	bindings := environmentIdentityBindings{Stack: env["CLOUD_STACK_NAME"], Owners: map[string]string{}}
	legacy := false
	for _, object := range live {
		meta := deploymentplan.Map(object["metadata"])
		bindings.Owners[deploymentplan.ID(object)] = deploymentplan.String(meta["uid"])
		if meta["namespace"] == bindings.Stack+"-video-cloud" && meta["name"] == "video-cloud-otaregistrar" {
			legacy = true
		}
	}
	if legacy {
		stage := "deployment.early.ota-retirement"
		environmentDeploymentProgress(ctx, stage)
		if _, err := retireLegacyOTARegistrar(nil, live, bindings); err != nil {
			return err
		}
		environmentDeploymentPrecheckPassed(ctx, stage)
	}
	for _, object := range live {
		meta := deploymentplan.Map(object["metadata"])
		if object["kind"] != "Deployment" || meta["namespace"] != bindings.Stack+"-video-cloud" {
			continue
		}
		name := deploymentplan.String(meta["name"])
		enabled := true
		switch name {
		case "video-cloud-mqttfoundation":
			enabled = lkeMQTTFoundationRegistrationEnabled(env)
		case otaServiceWorkloadName:
			enabled = lkeOTAServiceRegistrationEnabled(env)
		default:
			continue
		}
		stage := "deployment.early.renderer." + name
		environmentDeploymentProgress(ctx, stage)
		if !enabled {
			id := deploymentplan.ID(object)
			if name == "video-cloud-mqttfoundation" {
				return &deploymentplan.Blocker{ID: "deployment.inventory.disabled", Resource: id, Cause: "an existing managed consumer is disabled in the maintained target", Next: "align its maintained enablement with the existing environment, or qualify an explicit controlled retirement; repeat preflight"}
			}
			return environmentMissingRendererBlocker(id)
		}
		environmentDeploymentPrecheckPassed(ctx, stage)
	}
	return nil
}

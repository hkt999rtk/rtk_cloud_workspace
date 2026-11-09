package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Rebuild a historical recipe with the custody inputs of its already reviewed
// child plan. Live Pods are never used as rendering inputs. The caller still
// verifies the plan hash, journal, resource bodies and original resource UIDs.
func projectEnvironmentCustodyFrozenRecipe(cfg deploymentConfig, bootstrap environmentCreateBootstrapPlan, generation, serviceRoot string, definitions []deploymentplan.Definition, frozen *deploymentplan.Plan) ([]deploymentplan.Definition, error) {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return definitions, nil
	}
	binding, err := createEnvironmentCustodyBinding(cfg, bootstrap, generation, "")
	if err != nil {
		return nil, err
	}
	if !environmentCustodyStageOwnsStorage(binding, definitions) {
		return definitions, nil
	}
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("accepted custody recipe differs from its original generation, workload or transport inputs")
	}
	if frozen == nil {
		return bad()
	}
	owners := map[string]environmentCustodyOwnerBinding{}
	for _, owner := range binding.Owners {
		owners[cfg.Environment+"/"+generation+"/"+owner.ID] = owner
	}
	seen, bootstrapRoot, managedRoot := false, "", ""
	for _, resource := range frozen.Resources {
		pod := environmentPKIPod(resource.Desired)
		if pod == nil {
			continue
		}
		namespace := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["namespace"])
		for _, collection := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(pod[collection]) {
				container := deploymentplan.Map(raw)
				if mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" {
					continue
				}
				owner, exists := owners[mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX")]
				b, r := mqttFoundationEnv(container, "PKI_STATE_BOOTSTRAP_ROOT_SHA256"), mqttFoundationEnv(container, "PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256")
				if !exists || namespace != owner.Namespace || pod["serviceAccountName"] != owner.ServiceAccount || mqttFoundationEnv(container, "PKI_STATE_KV_MOUNT") != cfg.Values["PKI_STATE_KV_MOUNT"] || mqttFoundationEnv(container, "PKI_ENVIRONMENT") != cfg.Environment || (b != "" && !environmentPreflightDigest.MatchString(b)) || (r != "" && !environmentPreflightDigest.MatchString(r)) || b == "" && r == "" || seen && (b != bootstrapRoot || r != managedRoot) {
					return bad()
				}
				bootstrapRoot, managedRoot, seen = b, r, true
			}
		}
	}
	if !seen {
		return bad()
	}
	binding.OpenBaoRootSHA256, binding.ServiceRootSHA256 = managedRoot, serviceRoot
	return projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, bootstrapRoot, definitions)
}

package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

func otaRegistrarRetirementBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.ota.retirement", Resource: id, Cause: cause, Next: "keep the independent OTA service and its original identity; stop/drain the legacy registrar and verify core OTA cutover before repeating preflight; never delete the shared OTA identity Secret or data"}
}

// Retire only the exact, already stopped legacy owners. This does not infer
// retirement from replicas=0 for other workloads or discard shared identities.
func retireLegacyOTARegistrar(definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) ([]deploymentplan.Definition, error) {
	namespace := bindings.Stack + "-video-cloud"
	var retired []deploymentplan.Object
	replacement, cutover := false, false
	for _, object := range live {
		meta := deploymentplan.Map(object["metadata"])
		if meta["namespace"] != namespace {
			continue
		}
		name, kind := meta["name"], object["kind"]
		if kind == "Deployment" && name == "video-cloud-otaservice" && nonnegativeNumber(deploymentplan.Map(object["spec"])["replicas"]) > 0 {
			replacement = true
		}
		if kind == "Deployment" && name == "video-cloud-api" {
			for _, value := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
				if mqttFoundationEnv(deploymentplan.Map(value), "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED") == "true" {
					cutover = true
				}
			}
		}
		if kind == "Pod" && deploymentplan.Map(meta["labels"])["app.kubernetes.io/name"] == "video-cloud-otaregistrar" {
			phase := deploymentplan.Map(object["status"])["phase"]
			if phase != "Succeeded" && phase != "Failed" {
				return nil, otaRegistrarRetirementBlocker(deploymentplan.ID(object), "legacy registrar still has a non-terminal Pod")
			}
		}
		if name != "video-cloud-otaregistrar" || (kind != "Deployment" && kind != "Service") {
			continue
		}
		id := deploymentplan.ID(object)
		if bindings.Owners[id] == "" || bindings.Owners[id] != meta["uid"] || meta["deletionTimestamp"] != nil {
			return nil, otaRegistrarRetirementBlocker(id, "legacy owner cannot be verified")
		}
		if kind == "Deployment" && (deploymentplan.Map(object["spec"])["replicas"] != float64(0) || nonnegativeNumber(deploymentplan.Map(object["status"])["replicas"]) != 0) {
			return nil, otaRegistrarRetirementBlocker(id, "legacy registrar is not fully stopped")
		}
		retired = append(retired, object)
	}
	if len(retired) == 0 {
		return definitions, nil
	}
	if !replacement || !cutover {
		return nil, otaRegistrarRetirementBlocker("video-cloud-otaregistrar", "independent OTA deployment and existing core cutover are not verified")
	}
	for _, object := range retired {
		id := deploymentplan.ID(object)
		for _, definition := range definitions {
			if deploymentplan.ID(definition.Object) == id {
				return nil, otaRegistrarRetirementBlocker(id, "target attempts to recreate a retired registrar")
			}
		}
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": object["apiVersion"], "kind": object["kind"], "metadata": map[string]any{"name": "video-cloud-otaregistrar", "namespace": namespace}}, Phase: "retirement", Retire: true})
	}
	return definitions, nil
}
func nonnegativeNumber(value any) float64 {
	if value == nil {
		return 0
	}
	if number, ok := value.(float64); ok && number >= 0 {
		return number
	}
	return -1
}

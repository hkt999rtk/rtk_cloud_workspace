package main

import (
	"errors"
	"regexp"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var environmentDurableSize = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi|Ti)$`)

// Persistent component storage is maintained intent, independent of PKI
// bindings and of the currently installed Pod spec. Single RWO writers use
// Recreate so an update cannot start a second writer against the same claim.
func applyEnvironmentDurableStorage(definitions []deploymentplan.Definition, env map[string]string) ([]deploymentplan.Definition, error) {
	for _, component := range []struct{ name, key, container string }{
		{"redis", "REDIS_DATA_STORAGE", "redis"},
		{"video-cloud-prometheus", "PROMETHEUS_DATA_STORAGE", "prometheus"},
	} {
		size := env[component.key]
		if !environmentDurableSize.MatchString(size) {
			return nil, errors.New("deployment.target.storage: explicit positive Mi/Gi/Ti durable storage sizes are required")
		}
		found := false
		for i := range definitions {
			object := definitions[i].Object
			metadata := deploymentplan.Map(object["metadata"])
			if object["kind"] != "Deployment" || metadata["name"] != component.name {
				continue
			}
			if found {
				return nil, errors.New("deployment.target.storage: duplicate durable component")
			}
			found = true
			spec := deploymentplan.Map(object["spec"])
			if n, ok := spec["replicas"].(float64); !ok || n != 1 {
				return nil, &deploymentplan.Blocker{ID: "deployment.target.storage", Resource: deploymentplan.ID(object), Cause: "durable RWO component requires exactly one writer", Next: "maintain one replica or implement a separately qualified HA storage profile"}
			}
			spec["strategy"] = map[string]any{"type": "Recreate"}
			pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
			claimName := component.name + "-data"
			volumes := certIssuerObjectList(pod["volumes"])
			dataFound := false
			for j, value := range volumes {
				if deploymentplan.Map(value)["name"] == "data" {
					if dataFound {
						return nil, errors.New("deployment.target.storage: duplicate durable data mount")
					}
					dataFound = true
					volumes[j] = map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": claimName}}
				}
			}
			if !dataFound {
				return nil, errors.New("deployment.target.storage: durable renderer has no data volume")
			}
			pod["volumes"] = volumes
			// The Prometheus image runs without root; the claim must be writable
			// through its pod filesystem group on first mount.
			if component.name == "video-cloud-prometheus" {
				security := deploymentplan.Map(pod["securityContext"])
				if security == nil {
					security = map[string]any{}
					pod["securityContext"] = security
				}
				security["fsGroup"] = float64(65534)
			}
			containerFound := false
			mountPath := "/data"
			if component.name == "video-cloud-prometheus" {
				mountPath = "/prometheus"
			}
			for _, value := range certIssuerObjectList(pod["containers"]) {
				container := deploymentplan.Map(value)
				if container["name"] != component.container {
					continue
				}
				containerFound = true
				mountFound := false
				for _, mount := range certIssuerObjectList(container["volumeMounts"]) {
					m := deploymentplan.Map(mount)
					if m["name"] == "data" && m["readOnly"] != true && m["mountPath"] == mountPath && deploymentplan.String(m["subPath"]) == "" && deploymentplan.String(m["subPathExpr"]) == "" {
						mountFound = true
					}
				}
				if !mountFound {
					return nil, errors.New("deployment.target.storage: durable application data mount is missing")
				}
				if component.name == "redis" {
					container["command"] = []any{"redis-server"}
					container["args"] = []any{"--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec"}
				}
			}
			if !containerFound {
				return nil, errors.New("deployment.target.storage: durable application container is missing")
			}
			claimSpec := map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": size}}}
			if class := env["DURABLE_DATA_STORAGE_CLASS"]; class != "" {
				claimSpec["storageClassName"] = class
			}
			claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": claimName, "namespace": metadata["namespace"]}, "spec": claimSpec}
			definitions = append(definitions, deploymentplan.Definition{Object: claim, Phase: "storage"})
		}
		if !found {
			return nil, errors.New("deployment.target.storage: enabled durable component is absent")
		}
	}
	return definitions, nil
}

// A new claim must never silently replace writable-layer/emptyDir data. The
// migration must switch the actual old owner to the verified populated claim
// before an ordinary complete update can plan that same binding.
func validateEnvironmentDurableStorageTransition(definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	byID := map[string]deploymentplan.Object{}
	for _, object := range live {
		byID[deploymentplan.ID(object)] = object
	}
	for _, definition := range definitions {
		object := definition.Object
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if object["kind"] != "Deployment" || name != "redis" && name != "video-cloud-prometheus" {
			continue
		}
		id := deploymentplan.ID(object)
		current := byID[id]
		if current == nil {
			continue
		}
		oldPod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(current["spec"])["template"])["spec"])
		matched := false
		for _, volume := range certIssuerObjectList(oldPod["volumes"]) {
			v := deploymentplan.Map(volume)
			if v["name"] == "data" && deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] == name+"-data" {
				matched = true
			}
		}
		containerName, mountPath := "redis", "/data"
		if name == "video-cloud-prometheus" {
			containerName, mountPath = "prometheus", "/prometheus"
		}
		mounted := false
		for _, value := range certIssuerObjectList(oldPod["containers"]) {
			container := deploymentplan.Map(value)
			if container["name"] != containerName {
				continue
			}
			for _, value := range certIssuerObjectList(container["volumeMounts"]) {
				mount := deploymentplan.Map(value)
				if mount["name"] == "data" && mount["mountPath"] == mountPath && mount["readOnly"] != true && deploymentplan.String(mount["subPath"]) == "" && deploymentplan.String(mount["subPathExpr"]) == "" {
					mounted = true
				}
			}
		}
		namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
		claim := byID["PersistentVolumeClaim/"+namespace+"/"+name+"-data"]
		if !matched || !mounted || claim == nil || deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil {
			return &deploymentplan.Blocker{ID: "deployment.migration.durable-storage", Resource: id, Cause: "existing durable state is not bound to the target persistent claim", Next: "quiesce the exact source writer, retain its recovery archive, copy and verify its data into the target claim, then complete the independent storage migration and repeat full preflight; ordinary upgrade cannot initialize replacement data"}
		}
	}
	return nil
}

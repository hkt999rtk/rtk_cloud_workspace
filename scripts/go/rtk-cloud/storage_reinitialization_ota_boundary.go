package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// A storage repair may preserve an already completed handoff without draining
// legacy work again. It must not introduce core handlers, device routes or a
// dedicated process; those transitions retain their separate 48h drain gates.
type storageReinitializationOTABoundary struct {
	snapshot map[string]string
	mode     string
}

func (g *storageReinitializationOTABoundary) verify(body []byte, env map[string]string) error {
	if !storageReinitializationOTATrue(env["LKE_OTA_SERVICE_REGISTRATION_ENABLED"]) {
		return errors.New("OTA storage-only repair requires selected service registration")
	}
	if storageReinitializationOTATrue(env["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"]) {
		return errors.New("OTA storage-only repair cannot replace a core OTA registrar")
	}
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return errors.New("invalid OTA storage-only boundary inventory")
	}
	snapshot := map[string]string{}
	flags := map[string]any{}
	for _, key := range []string{"LKE_OTA_CORE_CUTOVER_ENABLED", "LKE_OTA_SERVICE_EDGE_ENABLED", "LKE_OTA_SERVICE_REGISTRATION_ENABLED", "LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"} {
		flags[key] = env[key]
	}
	snapshot["selected-handoff"] = storageReinitializationOTABoundaryHash(flags)
	wantedImage := ""
	for _, object := range inventory.Items {
		if object["kind"] != "Deployment" {
			continue
		}
		containers, _ := storageCutoverGet(object, "/spec/template/spec/containers").([]any)
		for _, entry := range containers {
			container := storageCutoverMap(entry)
			if container["name"] != "otaservice" {
				continue
			}
			wantedImage = storageCutoverString(container["image"])
			if wantedImage == "" {
				return errors.New("OTA storage-only repair requires an existing dedicated image")
			}
			identity := map[string]any{"uid": storageCutoverGet(object, "/metadata/uid"), "image": wantedImage, "command": container["command"], "args": container["args"]}
			snapshot["ota-process"] = storageReinitializationOTABoundaryHash(identity)
		}
	}
	if wantedImage == "" {
		return errors.New("OTA storage-only repair requires an existing dedicated Deployment")
	}
	for _, object := range inventory.Items {
		kind := storageCutoverString(object["kind"])
		path := "/spec/template/spec/containers"
		if kind == "Pod" {
			if phase := storageCutoverGet(object, "/status/phase"); phase == "Succeeded" || phase == "Failed" {
				continue
			}
			path = "/spec/containers"
		} else if kind != "Deployment" {
			continue
		}
		containers, _ := storageCutoverGet(object, path).([]any)
		for _, entry := range containers {
			container := storageCutoverMap(entry)
			if container["name"] != "otaservice" {
				continue
			}
			command, _ := container["command"].([]any)
			if !reflect.DeepEqual(command, []any{"/app/otaservice"}) || container["image"] != wantedImage {
				return errors.New("OTA storage-only repair must preserve the existing dedicated process and image")
			}
			if err := storageReinitializationOTALiteralFlag(container, "VIDEO_CLOUD_OTA_SERVICE_ENABLED"); err != nil {
				return err
			}
		}
	}
	core, err := storageCutoverRead("Deployment", lkeNamespaceName(env, "video-cloud"), "video-cloud-api")
	if err != nil {
		return err
	}
	ingress, err := storageCutoverRead("Ingress", lkeIngressNamespace(env), "video-cloud-staging-device-mtls")
	if err != nil {
		return err
	}
	for name, object := range map[string]map[string]any{"core": core, "device-ingress": ingress} {
		if object == nil {
			snapshot[name] = "absent"
			continue
		}
		if storageCutoverGet(object, "/metadata/uid") == nil || storageCutoverGet(object, "/metadata/uid") == "" {
			return errors.New("OTA storage-only boundary lacks UID")
		}
		identity := map[string]any{"uid": storageCutoverGet(object, "/metadata/uid"), "generation": storageCutoverGet(object, "/metadata/generation"), "spec": object["spec"], "annotations": storageCutoverGet(object, "/metadata/annotations"), "labels": storageCutoverGet(object, "/metadata/labels")}
		snapshot[name] = storageReinitializationOTABoundaryHash(identity)
	}
	if g.snapshot != nil && !reflect.DeepEqual(g.snapshot, snapshot) {
		return errors.New("core, device ingress or dedicated OTA process changed during storage-only reinitialization")
	}
	completed := storageReinitializationOTATrue(env["LKE_OTA_CORE_CUTOVER_ENABLED"]) && storageReinitializationOTATrue(env["LKE_OTA_SERVICE_EDGE_ENABLED"]) && storageReinitializationOTAObservedCore(core) && ingress != nil && lkeValidateActiveOTADeviceEdgeRoute(env, ingress) == nil
	if completed {
		if err := lkeRequireObservedOTACoreCutover(env); err != nil {
			if g.mode == "completed-handoff" {
				return fmt.Errorf("observed core cutover changed during OTA storage-only repair: %w", err)
			}
			completed = false
		}
	}
	mode := "legacy-drain"
	if completed {
		mode = "completed-handoff"
	}
	if g.mode != "" && mode != g.mode {
		return errors.New("OTA handoff state changed during storage-only reinitialization")
	}
	if !completed {
		// Preserve the original workflow for an existing dedicated process that
		// still needs core/edge handoff. Never fall back after choosing exemption.
		if err := lkeRequireLegacyOTADrain(env); err != nil {
			return err
		}
	}
	g.snapshot = snapshot
	g.mode = mode
	return nil
}

func storageReinitializationOTAObservedCore(core map[string]any) bool {
	if core == nil {
		return false
	}
	apps := 0
	containers, _ := storageCutoverGet(core, "/spec/template/spec/containers").([]any)
	for _, entry := range containers {
		container := storageCutoverMap(entry)
		if container["name"] != "app" {
			continue
		}
		apps++
		if from, present := container["envFrom"]; present {
			entries, ok := from.([]any)
			if !ok || len(entries) != 0 {
				return false
			}
		}
		if storageReinitializationOTALiteralFlag(container, "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED") != nil {
			return false
		}
	}
	return apps == 1
}

func storageReinitializationOTATrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

func storageReinitializationOTALiteralFlag(container map[string]any, key string) error {
	seen := 0
	env, _ := container["env"].([]any)
	for _, entry := range env {
		variable := storageCutoverMap(entry)
		if variable["name"] != key {
			continue
		}
		seen++
		enabled, err := strconv.ParseBool(storageCutoverString(variable["value"]))
		if _, indirect := variable["valueFrom"]; indirect || err != nil || !enabled {
			return fmt.Errorf("OTA storage-only repair requires literal enabled %s", key)
		}
	}
	if seen != 1 {
		return fmt.Errorf("OTA storage-only repair requires exactly one enabled %s binding", key)
	}
	return nil
}

func storageReinitializationOTABoundaryHash(identity map[string]any) string {
	body, _ := json.Marshal(identity)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

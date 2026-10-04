package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Reinitialization preserves delivery configuration. Only a CDN configuration
// already selected by the Deployment and its running Pods may waive direct
// Cloud Pulse qualification; selecting a URL does not change the running app.
func validateStorageReinitializationOTADelivery(body []byte, selectedEnv map[string]string) error {
	if err := lkeValidateOTACDNBaseURL(selectedEnv); err != nil {
		return err
	}
	wantedURL := selectedEnv["VIDEO_CLOUD_OTA_CDN_BASE_URL"]
	wantedName := firstNonEmpty(selectedEnv["VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"], "__token__")
	if strings.Contains(wantedURL, "$(") || strings.Contains(wantedName, "$(") {
		return errors.New("selected OTA CDN delivery configuration must contain literal values")
	}
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return errors.New("invalid OTA delivery inventory")
	}
	deployments, pods := 0, 0
	namespace := lkeNamespaceName(selectedEnv, "video-cloud")
	for _, object := range inventory.Items {
		kind := storageCutoverString(object["kind"])
		path := "/spec/template/spec/containers"
		switch kind {
		case "Deployment":
			if storageCutoverGet(object, "/metadata/namespace") != namespace || storageCutoverGet(object, "/metadata/name") != otaServiceWorkloadName {
				return errors.New("OTA delivery inventory contains an unexpected Deployment")
			}
			deployments++
		case "Pod":
			if phase := storageCutoverGet(object, "/status/phase"); phase == "Succeeded" || phase == "Failed" {
				continue
			}
			if storageCutoverGet(object, "/metadata/namespace") != namespace {
				return errors.New("OTA delivery inventory contains an active Pod outside the selected namespace")
			}
			pods++
			path = "/spec/containers"
		default:
			continue
		}
		containers, _ := storageCutoverGet(object, path).([]any)
		apps := 0
		for _, entry := range containers {
			container := storageCutoverMap(entry)
			if container["name"] != "otaservice" {
				continue
			}
			apps++
			if err := validateStorageReinitializationOTAContainerDelivery(container, wantedURL, wantedName); err != nil {
				return fmt.Errorf("OTA delivery configuration %s/%s/%s: %w", kind, storageCutoverGet(object, "/metadata/namespace"), storageCutoverGet(object, "/metadata/name"), err)
			}
		}
		if apps != 1 {
			return errors.New("OTA delivery inventory requires exactly one otaservice app container per Deployment and active Pod")
		}
	}
	if deployments != 1 || pods == 0 {
		return errors.New("OTA delivery verification requires its Deployment and owned active Pods")
	}
	if wantedURL != "" {
		return lkeRequireOTACDNRuntimeSecret(selectedEnv)
	}
	return nil
}

func validateStorageReinitializationOTAContainerDelivery(container map[string]any, wantedURL, wantedName string) error {
	// envFrom values are captured at Pod creation. Reading the referenced object
	// now cannot establish an existing process's effective delivery mode.
	if from, present := container["envFrom"]; present {
		entries, ok := from.([]any)
		if !ok || len(entries) != 0 {
			return errors.New("indirect OTA delivery envFrom bindings require an explicit literal mapping")
		}
	}
	const urlKey = "VIDEO_CLOUD_OTA_CDN_BASE_URL"
	const nameKey = "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"
	const tokenKey = "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"
	bindings := map[string]map[string]any{}
	env, ok := container["env"].([]any)
	if !ok {
		return errors.New("OTA delivery environment is missing")
	}
	for _, entry := range env {
		item := storageCutoverMap(entry)
		name := storageCutoverString(item["name"])
		if name != urlKey && name != nameKey && name != tokenKey {
			continue
		}
		if _, found := bindings[name]; found {
			return errors.New("duplicate OTA CDN environment binding")
		}
		bindings[name] = item
	}
	for key, wanted := range map[string]string{urlKey: wantedURL, nameKey: wantedName} {
		item := bindings[key]
		value, literal := item["value"].(string)
		// The application defaults an absent URL to direct delivery and an
		// absent/empty token name to __token__. The API also omits empty value.
		if _, present := item["value"]; !present {
			literal = true
		}
		_, indirect := item["valueFrom"]
		effective := value
		if key == nameKey {
			effective = firstNonEmpty(value, "__token__")
		}
		if !literal || indirect || strings.Contains(value, "$(") || effective != wanted {
			return fmt.Errorf("%s must be a literal matching the selected delivery configuration", key)
		}
	}
	key := bindings[tokenKey]
	if wantedURL == "" {
		if key != nil {
			return errors.New("direct OTA delivery must not bind a CDN token key")
		}
		return nil
	}
	from := storageCutoverMap(key["valueFrom"])
	ref := storageCutoverMap(from["secretKeyRef"])
	_, literal := key["value"]
	if literal || len(from) != 1 || ref["name"] != "ota-cdn-runtime" || ref["key"] != tokenKey || (ref["optional"] != nil && ref["optional"] != false) {
		return errors.New("CDN delivery requires the canonical ota-cdn-runtime token key Secret reference")
	}
	return nil
}

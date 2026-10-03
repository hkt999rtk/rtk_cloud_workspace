package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// A dedicated OTA service has a separate activation gate. Media activation may
// leave its exact existing controller tree alone, but cannot change that tree
// indirectly through a Secret or accept a replacement during the operation.
type storageReinitializationMediaIsolation struct {
	stack, sourceBucket string
	snapshot            map[string]string
	excluded            []map[string]any
	destinationSecrets  map[string]bool
}

func (g *storageReinitializationMediaIsolation) filter(body []byte) ([]byte, error) {
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return nil, errors.New("invalid media consumer inventory")
	}
	selected, seen := map[string]bool{}, map[string]bool{}
	rootFound := false
	for _, object := range inventory.Items {
		uid := storageCutoverString(storageCutoverGet(object, "/metadata/uid"))
		if uid != "" && seen[uid] {
			return nil, errors.New("duplicate consumer UID in media inventory")
		}
		seen[uid] = true
		if object["kind"] == "Deployment" && storageCutoverGet(object, "/metadata/namespace") == g.stack+"-video-cloud" && storageCutoverGet(object, "/metadata/name") == otaServiceWorkloadName {
			if uid == "" || rootFound {
				return nil, errors.New("excluded OTA deployment lacks a unique UID")
			}
			rootFound = true
			selected[uid] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, object := range inventory.Items {
			uid := storageCutoverString(storageCutoverGet(object, "/metadata/uid"))
			kind := storageCutoverString(object["kind"])
			if (kind == "ReplicaSet" || kind == "Pod") && storageCutoverGet(object, "/metadata/namespace") == g.stack+"-video-cloud" && !selected[uid] && selected[storageCutoverControllerUID(object)] {
				if uid == "" {
					return nil, errors.New("excluded OTA descendant lacks UID")
				}
				selected[uid], changed = true, true
			}
		}
	}
	items, excluded := []map[string]any{}, []map[string]any{}
	snapshot := map[string]string{}
	cache := map[string]map[string]any{}
	for _, object := range inventory.Items {
		uid := storageCutoverString(storageCutoverGet(object, "/metadata/uid"))
		ns := storageCutoverString(storageCutoverGet(object, "/metadata/namespace"))
		kind := storageCutoverString(object["kind"])
		phase := storageCutoverGet(object, "/status/phase")
		if !selected[uid] && (kind == "ReplicaSet" || (kind == "Job" && storageCutoverFinishedJob(object)) || (kind == "Pod" && (phase == "Succeeded" || phase == "Failed"))) {
			items = append(items, object)
			continue
		}
		path := storageCutoverPodPath(kind)
		if kind == "ReplicaSet" {
			path = "/spec/template/spec"
		}
		if kind == "Pod" {
			path = "/spec"
		}
		bindings := []any{}
		for _, containerType := range []string{"containers", "initContainers", "ephemeralContainers"} {
			containers, _ := storageCutoverGet(object, path+"/"+containerType).([]any)
			for _, value := range containers {
				container := storageCutoverMap(value)
				effective, _, err := storageCutoverEffectiveStorageEnv(container, ns, cache)
				if err != nil {
					return nil, err
				}
				bindings = append(bindings, effective)
				knownOTA := storageCutoverGet(object, "/metadata/name") == otaServiceWorkloadName || container["name"] == "otaservice"
				if !selected[uid] && (effective["VIDEO_CLOUD_OTA_BLOB_BUCKET"] == g.sourceBucket || (knownOTA && (effective["VIDEO_CLOUD_BLOB_BUCKET"] == g.sourceBucket || effective["LINODE_OBJ_BUCKET"] == g.sourceBucket))) {
					return nil, fmt.Errorf("additional source-bound OTA consumer %s/%s requires an explicit owner mapping", ns, storageCutoverGet(object, "/metadata/name"))
				}
			}
		}
		if !selected[uid] {
			items = append(items, object)
			continue
		}
		// Keep the raw excluded objects for Secret-reference checks. Hash the
		// immutable identity, complete specification and resolved storage fields;
		// routine readiness/status updates do not invalidate the snapshot.
		excluded = append(excluded, object)
		value, _ := json.Marshal(map[string]any{"kind": kind, "namespace": ns, "name": storageCutoverGet(object, "/metadata/name"), "owners": storageCutoverGet(object, "/metadata/ownerReferences"), "spec": object["spec"], "storage": bindings})
		snapshot[uid] = fmt.Sprintf("%x", sha256.Sum256(value))
	}
	if g.snapshot != nil && !reflect.DeepEqual(g.snapshot, snapshot) {
		return nil, errors.New("excluded OTA identity or storage definition changed during media reinitialization")
	}
	if err := g.checkExcludedSecretReferences(excluded); err != nil {
		return nil, err
	}
	g.snapshot, g.excluded = snapshot, excluded
	return json.Marshal(map[string]any{"items": items})
}

func (g *storageReinitializationMediaIsolation) protectDestinationSecrets(mutations []storageCutoverMutation) error {
	g.destinationSecrets = map[string]bool{}
	for _, mutation := range mutations {
		if mutation.Kind == "Secret" {
			g.destinationSecrets[mutation.Namespace+"/"+mutation.Name] = true
		}
	}
	return g.checkExcludedSecretReferences(g.excluded)
}

func (g *storageReinitializationMediaIsolation) checkExcludedSecretReferences(excluded []map[string]any) error {
	for _, object := range excluded {
		ns := storageCutoverString(storageCutoverGet(object, "/metadata/namespace"))
		var inspect func(any) bool
		inspect = func(value any) bool {
			switch v := value.(type) {
			case map[string]any:
				for key, child := range v {
					if key == "secretName" && g.destinationSecrets[ns+"/"+storageCutoverString(child)] {
						return true
					}
					if key == "secretKeyRef" || key == "secretRef" || key == "secret" || key == "nodePublishSecretRef" {
						if ref := storageCutoverMap(child); g.destinationSecrets[ns+"/"+storageCutoverString(ref["name"])] {
							return true
						}
					}
					if key == "imagePullSecrets" {
						refs, _ := child.([]any)
						for _, ref := range refs {
							if g.destinationSecrets[ns+"/"+storageCutoverString(storageCutoverMap(ref)["name"])] {
								return true
							}
						}
					}
					if inspect(child) {
						return true
					}
				}
			case []any:
				for _, child := range v {
					if inspect(child) {
						return true
					}
				}
			}
			return false
		}
		if inspect(object["spec"]) {
			return errors.New("excluded OTA consumer references a destination Secret that media reinitialization would change")
		}
	}
	return nil
}

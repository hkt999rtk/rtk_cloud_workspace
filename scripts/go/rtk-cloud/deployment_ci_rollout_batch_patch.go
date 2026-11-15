package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// All selected containers in one Deployment change in the same Kubernetes
// patch, so its Pod template rolls only once. expected is used during recovery.
func ciRolloutWorkloadPatch(raw []byte, target ciRolloutTarget, changes, expected map[string]string) ([]byte, map[string]string, error) {
	var object struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Namespace       string `json:"namespace"`
			Name            string `json:"name"`
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	bad := func() ([]byte, map[string]string, error) {
		return nil, nil, errors.New("CI rollout: workload or selected image changed")
	}
	if json.Unmarshal(raw, &object) != nil || object.APIVersion != "apps/v1" || object.Kind != "Deployment" || target.Namespace == "" || target.Name == "" || target.UID == "" || object.Metadata.Namespace != target.Namespace || object.Metadata.Name != target.Name || object.Metadata.UID != target.UID || object.Metadata.ResourceVersion == "" || len(changes) == 0 {
		return bad()
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	ops := []operation{{"test", "/metadata/uid", target.UID}, {"test", "/metadata/resourceVersion", object.Metadata.ResourceVersion}}
	previous := map[string]string{}
	seen := map[string]bool{}
	for index, container := range object.Spec.Template.Spec.Containers {
		image, ok := changes[container.Name]
		if !ok {
			continue
		}
		if seen[container.Name] || !immutableEnvironmentImage.MatchString(image) || !immutableEnvironmentImage.MatchString(container.Image) || environmentImageRepository(image) != environmentImageRepository(container.Image) {
			return bad()
		}
		seen[container.Name] = true
		if expected != nil && expected[container.Name] != container.Image && image != container.Image {
			return bad()
		}
		previous[container.Name] = container.Image
		if image == container.Image {
			continue
		}
		path := fmt.Sprintf("/spec/template/spec/containers/%d/image", index)
		ops = append(ops, operation{"test", path, container.Image}, operation{"replace", path, image})
	}
	if len(seen) != len(changes) || expected != nil && len(expected) != len(changes) {
		return bad()
	}
	if len(ops) == 2 {
		return nil, previous, nil
	}
	encoded, err := json.Marshal(ops)
	return encoded, previous, err
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ciRolloutImagePatch changes one explicitly selected container and fails if
// the observed workload was concurrently replaced or modified.
func ciRolloutImagePatch(raw []byte, namespace, name, uid, container, image string) ([]byte, error) {
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
	bad := func() ([]byte, error) {
		return nil, errors.New("CI rollout: selected workload or image does not match observed deployment")
	}
	if json.Unmarshal(raw, &object) != nil || object.APIVersion != "apps/v1" || object.Kind != "Deployment" || namespace == "" || name == "" || container == "" || object.Metadata.Namespace != namespace || object.Metadata.Name != name || uid == "" || object.Metadata.UID != uid || object.Metadata.ResourceVersion == "" || !immutableEnvironmentImage.MatchString(image) {
		return bad()
	}
	index := -1
	old := ""
	for i, c := range object.Spec.Template.Spec.Containers {
		if c.Name == container {
			if index != -1 {
				return bad()
			}
			index = i
			old = c.Image
		}
	}
	if index < 0 || old == "" || environmentImageRepository(old) != environmentImageRepository(image) {
		return bad()
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	path := fmt.Sprintf("/spec/template/spec/containers/%d/image", index)
	return json.Marshal([]operation{{"test", "/metadata/uid", object.Metadata.UID}, {"test", "/metadata/resourceVersion", object.Metadata.ResourceVersion}, {"test", path, old}, {"replace", path, image}})
}

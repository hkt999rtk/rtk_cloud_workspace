package main

import (
	"encoding/json"
	"errors"
	"strings"
)

func ciRolloutStateName(repo string) (string, error) {
	if !ciRepositoryName.MatchString(repo) {
		return "", errors.New("CI rollout: invalid repository")
	}
	parts := strings.Split(repo, "/")
	name := "rtk-ci-state-" + strings.ReplaceAll(strings.ToLower(parts[1]), "_", "-")
	if len(name) > 63 || !ciRolloutKubernetesName.MatchString(name) {
		return "", errors.New("CI rollout: invalid state name")
	}
	return name, nil
}

func ciRolloutStatePatch(raw []byte, namespace, repo, branch string, image environmentReleaseImage) ([]byte, error) {
	return ciRolloutStateBatchPatch(raw, namespace, repo, branch, map[string]environmentReleaseImage{environmentImageRepository(image.Reference): image})
}

func ciRolloutStateBatchPatch(raw []byte, namespace, repo, branch string, images map[string]environmentReleaseImage) ([]byte, error) {
	var object struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Namespace       string `json:"namespace"`
			Name            string `json:"name"`
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	bad := func() ([]byte, error) { return nil, errors.New("CI rollout: completed version state is invalid") }
	name, err := ciRolloutStateName(repo)
	if err != nil || json.Unmarshal(raw, &object) != nil || object.APIVersion != "v1" || object.Kind != "ConfigMap" || namespace == "" || object.Metadata.Namespace != namespace || object.Metadata.Name != name || object.Metadata.UID == "" || object.Metadata.ResourceVersion == "" || !validCIBranchName(branch) || len(images) == 0 {
		return bad()
	}
	old, exists := object.Data["state.json"]
	if !exists {
		return bad()
	}
	state := ciRolloutState{Environment: "dev", Repository: repo, Branch: branch, Images: map[string]environmentReleaseImage{}}
	if old != "" {
		if decodeEnvironmentJSON([]byte(old), &state) != nil || state.Environment != "dev" || state.Repository != repo || state.Images == nil {
			return bad()
		}
		state.Branch = branch
	}
	state.Origin = "completed"
	state.Pending = nil
	for repository, image := range images {
		if repository != environmentImageRepository(image.Reference) {
			return bad()
		}
		state.Images[repository] = image
	}
	if _, err = projectCIRolloutState(environmentImageSelection{Environment: "dev"}, ciBranchSelection{repo: branch}, state); err != nil {
		return bad()
	}
	next, err := json.Marshal(state)
	if err != nil {
		return bad()
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	return json.Marshal([]operation{{"test", "/metadata/uid", object.Metadata.UID}, {"test", "/metadata/resourceVersion", object.Metadata.ResourceVersion}, {"test", "/data/state.json", old}, {"replace", "/data/state.json", string(next)}})
}

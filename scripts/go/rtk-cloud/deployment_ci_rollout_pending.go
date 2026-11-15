package main

import (
	"encoding/json"
	"errors"
	"strings"
)

func ciRolloutPendingStatePatch(raw []byte, namespace, repo, branch string, pending *ciRolloutPending) ([]byte, error) {
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
	bad := func() ([]byte, error) { return nil, errors.New("CI rollout: invalid pending recovery state") }
	name, err := ciRolloutStateName(repo)
	if err != nil || json.Unmarshal(raw, &object) != nil || object.APIVersion != "v1" || object.Kind != "ConfigMap" || object.Metadata.Namespace != namespace || object.Metadata.Name != name || object.Metadata.UID == "" || object.Metadata.ResourceVersion == "" || !validCIBranchName(branch) || !strings.HasSuffix(namespace, "-video-cloud") {
		return bad()
	}
	old, ok := object.Data["state.json"]
	if !ok {
		return bad()
	}
	state := ciRolloutState{Environment: "dev", Repository: repo, Branch: branch, Images: map[string]environmentReleaseImage{}}
	if old != "" {
		if decodeEnvironmentJSON([]byte(old), &state) != nil || state.Environment != "dev" || state.Repository != repo || state.Images == nil {
			return bad()
		}
	}
	if pending != nil {
		if pending.Repository != repo || pending.Branch != branch || pending.RunID <= 0 || pending.Attempt <= 0 || pending.LeaseUID == "" || pending.Holder == "" {
			return bad()
		}
		if state.Pending != nil && (state.Pending.LeaseUID != pending.LeaseUID || state.Pending.Holder != pending.Holder || state.Pending.RunID != pending.RunID || state.Pending.Attempt != pending.Attempt) {
			return bad()
		}
		stack := strings.TrimSuffix(namespace, "-video-cloud")
		for _, change := range pending.Changes {
			if !strings.HasPrefix(change.Namespace, stack+"-") || !ciRolloutKubernetesName.MatchString(change.Namespace) || !ciRolloutKubernetesName.MatchString(change.Name) || change.UID == "" || change.Container == "" || !immutableEnvironmentImage.MatchString(change.PreviousImage) || !immutableEnvironmentImage.MatchString(change.DesiredImage) || environmentImageRepository(change.PreviousImage) != environmentImageRepository(change.DesiredImage) || !strings.HasPrefix(environmentImageRepository(change.DesiredImage), "ghcr.io/"+repo+"/") {
				return bad()
			}
		}
	}
	state.Pending = pending
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

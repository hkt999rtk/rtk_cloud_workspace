package main

import (
	"errors"
	"strings"
)

// ciRolloutState is the last completed immutable rollout for one repository.
// It is deployment data; branch selection remains environment configuration.
type ciRolloutState struct {
	Environment string                             `json:"environment"`
	Repository  string                             `json:"repository"`
	Branch      string                             `json:"branch"`
	Origin      string                             `json:"origin,omitempty"`
	Images      map[string]environmentReleaseImage `json:"images"`
	Pending     *ciRolloutPending                  `json:"pending,omitempty"`
}

type ciRolloutPending struct {
	Repository string                   `json:"repository"`
	Branch     string                   `json:"branch"`
	RunID      int64                    `json:"run_id"`
	Attempt    int                      `json:"run_attempt"`
	LeaseUID   string                   `json:"lease_uid"`
	Holder     string                   `json:"holder"`
	Changes    []ciRolloutPendingChange `json:"changes,omitempty"`
}
type ciRolloutPendingChange struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
	Container     string `json:"container"`
	PreviousImage string `json:"previous_image"`
	DesiredImage  string `json:"desired_image"`
}

// A full create/upgrade may reuse completed dev CI rollout facts for explicitly
// tracked repositories, while preserving all unrelated environment selections.
func projectCIRolloutState(selection environmentImageSelection, policy ciBranchSelection, state ciRolloutState) (environmentImageSelection, error) {
	if state.Environment != "dev" || selection.Environment != "dev" || !ciRepositoryName.MatchString(state.Repository) || !validCIBranchName(state.Branch) || (state.Origin != "" && state.Origin != "observed" && state.Origin != "completed") {
		return environmentImageSelection{}, errors.New("CI rollout: invalid completed environment state")
	}
	if policy[state.Repository] == "" {
		return selection, nil
	}
	roles := environmentVersionRoles()
	result := environmentImageSelection{Environment: selection.Environment, Images: make(map[string]environmentReleaseImage, len(selection.Images))}
	for role, image := range selection.Images {
		result.Images[role] = image
	}
	for repository, image := range state.Images {
		if !strings.HasPrefix(repository, "ghcr.io/"+state.Repository+"/") || environmentImageRepository(image.Reference) != repository || !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Publication == nil || image.Publication.Repository != state.Repository || image.Publication.RunID <= 0 || image.Publication.Attempt <= 0 || !environmentPreflightDigest.MatchString(image.Publication.ManifestSHA) {
			return environmentImageSelection{}, errors.New("CI rollout: invalid completed image state")
		}
		matched := false
		for role, expected := range roles {
			if expected == repository {
				result.Images[role] = image
				matched = true
			}
		}
		if !matched {
			return environmentImageSelection{}, errors.New("CI rollout: completed image has no environment role")
		}
	}
	return result, nil
}

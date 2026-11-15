package main

import (
	"errors"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutBatchStep struct {
	Target ciRolloutTarget
	Images map[string]string
}

func ciRolloutImageRepositories(plan *deploymentplan.Plan, repo string) ([]string, error) {
	if plan == nil || plan.Environment != "dev" || !ciRepositoryName.MatchString(repo) {
		return nil, errors.New("CI rollout: selected dev plan and repository required")
	}
	known := map[string]bool{}
	for _, repository := range environmentVersionRoles() {
		if strings.HasPrefix(repository, "ghcr.io/"+repo+"/") {
			known[repository] = true
		}
	}
	found := map[string]bool{}
	for _, resource := range plan.Resources {
		object := resource.Desired
		if deploymentplan.String(object["kind"]) != "Deployment" || resource.Action == "retire" {
			continue
		}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		for _, entry := range containers {
			image := deploymentplan.String(deploymentplan.Map(entry)["image"])
			repository := environmentImageRepository(image)
			if known[repository] {
				if !immutableEnvironmentImage.MatchString(image) {
					return nil, errors.New("CI rollout: existing workload image is not immutable")
				}
				found[repository] = true
			}
		}
	}
	result := make([]string, 0, len(found))
	for repository := range found {
		result = append(result, repository)
	}
	if len(result) == 0 {
		return nil, errors.New("CI rollout: no selected repository workloads")
	}
	sort.Strings(result)
	return result, nil
}

// All images in a repository event must come from the same successful CI
// publication. Steps retain the environment plan's order and group containers
// that share a Deployment into one Pod-template update.
func ciRolloutBatchSteps(plan *deploymentplan.Plan, policy ciBranchSelection, repo, branch string, images map[string]environmentReleaseImage) ([]ciRolloutBatchStep, error) {
	if plan == nil || plan.Environment != "dev" || !policy.matches(repo, branch) || len(images) == 0 {
		return nil, errors.New("CI rollout: selected dev plan, branch and images required")
	}
	supported := map[string]bool{}
	for _, r := range environmentVersionRoles() {
		if strings.HasPrefix(r, "ghcr.io/"+repo+"/") {
			supported[r] = true
		}
	}
	var revision, manifest string
	var runID int64
	var attempt int
	for repository, image := range images {
		p := image.Publication
		if !supported[repository] || environmentImageRepository(image.Reference) != repository || !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || p == nil || p.Repository != repo || p.Branch != branch || p.RunID <= 0 || p.Attempt <= 0 || !environmentPreflightDigest.MatchString(p.ManifestSHA) {
			return nil, errors.New("CI rollout: invalid selected publication")
		}
		if revision == "" {
			revision, manifest, runID, attempt = image.SourceRevision, p.ManifestSHA, p.RunID, p.Attempt
		} else if revision != image.SourceRevision || manifest != p.ManifestSHA || runID != p.RunID || attempt != p.Attempt {
			return nil, errors.New("CI rollout: images do not belong to one source and publication")
		}
	}
	var steps []ciRolloutBatchStep
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, resource := range plan.Resources {
		object := resource.Desired
		if deploymentplan.String(object["kind"]) != "Deployment" || deploymentplan.String(object["apiVersion"]) != "apps/v1" || resource.Action == "retire" {
			continue
		}
		metadata := deploymentplan.Map(object["metadata"])
		namespace, name := deploymentplan.String(metadata["namespace"]), deploymentplan.String(metadata["name"])
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		step := ciRolloutBatchStep{Target: ciRolloutTarget{Namespace: namespace, Name: name, UID: resource.UID}, Images: map[string]string{}}
		for _, entry := range containers {
			c := deploymentplan.Map(entry)
			container := deploymentplan.String(c["name"])
			repository := environmentImageRepository(deploymentplan.String(c["image"]))
			image, ok := images[repository]
			if !ok {
				continue
			}
			if container == "" || step.Images[container] != "" {
				return nil, errors.New("CI rollout: ambiguous selected container")
			}
			step.Images[container] = image.Reference
			used[repository] = true
		}
		if len(step.Images) == 0 {
			continue
		}
		id := namespace + "/" + name
		if namespace == "" || name == "" || resource.UID == "" || seen[id] {
			return nil, errors.New("CI rollout: invalid workload identity")
		}
		seen[id] = true
		steps = append(steps, step)
	}
	if len(steps) == 0 || len(used) != len(images) {
		return nil, errors.New("CI rollout: selected images do not match environment workloads")
	}
	return steps, nil
}

package main

import (
	"errors"
	"strings"
)

// ciRolloutRun and ciRolloutJob describe authenticated GitHub API observations.
// A webhook payload alone must never be treated as one of these observations.
type ciRolloutRun struct {
	Repository, Workflow, Event, Branch, Revision, Status, Conclusion string
	ID                                                                int64
	Attempt                                                           int
}

type ciRolloutJob struct {
	Name, Status, Conclusion string
}

// selectCIRolloutImage verifies the environment-selected branch, the current
// branch head, the successful CI and publication runs, and the exact immutable
// artifact. Its caller is responsible for obtaining these independently from
// the CI provider and for rechecking branch head before any mutation.
func selectCIRolloutImage(policy ciBranchSelection, branchHead string, ci, publication ciRolloutRun, jobs []ciRolloutJob, imageRepository string, manifest []byte) (environmentReleaseImage, error) {
	bad := func() (environmentReleaseImage, error) {
		return environmentReleaseImage{}, errors.New("CI rollout requires the environment-selected branch and its current, successful CI and image publication")
	}
	if !policy.matches(ci.Repository, ci.Branch) || !deploymentUpgradeSHA.MatchString(branchHead) || branchHead != ci.Revision || ci.Workflow != ".github/workflows/ci.yml" || (ci.Event != "push" && ci.Event != "workflow_dispatch") || ci.Status != "completed" || ci.Conclusion != "success" || ci.ID <= 0 || ci.Attempt <= 0 {
		return bad()
	}
	if publication.Repository != ci.Repository || publication.Branch != ci.Branch || publication.Revision != ci.Revision || publication.Workflow != ".github/workflows/release.yml" || (publication.Event != "push" && publication.Event != "workflow_dispatch") || publication.Status != "completed" || publication.Conclusion != "success" || publication.ID <= 0 || publication.Attempt <= 0 {
		return bad()
	}
	if imageRepository == "" || !strings.HasPrefix(imageRepository, "ghcr.io/"+ci.Repository+"/") {
		return bad()
	}
	published := 0
	for _, job := range jobs {
		if job.Name == "Publish LKE image" {
			if job.Status != "completed" || job.Conclusion != "success" {
				return bad()
			}
			published++
		}
	}
	if published != 1 {
		return bad()
	}
	image, err := selectEnvironmentPublishedImage(manifest, ci.Repository, publication.ID, publication.Attempt, ci.Revision, imageRepository, false)
	if err != nil {
		return environmentReleaseImage{}, err
	}
	image.Publication = &createVersionPublication{Repository: ci.Repository, Branch: ci.Branch, RunID: publication.ID, Attempt: publication.Attempt, ManifestSHA: environmentPreflightBytesSHA256(manifest)}
	return image, nil
}

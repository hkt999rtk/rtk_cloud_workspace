package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

var errCIRolloutNotReady = errors.New("CI rollout: selected branch is waiting for successful CI and publication")

// Both CI and publication events trigger the workflow. Missing or still
// running evidence is normal until the other event arrives.
func ciRolloutSuccessfulRunForBranch(raw []byte, revision, branch string) (int64, error) {
	var result struct {
		Total int `json:"total_count"`
		Runs  []struct {
			ID         int64  `json:"id"`
			Revision   string `json:"head_sha"`
			Branch     string `json:"head_branch"`
			Event      string `json:"event"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Runs == nil || result.Total > len(result.Runs) {
		return 0, errors.New("CI rollout: incomplete workflow run inventory")
	}
	for _, run := range result.Runs {
		if run.Revision != revision || run.Branch != branch || (run.Event != "push" && run.Event != "workflow_dispatch") {
			continue
		}
		if run.ID <= 0 || run.Status != "completed" || run.Conclusion != "success" {
			return 0, errCIRolloutNotReady
		}
		return run.ID, nil
	}
	return 0, errCIRolloutNotReady
}

// resolveCIRolloutImage reads authenticated provider state; event payloads do
// not confer authority to select a revision or image.
func resolveCIRolloutImage(ctx context.Context, policy ciBranchSelection, repo, imageRepository string, run environmentReleaseCommand) (environmentReleaseImage, error) {
	bad := func() (environmentReleaseImage, error) {
		return environmentReleaseImage{}, errors.New("CI rollout: selected branch, successful CI and exact publication are unavailable")
	}
	branch := policy[repo]
	if !policy.matches(repo, branch) || !ciRepositoryName.MatchString(repo) || !validCIBranchName(branch) || run == nil {
		return bad()
	}
	get := func(endpoint string) ([]byte, error) { return run(ctx, "gh", "api", "repos/"+repo+"/"+endpoint) }
	readHead := func() (string, error) {
		raw, err := get("commits/" + url.PathEscape(branch))
		if err != nil {
			return "", err
		}
		var value struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(raw, &value) != nil || !deploymentUpgradeSHA.MatchString(value.SHA) {
			return "", errors.New("invalid branch head")
		}
		return value.SHA, nil
	}
	head, err := readHead()
	if err != nil {
		return bad()
	}
	readRun := func(workflow string) (ciRolloutRun, error) {
		var result ciRolloutRun
		raw, err := get("actions/workflows/" + workflow + "/runs?head_sha=" + head + "&per_page=100")
		if err != nil {
			return result, err
		}
		id, err := ciRolloutSuccessfulRunForBranch(raw, head, branch)
		if err != nil {
			return result, err
		}
		raw, err = get(fmt.Sprintf("actions/runs/%d", id))
		if err != nil {
			return result, err
		}
		var value struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			HeadBranch string `json:"head_branch"`
			Path       string `json:"path"`
			Event      string `json:"event"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Attempt    int    `json:"run_attempt"`
		}
		if json.Unmarshal(raw, &value) != nil || value.ID != id {
			return result, errors.New("invalid exact run")
		}
		return ciRolloutRun{Repository: repo, Workflow: value.Path, Event: value.Event, Branch: value.HeadBranch, Revision: value.HeadSHA, Status: value.Status, Conclusion: value.Conclusion, ID: value.ID, Attempt: value.Attempt}, nil
	}
	ci, err := readRun("ci.yml")
	if err != nil {
		if errors.Is(err, errCIRolloutNotReady) {
			return environmentReleaseImage{}, err
		}
		return bad()
	}
	publication, err := readRun("release.yml")
	if err != nil {
		if errors.Is(err, errCIRolloutNotReady) {
			return environmentReleaseImage{}, err
		}
		return bad()
	}
	raw, err := get(fmt.Sprintf("actions/runs/%d/jobs?per_page=100", publication.ID))
	if err != nil {
		return bad()
	}
	var listing struct {
		Total int `json:"total_count"`
		Jobs  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	if json.Unmarshal(raw, &listing) != nil || listing.Jobs == nil || listing.Total != len(listing.Jobs) {
		return bad()
	}
	jobs := make([]ciRolloutJob, 0, len(listing.Jobs))
	for _, job := range listing.Jobs {
		jobs = append(jobs, ciRolloutJob{Name: job.Name, Status: job.Status, Conclusion: job.Conclusion})
	}
	manifest, attempt, err := readEnvironmentPublication(ctx, repo, publication.ID, head, false, run)
	if err != nil {
		return environmentReleaseImage{}, err
	}
	if attempt != publication.Attempt {
		return bad()
	}
	selected, err := selectCIRolloutImage(policy, head, ci, publication, jobs, imageRepository, manifest)
	if err != nil {
		return environmentReleaseImage{}, err
	}
	finalHead, err := readHead()
	if err != nil || finalHead != head {
		return bad()
	}
	return selected, nil
}

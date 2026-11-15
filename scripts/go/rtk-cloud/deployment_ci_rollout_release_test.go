package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestCIRolloutReleaseRequiresSelectedBranchAndExactSuccessfulRuns(t *testing.T) {
	commit := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepository := "ghcr.io/" + repo + "/frontend"
	policy := ciBranchSelection{repo: "feature/design"}
	ci := ciRolloutRun{Repository: repo, Workflow: ".github/workflows/ci.yml", Event: "push", Branch: "feature/design", Revision: commit, Status: "completed", Conclusion: "success", ID: 11, Attempt: 1}
	publish := ciRolloutRun{Repository: repo, Workflow: ".github/workflows/release.yml", Event: "workflow_dispatch", Branch: "feature/design", Revision: commit, Status: "completed", Conclusion: "success", ID: 22, Attempt: 1}
	jobs := []ciRolloutJob{{Name: "Publish LKE image", Status: "completed", Conclusion: "success"}}
	manifest := []byte(fmt.Sprintf(`{"schema":"rtk-service-image/v1","repository":%q,"image":%q,"digest":%q,"source_commit":%q,"run_id":"22","run_attempt":"1"}`, repo, imageRepository, digest, commit))
	if got, err := selectCIRolloutImage(policy, commit, ci, publish, jobs, imageRepository, manifest); err != nil || got.Reference != imageRepository+"@"+digest || got.SourceRevision != commit || got.Publication == nil || got.Publication.Branch != "feature/design" {
		t.Fatal("selected CI and publication must resolve the exact immutable image", got, err)
	}
	for _, mode := range []string{"unselected-branch", "head-advanced", "ci-failed", "ci-pr", "ci-foreign-workflow", "publication-mismatch", "publication-pending", "job-missing", "job-failed", "wrong-image", "wrong-manifest-run"} {
		t.Run(mode, func(t *testing.T) {
			current, candidateCI, candidatePublish := commit, ci, publish
			candidateJobs := append([]ciRolloutJob(nil), jobs...)
			candidateImage, candidateManifest := imageRepository, append([]byte(nil), manifest...)
			switch mode {
			case "unselected-branch":
				candidateCI.Branch = "main"
			case "head-advanced":
				current = strings.Repeat("c", 40)
			case "ci-failed":
				candidateCI.Conclusion = "failure"
			case "ci-pr":
				candidateCI.Event = "pull_request"
			case "ci-foreign-workflow":
				candidateCI.Workflow = ".github/workflows/other.yml"
			case "publication-mismatch":
				candidatePublish.Revision = strings.Repeat("c", 40)
			case "publication-pending":
				candidatePublish.Status = "in_progress"
			case "job-missing":
				candidateJobs = nil
			case "job-failed":
				candidateJobs[0].Conclusion = "failure"
			case "wrong-image":
				candidateImage += "-foreign"
			case "wrong-manifest-run":
				candidateManifest = []byte(strings.Replace(string(manifest), `"run_id":"22"`, `"run_id":"23"`, 1))
			}
			if _, err := selectCIRolloutImage(policy, current, candidateCI, candidatePublish, candidateJobs, candidateImage, candidateManifest); err == nil {
				t.Fatal("unverified CI or image acquired rollout authority")
			}
		})
	}
}

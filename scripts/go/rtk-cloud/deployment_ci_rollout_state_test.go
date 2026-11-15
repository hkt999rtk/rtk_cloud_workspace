package main

import (
	"strings"
	"testing"
)

func TestCIRolloutStateProjectsCompletedBranchDigestIntoEnvironmentRelease(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	old := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("a", 64)}
	selected := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_FRONTEND_IMAGE": old}}
	next := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("c", 40), Publication: &createVersionPublication{Repository: repo, RunID: 33, Attempt: 1, ManifestSHA: strings.Repeat("d", 64)}}
	state := ciRolloutState{Environment: "dev", Repository: repo, Branch: "main", Images: map[string]environmentReleaseImage{imageRepo: next}}
	got, err := projectCIRolloutState(selected, ciBranchSelection{repo: "main"}, state)
	if err != nil || got.Images["LKE_FRONTEND_IMAGE"].Reference != next.Reference || selected.Images["LKE_FRONTEND_IMAGE"].Reference != old.Reference {
		t.Fatalf("completed CI version not projected safely: %#v %v", got, err)
	}
	got, err = projectCIRolloutState(selected, ciBranchSelection{}, state)
	if err != nil || got.Images["LKE_FRONTEND_IMAGE"].Reference != old.Reference {
		t.Fatal("unselected repository changed environment version", err)
	}
	state.Environment = "staging"
	if _, err = projectCIRolloutState(selected, ciBranchSelection{repo: "main"}, state); err == nil {
		t.Fatal("other environment state accepted")
	}
}

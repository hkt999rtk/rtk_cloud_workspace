package main

import (
	"context"
	"strings"
	"testing"
)

func TestCIRolloutSelectionsKeepUntrackedEnvironmentSettings(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	next := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40), Publication: &createVersionPublication{Repository: repo, RunID: 1, Attempt: 1, ManifestSHA: strings.Repeat("c", 64)}}
	original := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_ACCOUNT_MANAGER_IMAGE": {Reference: "unchanged"}}}
	read := func(_ context.Context, requested string) (*ciRolloutState, error) {
		if requested != repo {
			t.Fatalf("unexpected repository %s", requested)
		}
		return &ciRolloutState{Environment: "dev", Repository: repo, Branch: "main", Images: map[string]environmentReleaseImage{imageRepo: next}}, nil
	}
	got, err := applyEnvironmentCIRolloutStates(context.Background(), original, ciBranchSelection{repo: "main"}, read)
	if err != nil || got.Images["LKE_FRONTEND_IMAGE"].Reference != next.Reference || got.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference != "unchanged" || len(original.Images) != 1 {
		t.Fatalf("environment selection changed unexpectedly: %#v %v", got, err)
	}
}

func TestFreshDevResolvesConfiguredBranchWithoutReadingOldGeneration(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	original := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_ACCOUNT_MANAGER_IMAGE": {Reference: "unchanged"}}}
	expected := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	resolver := func(_ context.Context, policy ciBranchSelection, repository, image string) (environmentReleaseImage, error) {
		if !policy.matches(repo, "main") || repository != repo || image != imageRepo {
			t.Fatalf("unexpected selected branch %s %s", repository, image)
		}
		return expected, nil
	}
	got, err := resolveFreshCIBranchSelection(context.Background(), original, ciBranchSelection{repo: "main"}, resolver)
	if err != nil || got.Images["LKE_FRONTEND_IMAGE"].Reference != expected.Reference || got.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference != "unchanged" || len(original.Images) != 1 {
		t.Fatalf("fresh selection: %#v %v", got, err)
	}
}

func TestCIRolloutSelectionsRejectUnavailableOrForeignState(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	original := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{}}
	if _, err := applyEnvironmentCIRolloutStates(context.Background(), original, ciBranchSelection{repo: "main"}, nil); err == nil {
		t.Fatal("missing state reader accepted")
	}
	read := func(context.Context, string) (*ciRolloutState, error) {
		return &ciRolloutState{Environment: "dev", Repository: "foreign/repository"}, nil
	}
	if _, err := applyEnvironmentCIRolloutStates(context.Background(), original, ciBranchSelection{repo: "main"}, read); err == nil {
		t.Fatal("another repository's version state accepted")
	}
	if _, err := resolveFreshCIBranchSelection(context.Background(), original, ciBranchSelection{repo: "main"}, nil); err == nil {
		t.Fatal("missing fresh generation resolver accepted")
	}
}

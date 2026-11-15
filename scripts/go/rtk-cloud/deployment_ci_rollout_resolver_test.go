package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResolveCIRolloutFromAuthenticatedRuns(t *testing.T) {
	repo, branch := "hkt999rtk/rtk_cloud_frontend", "main"
	revision, digest := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
	imageRepo := "ghcr.io/" + repo + "/frontend"
	manifest := []byte(fmt.Sprintf(`{"schema":"rtk-service-image/v1","repository":%q,"image":%q,"digest":%q,"source_commit":%q,"run_id":"22","run_attempt":"1"}`, repo, imageRepo, digest, revision))
	archive := publicationZIP(t, "image-manifest.json", manifest)
	runJSON := func(id int, workflow string) []byte {
		return []byte(fmt.Sprintf(`{"id":%d,"head_sha":%q,"head_branch":%q,"path":%q,"event":"push","status":"completed","conclusion":"success","run_attempt":1}`, id, revision, branch, ".github/workflows/"+workflow))
	}
	responses := map[string][]byte{
		"repos/" + repo + "/commits/" + branch:                                                         []byte(fmt.Sprintf(`{"sha":%q}`, revision)),
		"repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + revision + "&per_page=100":      []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(11, "ci.yml"))),
		"repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + revision + "&per_page=100": []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(22, "release.yml"))),
		"repos/" + repo + "/actions/runs/11":                                                           runJSON(11, "ci.yml"),
		"repos/" + repo + "/actions/runs/22":                                                           runJSON(22, "release.yml"),
		"repos/" + repo + "/actions/runs/22/jobs?per_page=100":                                         []byte(`{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`),
		"repos/" + repo + "/actions/runs/22/artifacts?per_page=100":                                    []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":33,"name":"image-manifest","expired":false,"size_in_bytes":%d}]}`, len(archive))),
		"repos/" + repo + "/actions/artifacts/33/zip":                                                  archive,
	}
	run := func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command != "gh" || len(args) != 2 || args[0] != "api" {
			return nil, fmt.Errorf("unexpected command")
		}
		body, ok := responses[args[1]]
		if !ok {
			return nil, fmt.Errorf("missing authenticated response %s", args[1])
		}
		return body, nil
	}
	policy := ciBranchSelection{repo: branch}
	got, err := resolveCIRolloutImage(context.Background(), policy, repo, imageRepo, run)
	if err != nil || got.Reference != imageRepo+"@"+digest || got.Publication == nil || got.Publication.RunID != 22 {
		t.Fatalf("expected exact published image: %#v, %v", got, err)
	}
	tracked, err := verifyCIRolloutImageProvenance(context.Background(), policy, "LKE_FRONTEND_IMAGE", got, run)
	if err != nil || !tracked {
		t.Fatalf("tracked release must verify exact branch publication: %v", err)
	}
	foreign := got
	publicationCopy := *got.Publication
	publicationCopy.Branch = "feature/other"
	foreign.Publication = &publicationCopy
	if _, err = verifyCIRolloutImageProvenance(context.Background(), policy, "LKE_FRONTEND_IMAGE", foreign, run); err == nil {
		t.Fatal("different branch publication accepted")
	}
	releaseRuns := "repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + revision + "&per_page=100"
	savedRuns := responses[releaseRuns]
	responses[releaseRuns] = []byte(`{"total_count":0,"workflow_runs":[]}`)
	if _, err = resolveCIRolloutImage(context.Background(), policy, repo, imageRepo, run); !errors.Is(err, errCIRolloutNotReady) {
		t.Fatalf("pending publication must be reported as not ready: %v", err)
	}
	responses[releaseRuns] = savedRuns
	responses["repos/"+repo+"/actions/runs/11"] = []byte(strings.Replace(string(runJSON(11, "ci.yml")), `"conclusion":"success"`, `"conclusion":"failure"`, 1))
	if _, err = resolveCIRolloutImage(context.Background(), policy, repo, imageRepo, run); err == nil {
		t.Fatal("failed exact CI run accepted")
	}
	responses["repos/"+repo+"/actions/runs/11"] = runJSON(11, "ci.yml")
	responses["repos/"+repo+"/actions/runs/22/jobs?per_page=100"] = []byte(`{"total_count":2,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`)
	if _, err = resolveCIRolloutImage(context.Background(), policy, repo, imageRepo, run); err == nil {
		t.Fatal("incomplete job list accepted")
	}
	var valid any
	if json.Unmarshal(manifest, &valid) != nil {
		t.Fatal("invalid fixture")
	}
}

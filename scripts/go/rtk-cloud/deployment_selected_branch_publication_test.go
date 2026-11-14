package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSelectedFormalBranchPublicationKeepsExactEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "workflow", "event", "empty-branch", "ci-failed", "ci-pending-before-green", "ci-missing", "ci-source", "ci-branch", "ci-private-error", "digest", "attempt", "backup"} {
		t.Run(mode, func(t *testing.T) {
			const repo = "hkt999rtk/rtk_video_cloud"
			const branch = "codex/dev-public-crl-export"
			sha := strings.Repeat("a", 40)
			image := environmentReleaseImage{Reference: "ghcr.io/" + repo + "/video-cloud-api@sha256:" + strings.Repeat("b", 64), SourceRevision: sha}
			source := map[string]any{"id": int64(77), "head_sha": sha, "head_branch": branch, "path": ".github/workflows/release.yml", "event": "workflow_dispatch", "status": "completed", "conclusion": "success", "run_attempt": 2}
			manifest := map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": "2", "image": environmentImageRepository(image.Reference), "digest": "sha256:" + strings.Repeat("b", 64)}
			ci := map[string]any{"id": int64(76), "head_sha": sha, "head_branch": branch, "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}
			runs := []any{ci}
			switch mode {
			case "workflow":
				source["path"] = ".github/workflows/other.yml"
			case "event":
				source["event"] = "pull_request"
			case "empty-branch":
				source["head_branch"] = ""
			case "ci-failed":
				ci["conclusion"] = "failure"
			case "ci-pending-before-green":
				runs = []any{map[string]any{"id": int64(78), "head_sha": sha, "head_branch": branch, "event": "workflow_dispatch", "status": "in_progress", "conclusion": ""}, ci}
			case "ci-missing":
				runs = []any{}
			case "ci-source":
				ci["head_sha"] = strings.Repeat("c", 40)
			case "ci-branch":
				ci["head_branch"] = "other"
			case "digest":
				manifest["digest"] = "sha256:" + strings.Repeat("c", 64)
			case "attempt":
				manifest["run_attempt"] = "1"
			}
			raw, _ := json.Marshal(manifest)
			archive := publicationZIP(t, "image-manifest.json", raw)
			ciReads := 0
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				if command != "gh" || len(args) != 2 || args[0] != "api" {
					t.Fatal("unexpected command")
					return nil, nil
				}
				var value any
				switch args[1] {
				case "repos/" + repo + "/actions/runs/77":
					value = source
				case "repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + sha + "&per_page=100":
					ciReads++
					if mode == "ci-private-error" {
						return nil, errors.New("token=never-print")
					}
					value = map[string]any{"total_count": len(runs), "workflow_runs": runs}
				case "repos/" + repo + "/actions/runs/77/artifacts?per_page=100":
					value = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 99, "name": "image-manifest", "size_in_bytes": len(archive), "expired": false}}}
				case "repos/" + repo + "/actions/artifacts/99/zip":
					return archive, nil
				default:
					t.Fatal("publication escaped exact repository/source", args)
					return nil, nil
				}
				return json.Marshal(value)
			}
			ctx := newEnvironmentReleaseProofCapture(context.Background())
			proof, err := verifyEnvironmentPublication(ctx, repo, 77, image, mode == "backup", run)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if mode == "valid" {
				if len(proof) != 64 || ciReads != 1 {
					t.Fatal("exact branch CI or digest evidence missing")
				}
				proofs := snapshotVerifiedReleaseProofs(ctx, environmentRelease{Images: map[string]environmentReleaseImage{"LKE_PKI_MANAGEMENT_IMAGE": image}})
				if len(proofs) != 1 || proofs[0].RunID != 77 || proofs[0].RunAttempt != 2 || proofs[0].ManifestSHA256 != proof {
					t.Fatal("selected formal branch proof was not captured")
				}
			}
			if err != nil && strings.Contains(err.Error(), "never-print") {
				t.Fatal("private CI failure leaked")
			}
		})
	}
}

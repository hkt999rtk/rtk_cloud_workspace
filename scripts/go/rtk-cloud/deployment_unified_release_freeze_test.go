package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreezeLatestReleaseUsesFrozenGitlinksAndExactPublicationForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "root-dirty", "old-main", "leaf-dirty", "source-drift", "CI-pending", "MQTT-missing", "old-attempt", "backup-base", "mutable-provider", "fetch-failed", "private-error"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				_, _, providers := completeEnvironmentRenderFixture(environment)
				rootSHA, leafSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
				if mode == "mutable-provider" {
					providers.Images["LKE_POSTGRES_IMAGE"] = environmentReleaseImage{Reference: "postgres:16-alpine"}
				}
				cfg := deploymentConfig{Workspace: "/frozen", Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
				sourceChecks := map[string]int{}
				gitlinks := 0
				networkStarted := false
				refs := map[string]string{}
				run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
					if mode == "private-error" {
						return nil, errors.New("credential=never-public")
					}
					if command == "git" {
						directory, operation := args[1], args[2]
						switch operation {
						case "rev-parse":
							sha := leafSHA
							if directory == cfg.Workspace {
								sha = rootSHA
							}
							if args[3] == "origin/main" && mode == "old-main" {
								sha = strings.Repeat("c", 40)
							}
							return []byte(sha + "\n"), nil
						case "status":
							sourceChecks[directory]++
							if mode == "root-dirty" && directory == cfg.Workspace || mode == "leaf-dirty" && directory != cfg.Workspace || mode == "source-drift" && networkStarted {
								return []byte(" M source.go\n"), nil
							}
							return nil, nil
						case "fetch":
							if mode == "fetch-failed" {
								return nil, errors.New("private-fetch")
							}
							return nil, nil
						case "ls-tree":
							gitlinks++
							return []byte("160000 commit " + leafSHA + "\t" + args[5] + "\n"), nil
						}
					}
					if command != "gh" || len(args) != 2 || args[0] != "api" {
						t.Fatal("selection used an unfrozen tag or unknown command", command, args)
					}
					if !networkStarted {
						if gitlinks != len(lkeServiceImageSources()) || len(sourceChecks) != gitlinks+1 {
							t.Fatal("CI requested before all sources were frozen")
						}
						networkStarted = true
					}
					endpoint := args[1]
					parts := strings.Split(endpoint, "/")
					repo := parts[1] + "/" + parts[2]
					backup := repo == "hkt999rtk/rtk_cloud_workspace"
					sha := leafSHA
					if backup {
						sha = rootSHA
					}
					name, file := "image-manifest", "image-manifest.json"
					if backup {
						name, file = "postgres-backup-runner-images", "images.json"
					}
					if strings.Contains(endpoint, "/workflows/") {
						status, conclusion := "completed", "success"
						if mode == "CI-pending" {
							status, conclusion = "in_progress", ""
						}
						raw, _ := json.Marshal(map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 77, "head_sha": sha, "head_branch": "main", "event": "push", "status": status, "conclusion": conclusion}}})
						return raw, nil
					}
					if strings.HasSuffix(endpoint, "/runs/77") {
						return []byte(`{"id":77,"head_sha":"` + sha + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`), nil
					}
					if strings.HasSuffix(endpoint, "/artifacts?per_page=100") {
						return []byte(`{"total_count":1,"artifacts":[{"id":99,"name":"` + name + `","size_in_bytes":800,"expired":false}]}`), nil
					}
					if !strings.HasSuffix(endpoint, "/artifacts/99/zip") {
						t.Fatal("wrong publication identity")
					}
					attempt := "2"
					if mode == "old-attempt" {
						attempt = "1"
					}
					digest := "sha256:" + strings.Repeat("d", 64)
					manifest := map[string]any{"schema": "rtk-service-image/v1", "version": 1, "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": attempt}
					if backup {
						manifest["runner_tag"] = "ghcr.io/" + repo + "/postgres-backup:sha-" + sha
						manifest["runner_digest"] = digest
						manifest["source_postgres_image"] = providers.Images["LKE_POSTGRES_IMAGE"].Reference
						if mode == "backup-base" {
							manifest["source_postgres_image"] = "postgres@sha256:" + strings.Repeat("e", 64)
						}
						refs["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"] = "ghcr.io/" + repo + "/postgres-backup@" + digest
					} else {
						for _, source := range lkeServiceImageSources() {
							if "hkt999rtk/"+source.RepoName == repo {
								manifest["image"] = "ghcr.io/" + repo + "/" + source.Name
								manifest["digest"] = digest
								refs[source.EnvKey] = manifest["image"].(string) + "@" + digest
							}
						}
						if repo == "hkt999rtk/rtk_video_cloud" && mode != "MQTT-missing" {
							manifest["images"] = map[string]any{"emqx_pki": map[string]any{"image": "ghcr.io/" + repo + "/video-cloud-emqx-pki", "digest": digest}}
							refs["LKE_MQTT_PKI_IMAGE"] = "ghcr.io/" + repo + "/video-cloud-emqx-pki@" + digest
						}
					}
					raw, _ := json.Marshal(manifest)
					return publicationZIP(t, file, raw), nil
				}
				release, err := freezeEnvironmentRelease(context.Background(), cfg, providers, run)
				if (err == nil) != (mode == "valid") {
					t.Fatal("release decision differs", mode, err)
				}
				if err != nil && strings.Contains(err.Error(), "never-public") {
					t.Fatal("private source error escaped")
				}
				if mode == "valid" {
					if release.WorkspaceRevision != rootSHA || release.Environment != environment || len(refs) != 8 {
						t.Fatal("incomplete frozen release")
					}
					for key, reference := range refs {
						image := release.Images[key]
						wantSHA := leafSHA
						if key == "LKE_POSTGRES_BACKUP_RUNNER_IMAGE" {
							wantSHA = rootSHA
						}
						if image.Reference != reference || image.SourceRevision != wantSHA {
							t.Fatal("consumer publication does not match frozen source", key)
						}
					}
					for directory, count := range sourceChecks {
						if count != 2 {
							t.Fatal("source not fenced after publication", directory, count)
						}
					}
				}
			})
		}
	}
}
func TestFrozenReleaseDestinationNeverOverwritesExistingSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.json")
	if err := writeExclusiveEnvironmentRelease(path, []byte("reviewed release")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("release permissions")
	}
	if err := writeExclusiveEnvironmentRelease(path, []byte("replacement")); err == nil {
		t.Fatal("overwrote reviewed release")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "reviewed release" {
		t.Fatal("prior selection changed")
	}
	link := filepath.Join(t.TempDir(), "release.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if writeExclusiveEnvironmentRelease(link, []byte("redirect")) == nil {
		t.Fatal("followed destination symlink")
	}
}

func TestFrozenReleaseUsesOnlySelectedEnvironmentPrivateDirectory(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			workspace := writeDeploymentFixture(t, environment, "lke")
			for _, path := range []string{
				filepath.Join(t.TempDir(), "release.json"),
				filepath.Join(workspace, "cloud_env", environment, "runtime", "release.json"),
				filepath.Join(store.ConfigRoot, "other", "deployment", "releases", "release.json"),
				filepath.Join(store.Root, "pki", "release.json"),
				filepath.Join(store.Root, "deployment", "reports", "release.json"),
				filepath.Join(store.Root, "deployment", "releases", "release.txt"),
			} {
				if err := publishEnvironmentReleaseSelection(environment, path, []byte("fixture")); err == nil {
					t.Fatal("release escaped selected authority", path)
				}
				// Reject the destination before loading provider evidence or querying
				// GitHub; an invalid output is a usage error, not a publication failure.
				if err := runEnvironmentReleaseFreeze([]string{"--workspace", workspace, "--environment", environment, "--provider-release", "/missing-provider-evidence.json", "--output", path}); err != exitCode(2) {
					t.Fatal("invalid output reached publication verification", err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("rejected destination was written")
				}
			}
			path := filepath.Join(store.Root, "deployment", "releases", "candidate.json")
			if err := publishEnvironmentReleaseSelection(environment, path, []byte("original selection")); err != nil {
				t.Fatal(err)
			}
			for _, directory := range []string{store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
				if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("release directory was not private")
				}
			}
			if info, err := os.Lstat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("release was not private")
			}
			if err := publishEnvironmentReleaseSelection(environment, path, []byte("replacement")); err == nil {
				t.Fatal("release selection overwritten")
			}
			if raw, err := os.ReadFile(path); err != nil || string(raw) != "original selection" {
				t.Fatal("existing release changed")
			}
			if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := publishEnvironmentReleaseSelection(environment, filepath.Join(filepath.Dir(path), "public.json"), []byte("fixture")); err == nil {
				t.Fatal("public output directory accepted")
			}
			if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if err := publishEnvironmentReleaseSelection(environment, path, []byte("redirect")); err == nil {
				t.Fatal("followed output directory symlink")
			}
		})
	}
}

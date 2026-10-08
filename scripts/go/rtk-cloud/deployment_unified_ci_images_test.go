package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestDirectCIImagesUseFrozenGitlinksAndExactPublicationForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "backups-disabled", "root-dirty", "leaf-dirty", "source-drift", "CI-pending", "MQTT-missing", "host-published", "old-attempt", "backup-base", "mutable-provider", "legacy-file-invalid", "private-error"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				_, _, providers := completeEnvironmentRenderFixture(environment)
				legacyRoot := t.TempDir()
				t.Setenv("RTK_CLOUD_CONFIG_ROOT", legacyRoot)
				legacyPath := filepath.Join(legacyRoot, environment, "deployment", "release.json")
				if mode == "legacy-file-invalid" {
					if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(legacyPath, []byte("invalid retired file"), 0600); err != nil {
						t.Fatal(err)
					}
				}

				rootSHA, leafSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
				if mode == "mutable-provider" {
					providers.Images["LKE_POSTGRES_IMAGE"] = environmentReleaseImage{Reference: "postgres:16-alpine"}
				}
				cfg := deploymentConfig{Workspace: "/frozen", Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "DEPLOYMENT_POSTGRES_BACKUP_ENABLED": "true"}}
				for key, image := range providers.Images {
					if image.SourceRevision == "" {
						cfg.Values[environmentProviderImageConfigKey(key)] = image.Reference
					}
				}
				if mode == "backups-disabled" {
					cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] = "false"
				}
				sourceChecks := map[string]int{}
				gitlinks := 0
				networkStarted := false
				refs := map[string]string{}
				run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
					if mode == "private-error" {
						return nil, errors.New("credential=never-public")
					}
					if command == "docker" {
						return nil, errors.New("registry unavailable")
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
					if backup && mode == "backups-disabled" {
						t.Fatal("ordinary release selection requested backup publication")
					}
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
							if mode == "host-published" {
								manifest["images"].(map[string]any)["openbao_pki"] = map[string]any{"image": "ghcr.io/" + repo + "/video-cloud-openbao-pki", "digest": digest}
								refs["LKE_OPENBAO_PKI_IMAGE"] = "ghcr.io/" + repo + "/video-cloud-openbao-pki@" + digest
							}
						}
					}
					raw, _ := json.Marshal(manifest)
					return publicationZIP(t, file, raw), nil
				}
				release, err := loadEnvironmentCIImages(context.Background(), cfg, run)
				if (err == nil) != (mode == "valid" || mode == "host-published" || mode == "backups-disabled" || mode == "legacy-file-invalid") {
					t.Fatal("release decision differs", mode, err)
				}
				if err != nil && strings.Contains(err.Error(), "never-public") {
					t.Fatal("private source error escaped")
				}
				if mode == "backups-disabled" {
					if err != nil {
						t.Fatal(err)
					}
					if _, ok := release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"]; ok {
						t.Fatal("disabled backup image included")
					}
					return
				}
				if mode == "valid" || mode == "host-published" {
					for _, role := range environmentProviderImageKeys {
						if release.Images[role].Reference != cfg.Values[environmentProviderImageConfigKey(role)] {
							t.Fatal("neutral configuration did not reach the renderer image role", role)
						}
					}
					expectedRefs := 8
					if mode == "host-published" {
						expectedRefs++
					}
					if release.WorkspaceRevision != rootSHA || release.Environment != environment || len(refs) != expectedRefs {
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
					// Exercise the verifier used by real preflight against the complete
					// frozen release, including every shared-image consumer and backup.
					for _, verifyMode := range []string{"valid", "workspace-drift", "leaf-dirty", "gitlink-drift", "pull-failed", "OCI-drift", "CI-failed", "backup-source", "backup-base", "shared-source-drift", "typed-blocker", "private-error"} {
						t.Run("verify/"+verifyMode, func(t *testing.T) {
							candidate := release
							candidate.Images = map[string]environmentReleaseImage{}
							for key, image := range release.Images {
								candidate.Images[key] = image
							}
							if verifyMode == "workspace-drift" {
								candidate.WorkspaceRevision = strings.Repeat("e", 40)
							}
							if verifyMode == "backup-source" {
								image := candidate.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"]
								image.SourceRevision = leafSHA
								candidate.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"] = image
							}
							if verifyMode == "valid" {
								candidate.Images["LKE_REDIS_IMAGE"] = candidate.Images["LKE_FLEET_VALKEY_IMAGE"]
							}
							if verifyMode == "shared-source-drift" {
								image := candidate.Images["LKE_VIDEO_CLOUD_IMAGE"]
								image.SourceRevision = strings.Repeat("e", 40)
								candidate.Images["LKE_ZZZ_SHARED_SOURCE_IMAGE"] = image
							}
							pulled := map[string]int{}
							originalBlocker := &deploymentplan.Blocker{ID: "deployment.release.publication", Resource: "LKE_ACCOUNT_MANAGER_IMAGE", Cause: "managed publication proof is unavailable", Next: "Restore the existing managed publication proof and repeat complete preflight."}
							verifyRun := func(ctx context.Context, command string, args ...string) ([]byte, error) {
								if err := ctx.Err(); err != nil {
									return nil, err
								}
								if command == "git" && args[2] == "status" && args[1] != cfg.Workspace && verifyMode == "leaf-dirty" {
									return []byte(" M source.go\n"), nil
								}
								if command == "git" && args[2] == "ls-tree" && verifyMode == "gitlink-drift" {
									return []byte("160000 commit " + strings.Repeat("e", 40) + "\t" + args[5] + "\n"), nil
								}
								if command == "docker" {
									if args[0] == "pull" {
										if len(args) != 4 || args[1] != "--platform" || args[2] != "linux/amd64" {
											t.Fatal("release pull did not select the qualified platform", args)
										}
										pulled[args[3]]++
										if verifyMode == "pull-failed" {
											return nil, errors.New("private-pull-output")
										}
										return nil, nil
									}
									sha, base := leafSHA, providers.Images["LKE_POSTGRES_IMAGE"].Reference
									if strings.Contains(args[2], "/postgres-backup@") {
										sha = rootSHA
										if verifyMode == "backup-base" {
											base = "postgres:unapproved"
										}
									}
									if verifyMode == "OCI-drift" {
										sha = strings.Repeat("e", 40)
									}
									return json.Marshal(map[string]string{"org.opencontainers.image.revision": sha, "org.opencontainers.image.base.name": base})
								}
								if command == "gh" {
									if verifyMode == "typed-blocker" {
										return nil, originalBlocker
									}
									if verifyMode == "private-error" {
										return nil, errors.New("credential=never-public")
									}
									if strings.Contains(args[1], "/jobs?") {
										return []byte(`{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`), nil
									}
									if verifyMode == "CI-failed" && strings.Contains(args[1], "/workflows/") {
										return []byte(`{"total_count":0,"workflow_runs":[]}`), nil
									}
								}
								return run(ctx, command, args...)
							}
							err := verifyEnvironmentReleaseWithCommand(context.Background(), cfg, candidate, verifyRun)
							if (err == nil) != (verifyMode == "valid") {
								t.Fatal("frozen release verification accepted drift or omitted evidence", err)
							}
							if verifyMode != "valid" {
								wantedID := "deployment.release.publication"
								switch verifyMode {
								case "workspace-drift", "leaf-dirty", "gitlink-drift", "shared-source-drift":
									wantedID = "deployment.release.source"
								case "pull-failed":
									wantedID = "deployment.release.pull"
								case "backup-source", "backup-base":
									wantedID = "deployment.release.backup"
								}
								var blocker *deploymentplan.Blocker
								if !errors.As(err, &blocker) || blocker.ID != wantedID || blocker.Cause == "" || blocker.Next == "" || blocker.Resource == "" {
									t.Fatal("release failure lost actionable static diagnostics", err)
								}
								if verifyMode == "workspace-drift" {
									if blocker.Resource != "workspace" {
										t.Fatal("workspace failure reported another resource", blocker)
									}
								} else if _, ok := candidate.Images[blocker.Resource]; !ok {
									t.Fatal("release failure did not identify its maintained image role", blocker)
								}
								if verifyMode == "typed-blocker" && err != originalBlocker {
									t.Fatal("existing release Blocker was replaced", err)
								}
								raw, _ := json.Marshal(blocker)
								if strings.Contains(string(raw), "never-public") || strings.Contains(string(raw), "private-pull-output") {
									t.Fatal("private command output became release diagnostics")
								}
							}
							if verifyMode == "valid" {
								shared := candidate.Images["LKE_FLEET_VALKEY_IMAGE"].Reference
								if shared == "" || candidate.Images["LKE_REDIS_IMAGE"].Reference != shared || pulled[shared] != 1 {
									t.Fatal("shared Redis/Fleet image was not pulled exactly once", pulled[shared])
								}
								for _, image := range candidate.Images {
									if pulled[image.Reference] != 1 {
										t.Fatal("release consumer image was not pulled exactly once", image.Reference, pulled[image.Reference])
									}
								}
								if err := verifyEnvironmentReleaseWithCommand(context.Background(), cfg, candidate, verifyRun); err != nil {
									t.Fatal("second release verification failed", err)
								}
								for _, image := range candidate.Images {
									if pulled[image.Reference] != 2 {
										t.Fatal("second invocation reused an earlier pull", image.Reference, pulled[image.Reference])
									}
								}
							}
							if verifyMode == "shared-source-drift" && pulled[candidate.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference] != 1 {
								t.Fatal("shared source drift was not checked after the original image pull")
							}
							if verifyMode == "private-error" {
								plan := &deploymentplan.Plan{}
								q, err := plan.Qualify(context.Background(), deploymentplan.QualificationOptions{}, map[string]deploymentplan.GateCheck{"deployment.release": func(ctx context.Context, _ *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
									return deploymentplan.GateEvidence{}, verifyEnvironmentReleaseWithCommand(ctx, cfg, candidate, verifyRun)
								}})
								raw, _ := json.Marshal(q)
								if err == nil || strings.Contains(string(raw), "never-public") {
									t.Fatal("private publisher error became public evidence")
								}
							}
						})
					}
				}
			})
		}
	}
}

func TestConfiguredProviderImagesResolveExactRegistryDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	for _, mode := range []string{"valid", "pull-failed", "wrong-repository", "ambiguous", "implicit-latest"} {
		t.Run(mode, func(t *testing.T) {
			reference := "postgres:16-alpine"
			if mode == "implicit-latest" {
				reference = "postgres"
			}
			calls := 0
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				calls++
				if command != "docker" {
					t.Fatal("provider resolution used an unexpected tool")
				}
				if args[0] == "pull" {
					if mode == "pull-failed" {
						return nil, errors.New("private registry error")
					}
					return nil, nil
				}
				refs := []string{"postgres@" + digest}
				if mode == "wrong-repository" {
					refs = []string{"redis@" + digest}
				}
				if mode == "ambiguous" {
					refs = append(refs, "postgres@sha256:"+strings.Repeat("e", 64))
				}
				return json.Marshal(refs)
			}
			selected, err := resolveConfiguredProviderImage(context.Background(), reference, run)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s result=%s err=%v", mode, selected, err)
			}
			if mode == "valid" && selected != "postgres@"+digest {
				t.Fatal("wrong registry digest")
			}
			if mode == "implicit-latest" && calls != 0 {
				t.Fatal("implicit latest was pulled")
			}
		})
	}
}

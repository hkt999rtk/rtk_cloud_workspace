package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectFailedForwardToolUsesJournalBoundMergedRelease(t *testing.T) {
	testSelectFailedForwardToolUsesJournalBoundMergedRelease(t, "")
}

func TestSelectOpenBaoPublicCRLToolUsesSeparateJournalBoundRelease(t *testing.T) {
	testSelectFailedForwardToolUsesJournalBoundMergedRelease(t, openBaoPublicCRLToolPurpose)
}

func testSelectFailedForwardToolUsesJournalBoundMergedRelease(t *testing.T, purpose string) {
	revision := strings.Repeat("a", 40)
	repository := "hkt999rtk/rtk_video_cloud"
	imageRepo := "ghcr.io/" + repository + "/video-cloud-api"
	manifest, _ := json.Marshal(map[string]string{"schema": "rtk-service-image/v1", "repository": repository, "source_commit": revision, "run_id": "77", "run_attempt": "2", "image": imageRepo, "digest": "sha256:" + strings.Repeat("b", 64)})
	archive := publicationZIP(t, "image-manifest.json", manifest)
	caps := []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`)
	modes := []string{"ok", "foreign-parent", "unmerged", "wrong-merge", "no-release", "wrong-manifest", "no-capability", "missing-journal", "wrong-files", "producer", "producer-pending", "producer-allocated", "auto-publish", "auto-no-release", "auto-unmerged", "auto-wrong-files", "auto-no-capability"}
	if purpose != "" {
		caps = []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2","initial-openbao-public-crl-custody-v1"]}`)
		modes = append(modes, "wrong-purpose", "no-openbao-capability")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			store, e, _, _ := freshStageFixture(t)
			evidence := createFailedForwardPublication{Purpose: purpose, Schema: failedForwardPublicationSchema, Environment: e.Journal.Environment, Parent: e.Journal.PlanSHA256, Repository: repository, PullRequest: 123, MergeCommit: revision, RunID: 77, RunAttempt: 2, ManifestSHA256: environmentPreflightBytesSHA256(manifest)}
			if mode == "foreign-parent" {
				evidence.Parent = strings.Repeat("c", 64)
			}
			if mode == "wrong-purpose" {
				evidence.Purpose = ""
			}
			raw := mustProviderRecoveryJSON(evidence)
			file := "public-crl-v2.json"
			if purpose != "" {
				file = "openbao-public-crl-v1.json"
			}
			relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "repair-evidence", file)
			if (!strings.HasPrefix(mode, "producer") && !strings.HasPrefix(mode, "auto-")) || mode == "producer-allocated" {
				if err := store.write(relative, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "missing-journal" && mode != "producer" && !strings.HasPrefix(mode, "auto-") {
				key := "failed-stage-tool-publication-" + failedForwardBytesDigestForTest(raw)
				if err := e.BeginEntry(key, "failed-stage-tool-publication", raw, time.Now()); err != nil {
					t.Fatal(err)
				}
				if mode != "producer-pending" {
					if err := e.RecordReceipt(key, raw); err != nil {
						t.Fatal(err)
					}
				}
				if !strings.HasPrefix(mode, "producer") {
					if err := e.CompleteEntry(key, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
			}
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				switch command {
				case "gh":
					if len(args) != 2 || args[0] != "api" {
						return nil, errors.New("unexpected")
					}
					target := args[1]
					if strings.Contains(target, "/actions/workflows/release.yml/runs?head_sha=") {
						if !strings.Contains(target, "head_sha="+revision) {
							t.Fatal("queried another revision")
						}
						if mode == "auto-no-release" {
							return []byte(`{"total_count":0,"workflow_runs":[]}`), nil
						}
						return []byte(`{"total_count":1,"workflow_runs":[{"id":77,"head_sha":"` + revision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}`), nil
					}
					if strings.HasSuffix(target, "/pulls/123") {
						if mode == "unmerged" || mode == "auto-unmerged" {
							return []byte(`{"merged":false}`), nil
						}
						commit := revision
						if mode == "wrong-merge" {
							commit = strings.Repeat("d", 40)
						}
						return []byte(fmt.Sprintf(`{"number":123,"state":"closed","merged":true,"merge_commit_sha":%q,"changed_files":4,"base":{"ref":"main","repo":{"full_name":%q}}}`, commit, repository)), nil
					}
					if strings.HasSuffix(target, "/pulls/123/files?per_page=100") {
						if mode == "wrong-files" || mode == "auto-wrong-files" {
							return []byte(`[{"filename":"bad"}]`), nil
						}
						return []byte(`[{"filename":"cmd/serviceidentity-bootstrap/main.go"},{"filename":"cmd/serviceidentity-bootstrap/custody_public_crl.go"},{"filename":"cmd/serviceidentity-bootstrap/custody_public_crl_test.go"},{"filename":"cmd/serviceidentity-bootstrap/custody_public_crl_domains_test.go"}]`), nil
					}
					if strings.HasSuffix(target, "/runs/77") {
						if mode == "no-release" {
							return []byte(`{}`), nil
						}
						return []byte(`{"id":77,"head_sha":"` + revision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`), nil
					}
					if strings.HasSuffix(target, "/runs/77/artifacts?per_page=100") {
						return []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":99,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, len(archive))), nil
					}
					if strings.HasSuffix(target, "/artifacts/99/zip") {
						if mode == "wrong-manifest" {
							return publicationZIP(t, "image-manifest.json", []byte(`{}`)), nil
						}
						return archive, nil
					}
				case "docker":
					if len(args) == 4 && args[0] == "pull" && args[1] == "--platform" && args[2] == "linux/amd64" {
						if !strings.HasPrefix(args[3], imageRepo+"@sha256:") {
							t.Fatal("foreign tool image")
						}
						return nil, nil
					}
					if len(args) > 2 && args[len(args)-1] == "--deployment-capabilities" {
						if mode == "no-capability" || mode == "auto-no-capability" {
							return []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":[]}`), nil
						}
						if mode == "no-openbao-capability" {
							return []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`), nil
						}
						return caps, nil
					}
				}
				return nil, errors.New("unexpected command")
			}
			if strings.HasPrefix(mode, "auto-") {
				published, err := publishFailedForwardToolFromPRForPurpose(t.Context(), store, e, 123, run, time.Now(), purpose)
				if mode != "auto-publish" && mode != "auto-wrong-files" {
					if err == nil || len(e.Journal.Entries) != 0 {
						t.Fatal("missing release was persisted")
					}
					return
				}
				if err != nil || published.Parent != e.Journal.PlanSHA256 || published.MergeCommit != revision || published.RunID != 77 {
					t.Fatal(published, err)
				}
				count := len(e.Journal.Entries)
				if _, err = publishFailedForwardToolFromPRForPurpose(t.Context(), store, e, 123, run, time.Now(), purpose); err != nil || len(e.Journal.Entries) != count {
					t.Fatal("auto publication retry changed journal", err)
				}
			}
			if strings.HasPrefix(mode, "producer") {
				if err := recordFailedForwardPublication(t.Context(), store, e, evidence, run, time.Now()); err != nil {
					t.Fatal(err)
				}
				count := len(e.Journal.Entries)
				if err := recordFailedForwardPublication(t.Context(), store, e, evidence, run, time.Now()); err != nil {
					t.Fatal(err)
				}
				if len(e.Journal.Entries) != count {
					t.Fatal("retry added duplicate evidence")
				}
			}
			result, err := selectFailedForwardToolForPurpose(t.Context(), store, e, run, purpose)
			if (err == nil) != (mode == "ok" || mode == "wrong-files" || strings.HasPrefix(mode, "producer") || mode == "auto-publish" || mode == "auto-wrong-files") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if err == nil && (result.Image.SourceRevision != revision || result.Image.Reference != imageRepo+"@sha256:"+strings.Repeat("b", 64) || result.Publication.RunID != 77 || string(result.Capabilities) != string(caps)) {
				t.Fatal("did not bind exact merged CI publication and capability")
			}
		})
	}
}

func failedForwardBytesDigestForTest(raw []byte) string { return environmentPreflightBytesSHA256(raw) }

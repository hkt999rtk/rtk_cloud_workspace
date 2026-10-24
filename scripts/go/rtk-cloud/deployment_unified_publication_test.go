package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func publicationZIP(t *testing.T, name string, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestExactPublicationBindsDigestSourceRepositoryAndLatestAttempt(t *testing.T) {
	for _, backup := range []bool{false, true} {
		for _, mode := range []string{"valid", "wrong-source", "wrong-repository", "old-attempt", "wrong-digest", "missing-digest", "wrong-run", "expired", "duplicate", "missing", "incomplete-list", "oversized", "private-error"} {
			t.Run(mode+map[bool]string{true: "/backup", false: "/service"}[backup], func(t *testing.T) {
				repo := "hkt999rtk/rtk_video_cloud"
				pkg := "video-cloud-api"
				name, file := "image-manifest", "image-manifest.json"
				if backup {
					repo = "hkt999rtk/rtk_cloud_workspace"
					pkg = "postgres-backup"
					name, file = "postgres-backup-runner-images", "images.json"
				}
				sha := strings.Repeat("a", 40)
				digest := "sha256:" + strings.Repeat("b", 64)
				repository := "ghcr.io/" + repo + "/" + pkg
				image := environmentReleaseImage{Reference: repository + "@" + digest, SourceRevision: sha}
				manifest := map[string]any{"schema": "rtk-service-image/v1", "version": 1, "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": "2", "image": repository, "digest": digest, "runner_tag": repository + ":sha-" + sha, "runner_digest": digest}
				switch mode {
				case "wrong-source":
					manifest["source_commit"] = strings.Repeat("c", 40)
				case "wrong-repository":
					manifest["repository"] = "other/repo"
				case "old-attempt":
					manifest["run_attempt"] = "1"
				case "wrong-digest":
					manifest["digest"] = "sha256:" + strings.Repeat("c", 64)
					manifest["runner_digest"] = manifest["digest"]
				case "missing-digest":
					delete(manifest, "digest")
					delete(manifest, "runner_digest")
				case "wrong-run":
					manifest["run_id"] = "78"
				}
				raw, _ := json.Marshal(manifest)
				archive := publicationZIP(t, file, raw)
				calls := 0
				run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
					calls++
					if command != "gh" || len(args) != 2 || args[0] != "api" || !strings.HasPrefix(args[1], "repos/"+repo+"/actions/") {
						t.Fatal("publication escaped exact repository")
					}
					if mode == "private-error" {
						return nil, errors.New("credential=never-display")
					}
					if strings.HasSuffix(args[1], "/runs/77") {
						return []byte(`{"id":77,"head_sha":"` + sha + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`), nil
					}
					if strings.HasSuffix(args[1], "/artifacts?per_page=100") {
						entry := map[string]any{"id": 99, "name": name, "size_in_bytes": len(archive), "expired": false}
						list := []any{entry}
						total := 1
						switch mode {
						case "expired":
							entry["expired"] = true
						case "duplicate":
							list = append(list, entry)
							total = 2
						case "missing":
							list = []any{}
							total = 0
						case "incomplete-list":
							total = 101
						case "oversized":
							entry["size_in_bytes"] = publicationArchiveLimit + 1
						}
						out, _ := json.Marshal(map[string]any{"total_count": total, "artifacts": list})
						return out, nil
					}
					if !strings.HasSuffix(args[1], "/artifacts/99/zip") {
						t.Fatal("artifact ID drift")
					}
					return archive, nil
				}
				evidence, err := verifyEnvironmentPublication(context.Background(), repo, 77, image, backup, run)
				if (err == nil) != (mode == "valid") {
					t.Fatal(mode, err)
				}
				if mode == "valid" && (len(evidence) != 64 || calls != 3) {
					t.Fatal("publication not bound")
				}
				if err != nil && strings.Contains(err.Error(), "never-display") {
					t.Fatal("private subprocess leaked")
				}
			})
		}
	}
}

func TestPublicationArchiveRejectsUnexpectedOrUnboundedManifest(t *testing.T) {
	for _, mode := range []string{"wrong-path", "large-payload", "symlink", "multiple", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			var raw []byte
			switch mode {
			case "corrupt":
				raw = []byte("not ZIP")
			case "wrong-path":
				raw = publicationZIP(t, "../image-manifest.json", []byte("{}"))
			case "large-payload":
				raw = publicationZIP(t, "image-manifest.json", bytes.Repeat([]byte("a"), (1<<20)+1))
			default:
				var b bytes.Buffer
				w := zip.NewWriter(&b)
				h := &zip.FileHeader{Name: "image-manifest.json"}
				if mode == "symlink" {
					h.SetMode(os.ModeSymlink | 0600)
				}
				f, _ := w.CreateHeader(h)
				f.Write([]byte("{}"))
				if mode == "multiple" {
					f, _ = w.Create("image-manifest.json")
					f.Write([]byte("{}"))
				}
				w.Close()
				raw = b.Bytes()
			}
			if _, err := publicationJSON(raw, "image-manifest.json"); err == nil {
				t.Fatal("untrusted artifact accepted")
			}
		})
	}
}

func TestVideoCloudPublicationRequiresDigestForEverySelectedConsumer(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	sha := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	manifest := map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": "2", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": digest, "images": map[string]any{"emqx_pki": map[string]any{"image": "ghcr.io/" + repo + "/video-cloud-emqx-pki", "digest": digest}}}
	raw, _ := json.Marshal(manifest)
	for _, pkg := range []string{"video-cloud-api", "video-cloud-emqx-pki", "video-cloud-openbao-pki"} {
		err := verifyPublicationJSON(raw, repo, 77, 2, environmentReleaseImage{Reference: "ghcr.io/" + repo + "/" + pkg + "@" + digest, SourceRevision: sha}, false)
		if (err == nil) != (pkg != "video-cloud-openbao-pki") {
			t.Fatal("consumer borrowed another image publication")
		}
	}
}

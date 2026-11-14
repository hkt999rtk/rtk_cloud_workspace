package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// installUnifiedRepairPublications installs only synthetic, private test
// artifacts for an already-admitted isolated parent. The caller must include
// the same fake GHCR token in the original operator inputs before publication.
// It never writes or alters operator settings, identity, or a live environment.
func installUnifiedRepairPublications(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, original environmentCreatePlan) (map[string]environmentReleaseImage, map[string]environmentReleaseImage) {
	t.Helper()
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	actualRoot, err := filepath.EvalSymlinks(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(tempRoot, actualRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		t.Fatal("repair fixture requires an isolated temporary environment store")
	}
	if e == nil || e.Store != store || cfg.Environment != store.Environment || original.Environment != cfg.Environment || original.Stack != e.Journal.Stack || original.ConfigurationSHA256 != deploymentplan.Digest(cfg) || e.Check(t.Context()) != nil {
		t.Fatal("repair fixture requires the selected original parent")
	}
	operator, err := store.readOperator()
	if err != nil || operator["GHCR_PULL_TOKEN"] != "isolated-fake-gh-only" {
		t.Fatal("synthetic GH token must be present in the original operator before admission")
	}
	if os.Getenv("RTK_CLOUD_CONFIG_ROOT") != store.ConfigRoot {
		t.Fatal("selected private test store differs")
	}
	repo := "hkt999rtk/rtk_video_cloud"
	old := map[string]string{}
	for role, image := range original.Release.Images {
		name, ok := strings.CutPrefix(strings.Split(image.Reference, "@")[0], "ghcr.io/"+repo+"/")
		if !ok {
			continue
		}
		if name == "video-cloud-api" || name == "video-cloud-emqx-pki" || name == "video-cloud-openbao-pki" {
			if original.Bootstrap.Images[role] != image.Reference {
				t.Fatal("original image role is not bound to bootstrap")
			}
			old[name] = image.Reference
		}
	}
	if len(old) != 3 || original.Bootstrap.Image != old["video-cloud-api"] {
		t.Fatal("original parent requires all three selected official image roles")
	}
	dir := t.TempDir()
	type publication struct {
		repair, schema, revision string
		run, artifact            int64
		digest                   map[string]string
	}
	publications := []publication{
		{issuerLeaseMetadataRepair, issuerLeaseMetadataRepairSchema, strings.Repeat("b", 40), 7701, 9901, map[string]string{"video-cloud-api": strings.Repeat("d", 64), "video-cloud-emqx-pki": strings.Repeat("e", 64), "video-cloud-openbao-pki": strings.Repeat("f", 64)}},
		{mqttLogTrustRepair, mqttLogTrustRepairSchema, strings.Repeat("c", 40), 7702, 9902, map[string]string{"video-cloud-api": strings.Repeat("9", 64)}},
	}
	for _, v := range publications {
		images := map[string]any{}
		for name, digest := range v.digest {
			ref := "ghcr.io/" + repo + "/" + name
			if old[name] == ref+"@sha256:"+digest {
				t.Fatal("synthetic repair must be a distinct published image")
			}
			if name != "video-cloud-api" {
				images[name] = map[string]string{"image": ref, "digest": "sha256:" + digest}
			}
		}
		manifest := mustProviderRecoveryJSON(map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": v.revision, "run_id": fmt.Sprint(v.run), "run_attempt": "2", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": "sha256:" + v.digest["video-cloud-api"], "images": images})
		selection := mustProviderRecoveryJSON(map[string]any{"schema": v.schema, "parent_plan_sha256": e.Journal.PlanSHA256, "source_revision": v.revision, "run_id": v.run, "run_attempt": 2, "manifest_sha256": environmentPreflightBytesSHA256(manifest)})
		if err := store.write(filepath.Join("deployment/repairs", v.repair, "selection.json"), selection, false); err != nil {
			t.Fatal(err)
		}
		if err := store.write(filepath.Join("deployment/repairs", v.repair, v.revision, "image-manifest.json"), manifest, false); err != nil {
			t.Fatal(err)
		}
		archive := publicationZIP(t, "image-manifest.json", manifest)
		run := mustProviderRecoveryJSON(map[string]any{"id": v.run, "head_sha": v.revision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": 2})
		listing := mustProviderRecoveryJSON(map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": v.artifact, "name": "image-manifest", "expired": false, "size_in_bytes": len(archive)}}})
		for name, body := range map[string][]byte{fmt.Sprintf("run-%d.json", v.run): run, fmt.Sprintf("listing-%d.json", v.run): listing, fmt.Sprintf("artifact-%d.zip", v.artifact): archive} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	script := `#!/bin/sh
set -eu
test "$#" = 2 && test "$1" = api && test "${GH_TOKEN:-}" = isolated-fake-gh-only || exit 71
case "$2" in
 repos/hkt999rtk/rtk_video_cloud/actions/runs/7701) cat "$RTK_UNIFIED_CI_DIR/run-7701.json";;
 repos/hkt999rtk/rtk_video_cloud/actions/runs/7702) cat "$RTK_UNIFIED_CI_DIR/run-7702.json";;
 'repos/hkt999rtk/rtk_video_cloud/actions/runs/7701/artifacts?per_page=100') cat "$RTK_UNIFIED_CI_DIR/listing-7701.json";;
 'repos/hkt999rtk/rtk_video_cloud/actions/runs/7702/artifacts?per_page=100') cat "$RTK_UNIFIED_CI_DIR/listing-7702.json";;
 repos/hkt999rtk/rtk_video_cloud/actions/artifacts/9901/zip) cat "$RTK_UNIFIED_CI_DIR/artifact-9901.zip";;
 repos/hkt999rtk/rtk_video_cloud/actions/artifacts/9902/zip) cat "$RTK_UNIFIED_CI_DIR/artifact-9902.zip";;
 *) exit 72;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_UNIFIED_CI_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	first, _, err := loadCreateVideoRepairImages(context.Background(), cfg, store, e.Journal.PlanSHA256, issuerLeaseMetadataRepair, issuerLeaseMetadataRepairSchema, "video-cloud-api", "video-cloud-emqx-pki", "video-cloud-openbao-pki")
	if err != nil {
		t.Fatal("synthetic issuer-repair publication verification", err)
	}
	second, _, err := loadCreateVideoRepairImages(context.Background(), cfg, store, e.Journal.PlanSHA256, mqttLogTrustRepair, mqttLogTrustRepairSchema, "video-cloud-api")
	if err != nil {
		t.Fatal("synthetic log-repair publication verification", err)
	}
	if len(first) != 3 || len(second) != 1 || first["video-cloud-api"].Reference == second["video-cloud-api"].Reference || e.Check(t.Context()) != nil {
		t.Fatal("synthetic repair publication changed the original fence")
	}
	return first, second
}

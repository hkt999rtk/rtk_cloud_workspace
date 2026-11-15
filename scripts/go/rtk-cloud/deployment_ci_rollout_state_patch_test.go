package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCIRolloutStatePatchPreservesOtherFieldsAndUsesResourceVersion(t *testing.T) {
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	image := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("c", 40), Publication: &createVersionPublication{Repository: repo, RunID: 33, Attempt: 1, ManifestSHA: strings.Repeat("d", 64)}}
	raw := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"video-cloud-dev-video-cloud","name":"rtk-ci-state-rtk-cloud-frontend","uid":"state-uid","resourceVersion":"5"},"data":{"state.json":"","other":"untouched"}}`)
	patch, err := ciRolloutStatePatch(raw, "video-cloud-dev-video-cloud", repo, "main", image)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op, Path string
		Value    any
	}
	if json.Unmarshal(patch, &ops) != nil || len(ops) != 4 {
		t.Fatalf("invalid patch: %s", patch)
	}
	if ops[0].Path != "/metadata/uid" || ops[1].Path != "/metadata/resourceVersion" || ops[2].Path != "/data/state.json" || ops[3].Path != "/data/state.json" {
		t.Fatalf("unexpected patch %s", patch)
	}
	updated, ok := ops[3].Value.(string)
	if !ok || !strings.Contains(updated, image.Reference) || !strings.Contains(updated, `"branch":"main"`) {
		t.Fatalf("missing completed version %s", patch)
	}
	if _, err = ciRolloutStatePatch(raw, "video-cloud-staging-video-cloud", repo, "main", image); err == nil {
		t.Fatal("other environment state accepted")
	}
}

func TestCIRolloutStateBatchRecordsAllImagesTogether(t *testing.T) {
	repo := "hkt999rtk/rtk_video_cloud"
	a := "ghcr.io/" + repo + "/video-cloud-api"
	b := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	rev := strings.Repeat("a", 40)
	pub := &createVersionPublication{Repository: repo, Branch: "main", RunID: 7, Attempt: 1, ManifestSHA: strings.Repeat("b", 64)}
	images := map[string]environmentReleaseImage{a: {Reference: a + "@sha256:" + strings.Repeat("c", 64), SourceRevision: rev, Publication: pub}, b: {Reference: b + "@sha256:" + strings.Repeat("d", 64), SourceRevision: rev, Publication: pub}}
	raw := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"video-cloud-dev-video-cloud","name":"rtk-ci-state-rtk-video-cloud","uid":"state-uid","resourceVersion":"5"},"data":{"state.json":""}}`)
	patch, err := ciRolloutStateBatchPatch(raw, "video-cloud-dev-video-cloud", repo, "main", images)
	if err != nil || !strings.Contains(string(patch), images[a].Reference) || !strings.Contains(string(patch), images[b].Reference) {
		t.Fatalf("missing batch state: %s %v", patch, err)
	}
}

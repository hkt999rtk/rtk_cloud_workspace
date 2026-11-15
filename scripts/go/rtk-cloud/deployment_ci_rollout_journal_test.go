package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIRolloutJournalRecordsPriorImageInPrivateEnvironmentStore(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	repo := "hkt999rtk/rtk_cloud_frontend"
	revision := strings.Repeat("a", 40)
	image := environmentReleaseImage{Reference: "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("b", 64), SourceRevision: revision, Publication: &createVersionPublication{Repository: repo, RunID: 22, Attempt: 1, ManifestSHA: strings.Repeat("c", 64)}}
	journal, err := newCIRolloutJournal(store, repo, "main", image, "run-123-1")
	if err != nil {
		t.Fatal(err)
	}
	target := ciRolloutTarget{Namespace: "video-cloud-dev-frontend", Name: "frontend", Container: "frontend", UID: "uid"}
	old := "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("d", 64)
	if err = journal.record(context.Background(), "started", ciRolloutTarget{}, ""); err != nil {
		t.Fatal(err)
	}
	if err = journal.record(context.Background(), "applying", target, old); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, journal.relative))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || !strings.Contains(string(raw), old) || !strings.Contains(string(raw), image.Reference) || !strings.Contains(string(raw), revision) {
		t.Fatalf("missing recovery data: %s", raw)
	}
	info, err := os.Stat(filepath.Join(store.Root, journal.relative))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal permissions: %v %v", info, err)
	}
	if _, err = newCIRolloutJournal(store, repo, "main", image, "../escape"); err == nil {
		t.Fatal("unsafe journal ID accepted")
	}
}

func TestCIRolloutBatchJournalRecordsEachPriorAndNewImage(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	repo := "hkt999rtk/rtk_video_cloud"
	api := "ghcr.io/" + repo + "/video-cloud-api"
	mqtt := "ghcr.io/" + repo + "/video-cloud-emqx-pki"
	rev := strings.Repeat("a", 40)
	pub := &createVersionPublication{Repository: repo, Branch: "main", RunID: 7, Attempt: 1, ManifestSHA: strings.Repeat("b", 64)}
	images := map[string]environmentReleaseImage{api: {Reference: api + "@sha256:" + strings.Repeat("c", 64), SourceRevision: rev, Publication: pub}, mqtt: {Reference: mqtt + "@sha256:" + strings.Repeat("d", 64), SourceRevision: rev, Publication: pub}}
	journal, err := newCIRolloutBatchJournal(store, repo, "main", images, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	target := ciRolloutTarget{Namespace: "video-cloud-dev-video-cloud", Name: "mqtt-pki", Container: "mqtt", UID: "uid"}
	old := mqtt + "@sha256:" + strings.Repeat("e", 64)
	if err = journal.recordBatch(context.Background(), "started", ciRolloutTarget{}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = journal.recordBatch(context.Background(), "applying", target, old, images[mqtt].Reference); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, journal.relative))
	if err != nil || !strings.Contains(string(raw), old) || !strings.Contains(string(raw), images[mqtt].Reference) || !strings.Contains(string(raw), images[api].Reference) {
		t.Fatalf("batch recovery journal incomplete: %s %v", raw, err)
	}
}

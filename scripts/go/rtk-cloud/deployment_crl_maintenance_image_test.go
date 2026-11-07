package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCRLMaintenancePinnedProvenanceSurvivesCIArtifactRetention(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore(root, "staging")
	revision := strings.Repeat("a", 40)
	repository := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api"
	image := repository + "@sha256:" + strings.Repeat("b", 64)
	raw, _ := json.Marshal(map[string]any{"schema": "rtk-service-image/v1", "repository": "hkt999rtk/rtk_video_cloud", "source_commit": revision, "run_id": "77", "run_attempt": "2", "image": repository, "digest": "sha256:" + strings.Repeat("b", 64)})
	proof := crlMaintenanceImageEvidence{Environment: "staging", Image: image, Revision: revision, RunID: 77, Attempt: 2, Manifest: raw}
	if err := maintenanceWrite(store, maintenanceImageEvidencePath(image, revision), proof); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func(_ context.Context, cmd string, args ...string) ([]byte, error) {
		calls++
		if cmd != "docker" || len(args) != 4 || args[3] != image {
			t.Fatal("cached immutable provenance unexpectedly depended on checkout or expired CI artifact")
		}
		return nil, nil
	}
	got, err := maintenanceImageWithCommand(context.Background(), deploymentConfig{Environment: "staging"}, store, image, revision, run)
	if err != nil || got != image || calls != 1 {
		t.Fatalf("cached resolution: %s %v calls=%d", got, err, calls)
	}
	proof.Environment = "prod"
	if err := maintenanceWrite(store, maintenanceImageEvidencePath(image, revision), proof); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceImageWithCommand(context.Background(), deploymentConfig{Environment: "staging"}, store, image, revision, run); err == nil {
		t.Fatal("foreign provenance accepted")
	}
	proof.Environment = "staging"
	proof.Manifest = []byte(`{"schema":"rtk-service-image/v1"}`)
	_ = maintenanceWrite(store, maintenanceImageEvidencePath(image, revision), proof)
	if _, err := maintenanceImageWithCommand(context.Background(), deploymentConfig{Environment: "staging"}, store, image, revision, run); err == nil {
		t.Fatal("unbound arbitrary JSON accepted")
	}
	if _, err := maintenanceImageWithCommand(context.Background(), deploymentConfig{Environment: "staging"}, store, "", strings.Repeat("c", 40), func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no verified CI provenance")
	}); err == nil {
		t.Fatal("missing first-use CI provenance accepted")
	}
}

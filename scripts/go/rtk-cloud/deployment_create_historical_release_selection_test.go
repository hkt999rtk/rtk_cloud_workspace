package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalReleaseHandoffPreservesNormalEnvironmentImageSelection(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	const role = "LKE_OPENBAO_PKI_IMAGE"
	old := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)}
	selected := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)}
	selection := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{role: selected}}
	if err := store.write(filepath.Join("operator", "env", environmentImagesKey), mustProviderRecoveryJSON(selection), false); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("c", 40)
	cfg := deploymentConfig{Workspace: "/isolated-release", Environment: "dev"}
	release := environmentRelease{Environment: "dev", WorkspaceRevision: revision, Images: map[string]environmentReleaseImage{role: old}}
	command := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			if args[2] == "rev-parse" {
				return []byte(revision + "\n"), nil
			}
			if args[2] == "status" {
				return nil, nil
			}
		}
		if name == "docker" && len(args) == 4 && args[0] == "pull" && args[2] == "linux/amd64" && args[3] == old.Reference {
			return nil, nil
		}
		t.Fatalf("unexpected release operation %s %v", name, args)
		return nil, nil
	}
	if err := verifyEnvironmentReleaseWithCommand(t.Context(), cfg, release, command); err == nil {
		t.Fatal("ordinary preflight ignored current environment selection")
	}
	if err := verifyEnvironmentImageSources(t.Context(), cfg, release, command); err == nil {
		t.Fatal("ordinary execution ignored current environment selection")
	}
	historical := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{role: old}}
	if err := verifyEnvironmentReleaseWithCommand(t.Context(), cfg, release, command, historical); err != nil {
		t.Fatal("historical release handoff rejected", err)
	}
	if err := verifyEnvironmentImageSources(t.Context(), cfg, release, command, historical); err != nil {
		t.Fatal("historical execution source handoff rejected", err)
	}
	after, err := readEnvironmentImageSelection(store)
	if err != nil || deploymentplan.Digest(after) != deploymentplan.Digest(selection) {
		t.Fatal("historical verification modified environment versions", err)
	}
}

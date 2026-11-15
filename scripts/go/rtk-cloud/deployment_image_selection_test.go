package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestEnvironmentImageSelectionProjectsOnlyNamedRole(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		target := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
		image := environmentReleaseImage{Reference: target, SourceRevision: strings.Repeat("b", 40), Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 1, ManifestSHA: strings.Repeat("c", 64)}}
		selected, _ := json.Marshal(environmentImageSelection{Environment: env, Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": image}})
		values := map[string]string{"DEPLOYMENT_IMAGES": string(selected), "OPENBAO_PKI_IMAGE": "old", "OTHER": "preserved"}
		if err := projectEnvironmentImageSelection(env, values); err != nil || values["OPENBAO_PKI_IMAGE"] != target || values["OTHER"] != "preserved" {
			t.Fatalf("selection not projected: %v", err)
		}
		for _, bad := range []string{`{"environment":"other","images":{"LKE_OPENBAO_PKI_IMAGE":{"reference":"` + target + `"}}}`, `{"environment":"` + env + `","images":{"LKE_UNKNOWN_IMAGE":{"reference":"` + target + `"}}}`, `{"environment":"` + env + `","images":{"LKE_OPENBAO_PKI_IMAGE":{"reference":"mutable:latest"}}}`, `{"environment":"` + env + `","images":{}}`} {
			if _, err := parseEnvironmentImageSelection(env, bad); err == nil {
				t.Fatalf("invalid selection accepted: %s", bad)
			}
		}
		if s, err := parseEnvironmentImageSelection(env, ""); err != nil || len(s.Images) != 0 {
			t.Fatal("missing optional selection should preserve legacy roles", err)
		}
	}
}

func TestSelectedEnvironmentImageIsPulledWithoutVersionComparison(t *testing.T) {
	for _, operation := range []string{"create", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			configRoot := t.TempDir()
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
			store, err := newSecretStore(configRoot, "dev")
			if err != nil {
				t.Fatal(err)
			}
			image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40), Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 1, ManifestSHA: strings.Repeat("c", 64)}}
			selected, _ := json.Marshal(environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": image}})
			if err := store.write(filepath.Join("operator", "env", environmentImagesKey), selected, false); err != nil {
				t.Fatal(err)
			}
			cfg := deploymentConfig{Workspace: "/fixture", Environment: "dev"}
			release := environmentRelease{Environment: "dev", WorkspaceRevision: strings.Repeat("d", 40), Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": image}}
			pulls := 0
			run := func(ctx context.Context, cmd string, args ...string) ([]byte, error) {
				if cmd == "git" {
					return []byte(release.WorkspaceRevision + "\n"), nil
				}
				if cmd == "docker" && len(args) > 0 && args[0] == "pull" {
					pulls++
					return nil, nil
				}
				t.Fatalf("selected image compared against unrelated version or CI: %s %v", cmd, args)
				return nil, errors.New("unexpected")
			}
			if err := verifyEnvironmentReleaseWithCommand(t.Context(), cfg, release, run); err != nil || pulls != 1 {
				t.Fatal("selected image rejected", err, pulls)
			}
			changed := release
			changed.Images = map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: strings.Replace(image.Reference, strings.Repeat("a", 64), strings.Repeat("e", 64), 1)}}
			if err := verifyEnvironmentReleaseWithCommand(t.Context(), cfg, changed, run); err == nil {
				t.Fatal("changed target accepted")
			}
			if deploymentplan.Digest(image) != deploymentplan.Digest(release.Images["LKE_OPENBAO_PKI_IMAGE"]) {
				t.Fatal("selected settings changed")
			}
		})
	}
}

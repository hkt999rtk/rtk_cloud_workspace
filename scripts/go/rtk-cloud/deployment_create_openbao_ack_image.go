package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const createOpenBaoACKImageSelectionPath = "deployment/repairs/openbao-host-ack/selection.json"

type createOpenBaoACKImageSelection struct {
	Schema      string `json:"schema"`
	Parent      string `json:"parent_plan_sha256"`
	Role        string `json:"role"`
	Revision    string `json:"source_revision"`
	RunID       int64  `json:"run_id"`
	Attempt     int    `json:"run_attempt"`
	ManifestSHA string `json:"manifest_sha256"`
}

// This role-only selection does not replace the original application release.
// Its producer records the actual formal publication under the selected parent;
// every consumer rechecks that publication before admitting the repair image.
func loadCreateOpenBaoACKRepairImage(ctx context.Context, cfg deploymentConfig, store secretStore, parent string) (environmentReleaseImage, string, string, error) {
	return loadCreateOpenBaoACKRepairImageWithCommand(ctx, cfg, store, parent, environmentCIImageCommand(cfg))
}

func loadCreateOpenBaoACKRepairImageWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, run environmentReleaseCommand) (environmentReleaseImage, string, string, error) {
	blocked := func(reason string) (environmentReleaseImage, string, string, error) {
		return environmentReleaseImage{}, "", "", createProviderRecoveryBlock("OpenBao host ACK repair " + reason)
	}
	if ctx == nil || run == nil || cfg.Environment != store.Environment || !environmentPreflightDigest.MatchString(parent) {
		return blocked("requires the exact selected environment and original parent")
	}
	read := func(relative string) ([]byte, error) {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) {
			return nil, createProviderRecoveryBlock("OpenBao host ACK repair requires canonical private evidence")
		}
		if err = validateEnvironmentPreflightDirectories(store, filepath.Dir(path)); err != nil {
			return nil, err
		}
		return readBoundedEnvironmentFile(path, true)
	}
	raw, err := read(createOpenBaoACKImageSelectionPath)
	var selection createOpenBaoACKImageSelection
	if err != nil || decodeEnvironmentJSON(raw, &selection) != nil || selection.Schema != "rtk-cloud.create-openbao-host-ack-selection/v1" || selection.Parent != parent || selection.Role != "LKE_OPENBAO_PKI_IMAGE" || !deploymentUpgradeSHA.MatchString(selection.Revision) || selection.RunID <= 0 || selection.Attempt <= 0 || !environmentPreflightDigest.MatchString(selection.ManifestSHA) {
		return blocked("requires a complete original-parent role selection")
	}
	manifest, err := read(filepath.Join("deployment/repairs/openbao-host-ack", selection.Revision, "image-manifest.json"))
	if err != nil || environmentPreflightBytesSHA256(manifest) != selection.ManifestSHA {
		return blocked("manifest is missing or its recorded bytes changed")
	}
	const repo = "hkt999rtk/rtk_video_cloud"
	runPath := fmt.Sprintf("repos/%s/actions/runs/%d", repo, selection.RunID)
	formalRun := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		evidence, err := run(ctx, command, args...)
		if err == nil && command == "gh" && len(args) == 2 && args[0] == "api" && args[1] == runPath {
			var source struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(evidence, &source) != nil || source.Path != ".github/workflows/release.yml" {
				return nil, createProviderRecoveryBlock("OpenBao host ACK repair requires the formal release workflow")
			}
		}
		return evidence, err
	}
	fresh, attempt, err := readEnvironmentPublication(ctx, repo, selection.RunID, selection.Revision, false, formalRun)
	if err != nil || attempt != selection.Attempt || !bytes.Equal(fresh, manifest) || environmentPreflightBytesSHA256(fresh) != selection.ManifestSHA {
		return blocked("lacks the same actual bytes from the current successful formal main publication attempt")
	}
	image, err := selectEnvironmentPublishedImage(fresh, repo, selection.RunID, selection.Attempt, selection.Revision, "ghcr.io/"+repo+"/video-cloud-openbao-pki", false)
	if err != nil {
		return blocked("publication does not bind the exact immutable OpenBao target to its source and attempt")
	}
	return image, environmentPreflightBytesSHA256(raw), environmentPreflightBytesSHA256(manifest), nil
}

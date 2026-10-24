package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createCertIssuerPrepareImageSelectionPath = "deployment/repairs/certissuer-provider-prepare/selection.json"

type createCertIssuerPrepareImageSelection struct {
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
func loadCreateCertIssuerPrepareRepairImage(ctx context.Context, cfg deploymentConfig, store secretStore, parent string) (environmentReleaseImage, string, string, error) {
	return loadCreateCertIssuerPrepareRepairImageWithCommand(ctx, cfg, store, parent, environmentCIImageCommand(cfg))
}

func loadCreateCertIssuerPrepareRepairImageWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, run environmentReleaseCommand) (environmentReleaseImage, string, string, error) {
	blocked := func(reason string) (environmentReleaseImage, string, string, error) {
		return environmentReleaseImage{}, "", "", createProviderRecoveryBlock("CertIssuer provider trust repair " + reason)
	}
	if ctx == nil || run == nil || cfg.Environment != store.Environment || !environmentPreflightDigest.MatchString(parent) {
		return blocked("requires the exact selected environment and original parent")
	}
	read := func(relative string) ([]byte, error) {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) {
			return nil, createProviderRecoveryBlock("CertIssuer provider trust repair requires canonical private evidence")
		}
		if err = validateEnvironmentPreflightDirectories(store, filepath.Dir(path)); err != nil {
			return nil, err
		}
		return readBoundedEnvironmentFile(path, true)
	}
	raw, err := read(createCertIssuerPrepareImageSelectionPath)
	var selection createCertIssuerPrepareImageSelection
	if err != nil || decodeEnvironmentJSON(raw, &selection) != nil || selection.Schema != "rtk-cloud.create-certissuer-provider-prepare-selection/v1" || selection.Parent != parent || selection.Role != "LKE_CERTISSUER_IMAGE" || !deploymentUpgradeSHA.MatchString(selection.Revision) || selection.RunID <= 0 || selection.Attempt <= 0 || !environmentPreflightDigest.MatchString(selection.ManifestSHA) {
		return blocked("requires a complete original-parent role selection")
	}
	manifest, err := read(filepath.Join("deployment/repairs/certissuer-provider-prepare", selection.Revision, "image-manifest.json"))
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
				return nil, createProviderRecoveryBlock("CertIssuer provider trust repair requires the formal release workflow")
			}
		}
		return evidence, err
	}
	fresh, attempt, err := readEnvironmentPublication(ctx, repo, selection.RunID, selection.Revision, false, formalRun)
	if err != nil || attempt != selection.Attempt || !bytes.Equal(fresh, manifest) || environmentPreflightBytesSHA256(fresh) != selection.ManifestSHA {
		return blocked("lacks the same actual bytes from the current successful formal main publication attempt")
	}
	image, err := selectEnvironmentPublishedImage(fresh, repo, selection.RunID, selection.Attempt, selection.Revision, "ghcr.io/"+repo+"/video-cloud-api", false)
	if err != nil {
		return blocked("publication does not bind the exact immutable CertIssuer API target to its source and attempt")
	}
	return image, environmentPreflightBytesSHA256(raw), environmentPreflightBytesSHA256(manifest), nil
}

// This formal API image is admitted only for the original CertIssuer owner.
// It never replaces the original application release or any other controller.
type createCertIssuerPrepareImageRecovery struct {
	Image                     environmentReleaseImage
	SelectionSHA, ManifestSHA string
}

func loadCreateCertIssuerPrepareRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, parent string) (*createCertIssuerPrepareImageRecovery, error) {
	image, selection, manifest, err := loadCreateCertIssuerPrepareRepairImage(ctx, cfg, store, parent)
	if err != nil {
		return nil, err
	}
	return &createCertIssuerPrepareImageRecovery{Image: image, SelectionSHA: selection, ManifestSHA: manifest}, nil
}

const managedCoreCertIssuerPrepareMaterialPrefix = "accepted-service-core-complete-issuer-partial-certissuer-prepare-"
const managedCoreCertIssuerPrepareFilename = "certissuer-provider-prepare-image.json"

func validateManagedCoreCertIssuerPrepare(proof *createCertIssuerPrepareImageRecovery) error {
	if proof == nil || !immutableEnvironmentImage.MatchString(proof.Image.Reference) || !strings.HasPrefix(proof.Image.Reference, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:") || !deploymentUpgradeSHA.MatchString(proof.Image.SourceRevision) || !environmentPreflightDigest.MatchString(proof.SelectionSHA) || !environmentPreflightDigest.MatchString(proof.ManifestSHA) {
		return managedCorePartialBlock("formal-certissuer-prepare-publication")
	}
	return nil
}

func addManagedCoreCertIssuerPrepareMaterials(materials map[string]string, proof *createCertIssuerPrepareImageRecovery) {
	materials[managedCoreCertIssuerPrepareMaterialPrefix+"image"] = deploymentplan.Digest(proof.Image)
	materials[managedCoreCertIssuerPrepareMaterialPrefix+"selection"] = proof.SelectionSHA
	materials[managedCoreCertIssuerPrepareMaterialPrefix+"manifest"] = proof.ManifestSHA
	materials[managedCoreCertIssuerPrepareMaterialPrefix+"source"] = deploymentplan.Digest(proof)
}

func loadManagedCoreQualifiedCertIssuerPrepare(store secretStore, review string, materials map[string]string) (*createCertIssuerPrepareImageRecovery, error) {
	if !environmentPreflightDigest.MatchString(review) {
		return nil, managedCorePartialBlock("qualified-certissuer-prepare-review")
	}
	var proof createCertIssuerPrepareImageRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedCoreCertIssuerPrepareFilename), &proof) != nil || validateManagedCoreCertIssuerPrepare(&proof) != nil {
		return nil, managedCorePartialBlock("qualified-certissuer-prepare-artifact")
	}
	expected := map[string]string{}
	addManagedCoreCertIssuerPrepareMaterials(expected, &proof)
	for k, v := range expected {
		if materials[k] != v {
			return nil, managedCorePartialBlock("qualified-certissuer-prepare-material")
		}
	}
	return &proof, nil
}

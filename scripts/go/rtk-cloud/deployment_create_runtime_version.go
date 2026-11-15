package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the explicit updater supplies an already publication/capability-verified
// replacement while authenticating the old operator. Ordinary verification has
// no replacement scope and must qualify its currently selected package itself.
type createRuntimeImageCapabilityRepairKey struct{}

func createRuntimeImageCapabilityRepair(ctx context.Context) map[string]environmentReleaseImage {
	images, _ := ctx.Value(createRuntimeImageCapabilityRepairKey{}).(map[string]environmentReleaseImage)
	return images
}

// A finite, recorded runtime-role update composes with the existing OpenBao
// handoff. All other settings must still match the original operator hash.
func validateCreateRuntimeVersionReceipt(environment string, current map[string]string, raw []byte) (map[string]string, map[string]environmentReleaseImage, error) {
	bad := func() (map[string]string, map[string]environmentReleaseImage, error) {
		return nil, nil, createProviderRecoveryBlock("runtime image selection differs from its exact environment updater receipt")
	}
	var receipt createVersionReceipt
	if decodeEnvironmentJSON(raw, &receipt) != nil || len(receipt.Differences) == 0 || len(receipt.Differences) > 2 {
		return bad()
	}
	selected, err := parseEnvironmentImageSelection(environment, current[environmentImagesKey])
	if err != nil || !sameCreateVersionJSON(selected, receipt.Selection) {
		return bad()
	}
	previous, err := parseEnvironmentImageSelection(environment, receipt.Previous)
	if err != nil {
		return bad()
	}
	images := map[string]environmentReleaseImage{}
	for _, d := range receipt.Differences {
		if d.Role != "LKE_PKI_CONTROLLER_IMAGE" && d.Role != "LKE_PKI_MANAGEMENT_IMAGE" || images[d.Role].Reference != "" {
			return bad()
		}
		image, exists := selected.Images[d.Role]
		if !exists || !sameCreateVersionJSON(image, json.RawMessage(d.Latest)) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Publication == nil || image.Publication.Repository != "hkt999rtk/rtk_video_cloud" || image.Publication.Backup || image.Publication.RunID <= 0 || image.Publication.Attempt <= 0 || !environmentPreflightDigest.MatchString(image.Publication.ManifestSHA) || d.RunURL != fmt.Sprintf("https://github.com/%s/actions/runs/%d", image.Publication.Repository, image.Publication.RunID) {
			return bad()
		}
		before := previous.Images[d.Role].Reference
		if before == "" {
			before = current[strings.TrimPrefix(d.Role, "LKE_")]
		}
		if before == "" || d.Current != before || image.Reference == before {
			return bad()
		}
		images[d.Role] = image
	}
	for role, image := range previous.Images {
		if images[role].Reference == "" && deploymentplan.Digest(image) != deploymentplan.Digest(selected.Images[role]) {
			return bad()
		}
	}
	for role, image := range selected.Images {
		if images[role].Reference == "" && deploymentplan.Digest(image) != deploymentplan.Digest(previous.Images[role]) {
			return bad()
		}
	}
	restored := appendMap(current, nil)
	if receipt.Previous == "" {
		delete(restored, environmentImagesKey)
	} else {
		restored[environmentImagesKey] = receipt.Previous
	}
	return restored, images, nil
}
func selectedCreateRuntimeVersionUpdate(ctx context.Context, cfg deploymentConfig, store secretStore, current map[string]string, run environmentReleaseCommand) (map[string]string, map[string]environmentReleaseImage, error) {
	dir, err := store.safePath("deployment/version-updates")
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return current, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	// Receipts preserve the literal previous selection so each reverse step
	// leaves all other operator inputs untouched. Deduplicate equivalent copies
	// before requiring one exact transition at every selection in the chain.
	receipts := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(dir, entry.Name()), true)
		if err != nil {
			return nil, nil, err
		}
		var receipt createVersionReceipt
		if decodeEnvironmentJSON(raw, &receipt) == nil {
			receipts[deploymentplan.Digest(receipt)] = raw
		}
	}
	restored := current
	images := map[string]environmentReleaseImage{}
	seen := map[string]bool{}
	for {
		selection, err := parseEnvironmentImageSelection(cfg.Environment, restored[environmentImagesKey])
		if err != nil {
			return nil, nil, err
		}
		key := deploymentplan.Digest(selection)
		if seen[key] {
			return nil, nil, createProviderRecoveryBlock("runtime image updater receipts contain a selection cycle")
		}
		seen[key] = true
		var previous map[string]string
		var roles map[string]environmentReleaseImage
		matches := 0
		for _, raw := range receipts {
			candidate, selected, err := validateCreateRuntimeVersionReceipt(cfg.Environment, restored, raw)
			if err != nil {
				continue
			}
			matches++
			previous, roles = candidate, selected
		}
		if matches == 0 {
			break
		}
		if matches != 1 {
			return nil, nil, createProviderRecoveryBlock("runtime image selection has ambiguous updater receipts")
		}
		for role, image := range roles {
			// Walk newest to oldest: history authenticates the original
			// operator, while the current selection remains authoritative.
			if images[role].Reference == "" {
				images[role] = image
			}
		}
		restored = previous
	}
	if len(images) == 0 {
		return current, nil, nil
	}
	verified := map[string]environmentReleaseImage{}
	for role, image := range images {
		key := image.Reference + image.SourceRevision + deploymentplan.Digest(image.Publication)
		result, ok := verified[key]
		if !ok {
			result, err = verifyCreateVersionPublication(ctx, createVersionImage{Reference: image.Reference, SourceRevision: image.SourceRevision, Publication: image.Publication}, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", run)
			if err != nil {
				return nil, nil, err
			}
			if err = verifyCreateRuntimeImageCapabilities(ctx, image.Reference, run); err != nil {
				return nil, nil, err
			}
			verified[key] = result
		}
		if role == "LKE_PKI_MANAGEMENT_IMAGE" && current["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] == "managed-public-pem-v1" {
			capabilityImage := image.Reference
			if replacement := createRuntimeImageCapabilityRepair(ctx)[role]; replacement.Reference != "" {
				capabilityImage = replacement.Reference
			}
			if err = verifyCreateRuntimeManagementCRLCapabilities(ctx, capabilityImage, run); err != nil {
				return nil, nil, err
			}
		}
		images[role] = result
	}
	return restored, images, nil
}
func verifyCreateRuntimeImageCapabilities(ctx context.Context, image string, run environmentReleaseCommand) error {
	raw, err := run(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", image, "--deployment-capabilities")
	var caps struct {
		Schema   string   `json:"schema"`
		Service  string   `json:"service"`
		Features []string `json:"features"`
	}
	if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &caps) != nil || caps.Schema != "rtk-cloud.deployment-capabilities/v1" || caps.Service != "serviceidentity-bootstrap" {
		return createProviderRecoveryBlock("selected runtime package has no native OpenBao state capability evidence")
	}
	for _, feature := range serviceIdentityBootstrapCapabilities("openbao") {
		if !slices.Contains(caps.Features, feature) {
			return createProviderRecoveryBlock("selected runtime package does not support the environment OpenBao state backend")
		}
	}
	return nil
}

func verifyCreateRuntimeManagementCRLCapabilities(ctx context.Context, image string, run environmentReleaseCommand) error {
	raw, err := run(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/pkimanagement", image, "--deployment-capabilities")
	var caps struct {
		Schema   string   `json:"schema"`
		Service  string   `json:"service"`
		Features []string `json:"features"`
	}
	if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &caps) != nil || caps.Schema != "rtk-cloud.deployment-capabilities/v1" || caps.Service != "pkimanagement" || !slices.Contains(caps.Features, "public-registration-crl-bundle-v1") {
		return createProviderRecoveryBlock("selected Management runtime package has no native public registration CRL bundle capability evidence")
	}
	return nil
}

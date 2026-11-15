package main

import (
	"context"
	"errors"
	"strings"
)

// The caller supplies either freshly authenticated CI selection or the recorded
// successful environment rollout. This checks the selected role and branch;
// it does not substitute another version or require old CI artifacts to persist.
func verifyCIRolloutImageProvenance(_ context.Context, policy ciBranchSelection, role string, image environmentReleaseImage, _ environmentReleaseCommand) (bool, error) {
	if image.Publication == nil || image.Publication.Branch == "" || policy[image.Publication.Repository] == "" {
		return false, nil
	}
	publication := image.Publication
	repository := environmentVersionRoles()[role]
	if !policy.matches(publication.Repository, publication.Branch) || repository == "" || !strings.HasPrefix(repository, "ghcr.io/"+publication.Repository+"/") || environmentImageRepository(image.Reference) != repository || !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || publication.RunID <= 0 || publication.Attempt <= 0 || !environmentPreflightDigest.MatchString(publication.ManifestSHA) {
		return true, errors.New("CI rollout: completed image does not match the selected environment branch and role")
	}
	return true, nil
}

func environmentCIBranchPolicy(cfg deploymentConfig) (ciBranchSelection, error) {
	if cfg.Environment != "dev" || cfg.EnvironmentRoot == "" {
		return ciBranchSelection{}, nil
	}
	return readCIBranchSelection(cfg)
}

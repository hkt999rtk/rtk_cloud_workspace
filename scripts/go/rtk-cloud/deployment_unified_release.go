package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const (
	environmentReleaseSourceNext      = "Use a clean checkout of the selected workspace revision and its exact pinned service revisions, then repeat complete preflight."
	environmentReleasePullNext        = "Verify the maintained image reference, registry availability and selected environment pull credentials, then repeat complete preflight."
	environmentReleasePublicationNext = "Verify selected environment GitHub access and the latest successful exact-source main CI/Release run with its unexpired formal image artifact; republish the frozen source if evidence is missing, then repeat complete preflight."
	environmentReleaseBackupNext      = "Publish the selected workspace revision through workspace-test-baseline.yml and postgres-backup-image.yml with the selected PostgreSQL digest, then repeat complete preflight."
)

func environmentReleaseFailure(err error, id, resource, cause, next string) error {
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) {
		if id == "deployment.release.source" && blocker.ID == id && blocker.Resource == "" && resource != "" {
			copy := *blocker
			copy.Resource = resource
			return &copy
		}
		return err
	}
	return &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: next}
}

func verifyEnvironmentRelease(ctx context.Context, cfg deploymentConfig, release environmentRelease) error {
	return verifyEnvironmentReleaseWithCommand(ctx, cfg, release, environmentCIImageCommand(cfg))
}

func verifyEnvironmentReleaseWithCommand(ctx context.Context, cfg deploymentConfig, release environmentRelease, run func(context.Context, string, ...string) ([]byte, error)) error {
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, release.WorkspaceRevision, run); err != nil {
		return environmentReleaseFailure(err, "deployment.release.source", "workspace", "executing workspace checkout is not clean or does not match the selected immutable release", environmentReleaseSourceNext)
	}
	keys := make([]string, 0, len(release.Images))
	for key := range release.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pulled := map[string]bool{}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		image := release.Images[key]
		if !pulled[image.Reference] {
			if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
				return environmentReleaseFailure(err, "deployment.release.pull", key, "selected immutable image is unavailable for linux/amd64", environmentReleasePullNext)
			}
			pulled[image.Reference] = true
		}
		if !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		if environmentImageRepository(image.Reference) == "ghcr.io/hkt999rtk/rtk_cloud_workspace/postgres-backup" {
			if image.SourceRevision != release.WorkspaceRevision {
				return environmentReleaseFailure(nil, "deployment.release.backup", key, "backup runner source differs from the selected workspace release", environmentReleaseBackupNext)
			}
			if err := verifyEnvironmentBackupPublication(ctx, image, run); err != nil {
				return err
			}
			continue
		}
		source, err := deploymentUpgradeImageSource(image.Reference)
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.source", key, "selected image has no maintained RTK source and publication mapping", environmentReleaseSourceNext)
		}
		// The workspace release, service revision and OCI publication must all
		// identify the same source. Never borrow CI evidence from a newer HEAD.
		raw, err := run(ctx, "git", "-C", cfg.Workspace, "ls-tree", release.WorkspaceRevision, "--", source.RepoPath)
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.source", key, "selected workspace release gitlink cannot be read", environmentReleaseSourceNext)
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 4 || fields[0] != "160000" || fields[1] != "commit" || fields[2] != image.SourceRevision || fields[3] != source.RepoPath {
			return environmentReleaseFailure(nil, "deployment.release.source", key, "selected image source differs from its workspace release gitlink", environmentReleaseSourceNext)
		}
		raw, err = run(ctx, "git", "-C", filepath.Join(cfg.Workspace, source.RepoPath), "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(raw)) != image.SourceRevision {
			return environmentReleaseFailure(err, "deployment.release.source", key, "schema and startup source checkout differs from the selected image source revision", environmentReleaseSourceNext)
		}
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, source.RepoPath), image.SourceRevision, run); err != nil {
			return environmentReleaseFailure(err, "deployment.release.source", key, "selected image source checkout is not clean or does not match its immutable release", environmentReleaseSourceNext)
		}
		if err := verifyDeploymentUpgradeImageCIAtWithCommand(ctx, image.Reference, image.SourceRevision, run); err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "selected image OCI revision or exact-source CI and publication evidence cannot be verified", environmentReleasePublicationNext)
		}
		repo := "hkt999rtk/" + source.RepoName
		raw, err = run(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/release.yml/runs?head_sha="+image.SourceRevision+"&per_page=100")
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "exact-source Release run inventory is unavailable", environmentReleasePublicationNext)
		}
		runID, err := deploymentUpgradeSuccessfulRun(raw, image.SourceRevision)
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "a complete successful latest exact-source main Release run cannot be verified", environmentReleasePublicationNext)
		}
		if _, err = verifyEnvironmentPublication(ctx, repo, runID, image, false, run); err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "selected immutable image cannot be verified against the exact successful Release attempt's formal artifact", environmentReleasePublicationNext)
		}
	}
	if runner, ok := release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"]; ok {
		raw, err := run(ctx, "docker", "image", "inspect", runner.Reference, "--format", "{{json .Config.Labels}}")
		var labels map[string]string
		if err != nil || json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.base.name"] != release.Images["LKE_POSTGRES_IMAGE"].Reference {
			return environmentReleaseFailure(err, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "backup runner provenance does not match the selected PostgreSQL digest", environmentReleaseBackupNext)
		}
	}
	return nil
}

func verifyEnvironmentReleaseCheckout(ctx context.Context, directory, revision string, run func(context.Context, string, ...string) ([]byte, error)) error {
	if !deploymentUpgradeSHA.MatchString(revision) || directory == "" || run == nil {
		return environmentReleaseFailure(nil, "deployment.release.source", "", "exact source revision and checkout are required", environmentReleaseSourceNext)
	}
	raw, err := run(ctx, "git", "-C", directory, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(raw)) != revision {
		return environmentReleaseFailure(err, "deployment.release.source", "", "executing checkout differs from the selected immutable release", environmentReleaseSourceNext)
	}
	raw, err = run(ctx, "git", "-C", directory, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		return environmentReleaseFailure(err, "deployment.release.source", "", "executing checkout has unqualified source changes", environmentReleaseSourceNext)
	}
	return nil
}

func verifyEnvironmentBackupPublication(ctx context.Context, image environmentReleaseImage, run func(context.Context, string, ...string) ([]byte, error)) error {
	raw, err := run(ctx, "docker", "image", "inspect", image.Reference, "--format", "{{json .Config.Labels}}")
	var labels map[string]string
	if err != nil || json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.revision"] != image.SourceRevision {
		return environmentReleaseFailure(err, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "backup runner OCI revision differs from its release source", environmentReleaseBackupNext)
	}
	for _, workflow := range []string{"workspace-test-baseline.yml", "postgres-backup-image.yml"} {
		raw, err = run(ctx, "gh", "api", "repos/hkt999rtk/rtk_cloud_workspace/actions/workflows/"+workflow+"/runs?head_sha="+image.SourceRevision+"&per_page=100")
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "required backup runner workflow run inventory is unavailable", environmentReleaseBackupNext)
		}
		runID, runErr := deploymentUpgradeSuccessfulRun(raw, image.SourceRevision)
		if runErr != nil {
			return environmentReleaseFailure(runErr, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "required exact-source workspace CI or backup-image publication has no verified successful main run", environmentReleaseBackupNext)
		}
		if workflow == "postgres-backup-image.yml" {
			if _, err = verifyEnvironmentPublication(ctx, "hkt999rtk/rtk_cloud_workspace", runID, image, true, run); err != nil {
				return environmentReleaseFailure(err, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "backup runner digest cannot be verified against the exact successful publication attempt's formal artifact", environmentReleaseBackupNext)
			}
		}
	}
	return nil
}

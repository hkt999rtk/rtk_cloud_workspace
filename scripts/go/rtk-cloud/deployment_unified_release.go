package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func verifyEnvironmentRelease(ctx context.Context, cfg deploymentConfig, release environmentRelease) error {
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, release.WorkspaceRevision, deploymentUpgradeCommand); err != nil {
		return err
	}
	keys := make([]string, 0, len(release.Images))
	for key := range release.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		image := release.Images[key]
		if _, err := deploymentUpgradeCommand(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
			return fmt.Errorf("deployment.release.pull: %s is unavailable for linux/amd64", key)
		}
		if !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		if environmentImageRepository(image.Reference) == "ghcr.io/hkt999rtk/rtk_cloud_workspace/postgres-backup" {
			if image.SourceRevision != release.WorkspaceRevision {
				return errors.New("deployment.release.backup: runner source differs from the workspace release")
			}
			if err := verifyEnvironmentBackupPublication(ctx, image); err != nil {
				return err
			}
			continue
		}
		source, err := deploymentUpgradeImageSource(image.Reference)
		if err != nil {
			return err
		}
		// The workspace release, service revision and OCI publication must all
		// identify the same source. Never borrow CI evidence from a newer HEAD.
		raw, err := deploymentUpgradeCommand(ctx, "git", "-C", cfg.Workspace, "ls-tree", release.WorkspaceRevision, "--", source.RepoPath)
		if err != nil {
			return errors.New("deployment.release.source: workspace release revision is unavailable")
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 4 || fields[0] != "160000" || fields[1] != "commit" || fields[2] != image.SourceRevision || fields[3] != source.RepoPath {
			return fmt.Errorf("deployment.release.source: %s differs from its workspace release gitlink", key)
		}
		raw, err = deploymentUpgradeCommand(ctx, "git", "-C", filepath.Join(cfg.Workspace, source.RepoPath), "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(raw)) != image.SourceRevision {
			return fmt.Errorf("deployment.release.source: %s schema/startup source checkout differs from the selected release", key)
		}
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, source.RepoPath), image.SourceRevision, deploymentUpgradeCommand); err != nil {
			return fmt.Errorf("deployment.release.source: %s checkout contains source drift; use a clean exact-release checkout", key)
		}
		if err := verifyDeploymentUpgradeImageCIAt(ctx, image.Reference, image.SourceRevision); err != nil {
			return err
		}
		repo := "hkt999rtk/" + source.RepoName
		raw, err = deploymentUpgradeCommand(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/release.yml/runs?head_sha="+image.SourceRevision+"&per_page=100")
		if err != nil {
			return errors.New("deployment.release.publication: release run inventory is unavailable")
		}
		runID, err := deploymentUpgradeSuccessfulRun(raw, image.SourceRevision)
		if err != nil {
			return err
		}
		if _, err = verifyEnvironmentPublication(ctx, repo, runID, image, false, deploymentUpgradeCommand); err != nil {
			return err
		}
	}
	if runner, ok := release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"]; ok {
		raw, err := deploymentUpgradeCommand(ctx, "docker", "image", "inspect", runner.Reference, "--format", "{{json .Config.Labels}}")
		var labels map[string]string
		if err != nil || json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.base.name"] != release.Images["LKE_POSTGRES_IMAGE"].Reference {
			return errors.New("deployment.release.backup: backup runner provenance does not match the selected PostgreSQL digest")
		}
	}
	return nil
}

func verifyEnvironmentReleaseCheckout(ctx context.Context, directory, revision string, run func(context.Context, string, ...string) ([]byte, error)) error {
	if !deploymentUpgradeSHA.MatchString(revision) || directory == "" || run == nil {
		return errors.New("deployment.release.source: exact source revision and checkout are required")
	}
	raw, err := run(ctx, "git", "-C", directory, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(raw)) != revision {
		return errors.New("deployment.release.source: executing checkout differs from the selected immutable release")
	}
	raw, err = run(ctx, "git", "-C", directory, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		return errors.New("deployment.release.source: executing checkout has unqualified source changes; use a clean exact-release checkout")
	}
	return nil
}

func verifyEnvironmentBackupPublication(ctx context.Context, image environmentReleaseImage) error {
	raw, err := deploymentUpgradeCommand(ctx, "docker", "image", "inspect", image.Reference, "--format", "{{json .Config.Labels}}")
	var labels map[string]string
	if err != nil || json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.revision"] != image.SourceRevision {
		return errors.New("deployment.release.backup: runner OCI revision differs from its release source")
	}
	for _, workflow := range []string{"workspace-test-baseline.yml", "postgres-backup-image.yml"} {
		raw, err = deploymentUpgradeCommand(ctx, "gh", "api", "repos/hkt999rtk/rtk_cloud_workspace/actions/workflows/"+workflow+"/runs?head_sha="+image.SourceRevision+"&per_page=100")
		if err != nil {
			return err
		}
		runID, runErr := deploymentUpgradeSuccessfulRun(raw, image.SourceRevision)
		if runErr != nil {
			return fmt.Errorf("deployment.release.backup: %s exact-source publication evidence is unavailable", workflow)
		}
		if workflow == "postgres-backup-image.yml" {
			if _, err = verifyEnvironmentPublication(ctx, "hkt999rtk/rtk_cloud_workspace", runID, image, true, deploymentUpgradeCommand); err != nil {
				return err
			}
		}
	}
	return nil
}

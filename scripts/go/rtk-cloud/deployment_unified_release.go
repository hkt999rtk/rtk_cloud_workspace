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
	environmentReleaseSourceNext      = "Select the frozen workspace revision and its exact pinned service revisions, then repeat complete preflight."
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
	if err != nil {
		code := ""
		switch err.Error() {
		case "selected environment credentials unavailable":
			code = "ENV_STORE"
		case "selected environment GHCR_PULL_TOKEN is required for formal CI artifact access":
			code = "ENV_CI_TOKEN"
		case "formal CI artifact access failed; check environment token permissions and provider availability":
			code = "CI_ACCESS"
		case "candidate image revision does not match the selected source checkout; pull the exact image and select its source":
			code = "OCI_REVISION"
		case "docker release prerequisite failed; check authentication, tool availability and deadline":
			code = "DOCKER_READ"
		case "formal LKE image publication job has not passed":
			code = "PUBLICATION_JOB"
		case "image publication job evidence is malformed or incomplete":
			code = "PUBLICATION_INVENTORY"
		}
		if code != "" {
			cause += " [" + code + "]"
		}
	}
	return &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: next}
}

func verifyEnvironmentRelease(ctx context.Context, cfg deploymentConfig, release environmentRelease) error {
	return verifyEnvironmentReleaseWithCommand(ctx, cfg, release, environmentCIImageCommand(cfg))
}

// Only catalog roles and fixed query steps reach progress output. Image refs,
// GitHub paths, source revisions and subprocess diagnostics remain private.
func environmentReleaseProgressStage(key, step string) string {
	role := "selected-image"
	switch key {
	case "workspace":
		role = "workspace"
	case "LKE_POSTGRES_BACKUP_RUNNER_IMAGE":
		role = "postgres-backup"
	case "LKE_MQTT_PKI_IMAGE":
		role = "mqtt-pki"
	case "LKE_OPENBAO_PKI_IMAGE":
		role = "openbao-pki"
	default:
		for _, source := range lkeServiceImageSources() {
			if key == source.EnvKey {
				role = source.Name
				break
			}
		}
		for _, provider := range environmentProviderImageKeys {
			if key == provider {
				role = strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(provider, "LKE_"), "_IMAGE"), "_", "-"))
				break
			}
		}
	}
	switch step {
	case "workspace-pin", "checkout-head", "pull", "oci-labels", "ci-runs", "release-runs", "publication-jobs", "workspace-baseline-runs", "backup-runs", "release-run", "artifact-list", "artifact-download":
		return "deployment.release.verify." + role + "." + step
	default:
		return ""
	}
}

// Classify fixed command shapes rather than exposing arguments or endpoints.
// Multiple observations of the same prerequisite retain the same progress ID;
// the command adapter still executes and tracks every individual observation.
func environmentReleaseProgressStep(command string, args []string) string {
	switch command {
	case "git":
		if len(args) >= 4 && args[0] == "-C" {
			switch args[2] {
			case "ls-tree":
				return "workspace-pin"
			case "rev-parse":
				if args[3] == "HEAD" {
					return "checkout-head"
				}
			}
		}
	case "docker":
		if len(args) > 0 && args[0] == "pull" {
			return "pull"
		}
		if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
			return "oci-labels"
		}
	case "gh":
		if len(args) < 2 || args[0] != "api" {
			return ""
		}
		endpoint, _, _ := strings.Cut(args[1], "?")
		_, action, found := strings.Cut(endpoint, "/actions/")
		if !strings.HasPrefix(endpoint, "repos/") || !found {
			return ""
		}
		switch action {
		case "workflows/ci.yml/runs":
			return "ci-runs"
		case "workflows/release.yml/runs":
			return "release-runs"
		case "workflows/workspace-test-baseline.yml/runs":
			return "workspace-baseline-runs"
		case "workflows/postgres-backup-image.yml/runs":
			return "backup-runs"
		}
		parts := strings.Split(action, "/")
		if len(parts) >= 2 && parts[1] != "" {
			if parts[0] == "runs" {
				if len(parts) == 2 {
					return "release-run"
				}
				if len(parts) == 3 {
					switch parts[2] {
					case "jobs":
						return "publication-jobs"
					case "artifacts":
						return "artifact-list"
					}
				}
			}
			if parts[0] == "artifacts" && len(parts) == 3 && parts[2] == "zip" {
				return "artifact-download"
			}
		}
	}
	return ""
}

func environmentReleaseProgressCommand(key string, run environmentReleaseCommand, advance func(string)) environmentReleaseCommand {
	if run == nil {
		return nil
	}
	return func(ctx context.Context, command string, args ...string) ([]byte, error) {
		action := "read"
		switch command {
		case "docker":
			action = "release-image"
		case "gh":
			action = "release-ci"
		}
		if stage := environmentReleaseProgressStage(key, environmentReleaseProgressStep(command, args)); stage != "" {
			if advance != nil {
				advance(stage)
			} else {
				environmentDeploymentProgress(ctx, stage)
			}
		}
		done := environmentDeploymentOperation(ctx, action, "")
		defer done()
		return run(ctx, command, args...)
	}
}

func verifyEnvironmentReleaseWithCommand(ctx context.Context, cfg deploymentConfig, release environmentRelease, run func(context.Context, string, ...string) ([]byte, error)) error {
	nextStep, abortSteps := environmentDeploymentSteps(ctx)
	defer abortSteps()
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, release.WorkspaceRevision, environmentReleaseProgressCommand("workspace", run, nextStep)); err != nil {
		return environmentReleaseFailure(err, "deployment.release.source", "workspace", "executing workspace revision does not match the selected immutable release", environmentReleaseSourceNext)
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
		imageRun := environmentReleaseProgressCommand(key, run, nextStep)
		if !pulled[image.Reference] {
			if _, err := imageRun(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
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
			if err := verifyEnvironmentBackupPublication(ctx, image, imageRun); err != nil {
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
		raw, err := imageRun(ctx, "git", "-C", cfg.Workspace, "ls-tree", release.WorkspaceRevision, "--", source.RepoPath)
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.source", key, "selected workspace release gitlink cannot be read", environmentReleaseSourceNext)
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 4 || fields[0] != "160000" || fields[1] != "commit" || fields[2] != image.SourceRevision || fields[3] != source.RepoPath {
			return environmentReleaseFailure(nil, "deployment.release.source", key, "selected image source differs from its workspace release gitlink", environmentReleaseSourceNext)
		}
		raw, err = imageRun(ctx, "git", "-C", filepath.Join(cfg.Workspace, source.RepoPath), "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(raw)) != image.SourceRevision {
			return environmentReleaseFailure(err, "deployment.release.source", key, "schema and startup source checkout differs from the selected image source revision", environmentReleaseSourceNext)
		}
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, source.RepoPath), image.SourceRevision, imageRun); err != nil {
			return environmentReleaseFailure(err, "deployment.release.source", key, "selected image source revision does not match its immutable release", environmentReleaseSourceNext)
		}
		if err := verifyDeploymentUpgradeImageCIAtWithCommand(ctx, image.Reference, image.SourceRevision, imageRun); err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "selected image OCI revision or exact-source CI and publication evidence cannot be verified", environmentReleasePublicationNext)
		}
		repo := "hkt999rtk/" + source.RepoName
		raw, err = imageRun(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/release.yml/runs?head_sha="+image.SourceRevision+"&per_page=100")
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "exact-source Release run inventory is unavailable", environmentReleasePublicationNext)
		}
		runID, err := deploymentUpgradeSuccessfulRun(raw, image.SourceRevision)
		if err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "a complete successful latest exact-source main Release run cannot be verified", environmentReleasePublicationNext)
		}
		if _, err = verifyEnvironmentPublication(ctx, repo, runID, image, false, imageRun); err != nil {
			return environmentReleaseFailure(err, "deployment.release.publication", key, "selected immutable image cannot be verified against the exact successful Release attempt's formal artifact", environmentReleasePublicationNext)
		}
	}
	if runner, ok := release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"]; ok {
		raw, err := environmentReleaseProgressCommand("LKE_POSTGRES_BACKUP_RUNNER_IMAGE", run, nextStep)(ctx, "docker", "image", "inspect", runner.Reference, "--format", "{{json .Config.Labels}}")
		var labels map[string]string
		if err != nil || json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.base.name"] != release.Images["LKE_POSTGRES_IMAGE"].Reference {
			return environmentReleaseFailure(err, "deployment.release.backup", "LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "backup runner provenance does not match the selected PostgreSQL digest", environmentReleaseBackupNext)
		}
	}
	nextStep("")
	return nil
}

func verifyEnvironmentReleaseCheckout(ctx context.Context, directory, revision string, run func(context.Context, string, ...string) ([]byte, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !deploymentUpgradeSHA.MatchString(revision) || directory == "" || run == nil {
		return environmentReleaseFailure(nil, "deployment.release.source", "", "exact source revision and checkout are required", environmentReleaseSourceNext)
	}
	raw, err := run(ctx, "git", "-C", directory, "rev-parse", "HEAD")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil || strings.TrimSpace(string(raw)) != revision {
		return environmentReleaseFailure(err, "deployment.release.source", "", "executing checkout differs from the selected immutable release", environmentReleaseSourceNext)
	}
	return nil
}

// Schema requirements belong to the published image's source commit. Working
// tree additions or deletions must not change that image's migration inventory.
func verifyEnvironmentReleaseSchemas(ctx context.Context, cfg deploymentConfig, store secretStore, release environmentRelease) error {
	return verifyEnvironmentReleaseSchemasForPlan(ctx, cfg, store, release, nil, nil)
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

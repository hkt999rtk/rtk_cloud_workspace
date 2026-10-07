package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
	"strings"
	"time"
)

var environmentProviderImageKeys = []string{
	"LKE_POSTGRES_IMAGE", "LKE_REDIS_IMAGE", "LKE_FLEET_VALKEY_IMAGE", "LKE_REDIS_EXPORTER_IMAGE",
	"LKE_MQTT_IMAGE", "LKE_PROMETHEUS_IMAGE", "LKE_LOKI_IMAGE", "LKE_GRAFANA_IMAGE",
	"LKE_LOG_COLLECTOR_IMAGE", "LKE_OPENBAO_IMAGE", "LKE_ALPINE_IMAGE", "LKE_INGRESS_IMAGE",
}

// Maintained workload images are provider-neutral. Legacy image role names
// remain internal so existing renderers and identity bindings stay unchanged.
func environmentProviderImageConfigKey(role string) string {
	if role == "LKE_INGRESS_IMAGE" {
		return "INGRESS_CONTROLLER_IMAGE"
	}
	return strings.TrimPrefix(role, "LKE_")
}

func loadEnvironmentCIImages(ctx context.Context, cfg deploymentConfig, run environmentReleaseCommand) (environmentRelease, error) {
	providers := environmentRelease{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Images: map[string]environmentReleaseImage{}}
	values := appendMap(cfg.Values, cfg.AdapterResolved)
	for _, key := range environmentProviderImageKeys {
		configKey := environmentProviderImageConfigKey(key)
		reference := strings.TrimSpace(values[configKey])
		if reference == "" {
			continue
		}
		pinned, err := resolveConfiguredProviderImage(ctx, reference, run)
		if err != nil {
			return environmentRelease{}, &deploymentplan.Blocker{ID: "deployment.provider.images", Resource: configKey, Cause: "configured provider image cannot be pinned to an immutable registry digest", Next: "correct the maintained provider image reference and registry access, then repeat preflight"}
		}
		providers.Images[key] = environmentReleaseImage{Reference: pinned}
	}
	selected, err := resolveEnvironmentCIImages(ctx, cfg, providers, run)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return selected, err
		}
		return selected, &deploymentplan.Blocker{ID: "deployment.release", Cause: "the selected workspace service revisions have no complete verifiable formal CI image-manifest.json selection", Next: "check GitHub access and exact-source successful Release publication with unexpired image-manifest artifacts; publish any missing service revision, then repeat preflight"}
	}
	return selected, nil
}

func resolveConfiguredProviderImage(ctx context.Context, reference string, run environmentReleaseCommand) (string, error) {
	if immutableEnvironmentImage.MatchString(reference) {
		return reference, nil
	}
	if run == nil || reference == "" || strings.ContainsAny(reference, " \t\r\n") || strings.HasPrefix(reference, "-") || strings.HasSuffix(reference, ":latest") || strings.LastIndex(reference, ":") <= strings.LastIndex(reference, "/") {
		return "", errors.New("explicit configured provider version required")
	}
	if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", reference); err != nil {
		return "", errors.New("configured provider image unavailable")
	}
	raw, err := run(ctx, "docker", "image", "inspect", reference, "--format", "{{json .RepoDigests}}")
	var refs []string
	if err != nil || json.Unmarshal(raw, &refs) != nil {
		return "", errors.New("provider registry digest unavailable")
	}
	selected := ""
	for _, candidate := range refs {
		if immutableEnvironmentImage.MatchString(candidate) && environmentImageRepository(candidate) == environmentImageRepository(reference) {
			if selected != "" && selected != candidate {
				return "", errors.New("provider registry digest is ambiguous")
			}
			selected = candidate
		}
	}
	if selected == "" {
		return "", errors.New("provider registry digest does not match the configured repository")
	}
	return selected, nil
}

func verifyEnvironmentImageSources(ctx context.Context, cfg deploymentConfig, selected environmentRelease, run environmentReleaseCommand) error {
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, selected.WorkspaceRevision, run); err != nil {
		return err
	}
	keys := make([]string, 0, len(selected.Images))
	for key := range selected.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		image := selected.Images[key]
		if image.SourceRevision == "" || !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		if strings.Contains(image.Reference, "/rtk_cloud_workspace/postgres-backup@") {
			continue
		}
		source, err := deploymentUpgradeImageSource(image.Reference)
		if err != nil {
			return err
		}
		if seen[source.RepoPath] {
			continue
		}
		seen[source.RepoPath] = true
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, source.RepoPath), image.SourceRevision, run); err != nil {
			return err
		}
	}
	return nil
}

type environmentReleaseCommand func(context.Context, string, ...string) ([]byte, error)

// Resolve the selected workspace service revisions from formal CI image-manifest
// artifacts. Provider versions come from maintained configuration, not a file.
func resolveEnvironmentCIImages(ctx context.Context, cfg deploymentConfig, providers environmentRelease, run environmentReleaseCommand) (environmentRelease, error) {
	result := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Images: map[string]environmentReleaseImage{}}
	if run == nil || providers.Environment != result.Environment || providers.Stack != result.Stack {
		return result, errors.New("deployment.release: reviewed provider selection must match the environment")
	}
	raw, err := run(ctx, "git", "-C", cfg.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return result, errors.New("deployment.release.source: workspace revision unavailable")
	}
	result.WorkspaceRevision = strings.TrimSpace(string(raw))
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, result.WorkspaceRevision, run); err != nil {
		return result, err
	}
	// The clean selected workspace is the deployment target. Each pinned service
	// revision must have its own successful formal CI publication below.
	type frozenSource struct {
		source   lkeServiceImageSource
		revision string
	}
	var sources []frozenSource
	for _, source := range lkeServiceImageSources() {
		raw, err = run(ctx, "git", "-C", cfg.Workspace, "ls-tree", result.WorkspaceRevision, "--", source.RepoPath)
		fields := strings.Fields(string(raw))
		if err != nil || len(fields) != 4 || fields[0] != "160000" || fields[1] != "commit" || fields[3] != source.RepoPath || !deploymentUpgradeSHA.MatchString(fields[2]) {
			return result, errors.New("deployment.release.source: complete workspace service gitlinks required")
		}
		revision := fields[2]
		directory := filepath.Join(cfg.Workspace, source.RepoPath)
		if err := verifyEnvironmentReleaseCheckout(ctx, directory, revision, run); err != nil {
			return result, err
		}
		sources = append(sources, frozenSource{source, revision})
	}
	for key, image := range providers.Images {
		if strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		if !strings.HasPrefix(key, "LKE_") || !strings.HasSuffix(key, "_IMAGE") || !immutableEnvironmentImage.MatchString(image.Reference) || image.SourceRevision != "" {
			return result, errors.New("deployment.release: upstream provider versions require explicit immutable digests")
		}
		result.Images[key] = image
	}
	for _, frozen := range sources {
		repo := "hkt999rtk/" + frozen.source.RepoName
		evidence, runID, attempt, err := latestEnvironmentPublication(ctx, repo, "release.yml", frozen.revision, false, run)
		if err != nil {
			return result, err
		}
		repository := "ghcr.io/" + repo + "/" + frozen.source.Name
		image, err := selectEnvironmentPublishedImage(evidence, repo, runID, attempt, frozen.revision, repository, false)
		if err != nil {
			return result, err
		}
		result.Images[frozen.source.EnvKey] = image
		if frozen.source.RepoName == "rtk_video_cloud" {
			image, err = selectEnvironmentPublishedImage(evidence, repo, runID, attempt, frozen.revision, "ghcr.io/"+repo+"/video-cloud-emqx-pki", false)
			if err != nil {
				return result, err
			}
			result.Images["LKE_MQTT_PKI_IMAGE"] = image
			// The host/installer has its own exact-source formal CI package.
			// Stock OpenBao remains the provider image; no local dev package is reused.
			if host, hostErr := selectEnvironmentPublishedImage(evidence, repo, runID, attempt, frozen.revision, "ghcr.io/"+repo+"/video-cloud-openbao-pki", false); hostErr == nil {
				result.Images["LKE_OPENBAO_PKI_IMAGE"] = host
			}
			// Missing host publication blocks only a target with the managed host
			// enabled; an existing static TLS listener does not use this package.

		}
	}
	if result.Images["LKE_POSTGRES_IMAGE"].Reference == "" || result.Images["LKE_INGRESS_IMAGE"].Reference == "" || result.Images["LKE_OPENBAO_IMAGE"].Reference == "" {
		return result, &deploymentplan.Blocker{ID: "deployment.provider.images", Cause: "maintained PostgreSQL, ingress or OpenBao image version is not configured", Next: "maintain POSTGRES_IMAGE, INGRESS_CONTROLLER_IMAGE and OPENBAO_IMAGE in cloud_deploy/architectures/kubernetes/architecture.env or cloud_env/<environment>/overrides/architecture.env; preflight pins their registry digests without a local release.json"}
	}
	if cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" {
		repo := "hkt999rtk/rtk_cloud_workspace"
		evidence, runID, attempt, err := latestEnvironmentPublication(ctx, repo, "postgres-backup-image.yml", result.WorkspaceRevision, true, run)
		if err != nil {
			return result, err
		}
		var backup struct {
			Postgres string `json:"source_postgres_image"`
		}
		if json.Unmarshal(evidence, &backup) != nil || backup.Postgres != result.Images["LKE_POSTGRES_IMAGE"].Reference {
			return result, errors.New("deployment.release.backup: published runner PostgreSQL base differs from reviewed provider digest")
		}
		image, err := selectEnvironmentPublishedImage(evidence, repo, runID, attempt, result.WorkspaceRevision, "ghcr.io/"+repo+"/postgres-backup", true)
		if err != nil {
			return result, err
		}
		result.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"] = image
	}
	// Repeat source checks after network requests. A changed source must not be
	// silently frozen as a different release midway through evidence collection.
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, result.WorkspaceRevision, run); err != nil {
		return result, err
	}
	for _, frozen := range sources {
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, frozen.source.RepoPath), frozen.revision, run); err != nil {
			return result, err
		}
	}
	return result, nil
}
func latestEnvironmentPublication(ctx context.Context, repo, workflow, revision string, backup bool, run environmentReleaseCommand) ([]byte, int64, int, error) {
	raw, err := run(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/"+workflow+"/runs?head_sha="+revision+"&per_page=100")
	if err != nil {
		return nil, 0, 0, errors.New("deployment.release.publication: exact-source run inventory unavailable")
	}
	id, err := deploymentUpgradeSuccessfulRun(raw, revision)
	if err != nil {
		return nil, 0, 0, err
	}
	evidence, attempt, err := readEnvironmentPublication(ctx, repo, id, revision, backup, run)
	return evidence, id, attempt, err
}
func selectEnvironmentPublishedImage(raw []byte, repo string, id int64, attempt int, revision, repository string, backup bool) (environmentReleaseImage, error) {
	var manifest struct {
		Image        string `json:"image"`
		Digest       string `json:"digest"`
		RunnerTag    string `json:"runner_tag"`
		RunnerDigest string `json:"runner_digest"`
		Images       map[string]struct {
			Image  string `json:"image"`
			Digest string `json:"digest"`
		} `json:"images"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return environmentReleaseImage{}, errors.New("deployment.release.publication: invalid publication manifest")
	}
	digest := ""
	if backup {
		if environmentImageRepository(manifest.RunnerTag) == repository {
			digest = manifest.RunnerDigest
		}
	} else {
		if manifest.Image == repository {
			digest = manifest.Digest
		}
		for _, entry := range manifest.Images {
			if entry.Image == repository {
				if digest != "" && digest != entry.Digest {
					return environmentReleaseImage{}, errors.New("deployment.release.publication: conflicting consumer digest")
				}
				digest = entry.Digest
			}
		}
	}
	image := environmentReleaseImage{Reference: repository + "@" + digest, SourceRevision: revision}
	if !immutableEnvironmentImage.MatchString(image.Reference) {
		return image, errors.New("deployment.release.publication: exact consumer digest missing; republish the frozen source through formal CI")
	}
	if err := verifyPublicationJSON(raw, repo, id, attempt, image, backup); err != nil {
		return image, err
	}
	return image, nil
}

func environmentCIImageCommand(cfg deploymentConfig) environmentReleaseCommand {
	return func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command != "gh" {
			return deploymentUpgradeCommand(ctx, command, args...)
		}
		store, err := newSecretStore("", cfg.Environment)
		if err != nil {
			return nil, errors.New("selected environment credentials unavailable")
		}
		operator, err := store.readOperator()
		if err != nil || operator["GHCR_PULL_TOKEN"] == "" {
			return nil, errors.New("selected environment GHCR_PULL_TOKEN is required for formal CI artifact access")
		}
		readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(readCtx, command, args...)
		cmd.WaitDelay = time.Second
		cmd.Env = append(os.Environ(), "GH_TOKEN="+operator["GHCR_PULL_TOKEN"])
		raw, err := cmd.Output()
		if err != nil {
			return nil, errors.New("formal CI artifact access failed; check environment token permissions and provider availability")
		}
		return raw, nil
	}
}

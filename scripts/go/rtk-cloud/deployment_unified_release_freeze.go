package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type environmentReleaseCommand func(context.Context, string, ...string) ([]byte, error)

// Freeze all source revisions before asking CI for publication. The reviewed
// provider manifest supplies immutable upstream versions, never application tags.
func freezeEnvironmentRelease(ctx context.Context, cfg deploymentConfig, providers environmentRelease, run environmentReleaseCommand) (environmentRelease, error) {
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
	// Refresh main before describing this as latest. Fetch does not change the
	// checkout; out-of-date source must be synchronized separately.
	if _, err := run(ctx, "git", "-C", cfg.Workspace, "fetch", "--prune", "origin", "main"); err != nil {
		return result, errors.New("deployment.release.source: remote main cannot be refreshed")
	}
	raw, err = run(ctx, "git", "-C", cfg.Workspace, "rev-parse", "origin/main")
	if err != nil || strings.TrimSpace(string(raw)) != result.WorkspaceRevision {
		return result, errors.New("deployment.release.source: synchronize the workspace to refreshed main before selecting latest")
	}
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
	if result.Images["LKE_POSTGRES_IMAGE"].Reference == "" || result.Images["LKE_INGRESS_IMAGE"].Reference == "" || result.Images["LKE_OPENBAO_IMAGE"].Reference == "" {
		return result, errors.New("deployment.release: reviewed PostgreSQL, ingress and OpenBao provider selections required")
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
		}
	}
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

func runEnvironmentReleaseFreeze(args []string) error {
	fs := flag.NewFlagSet("deployment freeze-release", flag.ContinueOnError)
	environment := fs.String("environment", "", "maintained environment")
	workspace := fs.String("workspace", "", "workspace root")
	environmentRoot := fs.String("environment-root", "", "explicit maintained environment root")
	providerRelease := fs.String("provider-release", "", "reviewed immutable provider release manifest")
	output := fs.String("output", "", "new absolute JSON manifest under ~/.config/rtk_cloud/<environment>/deployment/releases/; existing files are never overwritten")
	timeout := fs.Duration("timeout", 10*time.Minute, "source and publication deadline")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return exitCode(2)
	}
	if fs.NArg() != 0 || *environment == "" || *providerRelease == "" || *output == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "error: --environment, --provider-release, --output and a positive --timeout are required")
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: maintained configuration unavailable")
		return exitCode(2)
	}
	if err := validateEnvironmentDeploymentJSONPath(cfg.Environment, *output, "releases"); err != nil {
		fmt.Fprintln(os.Stderr, "error: --output must be a new JSON file in the selected environment deployment/releases directory with private real parent directories")
		return exitCode(2)
	}
	providers, err := loadEnvironmentRelease(cfg, *providerRelease)
	if err != nil {
		fmt.Fprintln(os.Stderr, "NO-GO deployment.release: reviewed immutable provider selection unavailable")
		return exitCode(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	release, err := freezeEnvironmentRelease(ctx, cfg, providers, deploymentUpgradeCommand)
	if err != nil {
		fmt.Fprintln(os.Stderr, "NO-GO deployment.release: frozen source, exact CI publication or provider compatibility could not be verified; synchronize main and republish the exact source before retrying")
		return exitCode(1)
	}
	raw, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return exitCode(1)
	}
	if err = publishEnvironmentReleaseSelection(cfg.Environment, *output, append(raw, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot save a new private release manifest; existing selection was preserved")
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "FROZEN environment=%s workspace-revision=%s\nnext: review the manifest and run full preflight with --release; freezing publication is not deployment GO\n", release.Environment, release.WorkspaceRevision)
	return nil
}

func publishEnvironmentReleaseSelection(environment, path string, raw []byte) error {
	if err := validateEnvironmentDeploymentJSONPath(environment, path, "releases"); err != nil {
		return err
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	return writeExclusiveEnvironmentRelease(path, raw)
}

func writeExclusiveEnvironmentRelease(path string, raw []byte) error {
	if !filepath.IsAbs(path) || len(raw) == 0 || len(raw) > 4<<20 {
		return errors.New("bounded absolute release destination required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(raw) || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("cannot persist complete release selection")
	}
	return nil
}

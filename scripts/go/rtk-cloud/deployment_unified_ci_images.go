package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
	"strings"
	"time"
)

var environmentProviderImageKeys = []string{
	"LKE_POSTGRES_IMAGE", "LKE_REDIS_IMAGE", "LKE_FLEET_VALKEY_IMAGE", "LKE_REDIS_EXPORTER_IMAGE",
	"LKE_MQTT_IMAGE", "LKE_PROMETHEUS_IMAGE", "LKE_LOKI_IMAGE", "LKE_GRAFANA_IMAGE",
	"LKE_LOG_COLLECTOR_IMAGE", "LKE_OPENBAO_IMAGE", "LKE_ALPINE_IMAGE", "LKE_INGRESS_IMAGE",
	"LKE_ZAMMAD_IMAGE", "LKE_ZAMMAD_MEMCACHED_IMAGE", "LKE_ZAMMAD_REDIS_IMAGE", "LKE_ZAMMAD_POSTGRES_IMAGE",
	"LKE_ZAMMAD_ELASTICSEARCH_IMAGE", "LKE_ZAMMAD_OS_SHELL_IMAGE", "LKE_ZAMMAD_ALPINE_IMAGE",
}

// Maintained workload images are provider-neutral. Legacy image role names
// remain internal so existing renderers and identity bindings stay unchanged.
func environmentProviderImageConfigKey(role string) string {
	if role == "LKE_INGRESS_IMAGE" {
		return "INGRESS_CONTROLLER_IMAGE"
	}
	return strings.TrimPrefix(role, "LKE_")
}

func loadEnvironmentCIImages(ctx context.Context, cfg deploymentConfig, run environmentReleaseCommand, operation ...string) (environmentRelease, error) {
	providers := environmentRelease{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Images: map[string]environmentReleaseImage{}}
	values := appendMap(cfg.Values, cfg.AdapterResolved)
	for _, key := range environmentProviderImageKeys {
		if strings.HasPrefix(key, "LKE_ZAMMAD_") && cfg.Values["SUPPORT_TICKETS_ENABLED"] != "true" {
			continue
		}
		configKey := environmentProviderImageConfigKey(key)
		reference := strings.TrimSpace(values[configKey])
		if reference == "" {
			continue
		}
		stage := "deployment.release.provider." + configKey
		environmentDeploymentProgress(ctx, stage)
		pinned, err := resolveConfiguredProviderImage(ctx, reference, run)
		environmentDeploymentProgressDone(ctx, stage, err)
		if err != nil {
			var pull *deploymentImagePullError
			if errors.As(err, &pull) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return environmentRelease{}, environmentReleaseFailure(err, "deployment.provider.images", configKey, "configured provider image pull failed", environmentReleasePullNext)
			}
			return environmentRelease{}, &deploymentplan.Blocker{ID: "deployment.provider.images", Resource: configKey, Cause: "configured provider image cannot be pinned to an immutable registry digest", Next: "correct the maintained provider image reference and registry access, then repeat preflight"}
		}
		providers.Images[key] = environmentReleaseImage{Reference: pinned}
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return environmentRelease{}, err
	}
	selection, err := readEnvironmentImageSelection(store)
	if err != nil {
		return environmentRelease{}, err
	}
	if len(operation) > 1 {
		return environmentRelease{}, errors.New("one deployment operation is required")
	}
	if len(operation) == 1 && operation[0] == "upgrade" {
		selection, err = readEnvironmentCIRolloutSelection(ctx, cfg, selection)
		if err != nil {
			return environmentRelease{}, err
		}
	}
	if len(operation) == 1 && operation[0] == "create" && cfg.Environment == "dev" {
		policy, policyErr := readCIBranchSelection(cfg)
		if policyErr != nil {
			return environmentRelease{}, policyErr
		}
		selection, err = resolveFreshCIBranchSelection(ctx, selection, policy, func(ctx context.Context, policy ciBranchSelection, repo, repository string) (environmentReleaseImage, error) {
			return resolveCIRolloutImage(ctx, policy, repo, repository, run)
		})
		if err != nil {
			return environmentRelease{}, err
		}
	}
	selected, err := resolveEnvironmentCIImages(ctx, cfg, providers, run, selection)
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
		var pull *deploymentImagePullError
		if errors.As(err, &pull) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
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

func verifyEnvironmentImageSources(ctx context.Context, cfg deploymentConfig, selected environmentRelease, run environmentReleaseCommand, selectedEvidence ...environmentImageSelection) error {
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, selected.WorkspaceRevision, run); err != nil {
		return err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	var selection environmentImageSelection
	if len(selectedEvidence) > 1 {
		return errors.New("one authenticated release selection required")
	}
	if len(selectedEvidence) == 1 {
		selection = selectedEvidence[0]
		if selection.Environment != cfg.Environment {
			return errors.New("release selection belongs to another environment")
		}
	} else {
		selection, err = readEnvironmentImageSelection(store)
		if err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(selected.Images))
	policy, err := environmentCIBranchPolicy(cfg)
	if err != nil {
		return err
	}
	for key := range selected.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		image := selected.Images[key]
		tracked, err := verifyCIRolloutImageProvenance(ctx, policy, key, image, run)
		if err != nil {
			return err
		}
		if tracked {
			continue
		}
		if explicit, ok := selection.Images[key]; ok {
			if deploymentplan.Digest(explicit) != deploymentplan.Digest(image) {
				return errors.New("selected environment image changed")
			}
			continue
		}
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

var errEnvironmentPublicationConsumerMissing = errors.New("deployment.release.publication: exact consumer digest missing; republish the frozen source through formal CI")

// Preserve only a static access class and, when applicable, the caller's
// cancellation/deadline. Native stderr and credentials never become a cause.
type deploymentCIReadError struct {
	Code  string
	cause error
}

func (e *deploymentCIReadError) Error() string {
	return "formal CI artifact access failed [" + e.Code + "]; check environment token permissions and provider availability"
}

func (e *deploymentCIReadError) Unwrap() error { return e.cause }

func isDeploymentCIReadError(err error) bool {
	var failure *deploymentCIReadError
	return errors.As(err, &failure)
}

func deploymentCIContextFailure(err error) *deploymentCIReadError {
	if errors.Is(err, context.Canceled) {
		return &deploymentCIReadError{Code: "CANCELED", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &deploymentCIReadError{Code: "TIMEOUT", cause: context.DeadlineExceeded}
	}
	return &deploymentCIReadError{Code: "ACCESS"}
}

// Only the existing option-free default GET form is eligible. Fields, input,
// explicit methods, workflow commands and GraphQL never enter a retry loop.
func retryableDeploymentCIGet(args []string) bool {
	if len(args) != 2 || args[0] != "api" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		return false
	}
	endpoint := strings.TrimSuffix(strings.SplitN(args[1], "?", 2)[0], "/")
	return endpoint != "graphql" && !strings.HasSuffix(endpoint, "/graphql")
}

var deploymentCIHTTPStatus = regexp.MustCompile(`(?i)\(HTTP ([1-5][0-9]{2})\)`)

func classifyDeploymentCIReadDiagnostic(raw []byte, attemptErr error) (string, bool) {
	s := strings.ToLower(string(raw))
	if strings.Contains(s, "bad credentials") || strings.Contains(s, "unauthorized") || strings.Contains(s, "authentication required") || strings.Contains(s, "gh auth login") {
		return "AUTH", false
	}
	if strings.Contains(s, "forbidden") {
		return "FORBIDDEN", false
	}
	if strings.Contains(s, "malformed") || strings.Contains(s, "invalid json") || strings.Contains(s, "error parsing json") {
		return "REQUEST", false
	}
	statuses := deploymentCIHTTPStatus.FindAllStringSubmatch(s, -1)
	for _, status := range statuses {
		switch status[1] {
		case "429", "502", "503", "504":
			continue
		case "401":
			return "AUTH", false
		case "403":
			return "FORBIDDEN", false
		case "404":
			return "NOT_FOUND", false
		default:
			return "REQUEST", false
		}
	}
	if len(statuses) != 0 {
		return "HTTP_" + statuses[len(statuses)-1][1], true
	}
	if errors.Is(attemptErr, context.DeadlineExceeded) || strings.Contains(s, "i/o timeout") || strings.Contains(s, "tls handshake timeout") || strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "client.timeout exceeded") {
		return "TIMEOUT", true
	}
	if strings.Contains(s, "connection reset") {
		return "NETWORK_RESET", true
	}
	if strings.Contains(s, "unexpected eof") {
		return "NETWORK_EOF", true
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "eof" || strings.HasSuffix(line, ": eof") {
			return "NETWORK_EOF", true
		}
	}
	return "ACCESS", false
}

func runDeploymentCIRead(ctx context.Context, args []string, token string, attemptTimeout, retryDelay time.Duration) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, deploymentCIContextFailure(err)
		}
		readCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		cmd := exec.CommandContext(readCtx, "gh", args...)
		cmd.WaitDelay = time.Second
		cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
		// Reuse the bounded in-memory diagnostic sink. Neither an ExitError
		// nor its stderr/partial stdout is retained in the returned error.
		var diagnostic deploymentPullDiagnostic
		cmd.Stderr = &diagnostic
		raw, err := cmd.Output()
		attemptErr := readCtx.Err()
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, deploymentCIContextFailure(err)
		}
		if err == nil {
			return raw, nil
		}
		code, transient := classifyDeploymentCIReadDiagnostic(diagnostic.data, attemptErr)
		failure := &deploymentCIReadError{Code: code}
		if code == "TIMEOUT" {
			failure.cause = context.DeadlineExceeded
		}
		if !retryableDeploymentCIGet(args) || !transient || attempt+1 >= deploymentReadMaxAttempts {
			return nil, failure
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, deploymentCIContextFailure(ctx.Err())
		case <-timer.C:
		}
	}
}

// Resolve the selected workspace service revisions from formal CI image-manifest
// artifacts. Provider versions come from maintained configuration, not a file.
func resolveEnvironmentCIImages(ctx context.Context, cfg deploymentConfig, providers environmentRelease, run environmentReleaseCommand, selections ...environmentImageSelection) (environmentRelease, error) {
	result := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Images: map[string]environmentReleaseImage{}}
	if len(selections) > 1 {
		return result, errors.New("one environment image selection required")
	}
	if len(selections) == 1 {
		if selections[0].Environment != cfg.Environment {
			return result, errors.New("image selection belongs to another environment")
		}
		for role, image := range selections[0].Images {
			result.Images[role] = image
		}
	}
	stage := "deployment.release.sources"
	environmentDeploymentProgress(ctx, stage)
	// A step is cleared only after its validation succeeds. Every early return
	// or panic finishes the active step as failed, without printing private errors.
	defer func() {
		if stage != "" {
			environmentDeploymentProgressResult(ctx, stage, "FAIL")
		}
	}()
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
		if result.Images[source.EnvKey].Reference != "" && (source.RepoName != "rtk_video_cloud" || result.Images["LKE_MQTT_PKI_IMAGE"].Reference != "" && result.Images["LKE_OPENBAO_PKI_IMAGE"].Reference != "") {
			continue
		}
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
	environmentDeploymentProgressDone(ctx, stage, nil)
	stage = ""
	for _, frozen := range sources {
		stage = "deployment.release.ci." + frozen.source.Name
		environmentDeploymentProgress(ctx, stage)
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
		if result.Images[frozen.source.EnvKey].Reference == "" {
			result.Images[frozen.source.EnvKey] = image
		}
		if frozen.source.RepoName == "rtk_video_cloud" {
			image, err = selectEnvironmentPublishedImage(evidence, repo, runID, attempt, frozen.revision, "ghcr.io/"+repo+"/video-cloud-emqx-pki", false)
			if err != nil {
				return result, err
			}
			if result.Images["LKE_MQTT_PKI_IMAGE"].Reference == "" {
				result.Images["LKE_MQTT_PKI_IMAGE"] = image
			}
			// The host/installer has its own exact-source formal CI package.
			// Stock OpenBao remains the provider image; no local dev package is reused.
			if host, hostErr := selectEnvironmentPublishedImage(evidence, repo, runID, attempt, frozen.revision, "ghcr.io/"+repo+"/video-cloud-openbao-pki", false); hostErr == nil && result.Images["LKE_OPENBAO_PKI_IMAGE"].Reference == "" {
				result.Images["LKE_OPENBAO_PKI_IMAGE"] = host
			}
			// Missing host publication blocks only a target with the managed host
			// enabled; an existing static TLS listener does not use this package.

		}
		environmentDeploymentProgressDone(ctx, stage, nil)
		stage = ""
	}
	if result.Images["LKE_POSTGRES_IMAGE"].Reference == "" || result.Images["LKE_INGRESS_IMAGE"].Reference == "" || result.Images["LKE_OPENBAO_IMAGE"].Reference == "" {
		return result, &deploymentplan.Blocker{ID: "deployment.provider.images", Cause: "maintained PostgreSQL, ingress or OpenBao image version is not configured", Next: "maintain POSTGRES_IMAGE, INGRESS_CONTROLLER_IMAGE and OPENBAO_IMAGE in cloud_deploy/architectures/kubernetes/architecture.env or cloud_env/<environment>/overrides/architecture.env; preflight pins their registry digests without a local release.json"}
	}
	if cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" && result.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"].Reference == "" {
		stage = "deployment.release.ci.postgres-backup"
		environmentDeploymentProgress(ctx, stage)
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
		environmentDeploymentProgressDone(ctx, stage, nil)
		stage = ""
	}
	// Repeat source checks after network requests. A changed source must not be
	// silently frozen as a different release midway through evidence collection.
	stage = "deployment.release.sources.recheck"
	environmentDeploymentProgress(ctx, stage)
	if err := verifyEnvironmentReleaseCheckout(ctx, cfg.Workspace, result.WorkspaceRevision, run); err != nil {
		return result, err
	}
	for _, frozen := range sources {
		if err := verifyEnvironmentReleaseCheckout(ctx, filepath.Join(cfg.Workspace, frozen.source.RepoPath), frozen.revision, run); err != nil {
			return result, err
		}
	}
	environmentDeploymentProgressDone(ctx, stage, nil)
	stage = ""
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
		return image, errEnvironmentPublicationConsumerMissing
	}
	if err := verifyPublicationJSON(raw, repo, id, attempt, image, backup); err != nil {
		return image, err
	}
	manifestOTA, err := publishedOTARegistration(raw, image)
	if err != nil {
		return image, err
	}
	image.OTARegistration = manifestOTA
	return image, nil
}

func environmentCIImageCommand(cfg deploymentConfig) environmentReleaseCommand {
	return func(ctx context.Context, command string, args ...string) ([]byte, error) {
		return cachedDeploymentCICommand(ctx, cfg.Environment, command, args, func() (output []byte, resultErr error) {
			kind := command
			if command == "gh" {
				kind = "github"
			}
			measured := beginDeploymentQuery(ctx, kind)
			defer func() { measured(resultErr) }()
			if command == "docker" && len(args) > 0 && args[0] == "pull" {
				// Release pulls use the same selected-environment credential and
				// process-lifetime boundary as full image qualification. Ambient
				// Docker Desktop helpers cannot supply another identity or hang
				// past cancellation. The subsequent OCI/CI checks stay unchanged.
				if len(args) != 4 || args[1] != "--platform" || args[2] != "linux/amd64" || args[3] == "" {
					return nil, errors.New("docker release prerequisite failed; check authentication, tool availability and deadline")
				}
				store, err := newSecretStore("", cfg.Environment)
				if err != nil {
					return nil, errors.New("selected environment credentials unavailable")
				}
				operator, err := store.readOperator()
				username, token := operator["GHCR_PULL_USERNAME"], operator["GHCR_PULL_TOKEN"]
				if err != nil || username == "" || token == "" || strings.ContainsAny(username+token, "\r\n") {
					return nil, errors.New("selected environment credentials unavailable")
				}
				if err := pullRolloutImageContext(ctx, cfg.Environment, username, token, args[3]); err != nil {
					var classified *deploymentImagePullError
					if errors.As(err, &classified) {
						return nil, classified
					}
					return nil, errors.New("docker release prerequisite failed; check authentication, tool availability and deadline")
				}
				return nil, nil
			}
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
			return runDeploymentCIRead(ctx, args, operator["GHCR_PULL_TOKEN"], 20*time.Second, newDeploymentCheckRuntime(ctx, cfg.Environment).retryDelay)
		})
	}
}

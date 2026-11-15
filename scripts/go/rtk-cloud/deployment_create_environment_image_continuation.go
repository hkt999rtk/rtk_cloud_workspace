package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"sync"
)

type createVersionPublication struct {
	Repository  string `json:"repository"`
	RunID       int64  `json:"run_id"`
	Attempt     int    `json:"run_attempt"`
	ManifestSHA string `json:"manifest_sha256"`
	Backup      bool   `json:"backup,omitempty"`
}
type createVersionImage struct {
	Reference      string                    `json:"reference"`
	SourceRevision string                    `json:"source_revision,omitempty"`
	Publication    *createVersionPublication `json:"publication,omitempty"`
}
type createVersionSelection struct {
	Environment string                     `json:"environment"`
	Images      map[string]json.RawMessage `json:"images"`
}
type createVersionDifference struct {
	Role    string          `json:"role"`
	Current string          `json:"current"`
	Latest  json.RawMessage `json:"latest"`
	RunURL  string          `json:"run_url"`
}
type createVersionReceipt struct {
	Previous    string                    `json:"previous_selection"`
	Selection   createVersionSelection    `json:"selection"`
	Differences []createVersionDifference `json:"differences"`
}

// Validate only the updater's recorded selected-environment transition.
// Publication authenticity is checked separately against its actual CI manifest.
func validateCreateOpenBaoVersionReceipt(environment string, current map[string]string, originalSHA, oldRef string, raw []byte) (createVersionImage, error) {
	bad := func() (createVersionImage, error) {
		return createVersionImage{}, errors.New("environment OpenBao image transition does not match the original operator and version receipt")
	}
	const key = "DEPLOYMENT_IMAGES"
	const role = "LKE_OPENBAO_PKI_IMAGE"
	const repo = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:"
	if !secretEnvironmentPattern.MatchString(environment) || !environmentPreflightDigest.MatchString(originalSHA) || !immutableEnvironmentImage.MatchString(oldRef) || !strings.HasPrefix(oldRef, repo) || len(raw) == 0 || len(raw) > 32<<20 {
		return bad()
	}
	var receipt createVersionReceipt
	if decodeEnvironmentJSON(raw, &receipt) != nil || len(receipt.Differences) != 1 || receipt.Selection.Environment != environment || len(receipt.Selection.Images) == 0 {
		return bad()
	}
	diff := receipt.Differences[0]
	if diff.Role != role || diff.RunURL == "" {
		return bad()
	}
	var selected createVersionSelection
	value, exists := current[key]
	if !exists || decodeEnvironmentJSON([]byte(value), &selected) != nil || selected.Environment != environment || !sameCreateVersionJSON(selected, receipt.Selection) {
		return bad()
	}
	selectedRaw, exists := selected.Images[role]
	if !exists || !sameCreateVersionRaw(selectedRaw, diff.Latest) {
		return bad()
	}
	var image createVersionImage
	if decodeEnvironmentJSON(selectedRaw, &image) != nil || !immutableEnvironmentImage.MatchString(image.Reference) || !strings.HasPrefix(image.Reference, repo) || image.Reference == oldRef || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Publication == nil || image.Publication.Repository != "hkt999rtk/rtk_video_cloud" || image.Publication.RunID <= 0 || image.Publication.Attempt <= 0 || !environmentPreflightDigest.MatchString(image.Publication.ManifestSHA) || image.Publication.Backup {
		return bad()
	}
	old := createVersionSelection{Environment: environment, Images: map[string]json.RawMessage{}}
	if receipt.Previous != "" && (decodeEnvironmentJSON([]byte(receipt.Previous), &old) != nil || old.Environment != environment || len(old.Images) == 0) {
		return bad()
	}
	previousRef := oldRef
	if original, exists := old.Images[role]; exists {
		var prior createVersionImage
		if decodeEnvironmentJSON(original, &prior) != nil || !immutableEnvironmentImage.MatchString(prior.Reference) || !strings.HasPrefix(prior.Reference, repo) {
			return bad()
		}
		// The original operator hash below authenticates this explicit
		// environment selection, even when the historical release used a
		// different base image. Never substitute that base as the old target.
		previousRef = prior.Reference
	}
	if diff.Current != previousRef || image.Reference == previousRef {
		return bad()
	}
	for k, v := range old.Images {
		if k != role && !sameCreateVersionRaw(v, selected.Images[k]) {
			return bad()
		}
	}
	for k, v := range selected.Images {
		if k != role && !sameCreateVersionRaw(v, old.Images[k]) {
			return bad()
		}
	}
	if _, err := restoreCreateVersionOperator(current, receipt.Previous, originalSHA); err != nil {
		return bad()
	}

	return image, nil
}
func restoreCreateVersionOperator(current map[string]string, previous, originalSHA string) (map[string]string, error) {
	const key = "DEPLOYMENT_IMAGES"
	bad := func() (map[string]string, error) {
		return nil, errors.New("original operator settings do not match the version receipt")
	}
	restored := make(map[string]string, len(current))
	for k, v := range current {
		restored[k] = v
	}
	if previous != "" {
		restored[key] = previous
		if deploymentplan.Digest(restored) != originalSHA {
			return bad()
		}
		return restored, nil
	}
	delete(restored, key)
	if deploymentplan.Digest(restored) == originalSHA {
		return restored, nil
	}
	restored[key] = ""
	if deploymentplan.Digest(restored) == originalSHA {
		return restored, nil
	}
	return bad()
}

func sameCreateVersionRaw(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var x, y any
	ad, bd := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	ad.UseNumber()
	bd.UseNumber()
	return ad.Decode(&x) == nil && bd.Decode(&y) == nil && deploymentplan.Digest(x) == deploymentplan.Digest(y)
}
func sameCreateVersionJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && sameCreateVersionRaw(x, y)
}

func verifyCreateOpenBaoVersionPublication(ctx context.Context, selected createVersionImage, run environmentReleaseCommand) (environmentReleaseImage, error) {
	return verifyCreateVersionPublication(ctx, selected, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki", run)
}
func verifyCreateVersionPublication(ctx context.Context, selected createVersionImage, repository string, run environmentReleaseCommand) (environmentReleaseImage, error) {
	bad := func() (environmentReleaseImage, error) {
		return environmentReleaseImage{}, createProviderRecoveryBlock("environment-selected OpenBao image lacks its exact formal CI publication")
	}
	p := selected.Publication
	if ctx == nil || run == nil || p == nil || p.Repository != "hkt999rtk/rtk_video_cloud" || p.RunID <= 0 || p.Attempt <= 0 || p.Backup || !environmentPreflightDigest.MatchString(p.ManifestSHA) || !deploymentUpgradeSHA.MatchString(selected.SourceRevision) {
		return bad()
	}
	runPath := fmt.Sprintf("repos/%s/actions/runs/%d", p.Repository, p.RunID)
	formal := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		raw, err := run(ctx, command, args...)
		if err == nil && command == "gh" && len(args) == 2 && args[0] == "api" && args[1] == runPath {
			var source struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(raw, &source) != nil || source.Path != ".github/workflows/release.yml" {
				return nil, errors.New("selected image is not from formal Release")
			}
		}
		return raw, err
	}
	manifest, attempt, err := readEnvironmentPublication(ctx, p.Repository, p.RunID, selected.SourceRevision, false, formal)
	if err != nil {
		return environmentReleaseImage{}, err
	}
	if attempt != p.Attempt || environmentPreflightBytesSHA256(manifest) != p.ManifestSHA {
		return bad()
	}
	image, err := selectEnvironmentPublishedImage(manifest, p.Repository, p.RunID, p.Attempt, selected.SourceRevision, repository, false)
	if err != nil || image.Reference != selected.Reference || image.SourceRevision != selected.SourceRevision {
		return bad()
	}
	image.Publication = selected.Publication
	return image, nil
}

// Read only the selected environment's updater receipts. The current setting
// chooses the image; an exact unique receipt authorizes the original operator
// comparison, and CI evidence confirms that same selected digest.
func resolveCreateOpenBaoEnvironmentUpdate(ctx context.Context, cfg deploymentConfig, store secretStore, current map[string]string, originalSHA, oldRef string, run environmentReleaseCommand) (environmentReleaseImage, string, string, error) {
	image, selected, manifest, _, err := resolveCreateOpenBaoEnvironmentUpdateWithPrevious(ctx, cfg, store, current, originalSHA, oldRef, run)
	return image, selected, manifest, err
}
func resolveCreateOpenBaoEnvironmentUpdateWithPrevious(ctx context.Context, cfg deploymentConfig, store secretStore, current map[string]string, originalSHA, oldRef string, run environmentReleaseCommand) (environmentReleaseImage, string, string, string, error) {
	selected, previous, err := selectedCreateOpenBaoEnvironmentUpdate(ctx, cfg, store, current, originalSHA, oldRef)
	if err != nil {
		return environmentReleaseImage{}, "", "", "", err
	}
	image, err := verifyCreateOpenBaoVersionPublication(ctx, selected, run)
	if err != nil {
		return environmentReleaseImage{}, "", "", "", err
	}
	return image, environmentPreflightBytesSHA256([]byte(current["DEPLOYMENT_IMAGES"])), selected.Publication.ManifestSHA, previous, nil
}

func selectedCreateOpenBaoEnvironmentUpdate(ctx context.Context, cfg deploymentConfig, store secretStore, current map[string]string, originalSHA, oldRef string) (createVersionImage, string, error) {
	bad := func() (createVersionImage, string, error) {
		return createVersionImage{}, "", createProviderRecoveryBlock("environment OpenBao image change lacks a unique exact updater receipt")
	}
	if ctx == nil || cfg.Environment != store.Environment {
		return bad()
	}
	dir, err := store.safePath(filepath.Join("deployment", "version-updates"))
	if err != nil || validateEnvironmentPreflightDirectories(store, dir) != nil {
		return bad()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return bad()
	}
	var selected createVersionImage
	previous := ""
	matches := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var receipt createVersionReceipt
		if readExecutionJSON(path, &receipt) != nil {
			return bad()
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return bad()
		}
		candidate, err := validateCreateOpenBaoVersionReceipt(cfg.Environment, current, originalSHA, oldRef, raw)
		if err != nil {
			continue
		}
		matches++
		selected, previous = candidate, receipt.Previous
	}
	if matches != 1 {
		return bad()
	}
	return selected, previous, nil
}

type createOperatorVersionState struct {
	mu    sync.Mutex
	cache *createOperatorVersionCache
}
type createOperatorVersionCache struct {
	CurrentSHA, OriginalSHA, OriginalRef, Previous, SelectionSHA, ManifestSHA string
	RuntimeCapabilityRepairSHA                                                string
	Image                                                                     environmentReleaseImage
	RuntimePrevious                                                           string
	RuntimeImages                                                             map[string]environmentReleaseImage
}

// Verify the environment's current operator values against the original
// create admission. An exact recorded OpenBao handoff and explicitly selected
// runtime role continuation may differ. The restored map validates history only.
func verifyCreateEnvironmentOperator(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, current map[string]string, original *environmentCreatePlan, commands ...environmentReleaseCommand) (map[string]string, environmentReleaseImage, error) {
	bad := func() (map[string]string, environmentReleaseImage, error) {
		return nil, environmentReleaseImage{}, createProviderRecoveryBlock("environment settings changed outside the authenticated image update")
	}
	if ctx == nil || e == nil || e.Store != store || cfg.Environment != store.Environment || len(commands) > 1 || original == nil || original.Environment != cfg.Environment || original.Stack != e.Journal.Stack || original.ConfigurationSHA256 != deploymentplan.Digest(cfg) || e.Check(ctx) != nil {
		return bad()
	}
	oldImage := original.Release.Images["LKE_OPENBAO_PKI_IMAGE"]
	currentSHA := deploymentplan.Digest(current)
	if currentSHA == original.OperatorSHA256 {
		return current, oldImage, nil
	}
	bound, err := authenticatedCreateOperatorPlan(store, e)
	if err != nil || deploymentplan.Digest(*bound) != deploymentplan.Digest(*original) {
		return bad()
	}
	state := e.versionState
	if state == nil {
		state = &createOperatorVersionState{}
		e.versionState = state
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	cache := state.cache
	capabilityRepairSHA := deploymentplan.Digest(createRuntimeImageCapabilityRepair(ctx))
	if cache == nil || cache.RuntimeCapabilityRepairSHA != capabilityRepairSHA || cache.CurrentSHA != currentSHA || cache.OriginalSHA != original.OperatorSHA256 || cache.OriginalRef != oldImage.Reference {
		run := environmentCIImageCommand(cfg)
		if len(commands) == 1 {
			run = commands[0]
		}
		base, runtimeImages, err := selectedCreateRuntimeVersionUpdate(ctx, cfg, store, current, run)
		if err != nil {
			return nil, environmentReleaseImage{}, err
		}
		selected, previous, err := selectedCreateOpenBaoEnvironmentUpdate(ctx, cfg, store, base, original.OperatorSHA256, oldImage.Reference)
		if err != nil {
			return nil, environmentReleaseImage{}, err
		}
		selectedSHA, manifestSHA := environmentPreflightBytesSHA256([]byte(base["DEPLOYMENT_IMAGES"])), selected.Publication.ManifestSHA
		image, completed, err := completedOpenBaoPublicationImage(ctx, cfg, store, e, selected, selectedSHA)
		if err != nil {
			return nil, environmentReleaseImage{}, err
		}
		if !completed {
			image, err = verifyCreateOpenBaoVersionPublication(ctx, selected, run)
			if err != nil {
				return nil, environmentReleaseImage{}, err
			}
		}
		if e.Check(ctx) != nil {
			return bad()
		}
		cache = &createOperatorVersionCache{RuntimeCapabilityRepairSHA: capabilityRepairSHA, CurrentSHA: currentSHA, OriginalSHA: original.OperatorSHA256, OriginalRef: oldImage.Reference, Previous: previous, SelectionSHA: selectedSHA, ManifestSHA: manifestSHA, Image: image, RuntimePrevious: base[environmentImagesKey], RuntimeImages: runtimeImages}
		state.cache = cache
	}
	restored, err := restoreCreateVersionOperator(current, cache.Previous, original.OperatorSHA256)
	if err != nil || e.Check(ctx) != nil {
		return bad()
	}
	return restored, cache.Image, nil
}

func authenticatedCreateOperatorPlan(store secretStore, e *environmentCreateExecution) (*environmentCreatePlan, error) {
	bad := func() (*environmentCreatePlan, error) {
		return nil, createProviderRecoveryBlock("original create plan artifact unavailable for environment image transition")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil {
		return bad()
	}
	path, err := store.safePath(filepath.Join("deployment", "create", "recovery", e.Journal.PlanSHA256, "qualification.json"))
	if err != nil {
		return bad()
	}
	var artifact environmentCreateProviderRecoveryArtifact
	if readExecutionJSON(path, &artifact) != nil || artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != e.Journal.PlanSHA256 || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || artifact.Qualification.Decision != "GO" || (e.originalPlanSHA != "" && deploymentplan.Digest(artifact.Plan.Plan) != e.originalPlanSHA) {
		return bad()
	}
	return &artifact.Plan.Plan, nil
}

func createOperatorVersionEvidence(e *environmentCreateExecution, current map[string]string) (string, string, bool) {
	if e == nil || e.versionState == nil {
		return "", "", false
	}
	state := e.versionState
	state.mu.Lock()
	defer state.mu.Unlock()
	c := state.cache
	if c == nil || c.CurrentSHA != deploymentplan.Digest(current) {
		return "", "", false
	}
	return c.SelectionSHA, c.ManifestSHA, true
}

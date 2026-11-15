package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const failedForwardPublicationSchema = "rtk-cloud.create-failed-stage-tool-publication/v1"
const openBaoPublicCRLToolPurpose = "openbao-public-crl"

func failedForwardPublicationFile(purpose string) (string, error) {
	switch purpose {
	case "":
		return "public-crl-v2.json", nil
	case openBaoPublicCRLToolPurpose:
		return "openbao-public-crl-v1.json", nil
	default:
		return "", createProviderRecoveryBlock("unsupported repair publication purpose")
	}
}

// This record is emitted by the PR/CI release workflow into the original
// generation journal; it is operational evidence, not environment settings.
type createFailedForwardPublication struct {
	Purpose        string `json:"purpose,omitempty"`
	Schema         string `json:"schema"`
	Environment    string `json:"environment"`
	Parent         string `json:"parent_plan_sha256"`
	Repository     string `json:"repository"`
	PullRequest    int64  `json:"pull_request"`
	MergeCommit    string `json:"merge_commit"`
	RunID          int64  `json:"run_id"`
	RunAttempt     int    `json:"run_attempt"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type failedForwardTool struct {
	Image        environmentReleaseImage            `json:"image"`
	Publication  environmentReleasePublicationProof `json:"publication"`
	Capabilities json.RawMessage                    `json:"capabilities"`
}

// selectFailedForwardTool consumes only machine-published, journal-bound PR/CI
// evidence for this exact original generation. It never searches for a latest
// version, inspects local dirty sources, or updates environment service images.
func selectFailedForwardTool(ctx context.Context, store secretStore, e *environmentCreateExecution, run environmentReleaseCommand) (failedForwardTool, error) {
	return selectFailedForwardToolForPurpose(ctx, store, e, run, "")
}

func selectFailedForwardToolForPurpose(ctx context.Context, store secretStore, e *environmentCreateExecution, run environmentReleaseCommand, purpose string) (failedForwardTool, error) {
	var empty failedForwardTool
	bad := func() (failedForwardTool, error) {
		return empty, createProviderRecoveryBlock("original generation has no verified merged repair PR and exact successful CI release with native CRL capability")
	}
	if e == nil || e.Store != store || run == nil || e.Check(ctx) != nil {
		return bad()
	}
	file, err := failedForwardPublicationFile(purpose)
	if err != nil {
		return bad()
	}
	rel := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "repair-evidence", file)
	path, err := store.safePath(rel)
	if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return bad()
	}
	var evidence createFailedForwardPublication
	if decodeEnvironmentJSON(raw, &evidence) != nil || evidence.Purpose != purpose || evidence.Schema != failedForwardPublicationSchema || evidence.Environment != store.Environment || evidence.Parent != e.Journal.PlanSHA256 || evidence.Repository != "hkt999rtk/rtk_video_cloud" || evidence.PullRequest <= 0 || !deploymentUpgradeSHA.MatchString(evidence.MergeCommit) || evidence.RunID <= 0 || evidence.RunAttempt <= 0 || !environmentPreflightDigest.MatchString(evidence.ManifestSHA256) {
		return bad()
	}
	key := "failed-stage-tool-publication-" + environmentPreflightBytesSHA256(raw)
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intentSHA, err := environmentCreatePlanSHA256(raw)
	var receipt createFailedForwardPublication
	if err != nil || entry == nil || entry.State != "completed" || entry.Phase != "failed-stage-tool-publication" || entry.IntentSHA256 != intentSHA || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(evidence) {
		return bad()
	}
	tool, err := verifyFailedForwardPublication(ctx, evidence, run)
	if err != nil || e.Check(ctx) != nil {
		return bad()
	}
	return tool, nil
}

func verifyFailedForwardPublication(ctx context.Context, evidence createFailedForwardPublication, run environmentReleaseCommand) (failedForwardTool, error) {
	var empty failedForwardTool
	bad := func() (failedForwardTool, error) {
		return empty, createProviderRecoveryBlock("exact merged repair PR and successful CI release with native CRL capability required")
	}
	if _, err := failedForwardPublicationFile(evidence.Purpose); err != nil {
		return bad()
	}
	if run == nil || evidence.Repository != "hkt999rtk/rtk_video_cloud" || evidence.PullRequest <= 0 || !deploymentUpgradeSHA.MatchString(evidence.MergeCommit) || evidence.RunID <= 0 || evidence.RunAttempt <= 0 || !environmentPreflightDigest.MatchString(evidence.ManifestSHA256) {
		return bad()
	}
	rawPR, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/pulls/%d", evidence.Repository, evidence.PullRequest))
	var pr struct {
		Number      int64  `json:"number"`
		State       string `json:"state"`
		Merged      bool   `json:"merged"`
		MergeCommit string `json:"merge_commit_sha"`
		Base        struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err != nil || json.Unmarshal(rawPR, &pr) != nil || pr.Number != evidence.PullRequest || pr.State != "closed" || !pr.Merged || pr.MergeCommit != evidence.MergeCommit || pr.Base.Ref != "main" || pr.Base.Repo.FullName != evidence.Repository {
		return bad()
	}
	proofCtx := newEnvironmentReleaseProofCapture(ctx)
	manifest, attempt, err := readEnvironmentPublication(proofCtx, evidence.Repository, evidence.RunID, evidence.MergeCommit, false, run)
	if err != nil || attempt != evidence.RunAttempt || environmentPreflightBytesSHA256(manifest) != evidence.ManifestSHA256 {
		return bad()
	}
	image, err := selectEnvironmentPublishedImage(manifest, evidence.Repository, evidence.RunID, attempt, evidence.MergeCommit, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", false)
	if err != nil {
		return bad()
	}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	captured := snapshotVerifiedReleaseProofs(proofCtx, release)
	if len(captured) != 1 {
		return bad()
	}
	if _, err = run(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
		return bad()
	}
	caps, err := run(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", image.Reference, "--deployment-capabilities")
	canonical, canonicalErr := canonicalFailedForwardCapabilities(caps)
	if err != nil || canonicalErr != nil {
		return bad()
	}
	if evidence.Purpose == openBaoPublicCRLToolPurpose {
		var parsed struct {
			Features []string `json:"features"`
		}
		if json.Unmarshal(canonical, &parsed) != nil || !slices.Contains(parsed.Features, openBaoPublicCRLCapability) {
			return bad()
		}
	}
	return failedForwardTool{Image: image, Publication: captured[0], Capabilities: canonical}, nil
}

// recordFailedForwardPublication is called by the PR/CI delivery workflow with
// the exact PR and Release result it just obtained. The workflow is not an
// operator-facing setting: every claim is re-queried before a private,
// parent-fenced, idempotent journal entry is created.
func recordFailedForwardPublication(ctx context.Context, store secretStore, e *environmentCreateExecution, evidence createFailedForwardPublication, run environmentReleaseCommand, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("repair publication cannot be bound to the original generation")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil || evidence.Schema != failedForwardPublicationSchema || evidence.Environment != store.Environment || evidence.Parent != e.Journal.PlanSHA256 {
		return bad()
	}
	if _, err := verifyFailedForwardPublication(ctx, evidence, run); err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	raw := mustProviderRecoveryJSON(evidence)
	key := "failed-stage-tool-publication-" + environmentPreflightBytesSHA256(raw)
	intent, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if err = e.BeginEntry(key, "failed-stage-tool-publication", raw, now); err != nil {
			return err
		}
	} else if entry.Phase != "failed-stage-tool-publication" || entry.IntentSHA256 != intent || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return bad()
	}
	file, err := failedForwardPublicationFile(evidence.Purpose)
	if err != nil {
		return bad()
	}
	relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "repair-evidence", file)
	if _, err = writeProductArtifact(store, relative, evidence); err != nil {
		return err
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry.State == "pending" {
		if err = e.RecordReceipt(key, raw); err != nil {
			return err
		}
	} else {
		var previous createFailedForwardPublication
		if decodeEnvironmentJSON(entry.Receipt, &previous) != nil || deploymentplan.Digest(previous) != deploymentplan.Digest(evidence) {
			return bad()
		}
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry.State == "allocated" {
		return e.CompleteEntry(key, now)
	}
	return e.Check(ctx)
}

// publishFailedForwardToolFromPR is driven by the PR/CI workflow using the
// PR number returned by that workflow. All other publication facts are read
// from GitHub; no image, commit, digest or environment setting is supplied.
func publishFailedForwardToolFromPR(ctx context.Context, store secretStore, e *environmentCreateExecution, pullRequest int64, run environmentReleaseCommand, now time.Time) (createFailedForwardPublication, error) {
	return publishFailedForwardToolFromPRForPurpose(ctx, store, e, pullRequest, run, now, "")
}

func publishFailedForwardToolFromPRForPurpose(ctx context.Context, store secretStore, e *environmentCreateExecution, pullRequest int64, run environmentReleaseCommand, now time.Time, purpose string) (createFailedForwardPublication, error) {
	var empty createFailedForwardPublication
	bad := func() (createFailedForwardPublication, error) {
		return empty, createProviderRecoveryBlock("merged repair PR and its exact successful Release evidence are required")
	}
	if e == nil || e.Store != store || pullRequest <= 0 || run == nil || e.Check(ctx) != nil {
		return bad()
	}
	if _, err := failedForwardPublicationFile(purpose); err != nil {
		return bad()
	}
	const repo = "hkt999rtk/rtk_video_cloud"
	raw, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/pulls/%d", repo, pullRequest))
	var pr struct {
		Number      int64  `json:"number"`
		State       string `json:"state"`
		Merged      bool   `json:"merged"`
		MergeCommit string `json:"merge_commit_sha"`
		Base        struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err != nil || json.Unmarshal(raw, &pr) != nil || pr.Number != pullRequest || pr.State != "closed" || !pr.Merged || pr.Base.Ref != "main" || pr.Base.Repo.FullName != repo || !deploymentUpgradeSHA.MatchString(pr.MergeCommit) {
		return bad()
	}
	raw, err = run(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/release.yml/runs?head_sha="+pr.MergeCommit+"&per_page=100")
	if err != nil {
		return bad()
	}
	runID, err := deploymentUpgradeSuccessfulRun(raw, pr.MergeCommit)
	if err != nil {
		return bad()
	}
	manifest, attempt, err := readEnvironmentPublication(ctx, repo, runID, pr.MergeCommit, false, run)
	if err != nil {
		return bad()
	}
	evidence := createFailedForwardPublication{Purpose: purpose, Schema: failedForwardPublicationSchema, Environment: store.Environment, Parent: e.Journal.PlanSHA256, Repository: repo, PullRequest: pullRequest, MergeCommit: pr.MergeCommit, RunID: runID, RunAttempt: attempt, ManifestSHA256: environmentPreflightBytesSHA256(manifest)}
	if err = recordFailedForwardPublication(ctx, store, e, evidence, run, now); err != nil {
		return empty, err
	}
	return evidence, nil
}

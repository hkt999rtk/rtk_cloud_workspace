package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const publicationArchiveLimit = 4 << 20

// Evidence is fetched from the exact successful GitHub run, never from a tag,
// an operator file or another rerun. OCI labels alone do not prove publication.
func verifyEnvironmentPublication(ctx context.Context, repo string, runID int64, image environmentReleaseImage, backup bool, run func(context.Context, string, ...string) ([]byte, error)) (string, error) {
	if runID <= 0 || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || !immutableEnvironmentImage.MatchString(image.Reference) {
		return "", errors.New("deployment.release.publication: exact run, source and immutable image required")
	}
	evidence, attempt, err := readEnvironmentPublication(ctx, repo, runID, image.SourceRevision, backup, run)
	if err != nil {
		return "", err
	}
	if err = verifyPublicationJSON(evidence, repo, runID, attempt, image, backup); err != nil {
		return "", err
	}
	digest := sha256.Sum256(evidence)
	return hex.EncodeToString(digest[:]), nil
}

func readEnvironmentPublication(ctx context.Context, repo string, runID int64, revision string, backup bool, run func(context.Context, string, ...string) ([]byte, error)) ([]byte, int, error) {
	if runID <= 0 || !deploymentUpgradeSHA.MatchString(revision) || run == nil {
		return nil, 0, errors.New("exact publication source and reader required")
	}
	raw, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/runs/%d", repo, runID))
	var source struct {
		ID         int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Attempt    int    `json:"run_attempt"`
	}
	if err != nil || json.Unmarshal(raw, &source) != nil || source.ID != runID || source.HeadSHA != revision || source.HeadBranch != "main" || (source.Event != "push" && source.Event != "workflow_dispatch") || source.Status != "completed" || source.Conclusion != "success" || source.Attempt < 1 {
		return nil, 0, errors.New("deployment.release.publication: exact successful main publication attempt cannot be verified")
	}
	raw, err = run(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/runs/%d/artifacts?per_page=100", repo, runID))
	var listing struct {
		Total     int `json:"total_count"`
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Expired bool   `json:"expired"`
			Size    int64  `json:"size_in_bytes"`
		} `json:"artifacts"`
	}
	if err != nil || json.Unmarshal(raw, &listing) != nil || listing.Artifacts == nil || listing.Total != len(listing.Artifacts) {
		return nil, 0, errors.New("deployment.release.publication: complete publication artifact inventory is unavailable")
	}
	name, file := "image-manifest", "image-manifest.json"
	if backup {
		name, file = "postgres-backup-runner-images", "images.json"
	}
	var id int64
	for _, artifact := range listing.Artifacts {
		if artifact.Name != name {
			continue
		}
		if id != 0 || artifact.ID <= 0 || artifact.Expired || artifact.Size <= 0 || artifact.Size > publicationArchiveLimit {
			return nil, 0, errors.New("deployment.release.publication: missing, ambiguous, expired or oversized release evidence; republish the exact source")
		}
		id = artifact.ID
	}
	if id == 0 {
		return nil, 0, errors.New("deployment.release.publication: formal immutable image artifact is missing; republish the exact source")
	}
	raw, err = run(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/artifacts/%d/zip", repo, id))
	if err != nil {
		return nil, 0, errors.New("deployment.release.publication: cannot retrieve the exact publication artifact")
	}
	evidence, err := publicationJSON(raw, file)
	if err != nil {
		return nil, 0, err
	}
	return evidence, source.Attempt, nil
}

func publicationJSON(raw []byte, expected string) ([]byte, error) {
	if len(raw) == 0 || len(raw) > publicationArchiveLimit {
		return nil, errors.New("deployment.release.publication: archive exceeds bounded evidence limits")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) != 1 {
		return nil, errors.New("deployment.release.publication: release evidence must contain one bounded manifest")
	}
	file := archive.File[0]
	if file.Name != expected || !file.Mode().IsRegular() || file.UncompressedSize64 > 1<<20 {
		return nil, errors.New("deployment.release.publication: unexpected or oversized manifest")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, errors.New("deployment.release.publication: manifest is unreadable")
	}
	defer reader.Close()
	evidence, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(evidence) > 1<<20 {
		return nil, errors.New("deployment.release.publication: invalid bounded manifest")
	}
	return evidence, nil
}

func verifyPublicationJSON(raw []byte, repo string, runID int64, attempt int, image environmentReleaseImage, backup bool) error {
	var manifest struct {
		Schema       string `json:"schema"`
		Version      int    `json:"version"`
		Repository   string `json:"repository"`
		Source       string `json:"source_commit"`
		RunID        string `json:"run_id"`
		Attempt      string `json:"run_attempt"`
		Image        string `json:"image"`
		Digest       string `json:"digest"`
		RunnerTag    string `json:"runner_tag"`
		RunnerDigest string `json:"runner_digest"`
		Images       map[string]struct {
			Image  string `json:"image"`
			Digest string `json:"digest"`
		} `json:"images"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || manifest.Source != image.SourceRevision || manifest.Repository != repo || manifest.RunID != strconv.FormatInt(runID, 10) || manifest.Attempt != strconv.Itoa(attempt) {
		return errors.New("deployment.release.publication: artifact source, repository or run attempt does not match")
	}
	refs := map[string]string{}
	if backup {
		if manifest.Version != 1 {
			return errors.New("deployment.release.publication: unsupported backup publication evidence")
		}
		refs[environmentImageRepository(manifest.RunnerTag)] = manifest.RunnerDigest
	} else {
		if manifest.Schema != "rtk-service-image/v1" {
			return errors.New("deployment.release.publication: unsupported service publication evidence")
		}
		refs[manifest.Image] = manifest.Digest
		for _, entry := range manifest.Images {
			if previous, ok := refs[entry.Image]; ok && previous != entry.Digest {
				return errors.New("deployment.release.publication: conflicting image publication digests")
			}
			refs[entry.Image] = entry.Digest
		}
	}
	repository, digest, ok := strings.Cut(image.Reference, "@")
	if !ok || refs[repository] != digest {
		return errors.New("deployment.release.publication: selected image digest was not published by the exact CI attempt")
	}
	return nil
}

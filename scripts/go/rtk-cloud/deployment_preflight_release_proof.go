package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// These facts are captured from the publication requests already used by
// preflight. They are separate from the deterministic rendered release digest.
type environmentReleasePublicationProof struct {
	ImageKey        string    `json:"image_key"`
	Reference       string    `json:"reference"`
	Repository      string    `json:"repository"`
	Workflow        string    `json:"workflow"`
	SourceRevision  string    `json:"source_revision"`
	RunID           int64     `json:"run_id"`
	RunAttempt      int       `json:"run_attempt"`
	ArtifactID      int64     `json:"artifact_id"`
	ArtifactName    string    `json:"artifact_name"`
	RunSHA256       string    `json:"run_sha256"`
	InventorySHA256 string    `json:"inventory_sha256"`
	ArchiveSHA256   string    `json:"archive_sha256"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	Manifest        []byte    `json:"manifest"`
	ObservedAt      time.Time `json:"observed_at"`
	Backup          bool      `json:"backup"`
}

type environmentReleaseProofCapture struct {
	mu     sync.Mutex
	proofs []environmentReleasePublicationProof
}

type environmentReleaseProofCaptureKey struct{}

func newEnvironmentReleaseProofCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, environmentReleaseProofCaptureKey{}, &environmentReleaseProofCapture{})
}

func captureEnvironmentPublicationProof(ctx context.Context, repo string, runID int64, attempt int, revision string, artifactID int64, name string, backup bool, runRaw, inventoryRaw, archiveRaw, manifestRaw []byte) {
	capture, ok := ctx.Value(environmentReleaseProofCaptureKey{}).(*environmentReleaseProofCapture)
	if !ok {
		return
	}
	workflow := "release.yml"
	if backup {
		workflow = "postgres-backup-image.yml"
	}
	proof := environmentReleasePublicationProof{Repository: repo, Workflow: workflow, SourceRevision: revision, RunID: runID, RunAttempt: attempt, ArtifactID: artifactID, ArtifactName: name, RunSHA256: environmentPreflightBytesSHA256(runRaw), InventorySHA256: environmentPreflightBytesSHA256(inventoryRaw), ArchiveSHA256: environmentPreflightBytesSHA256(archiveRaw), ManifestSHA256: environmentPreflightBytesSHA256(manifestRaw), Manifest: append([]byte(nil), manifestRaw...), ObservedAt: time.Now().UTC(), Backup: backup}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.proofs = append(capture.proofs, proof)
}

func snapshotVerifiedReleaseProofs(ctx context.Context, release environmentRelease) []environmentReleasePublicationProof {
	capture, ok := ctx.Value(environmentReleaseProofCaptureKey{}).(*environmentReleaseProofCapture)
	if !ok {
		return nil
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	keys := make([]string, 0, len(release.Images))
	for key := range release.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var proofs []environmentReleasePublicationProof
	for _, key := range keys {
		image := release.Images[key]
		if !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		var selected *environmentReleasePublicationProof
		for _, proof := range capture.proofs {
			if !strings.HasPrefix(image.Reference, "ghcr.io/"+proof.Repository+"/") || verifyPublicationJSON(proof.Manifest, proof.Repository, proof.RunID, proof.RunAttempt, image, proof.Backup) != nil {
				continue
			}
			if selected == nil || proof.RunID > selected.RunID || proof.RunID == selected.RunID && proof.RunAttempt > selected.RunAttempt {
				candidate := proof
				selected = &candidate
			}
		}
		if selected != nil {
			selected.ImageKey, selected.Reference = key, image.Reference
			proofs = append(proofs, *selected)
		}
	}
	return proofs
}

func environmentPreflightBytesSHA256(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

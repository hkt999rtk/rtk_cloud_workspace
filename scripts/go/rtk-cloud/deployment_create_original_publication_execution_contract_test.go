package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// This is the external CI publication protocol for isolated historical test
// images, just like createStateFixture. Parse/select and capture the exact
// payload before publishing the original artifact. It is neither a live CI
// receipt nor a replacement for running current recovery qualification.
func coreContractPublicationProofs(t *testing.T, release *environmentRelease) []environmentReleasePublicationProof {
	t.Helper()
	ctx := newEnvironmentReleaseProofCapture(context.Background())
	for role, image := range release.Images {
		if !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			continue
		}
		reference, digest, ok := strings.Cut(image.Reference, "@")
		parts := strings.Split(strings.TrimPrefix(reference, "ghcr.io/"), "/")
		if !ok || len(parts) < 3 {
			t.Fatal("isolated immutable publication reference malformed", role)
		}
		repository := strings.Join(parts[:2], "/")
		manifest, err := json.Marshal(map[string]string{"schema": "rtk-service-image/v1", "repository": repository, "source_commit": image.SourceRevision, "run_id": "70", "run_attempt": "2", "image": reference, "digest": digest})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectEnvironmentPublishedImage(manifest, repository, 70, 2, image.SourceRevision, reference, false)
		if err != nil || selected.Reference != image.Reference {
			t.Fatal("isolated exact publication selection failed", role, err)
		}
		captureEnvironmentPublicationProof(ctx, repository, 70, 2, image.SourceRevision, 71, "image-manifest", false, []byte(`{"id":70,"run_attempt":2}`), []byte(`{"artifact_id":71}`), []byte("isolated historical exact image archive"), manifest)
	}
	proofs := snapshotVerifiedReleaseProofs(ctx, *release)
	return proofs
}

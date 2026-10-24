package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func completePreflightProofFixture(t *testing.T, environment string) (*compiledEnvironmentDeployment, deploymentplan.Qualification, time.Time) {
	t.Helper()
	configRoot := t.TempDir()
	if err := os.Chmod(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore("", environment)
	if err != nil {
		t.Fatal(err)
	}
	stack := "video-cloud-" + environment
	cfg := deploymentConfig{Environment: environment, Workspace: "/synthetic/workspace", Values: map[string]string{"CLOUD_STACK_NAME": stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "600", "DEPLOYMENT_PROTECTION_PROFILE": "standard"}}
	publication := []byte("{\n  \"schema\": \"rtk-service-image/v1\",\n  \"repository\": \"hkt999rtk/rtk_video_cloud\",\n  \"source_commit\": \"" + strings.Repeat("b", 40) + "\",\n  \"run_id\": \"70\",\n  \"run_attempt\": \"2\",\n  \"image\": \"ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud\",\n  \"digest\": \"sha256:" + strings.Repeat("c", 64) + "\"\n}")
	image, err := selectEnvironmentPublishedImage(publication, "hkt999rtk/rtk_video_cloud", 70, 2, strings.Repeat("b", 40), "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud", false)
	if err != nil {
		t.Fatal(err)
	}
	release := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: environment, Stack: stack, WorkspaceRevision: strings.Repeat("a", 40), Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	object := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": "owned", "namespace": stack + "-video-cloud", "uid": "owner", "resourceVersion": "4", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"port": float64(80)}}}}
	bindings := environmentIdentityBindings{Version: 1, Environment: environment, Stack: stack, Owners: map[string]string{deploymentplan.ID(object): "owner"}}
	input := deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("synthetic-private-configuration"), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings), StorageSHA256: deploymentplan.Digest("storage")}
	plan, err := deploymentplan.Compile(input, []deploymentplan.Definition{{Object: deploymentplan.Clone(object), Phase: "routing", Retain: true}}, []deploymentplan.Object{object})
	if err != nil {
		t.Fatal(err)
	}
	ctx := newEnvironmentReleaseProofCapture(context.Background())
	captureEnvironmentPublicationProof(ctx, "hkt999rtk/rtk_video_cloud", 70, 2, image.SourceRevision, 71, "image-manifest", false, []byte(`{"id":70,"run_attempt":2}`), []byte(`{"artifact_id":71}`), []byte("synthetic archive bytes"), publication)
	qualifiedAt := time.Now().UTC()
	c := &compiledEnvironmentDeployment{Plan: plan, Release: release, Bindings: bindings, Store: store, Configuration: cfg, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("apiVersion: v1\nkind: Config\ncurrent-context: selected\nclusters:\n- name: selected\n  cluster:\n    server: https://cluster.example.test\n    certificate-authority-data: c3ludGhldGljLWNh\ncontexts:\n- name: selected\n  context:\n    cluster: selected\n    user: selected\nusers:\n- name: selected\n  user:\n    token: synthetic-only\n")}, PKICheckedAt: qualifiedAt.Add(-time.Second), PKIVerifiedUntil: qualifiedAt.Add(599 * time.Second), ReleaseProofs: snapshotVerifiedReleaseProofs(ctx, release)}
	qualification := deploymentplan.Qualification{Decision: "GO", PlanSHA256: plan.SHA256()}
	for _, id := range deploymentplan.RequiredGateIDs(false)[:6] {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest("synthetic-verified-" + id)})
	}
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.PreflightGeneration = generation
	record := environmentPreflightBindingsRecord{Schema: "rtk-cloud.preflight-bindings/v1", Environment: environment, Operation: "upgrade", Decision: "NO-GO", PlanSHA256: plan.SHA256(), BindingsSHA256: input.BindingsSHA256, Observed: &bindings}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil || store.write("deployment/identity-bindings.json", raw, true) != nil {
		t.Fatal("secondary observed binding fixture unavailable", err)
	}
	return c, qualification, qualifiedAt
}

func TestPreflightProofRoundTripKeepsExactFrozenArtifactAndRawCIManifest(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, qualification, at := completePreflightProofFixture(t, environment)
			planSHA, releaseSHA := c.Plan.SHA256(), deploymentplan.Digest(c.Release)
			proof, err := saveEnvironmentPreflightProof(c, qualification, at)
			if err != nil {
				t.Fatal("complete preflight qualification could not be published", err)
			}
			c.Proof = proof
			artifact, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", planSHA, at.Add(time.Second))
			if err != nil || artifact.Proof.SHA256() != proof.SHA256() || artifact.Plan.SHA256() != planSHA || deploymentplan.Digest(artifact.Release) != releaseSHA || deploymentplan.Digest(artifact.Bindings) != c.Plan.BindingsSHA256 || len(artifact.Proof.ReleaseProofs) != 1 || !bytes.Equal(artifact.Proof.ReleaseProofs[0].Manifest, c.ReleaseProofs[0].Manifest) || artifact.Proof.ReleaseProofs[0].ManifestSHA256 != environmentPreflightBytesSHA256(c.ReleaseProofs[0].Manifest) {
				t.Fatal("frozen qualification lost exact payloads or original artifact bytes", err)
			}
			if c.Plan.SHA256() != planSHA || deploymentplan.Digest(c.Release) != releaseSHA {
				t.Fatal("CI proof capture changed the deterministic deployment plan")
			}
			if !proof.ValidUntil.Equal(at.Add(600*time.Second)) || !proof.PKIVerifiedUntil.Equal(c.PKIVerifiedUntil) || !proof.PKICheckedAt.Equal(c.PKICheckedAt) {
				t.Fatal("qualification expiry replaced original PKI provenance")
			}
			for _, name := range []string{"proof.json", "plan.json", "release.json", "bindings.json"} {
				info, err := os.Lstat(filepath.Join(artifact.Directory, name))
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("qualification payload is not private", name, err)
				}
			}
			for directory := artifact.Directory; directory != c.Store.ConfigRoot; directory = filepath.Dir(directory) {
				info, err := os.Lstat(directory)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("qualification directory is not private", err)
				}
			}
			if err := verifyEnvironmentActivePreflightProof(c); err != nil {
				t.Fatal("fresh active qualification was not fenced", err)
			}
			if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", planSHA, proof.ValidUntil.Add(-time.Nanosecond)); err != nil {
				t.Fatal("qualification expired before its exact validity boundary", err)
			}
			if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", planSHA, proof.ValidUntil); err == nil {
				t.Fatal("expired qualification remained executable")
			}
		})
	}
}

func TestPreflightInvalidationBlocksOldArtifactsAndBindingOnlyRecords(t *testing.T) {
	c, qualification, at := completePreflightProofFixture(t, "dev")
	if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", c.Plan.SHA256(), at); err == nil {
		t.Fatal("legacy successful bindings were accepted as complete qualification")
	}
	proof, err := saveEnvironmentPreflightProof(c, qualification, at)
	if err != nil {
		t.Fatal(err)
	}
	c.Proof = proof
	active, err := readEnvironmentPreflightActive(c.Store)
	if err != nil {
		t.Fatal(err)
	}
	oldDirectory := filepath.Join(c.Store.Root, "deployment", "preflight", filepath.FromSlash(active.Artifact))
	original, err := os.ReadFile(filepath.Join(oldDirectory, "proof.json"))
	if err != nil || invalidateEnvironmentPreflightProof(c.Configuration) != nil {
		t.Fatal("active qualification could not be invalidated", err)
	}
	if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", c.Plan.SHA256(), at.Add(time.Second)); err == nil || verifyEnvironmentActivePreflightProof(c) == nil {
		t.Fatal("NO-GO preflight invalidation fell back to an older immutable artifact")
	}
	qualification.Gates = qualification.Gates[:5]
	if _, err := saveEnvironmentPreflightProof(c, qualification, at.Add(time.Second)); err == nil {
		t.Fatal("partial qualification was published")
	}
	after, err := os.ReadFile(filepath.Join(oldDirectory, "proof.json"))
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("new failed preflight overwrote historical complete qualification")
	}
	latest, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || latest.Decision != "NO-GO" || latest.Artifact != "" {
		t.Fatal("failed preflight left executable old qualification", latest, err)
	}
}

func TestPreflightProofRejectsTamperedIncompleteAndCrossEnvironmentEvidence(t *testing.T) {
	for _, mode := range []string{"missing-plan", "missing-release", "missing-proof", "changed-plan", "changed-release", "changed-bindings", "proof-hash", "checker", "checker-version", "missing-gate", "duplicate-gate", "failed-gate", "missing-digest", "future-proof", "invalid-expiry", "missing-pki", "wrong-run-attempt", "changed-manifest", "missing-ci-proof", "wrong-environment", "wrong-operation", "wrong-reviewed-plan", "config-drift", "legacy-binding-no-go", "legacy-binding-drift", "pointer-traversal", "pointer-symlink", "artifact-symlink", "public-proof", "duplicate-proof-field", "fifo-proof", "partial-artifact"} {
		t.Run(mode, func(t *testing.T) {
			c, qualification, at := completePreflightProofFixture(t, "dev")
			proof, err := saveEnvironmentPreflightProof(c, qualification, at)
			if err != nil {
				t.Fatal(err)
			}
			active, err := readEnvironmentPreflightActive(c.Store)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(c.Store.Root, "deployment", "preflight")
			directory := filepath.Join(base, filepath.FromSlash(active.Artifact))
			operation, reviewedSHA := "upgrade", c.Plan.SHA256()
			saveChangedProof := false
			switch mode {
			case "missing-plan", "missing-release", "missing-proof":
				name := strings.TrimPrefix(mode, "missing-") + ".json"
				if err := os.Remove(filepath.Join(directory, name)); err != nil {
					t.Fatal(err)
				}
			case "changed-plan", "changed-release", "changed-bindings":
				name := strings.TrimPrefix(mode, "changed-") + ".json"
				if err := os.WriteFile(filepath.Join(directory, name), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "proof-hash":
				active.ProofSHA256 = strings.Repeat("d", 64)
			case "checker":
				proof.CheckerSHA256 = strings.Repeat("d", 64)
				saveChangedProof = true
			case "checker-version":
				proof.CheckerVersion = "old-policy"
				saveChangedProof = true
			case "missing-gate":
				proof.Qualification.Gates = proof.Qualification.Gates[:5]
				saveChangedProof = true
			case "duplicate-gate":
				proof.Qualification.Gates[1] = proof.Qualification.Gates[0]
				saveChangedProof = true
			case "failed-gate":
				proof.Qualification.Gates[0].Status = "FAIL"
				saveChangedProof = true
			case "missing-digest":
				proof.Qualification.Gates[0].EvidenceSHA256 = ""
				saveChangedProof = true
			case "future-proof":
				at = proof.QualifiedAt.Add(-time.Nanosecond)
			case "invalid-expiry":
				proof.ValidUntil = proof.ValidUntil.Add(time.Second)
				saveChangedProof = true
			case "missing-pki":
				proof.PKIVerifiedUntil = time.Time{}
				saveChangedProof = true
			case "wrong-run-attempt":
				proof.ReleaseProofs[0].RunAttempt++
				saveChangedProof = true
			case "changed-manifest":
				proof.ReleaseProofs[0].Manifest = []byte(`{}`)
				saveChangedProof = true
			case "missing-ci-proof":
				proof.ReleaseProofs = nil
				saveChangedProof = true
			case "wrong-environment":
				c.Configuration.Environment = "staging"
			case "wrong-operation":
				operation = "create"
			case "wrong-reviewed-plan":
				reviewedSHA = strings.Repeat("d", 64)
			case "config-drift":
				c.Configuration.Values["CHANGED"] = "changed"
			case "legacy-binding-no-go", "legacy-binding-drift":
				var record environmentPreflightBindingsRecord
				path := filepath.Join(c.Store.Root, "deployment", "identity-bindings.json")
				if err := readEnvironmentPreflightJSONWithCanonical(path, &record, false); err != nil {
					t.Fatal(err)
				}
				if mode == "legacy-binding-no-go" {
					record.Decision = "NO-GO"
				} else {
					record.BindingsSHA256 = strings.Repeat("d", 64)
				}
				writeSyntheticPreflightJSON(t, path, record)
			case "pointer-traversal":
				active.Artifact = "../outside"
			case "pointer-symlink":
				path := filepath.Join(base, "active.json")
				if err := os.Rename(path, filepath.Join(base, "original-active.json")); err != nil || os.Symlink(filepath.Join(base, "original-active.json"), path) != nil {
					t.Fatal("symlink pointer fixture unavailable", err)
				}
			case "artifact-symlink":
				original := directory + "-original"
				if err := os.Rename(directory, original); err != nil || os.Symlink(original, directory) != nil {
					t.Fatal("symlink artifact fixture unavailable", err)
				}
			case "public-proof":
				if err := os.Chmod(filepath.Join(directory, "proof.json"), 0644); err != nil {
					t.Fatal(err)
				}
			case "duplicate-proof-field":
				path := filepath.Join(directory, "proof.json")
				raw, err := os.ReadFile(path)
				if err != nil || os.WriteFile(path, append(raw[:len(raw)-1], []byte(`,"schema":"rtk-cloud.preflight-proof/v1"}`)...), 0600) != nil {
					t.Fatal("duplicate JSON proof fixture unavailable", err)
				}
			case "fifo-proof":
				path := filepath.Join(directory, "proof.json")
				if err := os.Remove(path); err != nil || syscall.Mkfifo(path, 0600) != nil {
					t.Fatal("FIFO fixture unavailable", err)
				}
			case "partial-artifact":
				partial, err := os.MkdirTemp(filepath.Dir(directory), "qualification-")
				if err != nil {
					t.Fatal(err)
				}
				active.Artifact = c.Plan.SHA256() + "/" + filepath.Base(partial)
			}
			if saveChangedProof {
				writeSyntheticPreflightJSON(t, filepath.Join(directory, "proof.json"), proof)
				active.ProofSHA256 = proof.SHA256()
			}
			if mode != "pointer-symlink" {
				writeSyntheticPreflightJSON(t, filepath.Join(base, "active.json"), active)
			}
			if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, operation, reviewedSHA, at); err == nil {
				t.Fatal("invalid or uncertain qualification was accepted", mode)
			}
		})
	}
}

func writeSyntheticPreflightJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil || os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("synthetic qualification JSON fixture unavailable", err)
	}
}

func TestPreflightKubernetesProofSeparatesClusterAndAuthenticationIdentity(t *testing.T) {
	c, _, _ := completePreflightProofFixture(t, "dev")
	cluster, identity, err := environmentPreflightKubernetesIdentity(c.Backend.KubeconfigContent)
	changedToken := bytes.ReplaceAll(c.Backend.KubeconfigContent, []byte("synthetic-only"), []byte("other-synthetic-token"))
	otherCluster, otherIdentity, otherErr := environmentPreflightKubernetesIdentity(changedToken)
	if err != nil || otherErr != nil || cluster != otherCluster || identity == otherIdentity {
		t.Fatal("authentication change did not invalidate its own selected identity proof", err, otherErr)
	}
	changedAPI := bytes.ReplaceAll(c.Backend.KubeconfigContent, []byte("cluster.example.test"), []byte("other-cluster.example.test"))
	otherCluster, otherIdentity, otherErr = environmentPreflightKubernetesIdentity(changedAPI)
	if otherErr != nil || cluster == otherCluster || identity == otherIdentity {
		t.Fatal("cluster change did not invalidate selected cluster proof", otherErr)
	}
}

func TestPreflightCIProofCaptureUsesExistingExactRequestsWithoutResolvingAgain(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "unverified-attempt"}[valid], func(t *testing.T) {
			c, _, _ := completePreflightProofFixture(t, "dev")
			image := c.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
			manifest := append([]byte(nil), c.ReleaseProofs[0].Manifest...)
			if !valid {
				manifest = bytes.ReplaceAll(manifest, []byte(`"run_attempt": "2"`), []byte(`"run_attempt": "1"`))
			}
			archive := publicationZIP(t, "image-manifest.json", manifest)
			runRaw, _ := json.Marshal(map[string]any{"id": 70, "head_sha": image.SourceRevision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": 2})
			inventoryRaw, _ := json.Marshal(map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 71, "name": "image-manifest", "expired": false, "size_in_bytes": len(archive)}}})
			calls := 0
			run := func(_ context.Context, command string, args ...string) ([]byte, error) {
				calls++
				if command != "gh" || len(args) != 2 || args[0] != "api" {
					t.Fatal("publication capture introduced a new provider/tool request")
				}
				switch args[1] {
				case "repos/hkt999rtk/rtk_video_cloud/actions/runs/70":
					return runRaw, nil
				case "repos/hkt999rtk/rtk_video_cloud/actions/runs/70/artifacts?per_page=100":
					return inventoryRaw, nil
				case "repos/hkt999rtk/rtk_video_cloud/actions/artifacts/71/zip":
					return archive, nil
				default:
					t.Fatal("capture re-resolved CI or escaped the selected exact artifact", args)
					return nil, nil
				}
			}
			ctx := newEnvironmentReleaseProofCapture(context.Background())
			_, err := verifyEnvironmentPublication(ctx, "hkt999rtk/rtk_video_cloud", 70, image, false, run)
			proofs := snapshotVerifiedReleaseProofs(ctx, c.Release)
			if (err == nil) != valid || calls != 3 || len(proofs) != map[bool]int{true: 1, false: 0}[valid] {
				t.Fatal("capture added requests or trusted an unverified publication", err, calls, len(proofs))
			}
			if valid {
				proof := proofs[0]
				if proof.RunID != 70 || proof.RunAttempt != 2 || proof.ArtifactID != 71 || proof.RunSHA256 != environmentPreflightBytesSHA256(runRaw) || proof.InventorySHA256 != environmentPreflightBytesSHA256(inventoryRaw) || proof.ArchiveSHA256 != environmentPreflightBytesSHA256(archive) || proof.ManifestSHA256 != environmentPreflightBytesSHA256(manifest) || !bytes.Equal(proof.Manifest, manifest) {
					t.Fatal("capture lost exact run, attempt or original artifact hash bindings", proof)
				}
			}
		})
	}
}

func TestPreflightPublicationRejectsStaleGenerationsAndCannotRevokeANewerOne(t *testing.T) {
	c, qualification, at := completePreflightProofFixture(t, "dev")
	first := c.PreflightGeneration
	second, err := invalidateEnvironmentPreflightProofWithToken(c.Configuration)
	if err != nil || first == second {
		t.Fatal("new preflight did not invalidate with a distinct generation", err)
	}
	bindingsPath := filepath.Join(c.Store.Root, "deployment", "identity-bindings.json")
	originalBindings, err := os.ReadFile(bindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveEnvironmentPreflightProof(c, qualification, at); err == nil {
		t.Fatal("older qualification overwrote the newer preflight's invalidation")
	}
	afterBindings, err := os.ReadFile(bindingsPath)
	active, activeErr := readEnvironmentPreflightActive(c.Store)
	if err != nil || activeErr != nil || !bytes.Equal(originalBindings, afterBindings) || active.Generation != second || active.Decision != "NO-GO" {
		t.Fatal("stale publisher changed secondary binding GO or newer invalidation", err, activeErr)
	}
	c.PreflightGeneration = second
	proof, err := saveEnvironmentPreflightProof(c, qualification, at)
	if err != nil {
		t.Fatal("current generation could not publish complete evidence", err)
	}
	c.Proof = proof
	current, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || revokeEnvironmentPreflightProof(c.Configuration, first) != nil {
		t.Fatal("old cancellation could not be reconciled safely", err)
	}
	after, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || after != current || after.Decision != "GO" {
		t.Fatal("older canceled invocation revoked a newer successful qualification", err)
	}
	c.PreflightGeneration = first
	if _, err := saveEnvironmentPreflightProof(c, qualification, at); err == nil {
		t.Fatal("stale publisher replaced a newer successful qualification")
	}
	after, err = readEnvironmentPreflightActive(c.Store)
	if err != nil || after != current {
		t.Fatal("rejected stale publication changed the active successful proof", err)
	}
	if err := revokeEnvironmentPreflightProof(c.Configuration, second); err != nil {
		t.Fatal(err)
	}
	if verifyEnvironmentActivePreflightProof(c) == nil {
		t.Fatal("cancellation did not revoke its own generation")
	}
}

type preflightPublicationCancellationContext struct {
	context.Context
	cancel func()
	when   func() bool
}

func (c preflightPublicationCancellationContext) Err() error {
	if c.when() {
		c.cancel()
	}
	return c.Context.Err()
}

func TestPreflightPublicationCancellationNeverLeavesItsGenerationExecutable(t *testing.T) {
	for _, moment := range []string{"before-artifact", "artifact-written", "pointer-committed"} {
		t.Run(moment, func(t *testing.T) {
			c, qualification, at := completePreflightProofFixture(t, "dev")
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := preflightPublicationCancellationContext{Context: base, cancel: cancel, when: func() bool {
				if moment == "before-artifact" {
					return true
				}
				if moment == "artifact-written" {
					paths, _ := filepath.Glob(filepath.Join(c.Store.Root, "deployment", "preflight", c.Plan.SHA256(), "qualification-*", "proof.json"))
					return len(paths) > 0
				}
				active, err := readEnvironmentPreflightActive(c.Store)
				return err == nil && active.Decision == "GO"
			}}
			if _, err := saveEnvironmentPreflightProofContext(ctx, c, qualification, at); err == nil || !errors.Is(err, context.Canceled) {
				t.Fatal("canceled qualification publication reported success", err)
			}
			active, err := readEnvironmentPreflightActive(c.Store)
			if err != nil || active.Generation != c.PreflightGeneration || active.Decision != "NO-GO" || active.Artifact != "" {
				t.Fatal("canceled publication left executable GO evidence", active, err)
			}
			if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", c.Plan.SHA256(), at); err == nil {
				t.Fatal("canceled preflight remained consumable")
			}
		})
	}
}

func TestPreflightPublicationLockRefusesConcurrentOwnersWithoutBlocking(t *testing.T) {
	c, qualification, at := completePreflightProofFixture(t, "dev")
	lock, err := lockEnvironmentPreflightPublication(c.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	finished := make(chan error, 1)
	go func() {
		_, err := saveEnvironmentPreflightProof(c, qualification, at)
		finished <- err
	}()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("another publisher bypassed the original publication lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publication waited indefinitely for another owner's lock")
	}
	active, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || active.Decision != "NO-GO" || active.Generation != c.PreflightGeneration {
		t.Fatal("failed concurrent publication changed the original generation", active, err)
	}
}

func TestStalePreflightObservationsCannotReplaceANewerGeneration(t *testing.T) {
	for _, decision := range []string{"NO-GO", "GO"} {
		t.Run(decision, func(t *testing.T) {
			c, qualification, at := completePreflightProofFixture(t, "dev")
			first := c.PreflightGeneration
			second, err := invalidateEnvironmentPreflightProofWithToken(c.Configuration)
			if err != nil {
				t.Fatal(err)
			}
			c.PreflightGeneration = second
			if err := publishEnvironmentPreflightObservation(c.Configuration, c, second, publishEnvironmentPreflightBindings); err != nil {
				t.Fatal("current generation could not publish its observations", err)
			}
			if decision == "GO" {
				if _, err := saveEnvironmentPreflightProof(c, qualification, at); err != nil {
					t.Fatal(err)
				}
			}
			activePath := filepath.Join(c.Store.Root, "deployment", "preflight", "active.json")
			bindingPath := filepath.Join(c.Store.Root, "deployment", "identity-bindings.json")
			originalActive, activeErr := os.ReadFile(activePath)
			originalBindings, bindingErr := os.ReadFile(bindingPath)
			if activeErr != nil || bindingErr != nil {
				t.Fatal("new generation evidence unavailable", activeErr, bindingErr)
			}
			c.PreflightGeneration = first
			calls := 0
			publisher := func(cfg deploymentConfig, c *compiledEnvironmentDeployment, decision string) error {
				calls++
				return publishEnvironmentPreflightBindings(cfg, c, decision)
			}
			for _, observations := range []*compiledEnvironmentDeployment{nil, c} {
				if err := publishEnvironmentPreflightObservation(c.Configuration, observations, first, publisher); err == nil {
					t.Fatal("stale initial or compiled observations were published")
				}
			}
			afterActive, activeErr := os.ReadFile(activePath)
			afterBindings, bindingErr := os.ReadFile(bindingPath)
			if calls != 0 || activeErr != nil || bindingErr != nil || !bytes.Equal(originalActive, afterActive) || !bytes.Equal(originalBindings, afterBindings) {
				t.Fatal("stale observations overwrote a newer active proof or binding record", calls, activeErr, bindingErr)
			}
		})
	}
}

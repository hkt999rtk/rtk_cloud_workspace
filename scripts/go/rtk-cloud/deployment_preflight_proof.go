package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentPreflightProofSchema = "rtk-cloud.preflight-proof/v1"
const environmentPreflightActiveSchema = "rtk-cloud.preflight-active/v1"
const environmentPreflightCheckerVersion = "rtk-cloud.qualification/v1"
const environmentPreflightArtifactLimit = 32 << 20
const environmentPreflightValidityKey = "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS"

var environmentPreflightArtifactName = regexp.MustCompile(`^[a-f0-9]{64}/qualification-[0-9]+$`)
var environmentPreflightDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type environmentPreflightProof struct {
	Schema                   string                               `json:"schema"`
	Generation               string                               `json:"generation"`
	Environment              string                               `json:"environment"`
	Stack                    string                               `json:"stack"`
	Operation                string                               `json:"operation"`
	PlanSHA256               string                               `json:"plan_sha256"`
	ConfigurationSHA256      string                               `json:"configuration_sha256"`
	MaintainedConfigSHA256   string                               `json:"maintained_config_sha256"`
	InputsSHA256             string                               `json:"inputs_sha256"`
	ReleaseSHA256            string                               `json:"release_sha256"`
	BindingsSHA256           string                               `json:"bindings_sha256"`
	ClusterSHA256            string                               `json:"cluster_sha256"`
	KubernetesIdentitySHA256 string                               `json:"kubernetes_identity_sha256"`
	CheckerVersion           string                               `json:"checker_version"`
	CheckerSHA256            string                               `json:"checker_sha256"`
	QualifiedAt              time.Time                            `json:"qualified_at"`
	ValidUntil               time.Time                            `json:"valid_until"`
	PKICheckedAt             time.Time                            `json:"pki_checked_at"`
	PKIVerifiedUntil         time.Time                            `json:"pki_verified_until"`
	Qualification            deploymentplan.Qualification         `json:"qualification"`
	ReleaseProofs            []environmentReleasePublicationProof `json:"release_proofs"`
}

func (p environmentPreflightProof) SHA256() string { return deploymentplan.Digest(p) }

type environmentPreflightArtifact struct {
	Plan      *deploymentplan.Plan
	Release   environmentRelease
	Bindings  environmentIdentityBindings
	Proof     environmentPreflightProof
	Directory string
}

type environmentPreflightActive struct {
	Schema        string    `json:"schema"`
	Generation    string    `json:"generation"`
	Environment   string    `json:"environment"`
	Decision      string    `json:"decision"`
	PlanSHA256    string    `json:"plan_sha256,omitempty"`
	Artifact      string    `json:"artifact,omitempty"`
	ProofSHA256   string    `json:"proof_sha256,omitempty"`
	InvalidatedAt time.Time `json:"invalidated_at,omitempty"`
}

func environmentPreflightProofBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.preflight.proof", Cause: cause, Next: "run complete preflight for this environment and exact release; review its plan SHA-256 before execution; never substitute old or partial evidence"}
}

func environmentPreflightValidity(values map[string]string) (int64, error) {
	raw := values[environmentPreflightValidityKey]
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || raw != strconv.FormatInt(seconds, 10) || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0, &deploymentplan.Blocker{ID: "deployment.preflight.validity", Resource: environmentPreflightValidityKey, Cause: "an explicit positive bounded preflight evidence validity is required", Next: "maintain DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS in the shared architecture or environment architecture override, then repeat complete preflight"}
	}
	return seconds, nil
}

func environmentPreflightProofTimes(proof environmentPreflightProof, now time.Time) string {
	return fmt.Sprintf("qualified_at=%s valid_until=%s checked_at=%s", proof.QualifiedAt.UTC().Format(time.RFC3339Nano), proof.ValidUntil.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
}

// Root admission can diagnose freshness before full artifact validation. Actual
// publication and consumption still require the complete validator below.
func environmentPreflightProofTimingError(proof environmentPreflightProof, now time.Time) error {
	cause := ""
	switch {
	case proof.QualifiedAt.IsZero():
		cause = "qualification is missing its completion time"
	case proof.ValidUntil.IsZero():
		cause = "qualification is missing its evidence expiry time"
	case now.Before(proof.QualifiedAt):
		cause = "qualification is future-dated"
	case !now.Before(proof.ValidUntil):
		cause = "qualification evidence has expired"
	}
	if cause != "" {
		return environmentPreflightProofBlocker(cause + "; " + environmentPreflightProofTimes(proof, now))
	}
	return nil
}

func invalidateEnvironmentPreflightProof(cfg deploymentConfig) error {
	_, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	return err
}

func invalidateEnvironmentPreflightProofWithToken(cfg deploymentConfig) (string, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return "", environmentPreflightProofBlocker("selected canonical private store is unavailable")
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", environmentPreflightProofBlocker("unique qualification generation cannot be prepared")
	}
	generation := fmt.Sprintf("%x", random[:])
	if err := writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: generation, Environment: cfg.Environment, Decision: "NO-GO", InvalidatedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	return generation, nil
}

func publishEnvironmentPreflightObservation(cfg deploymentConfig, c *compiledEnvironmentDeployment, generation string, publishBindings func(deploymentConfig, *compiledEnvironmentDeployment, string) error) error {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil || !environmentPreflightDigest.MatchString(generation) || publishBindings == nil {
		return environmentPreflightProofBlocker("selected qualification generation and observed binding publisher are required")
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return err
	}
	defer lock.Close()
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Generation != generation || active.Decision != "NO-GO" {
		return environmentPreflightProofBlocker("another preflight invalidated or replaced these binding observations")
	}
	return publishBindings(cfg, c, "NO-GO")
}

func saveEnvironmentPreflightProof(c *compiledEnvironmentDeployment, qualification deploymentplan.Qualification, qualifiedAt time.Time) (*environmentPreflightProof, error) {
	return saveEnvironmentPreflightProofContext(context.Background(), c, qualification, qualifiedAt)
}

func saveEnvironmentPreflightProofContext(ctx context.Context, c *compiledEnvironmentDeployment, qualification deploymentplan.Qualification, qualifiedAt time.Time) (*environmentPreflightProof, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.Plan == nil || c.Store.Environment != c.Plan.Environment || c.Configuration.Environment != c.Plan.Environment {
		return nil, environmentPreflightProofBlocker("complete selected environment qualification is unavailable")
	}
	validity, err := environmentPreflightValidity(c.Configuration.Values)
	if err != nil {
		return nil, err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return nil, err
	}
	cluster, identity, err := environmentPreflightKubernetesIdentity(c.Backend.KubeconfigContent)
	if err != nil {
		return nil, err
	}
	qualifiedAt = qualifiedAt.UTC()
	proof := environmentPreflightProof{Schema: environmentPreflightProofSchema, Generation: c.PreflightGeneration, Environment: c.Plan.Environment, Stack: c.Plan.Stack, Operation: c.Plan.Operation, PlanSHA256: c.Plan.SHA256(), ConfigurationSHA256: c.Plan.ConfigurationSHA256, MaintainedConfigSHA256: deploymentplan.Digest(c.Configuration), InputsSHA256: deploymentplan.Digest(c.Plan.Inputs), ReleaseSHA256: c.Plan.ReleaseSHA256, BindingsSHA256: c.Plan.BindingsSHA256, ClusterSHA256: cluster, KubernetesIdentitySHA256: identity, CheckerVersion: environmentPreflightCheckerVersion, CheckerSHA256: checker, QualifiedAt: qualifiedAt, ValidUntil: qualifiedAt.Add(time.Duration(validity) * time.Second), PKICheckedAt: c.PKICheckedAt.UTC(), PKIVerifiedUntil: c.PKIVerifiedUntil.UTC(), Qualification: qualification, ReleaseProofs: append([]environmentReleasePublicationProof(nil), c.ReleaseProofs...)}
	if err := validateEnvironmentPreflightProof(proof, c.Configuration, c.Plan, qualifiedAt); err != nil {
		return nil, err
	}
	if deploymentplan.Digest(c.Release) != proof.ReleaseSHA256 || deploymentplan.Digest(c.Bindings) != proof.BindingsSHA256 || validateEnvironmentPreflightReleaseProofs(proof, c.Release) != nil {
		return nil, environmentPreflightProofBlocker("frozen release, bindings or exact CI publication artifacts are incomplete")
	}
	if err := verifyEnvironmentPreflightBindingRecordDecision(c.Store, c.Plan, c.Bindings, true); err != nil {
		return nil, err
	}
	base := filepath.Join(c.Store.Root, "deployment", "preflight")
	parent := filepath.Join(base, proof.PlanSHA256)
	if err := prepareEnvironmentPreflightDirectories(c.Store, parent); err != nil {
		return nil, err
	}
	attempt, err := os.MkdirTemp(parent, "qualification-")
	if err != nil {
		return nil, environmentPreflightProofBlocker("immutable private qualification directory could not be prepared")
	}
	for _, entry := range []struct {
		name  string
		value any
	}{{"plan.json", c.Plan}, {"release.json", c.Release}, {"bindings.json", c.Bindings}, {"proof.json", proof}} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := writeEnvironmentPreflightImmutableJSON(filepath.Join(attempt, entry.name), entry.value); err != nil {
			return nil, err
		}
	}
	for _, directory := range []string{attempt, parent} {
		if err := syncEnvironmentPreflightDirectory(directory); err != nil {
			return nil, err
		}
	}
	// A complete artifact is not executable until the last atomic GO pointer
	// publication. A failed or interrupted preflight keeps the active NO-GO.
	relative, err := filepath.Rel(base, attempt)
	if err != nil || !environmentPreflightArtifactName.MatchString(filepath.ToSlash(relative)) {
		return nil, environmentPreflightProofBlocker("qualification artifact is outside the selected canonical store")
	}
	lock, err := lockEnvironmentPreflightPublication(c.Store)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	current, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || current.Decision != "NO-GO" || current.Generation != proof.Generation {
		return nil, environmentPreflightProofBlocker("another preflight invalidated or replaced this qualification generation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentPreflightProof(proof, c.Configuration, c.Plan, time.Now().UTC()); err != nil {
		return nil, err
	}
	if deploymentplan.Digest(c.Release) != proof.ReleaseSHA256 || deploymentplan.Digest(c.Bindings) != proof.BindingsSHA256 {
		return nil, environmentPreflightProofBlocker("qualification inputs changed before publication")
	}
	if err := verifyEnvironmentPreflightBindingRecordDecision(c.Store, c.Plan, c.Bindings, true); err != nil {
		return nil, err
	}
	if err := publishEnvironmentPreflightBindings(c.Configuration, c, "GO"); err != nil {
		return nil, environmentPreflightProofBlocker("qualified secondary identity observations could not be published")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := environmentPreflightProofTimingError(proof, time.Now().UTC()); err != nil {
		return nil, err
	}
	active := environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: proof.Generation, Environment: c.Plan.Environment, Decision: "GO", PlanSHA256: proof.PlanSHA256, Artifact: filepath.ToSlash(relative), ProofSHA256: proof.SHA256()}
	if err := writeEnvironmentPreflightActive(c.Store, active); err != nil {
		return nil, errors.Join(err, revokeEnvironmentPreflightProofLocked(c.Store, proof.Generation))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, revokeEnvironmentPreflightProofLocked(c.Store, proof.Generation))
	}
	if err := environmentPreflightProofTimingError(proof, time.Now().UTC()); err != nil {
		return nil, errors.Join(err, revokeEnvironmentPreflightProofLocked(c.Store, proof.Generation))
	}
	return &proof, nil
}

func loadEnvironmentPreflightProof(cfg deploymentConfig, store secretStore, operation, sha string, now time.Time) (*environmentPreflightArtifact, error) {
	if store.Environment != cfg.Environment || !environmentPreflightDigest.MatchString(sha) || operation != "create" && operation != "upgrade" {
		return nil, environmentPreflightProofBlocker("exact selected environment, operation and reviewed plan digest are required")
	}
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Decision != "GO" || active.PlanSHA256 != sha {
		return nil, environmentPreflightProofBlocker("no active complete GO qualification matches the reviewed plan")
	}
	base := filepath.Join(store.Root, "deployment", "preflight")
	directory := filepath.Join(base, filepath.FromSlash(active.Artifact))
	if err := validateEnvironmentPreflightDirectories(store, directory); err != nil {
		return nil, err
	}
	artifact := &environmentPreflightArtifact{Plan: &deploymentplan.Plan{}, Directory: directory}
	for _, entry := range []struct {
		name  string
		value any
	}{{"proof.json", &artifact.Proof}, {"plan.json", artifact.Plan}, {"release.json", &artifact.Release}, {"bindings.json", &artifact.Bindings}} {
		if err := readEnvironmentPreflightJSON(filepath.Join(directory, entry.name), entry.value); err != nil {
			return nil, err
		}
	}
	if artifact.Proof.SHA256() != active.ProofSHA256 || artifact.Proof.Generation != active.Generation || artifact.Proof.PlanSHA256 != sha || artifact.Proof.Operation != operation || artifact.Plan.SHA256() != sha || deploymentplan.Digest(artifact.Release) != artifact.Proof.ReleaseSHA256 || deploymentplan.Digest(artifact.Bindings) != artifact.Proof.BindingsSHA256 || artifact.Release.Environment != cfg.Environment || artifact.Release.Stack != artifact.Plan.Stack || artifact.Bindings.Environment != cfg.Environment || artifact.Bindings.Stack != artifact.Plan.Stack {
		return nil, environmentPreflightProofBlocker("active qualification payload hashes or environment bindings do not match")
	}
	if err := validateEnvironmentPreflightProof(artifact.Proof, cfg, artifact.Plan, now); err != nil {
		return nil, err
	}
	if err := validateEnvironmentPreflightReleaseProofs(artifact.Proof, artifact.Release); err != nil {
		return nil, err
	}
	if err := verifyEnvironmentPreflightBindingRecord(store, artifact.Plan, artifact.Bindings); err != nil {
		return nil, err
	}
	// Concurrent preflight invalidation must win over this earlier read.
	fresh, err := readEnvironmentPreflightActive(store)
	if err != nil || fresh != active {
		return nil, environmentPreflightProofBlocker("active qualification changed while its payloads were being loaded")
	}
	return artifact, nil
}

func validateEnvironmentPreflightProof(proof environmentPreflightProof, cfg deploymentConfig, plan *deploymentplan.Plan, now time.Time) error {
	validity, err := environmentPreflightValidity(cfg.Values)
	if err != nil {
		return err
	}
	window, err := environmentPKIValidityWindow(cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || plan == nil || plan.Version != deploymentplan.Version || len(plan.Resources) == 0 || proof.Schema != environmentPreflightProofSchema || !environmentPreflightDigest.MatchString(proof.Generation) || proof.CheckerVersion != environmentPreflightCheckerVersion || proof.CheckerSHA256 != checker || proof.Environment != cfg.Environment || proof.Environment != plan.Environment || proof.Stack != plan.Stack || proof.Stack != cfg.Values["CLOUD_STACK_NAME"] || proof.Operation != plan.Operation || proof.PlanSHA256 != plan.SHA256() || proof.ConfigurationSHA256 != plan.ConfigurationSHA256 || proof.MaintainedConfigSHA256 != deploymentplan.Digest(cfg) || proof.InputsSHA256 != deploymentplan.Digest(plan.Inputs) || proof.ReleaseSHA256 != plan.ReleaseSHA256 || proof.BindingsSHA256 != plan.BindingsSHA256 || !environmentPreflightDigest.MatchString(proof.ClusterSHA256) || !environmentPreflightDigest.MatchString(proof.KubernetesIdentitySHA256) {
		return environmentPreflightProofBlocker("qualification schema, checker, environment or exact input hashes no longer match")
	}
	if err := environmentPreflightProofTimingError(proof, now); err != nil {
		return err
	}
	if !proof.ValidUntil.Equal(proof.QualifiedAt.Add(time.Duration(validity) * time.Second)) {
		return environmentPreflightProofBlocker("qualification evidence lifetime does not match " + environmentPreflightValidityKey + "=" + strconv.FormatInt(validity, 10) + "; " + environmentPreflightProofTimes(proof, now))
	}
	if proof.PKICheckedAt.IsZero() || proof.PKIVerifiedUntil.IsZero() {
		return environmentPreflightProofBlocker("qualification is missing its original completed PKI validity proof; " + environmentPreflightProofTimes(proof, now))
	}
	if proof.PKICheckedAt.After(proof.QualifiedAt) {
		return environmentPreflightProofBlocker("original PKI verification is later than qualification completion; " + environmentPreflightProofTimes(proof, now))
	}
	if !proof.PKIVerifiedUntil.Equal(proof.PKICheckedAt.Add(time.Duration(window) * time.Second)) {
		return environmentPreflightProofBlocker("original PKI validity proof does not match DEPLOYMENT_UPGRADE_WINDOW_SECONDS=" + strconv.FormatInt(window, 10) + "; " + environmentPreflightProofTimes(proof, now))
	}
	ids := deploymentplan.RequiredGateIDs(cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] == "production")
	if plan.Operation == "upgrade" {
		ids = ids[:6]
	}
	if proof.Qualification.Decision != "GO" || proof.Qualification.PlanSHA256 != proof.PlanSHA256 || len(proof.Qualification.Gates) != len(ids) {
		return environmentPreflightProofBlocker("qualification lacks every required successful gate for this exact operation")
	}
	for index, gate := range proof.Qualification.Gates {
		if gate.ID != ids[index] || gate.Status != "PASS" || !gate.Required || !environmentPreflightDigest.MatchString(gate.EvidenceSHA256) || gate.Cause != "" || gate.Next != "" || gate.Impact != "" || gate.Recheck != "" {
			return environmentPreflightProofBlocker("qualification contains missing, duplicate, failed or incomplete gate evidence")
		}
		if len(gate.PendingRolloutHealth) > 0 {
			if gate.ID != "deployment.pki" || plan.Operation != "upgrade" && !plan.HasCreateHandoff() {
				return environmentPreflightProofBlocker("pending rollout acceptance is outside the reviewed PKI gate")
			}
			seen := map[string]bool{}
			for _, id := range gate.PendingRolloutHealth {
				found := false
				for _, resource := range plan.Resources {
					found = found || resource.ID == id && resource.UID != "" && resource.Desired["kind"] == "Deployment" && (resource.Action == "update" || resource.Action == "retain")
				}
				if seen[id] || !found {
					return environmentPreflightProofBlocker("pending rollout health is not bound to original reviewed workloads")
				}
				seen[id] = true
			}
		}
	}
	return nil
}

func validateEnvironmentPreflightReleaseProofs(proof environmentPreflightProof, release environmentRelease) error {
	if release.Schema != "rtk-cloud.release/v1" || release.Environment != proof.Environment || release.Stack != proof.Stack || !deploymentUpgradeSHA.MatchString(release.WorkspaceRevision) || len(release.Images) == 0 {
		return environmentPreflightProofBlocker("complete immutable release and selected source revision are unavailable")
	}
	seen := map[string]bool{}
	for _, publication := range proof.ReleaseProofs {
		image, ok := release.Images[publication.ImageKey]
		name, workflow := "image-manifest", "release.yml"
		if publication.Backup {
			name, workflow = "postgres-backup-runner-images", "postgres-backup-image.yml"
		}
		if !ok || seen[publication.ImageKey] || publication.Reference != image.Reference || publication.SourceRevision != image.SourceRevision || !deploymentUpgradeSHA.MatchString(publication.SourceRevision) || !strings.HasPrefix(image.Reference, "ghcr.io/"+publication.Repository+"/") || publication.RunID <= 0 || publication.RunAttempt <= 0 || publication.ArtifactID <= 0 || publication.ArtifactName != name || publication.Workflow != workflow || publication.ObservedAt.IsZero() || publication.ObservedAt.After(proof.QualifiedAt) || !environmentPreflightDigest.MatchString(publication.RunSHA256) || !environmentPreflightDigest.MatchString(publication.InventorySHA256) || !environmentPreflightDigest.MatchString(publication.ArchiveSHA256) || publication.ManifestSHA256 != environmentPreflightBytesSHA256(publication.Manifest) || verifyPublicationJSON(publication.Manifest, publication.Repository, publication.RunID, publication.RunAttempt, image, publication.Backup) != nil {
			return environmentPreflightProofBlocker("selected CI run, attempt or formal artifact payload does not match the frozen release")
		}
		if image.OTARegistration != nil {
			manifest, err := publishedOTARegistration(publication.Manifest, image)
			if err != nil || manifest == nil || deploymentplan.Digest(manifest) != deploymentplan.Digest(image.OTARegistration) {
				return environmentPreflightProofBlocker("formal selected-image OTA descriptor does not match the frozen release")
			}
		}
		seen[publication.ImageKey] = true
	}
	for key, image := range release.Images {
		if !immutableEnvironmentImage.MatchString(image.Reference) || strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") && !seen[key] {
			return environmentPreflightProofBlocker("an immutable selected image lacks its captured exact CI publication proof")
		}
	}
	return nil
}

func verifyEnvironmentActivePreflightProof(c *compiledEnvironmentDeployment) error {
	if c == nil || c.Plan == nil || c.Proof == nil {
		return environmentPreflightProofBlocker("selected immutable qualification is unavailable")
	}
	active, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || active.Decision != "GO" || active.Generation != c.Proof.Generation || active.PlanSHA256 != c.Plan.SHA256() || active.ProofSHA256 != c.Proof.SHA256() {
		return environmentPreflightProofBlocker("active qualification was invalidated or replaced after review")
	}
	return verifyEnvironmentPreflightBindingRecord(c.Store, c.Plan, c.Bindings)
}

func verifyEnvironmentPreflightBindingRecord(store secretStore, plan *deploymentplan.Plan, bindings environmentIdentityBindings) error {
	return verifyEnvironmentPreflightBindingRecordDecision(store, plan, bindings, false)
}

func verifyEnvironmentPreflightBindingRecordDecision(store secretStore, plan *deploymentplan.Plan, bindings environmentIdentityBindings, allowNoGo bool) error {
	var record environmentPreflightBindingsRecord
	if readEnvironmentPreflightJSONWithCanonical(filepath.Join(store.Root, "deployment", "identity-bindings.json"), &record, false) != nil || record.Schema != "rtk-cloud.preflight-bindings/v1" || record.Environment != plan.Environment || record.Operation != plan.Operation || record.Decision != "GO" && !(allowNoGo && record.Decision == "NO-GO") || record.PlanSHA256 != plan.SHA256() || record.BindingsSHA256 != plan.BindingsSHA256 || record.Observed == nil || deploymentplan.Digest(*record.Observed) != plan.BindingsSHA256 || deploymentplan.Digest(bindings) != plan.BindingsSHA256 {
		return environmentPreflightProofBlocker("secondary observed identity bindings no longer match the complete qualification")
	}
	return nil
}

func environmentPreflightKubernetesIdentity(raw []byte) (string, string, error) {
	var frozen struct {
		Current  string `yaml:"current-context"`
		Clusters []struct {
			Name    string         `yaml:"name"`
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if len(raw) == 0 || len(raw) > 4<<20 || yaml.Unmarshal(raw, &frozen) != nil || frozen.Current == "" || len(frozen.Clusters) != 1 || frozen.Clusters[0].Name == "" || deploymentplan.String(frozen.Clusters[0].Cluster["server"]) == "" || deploymentplan.String(frozen.Clusters[0].Cluster["certificate-authority-data"]) == "" {
		return "", "", environmentPreflightProofBlocker("selected frozen Kubernetes cluster and authentication identity are unavailable")
	}
	return deploymentplan.Digest(frozen.Clusters[0]), environmentPreflightBytesSHA256(raw), nil
}

var environmentPreflightCheckerCache struct {
	sync.Once
	sha string
	err error
}

func environmentPreflightCheckerSHA256() (string, error) {
	environmentPreflightCheckerCache.Do(func() {
		path, err := os.Executable()
		if err != nil {
			environmentPreflightCheckerCache.err = environmentPreflightProofBlocker("executing qualification checker cannot be fingerprinted")
			return
		}
		file, err := os.Open(path)
		if err != nil {
			environmentPreflightCheckerCache.err = environmentPreflightProofBlocker("executing qualification checker cannot be fingerprinted")
			return
		}
		defer file.Close()
		hash := sha256.New()
		_, _ = io.WriteString(hash, environmentPreflightCheckerVersion+"\x00")
		if _, err := io.Copy(hash, file); err != nil {
			environmentPreflightCheckerCache.err = environmentPreflightProofBlocker("executing qualification checker cannot be fingerprinted")
			return
		}
		environmentPreflightCheckerCache.sha = fmt.Sprintf("%x", hash.Sum(nil))
	})
	return environmentPreflightCheckerCache.sha, environmentPreflightCheckerCache.err
}

func readEnvironmentPreflightActive(store secretStore) (environmentPreflightActive, error) {
	var active environmentPreflightActive
	directory := filepath.Join(store.Root, "deployment", "preflight")
	if err := validateEnvironmentPreflightDirectories(store, directory); err != nil {
		return active, err
	}
	if err := readEnvironmentPreflightJSON(filepath.Join(directory, "active.json"), &active); err != nil {
		return active, err
	}
	if active.Schema != environmentPreflightActiveSchema || !environmentPreflightDigest.MatchString(active.Generation) || active.Environment != store.Environment || active.Decision != "GO" && active.Decision != "NO-GO" || active.Decision == "GO" && (!environmentPreflightArtifactName.MatchString(active.Artifact) || !environmentPreflightDigest.MatchString(active.ProofSHA256) || !strings.HasPrefix(active.Artifact, active.PlanSHA256+"/")) || active.Decision == "NO-GO" && (active.Artifact != "" || active.PlanSHA256 != "" || active.ProofSHA256 != "" || active.InvalidatedAt.IsZero()) {
		return active, environmentPreflightProofBlocker("active qualification pointer is invalid or does not name one immutable canonical artifact")
	}
	return active, nil
}

func writeEnvironmentPreflightActive(store secretStore, active environmentPreflightActive) error {
	directory := filepath.Join(store.Root, "deployment", "preflight")
	if err := prepareEnvironmentPreflightDirectories(store, directory); err != nil {
		return err
	}
	raw, err := json.Marshal(active)
	if err != nil {
		return environmentPreflightProofBlocker("active qualification pointer cannot be encoded")
	}
	if err := store.write("deployment/preflight/active.json", raw, true); err != nil {
		return environmentPreflightProofBlocker("active qualification pointer cannot be committed atomically")
	}
	return syncEnvironmentPreflightDirectory(directory)
}

func lockEnvironmentPreflightPublication(store secretStore) (*os.File, error) {
	directory := filepath.Join(store.Root, "deployment", "preflight")
	if err := prepareEnvironmentPreflightDirectories(store, directory); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(directory, ".publication.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, environmentPreflightProofBlocker("private qualification publication lock is unavailable")
	}
	file := os.NewFile(uintptr(fd), "preflight-publication")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		file.Close()
		return nil, environmentPreflightProofBlocker("private qualification publication lock cannot be verified")
	}
	return file, nil
}

func revokeEnvironmentPreflightProof(cfg deploymentConfig, generation string) error {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil || !environmentPreflightDigest.MatchString(generation) {
		return environmentPreflightProofBlocker("original qualification generation is unavailable for revocation")
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return err
	}
	defer lock.Close()
	return revokeEnvironmentPreflightProofLocked(store, generation)
}

func revokeEnvironmentPreflightProofLocked(store secretStore, generation string) error {
	active, err := readEnvironmentPreflightActive(store)
	if err != nil {
		return err
	}
	if active.Generation != generation {
		return nil
	}
	return writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: generation, Environment: store.Environment, Decision: "NO-GO", InvalidatedAt: time.Now().UTC()})
}

func syncEnvironmentPreflightDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return environmentPreflightProofBlocker("private qualification directory cannot be durably synchronized")
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return environmentPreflightProofBlocker("private qualification directory cannot be durably synchronized")
	}
	return nil
}

func prepareEnvironmentPreflightDirectories(store secretStore, destination string) error {
	if !filepath.IsAbs(store.Root) || !filepath.IsAbs(store.ConfigRoot) || !secretEnvironmentPattern.MatchString(store.Environment) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) {
		return environmentPreflightProofBlocker("canonical selected environment store layout is required")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "preflight"), destination} {
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return environmentPreflightProofBlocker("private qualification directories cannot be prepared")
		}
		if err := validateEnvironmentPreflightDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironmentPreflightDirectories(store secretStore, destination string) error {
	if !filepath.IsAbs(store.Root) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) || !secretEnvironmentPattern.MatchString(store.Environment) {
		return environmentPreflightProofBlocker("canonical selected environment store layout is required")
	}
	relative, err := filepath.Rel(store.Root, destination)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return environmentPreflightProofBlocker("qualification evidence is outside the selected canonical store")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root} {
		if err := validateEnvironmentPreflightDirectory(directory); err != nil {
			return err
		}
	}
	current := store.Root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := validateEnvironmentPreflightDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironmentPreflightDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return environmentPreflightProofBlocker("original qualification directory must be a real private 0700 directory")
	}
	return nil
}

func readEnvironmentPreflightJSON(path string, value any) error {
	return readEnvironmentPreflightJSONWithCanonical(path, value, true)
}

func readEnvironmentPreflightJSONWithCanonical(path string, value any, requireCanonical bool) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return environmentPreflightProofBlocker("original complete qualification payload is unavailable")
	}
	file := os.NewFile(uintptr(fd), "preflight-evidence")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > environmentPreflightArtifactLimit {
		return environmentPreflightProofBlocker("qualification payload must be a bounded private 0600 regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, environmentPreflightArtifactLimit+1))
	if err != nil || len(raw) > environmentPreflightArtifactLimit || decodeEnvironmentJSON(raw, value) != nil {
		return environmentPreflightProofBlocker("qualification payload is invalid, incomplete or exceeds its bound")
	}
	canonical, err := json.Marshal(value)
	if err != nil || requireCanonical && !bytes.Equal(raw, canonical) {
		return environmentPreflightProofBlocker("qualification payload is noncanonical or contains ambiguous evidence")
	}
	return nil
}

func writeEnvironmentPreflightImmutableJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return environmentPreflightProofBlocker("complete qualification payload cannot be encoded within its bound")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return environmentPreflightProofBlocker("immutable qualification payload cannot be created")
	}
	written, writeErr := file.Write(raw)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil || written != len(raw) {
		return environmentPreflightProofBlocker("complete immutable qualification payload could not be persisted")
	}
	return nil
}

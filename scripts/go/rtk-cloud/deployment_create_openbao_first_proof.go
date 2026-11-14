package main

import (
	"context"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This records a verified source checkpoint only. It is not a complete
// deployment preflight and must never be treated as an overall GO decision.
const openBaoFirstCheckpointProofSchema = "rtk-cloud.openbao-first-checkpoint-evidence/v1"
const openBaoFirstCheckpointProofBase = "deployment/create/openbao-first-checkpoint"

type openBaoFirstCheckpointProof struct {
	Schema              string                            `json:"schema"`
	Environment         string                            `json:"environment"`
	Stack               string                            `json:"stack"`
	ConfigurationSHA256 string                            `json:"configuration_sha256"`
	CheckerSHA256       string                            `json:"checker_sha256"`
	QualifiedAt         time.Time                         `json:"qualified_at"`
	ValidUntil          time.Time                         `json:"valid_until"`
	Qualification       openBaoFirstRecoveryQualification `json:"qualification"`
}

type openBaoFirstCheckpointActive struct {
	Schema      string `json:"schema"`
	Environment string `json:"environment"`
	State       string `json:"state"`
	ProofSHA256 string `json:"proof_sha256,omitempty"`
}

func readOpenBaoFirstCheckpointArtifact(store secretStore, cfg deploymentConfig, sha string) (openBaoFirstCheckpointProof, openBaoFirstCheckpointActive, error) {
	var p openBaoFirstCheckpointProof
	var active openBaoFirstCheckpointActive
	bad := func() (openBaoFirstCheckpointProof, openBaoFirstCheckpointActive, error) {
		return openBaoFirstCheckpointProof{}, openBaoFirstCheckpointActive{}, createProviderRecoveryBlock("active original OpenBao-first checkpoint evidence is unavailable or changed")
	}
	if !environmentPreflightDigest.MatchString(sha) || createOpenBaoACKCompletionRead(store, filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), &active) != nil || active.Schema != openBaoFirstCheckpointProofSchema+"/active" || active.Environment != cfg.Environment || active.State != "complete" || active.ProofSHA256 != sha {
		return bad()
	}
	if createOpenBaoACKCompletionRead(store, filepath.Join(openBaoFirstCheckpointProofBase, sha, "proof.json"), &p) != nil || deploymentplan.Digest(p) != sha {
		return bad()
	}
	return p, active, nil
}

func validateOpenBaoFirstCheckpointProof(p openBaoFirstCheckpointProof, q openBaoFirstRecoveryQualification, cfg deploymentConfig, checker string, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("original OpenBao-first checkpoint evidence is incomplete, changed or expired")
	}
	seconds, err := environmentPreflightValidity(cfg.Values)
	if err != nil {
		return err
	}
	if cfg.Environment != "dev" || p.Schema != openBaoFirstCheckpointProofSchema || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.ConfigurationSHA256 != deploymentplan.Digest(cfg) || !environmentPreflightDigest.MatchString(checker) || p.CheckerSHA256 != checker || deploymentplan.Digest(p.Qualification) != deploymentplan.Digest(q) {
		return bad()
	}
	if q.Schema != "rtk-cloud.openbao-first-source/v1" || q.Environment != cfg.Environment || !environmentPreflightDigest.MatchString(q.ParentSHA256) || !environmentPreflightDigest.MatchString(q.ParentOwnerSHA256) || !environmentPreflightDigest.MatchString(q.ParentJournalSHA256) || !environmentPreflightDigest.MatchString(q.OperatorSHA256) || q.Foundation.ParentState != "completed" || !q.Foundation.Accepted || !environmentPreflightDigest.MatchString(q.Foundation.PlanSHA256) || !environmentPreflightDigest.MatchString(q.Foundation.JournalSHA256) || q.OpenBao != "initialized" || q.Transport != "completed" || q.Management != "completed" || q.Signer != "completed" {
		return bad()
	}
	if environmentPreflightProofTimingError(environmentPreflightProof{QualifiedAt: p.QualifiedAt, ValidUntil: p.ValidUntil}, now) != nil || !p.ValidUntil.Equal(p.QualifiedAt.Add(time.Duration(seconds)*time.Second)) {
		return bad()
	}
	return nil
}

// The caller owns the original execution lock. Invalidation is local to this
// subordinate evidence and does not publish or invalidate a full GO.
func publishOpenBaoFirstCheckpointProof(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string) (openBaoFirstCheckpointProof, string, error) {
	var p openBaoFirstCheckpointProof
	bad := func() (openBaoFirstCheckpointProof, string, error) {
		return openBaoFirstCheckpointProof{}, "", createProviderRecoveryBlock("original OpenBao-first evidence cannot be published")
	}
	if e == nil || e.Store != store || cfg.Environment != "dev" || e.Check(ctx) != nil {
		return bad()
	}
	base, err := prepareEnvironmentCreateDirectories(store, openBaoFirstCheckpointProofBase)
	if err != nil {
		return p, "", err
	}
	active := openBaoFirstCheckpointActive{Schema: openBaoFirstCheckpointProofSchema + "/active", Environment: cfg.Environment, State: "invalid"}
	if err := store.write(filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), mustProviderRecoveryJSON(active), true); err != nil {
		return p, "", err
	}
	if err := verifyOpenBaoFirstOuterGates(ctx, cfg, store, e, b, operator); err != nil {
		return p, "", err
	}
	q, err := qualifyOpenBaoFirstRecovery(ctx, cfg, store, e, b, operator)
	if err != nil {
		return p, "", err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return p, "", err
	}
	seconds, err := environmentPreflightValidity(cfg.Values)
	if err != nil {
		return p, "", err
	}
	now := time.Now().UTC()
	p = openBaoFirstCheckpointProof{Schema: openBaoFirstCheckpointProofSchema, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg), CheckerSHA256: checker, QualifiedAt: now, ValidUntil: now.Add(time.Duration(seconds) * time.Second), Qualification: q}
	if err := validateOpenBaoFirstCheckpointProof(p, q, cfg, checker, now); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	sha := deploymentplan.Digest(p)
	dir, err := prepareEnvironmentCreateDirectories(store, filepath.Join(openBaoFirstCheckpointProofBase, sha))
	if err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	if err := writeEnvironmentPreflightImmutableJSON(filepath.Join(dir, "proof.json"), p); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	if err := syncEnvironmentPreflightDirectory(dir); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	if err := syncEnvironmentPreflightDirectory(base); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	if err := verifyOpenBaoFirstOuterGates(ctx, cfg, store, e, b, operator); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	current, err := qualifyOpenBaoFirstRecovery(ctx, cfg, store, e, b, operator)
	if err != nil || validateOpenBaoFirstCheckpointProof(p, current, cfg, checker, time.Now().UTC()) != nil || e.Check(ctx) != nil {
		return bad()
	}
	active.State, active.ProofSHA256 = "complete", sha
	if err := store.write(filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), mustProviderRecoveryJSON(active), true); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	if err := syncEnvironmentPreflightDirectory(base); err != nil {
		return openBaoFirstCheckpointProof{}, "", err
	}
	return p, sha, nil
}

// Loading always rechecks current provider/Kubernetes observations and the
// original parent fence; a stored proof alone cannot authorize a write.
func loadOpenBaoFirstCheckpointProof(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, sha string) (openBaoFirstCheckpointProof, error) {
	var p openBaoFirstCheckpointProof
	bad := func() (openBaoFirstCheckpointProof, error) {
		return openBaoFirstCheckpointProof{}, createProviderRecoveryBlock("active original OpenBao-first checkpoint evidence is unavailable or changed")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(sha) || e.Check(ctx) != nil {
		return bad()
	}
	p, active, err := readOpenBaoFirstCheckpointArtifact(store, cfg, sha)
	if err != nil {
		return bad()
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return bad()
	}
	if err := verifyOpenBaoFirstOuterGates(ctx, cfg, store, e, b, operator); err != nil {
		return openBaoFirstCheckpointProof{}, err
	}
	q, err := qualifyOpenBaoFirstRecovery(ctx, cfg, store, e, b, operator)
	if err != nil || validateOpenBaoFirstCheckpointProof(p, q, cfg, checker, time.Now().UTC()) != nil || e.Check(ctx) != nil {
		return bad()
	}
	var after openBaoFirstCheckpointActive
	if createOpenBaoACKCompletionRead(store, filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), &after) != nil || deploymentplan.Digest(active) != deploymentplan.Digest(after) {
		return bad()
	}
	return p, nil
}

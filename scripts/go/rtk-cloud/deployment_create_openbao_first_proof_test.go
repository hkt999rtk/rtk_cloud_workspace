package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoFirstCheckpointProofRejectsStaleOrChangedEvidence(t *testing.T) {
	now := time.Now().UTC()
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", environmentPreflightValidityKey: "3600"}}
	sha := strings.Repeat("a", 64)
	checker := strings.Repeat("b", 64)
	q := openBaoFirstRecoveryQualification{Schema: "rtk-cloud.openbao-first-source/v1", Environment: "dev", ParentSHA256: sha, ParentJournalSHA256: sha, ParentOwnerSHA256: sha, OperatorSHA256: sha, Foundation: openBaoFirstStageQualification{ParentState: "completed", Accepted: true, PlanSHA256: sha, JournalSHA256: sha}, OpenBao: "initialized", Transport: "completed", Management: "completed", Signer: "completed"}
	proof := openBaoFirstCheckpointProof{Schema: openBaoFirstCheckpointProofSchema, Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: deploymentplan.Digest(cfg), CheckerSHA256: checker, QualifiedAt: now, ValidUntil: now.Add(time.Hour), Qualification: q}
	for _, mode := range []string{"original", "expired", "future", "wrong-window", "checker", "configuration", "owner", "journal", "uninitialized", "missing-material", "not-dev"} {
		t.Run(mode, func(t *testing.T) {
			p, current, config, at := proof, q, cfg, now
			switch mode {
			case "expired":
				at = proof.ValidUntil
			case "future":
				at = now.Add(-time.Nanosecond)
			case "wrong-window":
				p.ValidUntil = now.Add(time.Minute)
			case "checker":
				p.CheckerSHA256 = sha
			case "configuration":
				p.ConfigurationSHA256 = sha
			case "owner":
				current.ParentOwnerSHA256 = checker
			case "journal":
				current.ParentJournalSHA256 = checker
			case "uninitialized":
				current.OpenBao = "ready-to-initialize"
			case "missing-material":
				current.Signer = "absent"
			case "not-dev":
				config.Environment = "staging"
			}
			err := validateOpenBaoFirstCheckpointProof(p, current, config, checker, at)
			if mode == "original" && err != nil {
				t.Fatal(err)
			}
			if mode != "original" && err == nil {
				t.Fatal("unverified or stale checkpoint proof accepted")
			}
		})
	}
}

func TestOpenBaoFirstCheckpointArtifactDurabilityAndInvalidation(t *testing.T) {
	cfg, store, e, operator := openBaoFirstQualificationFixture(t, "dev")
	base, err := prepareEnvironmentCreateDirectories(store, openBaoFirstCheckpointProofBase)
	if err != nil {
		t.Fatal(err)
	}
	proof := openBaoFirstCheckpointProof{Schema: openBaoFirstCheckpointProofSchema, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg), Qualification: openBaoFirstRecoveryQualification{ParentSHA256: e.Journal.PlanSHA256, ParentOwnerSHA256: deploymentplan.Digest(e.owner), ParentJournalSHA256: deploymentplan.Digest(e.Journal)}}
	sha := deploymentplan.Digest(proof)
	dir, err := prepareEnvironmentCreateDirectories(store, filepath.Join(openBaoFirstCheckpointProofBase, sha))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proof.json")
	if err := writeEnvironmentPreflightImmutableJSON(path, proof); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint evidence is not private", err)
	}
	active := openBaoFirstCheckpointActive{Schema: openBaoFirstCheckpointProofSchema + "/active", Environment: cfg.Environment, State: "complete", ProofSHA256: sha}
	if err := store.write(filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), mustProviderRecoveryJSON(active), false); err != nil {
		t.Fatal(err)
	}
	got, _, err := readOpenBaoFirstCheckpointArtifact(store, cfg, sha)
	if err != nil || deploymentplan.Digest(got) != sha {
		t.Fatal("original immutable evidence did not round trip", err)
	}
	proof.Stack = "foreign"
	if err := os.WriteFile(path, mustProviderRecoveryJSON(proof), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOpenBaoFirstCheckpointArtifact(store, cfg, sha); err == nil {
		t.Fatal("changed durable evidence accepted")
	}
	before := deploymentplan.Digest(e.Journal)
	b := environmentKubernetesBackend{Store: store}
	if _, _, err := publishOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, b, operator); err == nil {
		t.Fatal("incomplete original prerequisites published evidence")
	}
	var invalid openBaoFirstCheckpointActive
	if createOpenBaoACKCompletionRead(store, filepath.Join(openBaoFirstCheckpointProofBase, "active.json"), &invalid) != nil || invalid.State != "invalid" || invalid.ProofSHA256 != "" {
		t.Fatal("failed requalification retained old evidence")
	}
	if _, _, err := readOpenBaoFirstCheckpointArtifact(store, cfg, sha); err == nil {
		t.Fatal("invalidated evidence was accepted")
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("preflight changed original parent journal")
	}
	if info, err := os.Stat(base); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("checkpoint directory is not private", err)
	}
}

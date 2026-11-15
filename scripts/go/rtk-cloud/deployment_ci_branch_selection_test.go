package main

import (
	"path/filepath"
	"testing"
)

func TestCIBranchSelectionUsesOnlyExplicitEnvironmentPolicy(t *testing.T) {
	policy, err := parseCIBranchSelection(`{"hkt999rtk/rtk_cloud_frontend":"feature/design","hkt999rtk/rtk_video_cloud":"main"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.matches("hkt999rtk/rtk_cloud_frontend", "feature/design") || policy.matches("hkt999rtk/rtk_cloud_frontend", "main") || policy.matches("hkt999rtk/other", "main") {
		t.Fatal("CI source must be explicitly selected by environment")
	}
	empty, err := parseCIBranchSelection("")
	if err != nil || empty.matches("hkt999rtk/rtk_cloud_frontend", "main") {
		t.Fatal("missing policy must not select a default branch", err)
	}
}

func TestCIBranchSelectionRejectsAmbiguousOrInvalidPolicy(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"other/repo":""}`, `{"other/repo":"-main"}`, `{"other/repo":"feature..broken"}`, `{"other/repo":"bad branch"}`, `{"other/repo":"main","other/repo":"other"}`, `{"/repo":"main"}`, `{"a/b/c":"main"}`} {
		if _, err := parseCIBranchSelection(raw); err == nil {
			t.Fatalf("invalid branch policy accepted: %s", raw)
		}
	}
}

func TestCIBranchSelectionComesFromSelectedEnvironment(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	path := filepath.Join(workspace, "cloud_env", "dev", "ci-rollout.env")
	writeTestFile(t, path, "CI_ROLLOUT_BRANCHES={\"hkt999rtk/rtk_cloud_frontend\":\"feature/design\"}\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := readCIBranchSelection(cfg)
	if err != nil || !policy.matches("hkt999rtk/rtk_cloud_frontend", "feature/design") || policy.matches("hkt999rtk/rtk_cloud_frontend", "main") {
		t.Fatal("resolved environment did not retain its explicit branch policy", err)
	}
}

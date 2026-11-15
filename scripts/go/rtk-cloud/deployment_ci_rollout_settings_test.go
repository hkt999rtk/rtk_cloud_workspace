package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCIRolloutReadsOnlyRelevantEnvironmentSettings(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\nUNRELATED_VALUE=ignored\n")
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/deployment.env"), "DEPLOYMENT_ARCHITECTURE=kubernetes\n")
	writeTestFile(t, filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS=300\nDEPLOYMENT_UPGRADE_WINDOW_SECONDS=3600\n")
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/overrides/architecture.env"), "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS=120\n")
	cfg, rollout, total, err := resolveCIRolloutSettings(root, "dev")
	if err != nil || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-dev" || rollout != 120*time.Second || total != time.Hour {
		t.Fatalf("unexpected rollout settings: %#v %s %s %v", cfg, rollout, total, err)
	}
	if _, _, _, err = resolveCIRolloutSettings(root, "prod"); err == nil {
		t.Fatal("non-dev rollout accepted")
	}
}

func TestCIRolloutCommandRejectsUnselectedRepositoryBeforeAccessingCluster(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\n")
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/deployment.env"), "DEPLOYMENT_ARCHITECTURE=kubernetes\n")
	writeTestFile(t, filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS=300\nDEPLOYMENT_UPGRADE_WINDOW_SECONDS=3600\n")
	writeTestFile(t, filepath.Join(root, "cloud_env/dev/ci-rollout.env"), "CI_ROLLOUT_BRANCHES={\"hkt999rtk/rtk_cloud_frontend\":\"main\"}\n")
	if err := run([]string{"ci-rollout", "--environment", "dev", "--workspace", root, "--repository", "hkt999rtk/rtk_other"}); err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatal("unselected repository must be rejected before accessing the cluster", err)
	}
}

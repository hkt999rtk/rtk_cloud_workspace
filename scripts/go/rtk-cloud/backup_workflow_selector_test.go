package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCoverageSelectorOwnsBackupWorkflowDependencies(t *testing.T) {
	selector, err := os.ReadFile(filepath.Join("..", "..", "ci", "select-coverage-jobs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		".github/workflows/postgres-backup-image.yml",
		"cloud_deploy/architectures/kubernetes/postgres-backup/Dockerfile",
		"cloud_deploy/architectures/kubernetes/postgres-backup/runner.sh",
	} {
		t.Run(changed, func(t *testing.T) {
			workspace := newPrePRTestWorkspaceWithScript(t, string(selector))
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = workspace
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
				}
				return strings.TrimSpace(string(out))
			}
			base := git("rev-parse", "HEAD")
			path := filepath.Join(workspace, changed)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed backup dependency\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			git("commit", "-m", "backup dependency")
			cmd := exec.Command("bash", "scripts/ci/select-coverage-jobs.sh", base, "HEAD", "pull_request")
			cmd.Dir = workspace
			cmd.Env = withoutEnvironmentKey(os.Environ(), "GITHUB_OUTPUT")
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("selector: %v: %s", err, raw)
			}
			got, err := parsePrePRSelection(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Policy || !reflect.DeepEqual(got.GoModules, []string{"workspace-tooling"}) || len(got.NodeModules) != 0 || got.AccountManagerPostgres || got.BillingPostgres || got.VideoCloudPostgresEMQX {
				t.Fatalf("backup dependency must select workspace coverage and policy only: %#v", got)
			}
			for _, field := range []string{"run_unit=true", "run_coverage=true"} {
				if !strings.Contains(string(raw), field+"\n") {
					t.Fatalf("backup dependency must schedule workspace coverage: missing %s: %s", field, raw)
				}
			}
		})
	}
}

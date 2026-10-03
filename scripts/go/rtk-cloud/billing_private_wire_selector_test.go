package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBillingPrivateWireSelectorTracksActualLoggerGitlink(t *testing.T) {
	selector, err := os.ReadFile(filepath.Join("..", "..", "ci", "select-coverage-jobs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := newPrePRTestWorkspaceWithScript(t, string(selector))
	leaf := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(leaf, "init")
	git(leaf, "config", "user.email", "private-wire-test@example.invalid")
	git(leaf, "config", "user.name", "Private Wire Test")
	if err := os.WriteFile(filepath.Join(leaf, "fixture.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(leaf, "add", ".")
	git(leaf, "commit", "-m", "before")
	before := git(leaf, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(leaf, "fixture.go"), []byte("package fixture\n// changed protocol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(leaf, "add", ".")
	git(leaf, "commit", "-m", "after")
	after := git(leaf, "rev-parse", "HEAD")
	setGitlink := func(path, revision string) {
		t.Helper()
		git(workspace, "update-index", "--add", "--cacheinfo", "160000,"+revision+","+path)
	}
	setGitlink("repos/rtk_billing", before)
	setGitlink("repos/rtk_cloud_logger", before)
	git(workspace, "commit", "-m", "base reviewed gitlinks")
	base := git(workspace, "rev-parse", "HEAD")
	for _, scenario := range []struct {
		name            string
		billing, logger string
		changed         string
		modules         []string
		billingPR       bool
	}{
		{name: "logger-only", billing: before, logger: after, changed: "repos/rtk_cloud_logger", modules: []string{"cloud-logger", "billing-service"}},
		{name: "billing-only", billing: after, logger: before, changed: "repos/rtk_billing", modules: []string{"billing-service"}, billingPR: true},
		{name: "both-no-duplicate", billing: after, logger: after, changed: "repos/rtk_billing\nrepos/rtk_cloud_logger", modules: []string{"billing-service", "cloud-logger"}, billingPR: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			setGitlink("repos/rtk_billing", scenario.billing)
			setGitlink("repos/rtk_cloud_logger", scenario.logger)
			git(workspace, "commit", "-m", scenario.name)
			if actual := git(workspace, "diff", "--name-only", base, "HEAD"); actual != scenario.changed {
				t.Fatalf("fixture changed paths = %q, want only %q", actual, scenario.changed)
			}
			command := exec.Command("bash", "scripts/ci/select-coverage-jobs.sh", base, "HEAD", "pull_request")
			command.Dir = workspace
			output := filepath.Join(t.TempDir(), "github-output")
			command.Env = append(withoutEnvironmentKey(os.Environ(), "GITHUB_OUTPUT"), "GITHUB_OUTPUT="+output)
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("actual selector: %v: %s", err, raw)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := parsePrePRSelection(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !selection.Policy || !reflect.DeepEqual(selection.GoModules, scenario.modules) || len(selection.NodeModules) != 0 || selection.AccountManagerPostgres || selection.VideoCloudPostgresEMQX || selection.BillingPostgres != scenario.billingPR {
				t.Fatalf("actual gitlink selection = %#v, want only modules %v and Billing PR=%v", selection, scenario.modules, scenario.billingPR)
			}
			for _, field := range []string{"run_unit=true", "run_coverage=true"} {
				if !strings.Contains(string(raw), field+"\n") {
					t.Fatalf("wire dependency must schedule unit acceptance: missing %s: %s", field, raw)
				}
			}
		})
	}
}

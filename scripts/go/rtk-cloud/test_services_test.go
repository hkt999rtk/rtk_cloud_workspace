package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunTestServicesValidatesFiltersBeforeExecutingSuites(t *testing.T) {
	if err := runTestServices([]string{"--repo", "rtk_cloud_admin", "--changed-since", "HEAD"}); err == nil || !strings.Contains(err.Error(), "either --repo or --changed-since") {
		t.Fatalf("conflicting filters error = %v", err)
	}
	if err := runTestServices([]string{"--repo", "not-managed"}); err == nil || !strings.Contains(err.Error(), "unknown managed service") {
		t.Fatalf("unknown repository error = %v", err)
	}
	if err := runTestServices([]string{"--qualification-only"}); err == nil || !strings.Contains(err.Error(), "requires --qualification-output-dir") {
		t.Fatalf("qualification-only error = %v", err)
	}
	if err := runTestServices([]string{
		"--qualification-only",
		"--qualification-output-dir", t.TempDir(),
		"--qualification-cases", "unknown-z,unknown-a",
	}); err == nil || err.Error() != "unknown qualification cases: unknown-a, unknown-z" {
		t.Fatalf("unknown qualification case error = %v", err)
	}
}

func TestRunTestServicesEmptyDiffDoesNotExecuteSuites(t *testing.T) {
	if err := runTestServices([]string{"--changed-since", "HEAD", "--head-ref", "HEAD"}); err != nil {
		t.Fatal(err)
	}
}

func TestSelectQualificationSpecs(t *testing.T) {
	available := []authorizationQualificationSpec{
		{TestID: "INT-ONE"},
		{TestID: "INT-TWO"},
	}

	all, err := selectQualificationSpecs("  ", available)
	if err != nil || !reflect.DeepEqual(all, available) {
		t.Fatalf("empty selection = %#v, %v", all, err)
	}

	selected, err := selectQualificationSpecs(" INT-TWO, INT-TWO, ,INT-ONE ", available)
	if err != nil {
		t.Fatal(err)
	}
	if want := []authorizationQualificationSpec{available[0], available[1]}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected specs = %#v, want %#v", selected, want)
	}

	selected, err = selectQualificationSpecs("missing-z,INT-ONE,missing-a", available)
	if selected != nil || err == nil || err.Error() != "unknown qualification cases: missing-a, missing-z" {
		t.Fatalf("unknown selection = %#v, %v", selected, err)
	}
}

func TestQualificationNPMInstallDirsFollowSelectedTargets(t *testing.T) {
	specs := []authorizationQualificationSpec{
		{
			TestID: "INT-JS-TWO", Repository: "rtk_cloud_client",
			Targets: []authorizationQualificationTarget{
				{WorkingDir: "packages/javascript", SetupCommands: [][]string{{"npm", "run", "build"}}, Command: []string{"node", "--test"}, Label: "javascript"},
			},
		},
		{
			TestID: "INT-JS-ONE", Repository: "rtk_cloud_client",
			Targets: []authorizationQualificationTarget{
				{WorkingDir: "packages/javascript", Command: []string{"npm", "test"}, Label: "javascript duplicate"},
			},
		},
		{TestID: "INT-GO", Repository: "rtk_video_cloud", Package: "./internal/apiapp", GoTest: "TestSomething"},
	}

	got, err := qualificationNPMInstallDirs(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"repos/rtk_cloud_client/packages/javascript"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("qualification npm install dirs = %v, want %v", got, want)
	}
}

func TestQualificationNPMInstallDirsRejectInvalidTarget(t *testing.T) {
	_, err := qualificationNPMInstallDirs([]authorizationQualificationSpec{{
		TestID: "INT-INVALID", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{{WorkingDir: "packages/javascript"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "INT-INVALID: qualification target requires") {
		t.Fatalf("invalid qualification target error = %v", err)
	}
}

func TestInstallQualificationNPMDependencies(t *testing.T) {
	original := qualificationNPMCI
	t.Cleanup(func() { qualificationNPMCI = original })

	workspace := t.TempDir()
	called := 0
	qualificationNPMCI = func(dir string, env map[string]string, name string, args ...string) error {
		called++
		if want := filepath.Join(workspace, "repos", "rtk_cloud_client", "packages", "javascript"); dir != want {
			t.Fatalf("npm directory = %q, want %q", dir, want)
		}
		if env["NPM_CONFIG_CACHE"] != filepath.Join(workspace, ".artifacts", "npm-cache") {
			t.Fatalf("npm cache = %q", env["NPM_CONFIG_CACHE"])
		}
		if name != "npm" || !reflect.DeepEqual(args, []string{"ci"}) {
			t.Fatalf("command = %s %v", name, args)
		}
		return nil
	}
	spec := authorizationQualificationSpec{
		TestID: "INT-JS", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{{
			WorkingDir: "packages/javascript", SetupCommands: [][]string{{"npm", "run", "build"}},
			Command: []string{"node", "--test"}, Label: "javascript",
		}},
	}
	if err := installQualificationNPMDependencies(workspace, []authorizationQualificationSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("npm ci calls = %d, want 1", called)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".artifacts", "npm-cache")); err != nil {
		t.Fatalf("npm cache was not created: %v", err)
	}

	called = 0
	if err := installQualificationNPMDependencies(workspace, []authorizationQualificationSpec{{
		TestID: "INT-GO", Repository: "rtk_video_cloud", Package: "./internal/apiapp", GoTest: "TestSomething",
	}}); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("npm ci called for Go-only qualification")
	}
}

func TestInstallQualificationNPMDependenciesReportsFailures(t *testing.T) {
	original := qualificationNPMCI
	t.Cleanup(func() { qualificationNPMCI = original })
	spec := authorizationQualificationSpec{
		TestID: "INT-JS", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{{
			WorkingDir: "packages/javascript", Command: []string{"npm", "test"}, Label: "javascript",
		}},
	}

	workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installQualificationNPMDependencies(workspaceFile, []authorizationQualificationSpec{spec}); err == nil || !strings.Contains(err.Error(), "create isolated npm cache") {
		t.Fatalf("cache creation error = %v", err)
	}

	wantErr := errors.New("npm ci failed")
	qualificationNPMCI = func(string, map[string]string, string, ...string) error { return wantErr }
	if err := installQualificationNPMDependencies(t.TempDir(), []authorizationQualificationSpec{spec}); !errors.Is(err, wantErr) {
		t.Fatalf("npm ci error = %v, want %v", err, wantErr)
	}
}

func TestSelectChangedServiceReposSelectsOnlyChangedGitlinks(t *testing.T) {
	got := selectChangedServiceRepos([]string{
		"docs/testing.md",
		"repos/rtk_cloud_admin",
		"repos/rtk_video_cloud",
	})
	want := []string{"rtk_cloud_admin", "rtk_video_cloud"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected repos = %v, want %v", got, want)
	}
}

func TestSelectChangedServiceReposSharedTestChangesSelectAll(t *testing.T) {
	for _, changed := range []string{
		".github/workflows/workspace-test-baseline.yml",
		".github/workflows/feature-qualification.yml",
		"go.work",
		"scripts/go/rtk-cloud/main.go",
		"tests/catalog.yaml",
		"repos/rtk_cloud_contracts_doc",
	} {
		t.Run(changed, func(t *testing.T) {
			got := selectChangedServiceRepos([]string{changed})
			if !reflect.DeepEqual(got, managedServiceRepos) {
				t.Fatalf("selected repos = %v, want all %v", got, managedServiceRepos)
			}
		})
	}
}

func TestSelectChangedServiceReposIgnoresUnrelatedWorkspaceFiles(t *testing.T) {
	got := selectChangedServiceRepos([]string{
		"docs/business-model.md",
		"loadtests/home-100k/README.md",
	})
	if len(got) != 0 {
		t.Fatalf("selected repos = %v, want none", got)
	}
}

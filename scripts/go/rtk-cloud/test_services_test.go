package main

import (
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
}

func TestRunTestServicesEmptyDiffDoesNotExecuteSuites(t *testing.T) {
	if err := runTestServices([]string{"--changed-since", "HEAD", "--head-ref", "HEAD"}); err != nil {
		t.Fatal(err)
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

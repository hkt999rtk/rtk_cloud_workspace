package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPrePRCoalescesOnlyCompleteWorkspaceCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		module coverageModule
		want   bool
	}{
		{"complete", coverageModule{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: []string{"./..."}}, true},
		{"narrowed", coverageModule{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: []string{"./rtk-cloud"}}, false},
		{"extra-selection", coverageModule{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: []string{"./...", "./rtk-cloud"}}, false},
		{"different-path", coverageModule{Name: "workspace-tooling", Kind: "go", Path: "other", Packages: []string{"./..."}}, false},
		{"different-kind", coverageModule{Name: "workspace-tooling", Kind: "node", Path: "scripts/go", Packages: []string{"./..."}}, false},
		{"different-module", coverageModule{Name: "other", Kind: "go", Path: "scripts/go", Packages: []string{"./..."}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := prePRWorkspaceCoverageIncludesBaseline(coverageConfig{Modules: []coverageModule{tc.module}}); got != tc.want {
				t.Fatalf("coalescing = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPrePRCoveredMatrixPreservesFixtureOrderAndEveryLaterGate(t *testing.T) {
	for _, mode := range []string{"complete", "coverage-failure", "matrix-tail-failure", "narrowed", "configuration-failure"} {
		t.Run(mode, func(t *testing.T) {
			workspace := newPrePRTestWorkspaceWithScript(t, `#!/usr/bin/env bash
cat <<'SELECTION'
policy=true
go_modules=["video-cloud","workspace-tooling","cloud-admin-backend"]
node_modules=["cloud-admin-web"]
account_manager_postgres=false
billing_postgres=false
video_cloud_postgres_emqx=true
SELECTION
`)
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			t.Setenv("VIDEO_CLOUD_TEST_DSN", "")
			oldCmd, oldMatrix, oldCombined := prePRRunCmd, prePRRunMatrix, prePRRunMatrixWithBaseline
			oldCoverage, oldInventory, oldUI := prePRRunCoverage, prePRRunInventory, prePRRunUI
			oldConfig, oldFixture := prePRLoadCoverageConfig, prePRStartVideoCloudPRFixtures
			t.Cleanup(func() {
				prePRRunCmd, prePRRunMatrix, prePRRunMatrixWithBaseline = oldCmd, oldMatrix, oldCombined
				prePRRunCoverage, prePRRunInventory, prePRRunUI = oldCoverage, oldInventory, oldUI
				prePRLoadCoverageConfig, prePRStartVideoCloudPRFixtures = oldConfig, oldFixture
			})
			var calls []string
			failed := errors.New("selected covered gate failed")
			prePRLoadCoverageConfig = func(string) (coverageConfig, error) {
				if mode == "configuration-failure" {
					return coverageConfig{}, failed
				}
				packages := []string{"./..."}
				if mode == "narrowed" {
					packages = []string{"./rtk-cloud"}
				}
				return coverageConfig{Modules: []coverageModule{{Name: "workspace-tooling", Kind: "go", Path: "scripts/go", Packages: packages}}}, nil
			}
			prePRRunCmd = func(_ string, name string, args ...string) error {
				if name != "git" || !reflect.DeepEqual(args, []string{"diff", "--check", "HEAD...HEAD"}) {
					t.Fatal("early diff check changed", name, args)
				}
				calls = append(calls, "diff")
				return nil
			}
			prePRRunMatrix = func([]string) error {
				calls = append(calls, "standalone-matrix")
				if os.Getenv("VIDEO_CLOUD_TEST_DSN") != "" {
					t.Fatal("noncoalesced baseline moved after fixtures")
				}
				return nil
			}
			prePRRunMatrixWithBaseline = func(_ []string, baseline func(string) error) error {
				calls = append(calls, "matrix-status")
				if baseline == nil {
					t.Fatal("missing complete baseline invocation")
				}
				if err := baseline(workspace); err != nil {
					return err
				}
				calls = append(calls, "matrix-policy-tail")
				if mode == "matrix-tail-failure" {
					return failed
				}
				return nil
			}
			prePRStartVideoCloudPRFixtures = func(string) (func(), error) {
				calls = append(calls, "fixtures-start")
				if err := os.Setenv("VIDEO_CLOUD_TEST_DSN", "isolated-fixture-selected"); err != nil {
					t.Fatal(err)
				}
				return func() {
					calls = append(calls, "fixtures-stop")
					if err := os.Unsetenv("VIDEO_CLOUD_TEST_DSN"); err != nil {
						t.Error(err)
					}
				}, nil
			}
			prePRRunCoverage = func(args []string) error {
				at := slices.Index(args, "--module")
				if at < 0 || at+1 == len(args) {
					t.Fatal("missing coverage selection", args)
				}
				module := args[at+1]
				calls = append(calls, "coverage:"+module)
				if module == "workspace-tooling,cloud-admin-backend" {
					if os.Getenv("VIDEO_CLOUD_TEST_DSN") != "isolated-fixture-selected" {
						t.Fatal("native bridge fixtures missing from covered baseline")
					}
					want := []string{"--profile", "unit", "--module", module, "--base-ref", "HEAD", "--head-ref", "HEAD", "--run-id", "single-run-go"}
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("coverage inputs = %q, want %q", args, want)
					}
					if mode == "coverage-failure" {
						return failed
					}
				}
				return nil
			}
			prePRRunInventory = func(args []string) error {
				calls = append(calls, "inventory")
				want := []string{"check", "--from-run", filepath.Join(workspace, ".artifacts/test-runs/single-run-node/coverage")}
				if !reflect.DeepEqual(args, want) {
					t.Fatal("JavaScript inventory check changed", args)
				}
				return nil
			}
			prePRRunUI = func(args []string) error {
				calls = append(calls, "ui")
				if !reflect.DeepEqual(args, []string{"--desktop", "--mobile", "--full", "--run-id", "single-run-ui", "--install"}) {
					t.Fatal("full UI selection changed", args)
				}
				return nil
			}
			err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--run-id", "single-run"})
			var want []string
			if mode != "configuration-failure" {
				want = []string{"diff"}
				if mode == "narrowed" {
					want = append(want, "standalone-matrix")
				}
				want = append(want, "fixtures-start", "coverage:video-cloud")
				if mode != "narrowed" {
					want = append(want, "matrix-status")
				}
				want = append(want, "coverage:workspace-tooling,cloud-admin-backend")
				if mode != "narrowed" && mode != "coverage-failure" {
					want = append(want, "matrix-policy-tail")
				}
				if mode == "complete" || mode == "narrowed" {
					want = append(want, "coverage:cloud-admin-web", "inventory", "ui")
				}
				want = append(want, "fixtures-stop")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %q, want %q", calls, want)
			}
			if strings.Contains(mode, "failure") {
				if !errors.Is(err, failed) {
					t.Fatalf("error = %v, want selected failure", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceMatrixRequiresAndPropagatesItsBaseline(t *testing.T) {
	if err := runTestMatrixWithBaseline(nil, nil); err == nil {
		t.Fatal("missing baseline accepted")
	}
	workspace := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	failed := errors.New("baseline test failed")
	calls := 0
	err := runTestMatrixWithBaseline(nil, func(path string) error {
		calls++
		if path != workspace {
			t.Fatalf("baseline workspace = %q, want %q", path, workspace)
		}
		return failed
	})
	if !errors.Is(err, failed) || calls != 1 {
		t.Fatalf("baseline calls=%d error=%v", calls, err)
	}
}

func TestCoveredWorkspaceInheritsBaselineGoWorkAndServicesStayIsolated(t *testing.T) {
	workspace := t.TempDir()
	bin := filepath.Join(workspace, "bin")
	moduleDir := filepath.Join(workspace, "module")
	for _, dir := range []string{bin, moduleDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s' \"$GOWORK\" > \"$COVERAGE_GOWORK_CAPTURE\"\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOWORK", filepath.Join(workspace, "selected.go.work"))
	for _, name := range []string{"workspace-tooling", "video-cloud"} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			capture := filepath.Join(out, "gowork")
			t.Setenv("COVERAGE_GOWORK_CAPTURE", capture)
			err := runGoCoverageModuleProfile(workspace, out, filepath.Join(out, "coverage.log"), coverageConfig{}, coverageModule{Name: name, Path: "module", Packages: []string{"./..."}}, "", "HEAD", "unit", &coverageCaseResult{})
			if err == nil || !strings.Contains(err.Error(), "before producing a profile") {
				t.Fatalf("unexpected command result: %v", err)
			}
			want := "off"
			if name == "workspace-tooling" {
				want = os.Getenv("GOWORK")
			}
			if got := readTestFile(t, capture); got != want {
				t.Fatalf("GOWORK=%q, want %q", got, want)
			}
		})
	}
}

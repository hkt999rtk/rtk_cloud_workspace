package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrePRReadinessCollectsFailuresBeforeAnySuite(t *testing.T) {
	oldCmd, oldCfg := prePRRunFixtureCommand, prePRLoadCoverageConfig
	t.Cleanup(func() { prePRRunFixtureCommand, prePRLoadCoverageConfig = oldCmd, oldCfg })
	prePRLoadCoverageConfig = func(string) (coverageConfig, error) {
		return coverageConfig{Modules: []coverageModule{{Name: "sample", Path: "sample", Packages: []string{"./..."}}}}, nil
	}
	var commands []string
	prePRRunFixtureCommand = func(_ string, env []string, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		switch name {
		case "go":
			if !strings.Contains(strings.Join(env, "\n"), "GOWORK=off") {
				t.Fatal("dependency probe must use coverage module mode")
			}
			return nil, errors.New("private dependency URL must not escape")
		case "git":
			return []byte("main.go\x00testdata/invalid.go\x00vendor/generated.go\x00"), nil
		case "gofmt":
			return []byte("main.go\n"), nil
		case "docker", "node", "python3":
			return nil, errors.New("unavailable")
		}
		return nil, nil
	}
	err := checkPrePRReadiness(t.TempDir(), prePRSelection{GoModules: []string{"sample", "missing"}, NodeModules: []string{"web"}, AccountManagerPostgres: true})
	for _, want := range []string{"Go dependencies", "formatting", "not configured", "Node.js", "Python", "Docker"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "private dependency") || strings.Contains(strings.Join(commands, "\n"), "gofmt -l main.go testdata") {
		t.Fatal("unsafe output or malformed fixtures included")
	}
}

func TestPrePRReadinessPullsOnlyMissingOwnedFixtureImages(t *testing.T) {
	oldCmd, oldCfg := prePRRunFixtureCommand, prePRLoadCoverageConfig
	t.Cleanup(func() { prePRRunFixtureCommand, prePRLoadCoverageConfig = oldCmd, oldCfg })
	prePRLoadCoverageConfig = func(string) (coverageConfig, error) {
		return coverageConfig{Modules: []coverageModule{{Name: "video-cloud"}}}, nil
	}
	var pulls []string
	prePRRunFixtureCommand = func(_ string, _ []string, name string, args ...string) ([]byte, error) {
		if name == "docker" && strings.Join(args, " ") == "image inspect emqx/emqx:latest" {
			return nil, errors.New("missing")
		}
		if name == "docker" && args[0] == "pull" {
			pulls = append(pulls, args[1])
		}
		return nil, nil
	}
	if err := checkPrePRReadiness(t.TempDir(), prePRSelection{VideoCloudPostgresEMQX: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pulls, ",") != "emqx/emqx:latest" {
		t.Fatalf("pulls=%v", pulls)
	}
}

func TestAccountManagerFixtureOwnsDatabaseAndRestoresEnvironment(t *testing.T) {
	old := prePRRunFixtureCommand
	t.Cleanup(func() { prePRRunFixtureCommand = old })
	t.Setenv("TEST_DATABASE_URL", "caller-database")
	t.Setenv("DATABASE_URL", "caller-runtime-database")
	t.Setenv("REPORT_FIXTURE_ID", "caller-fixture")
	var calls []string
	prePRRunFixtureCommand = func(_ string, _ []string, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch args[0] {
		case "port":
			return []byte("127.0.0.1:15432\n"), nil
		case "inspect":
			return []byte("sha256:owned-image\n"), nil
		}
		return nil, nil
	}
	cleanup, err := startAccountManagerPRFixture(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(os.Getenv("TEST_DATABASE_URL"), "127.0.0.1:15432/rtk_account_manager") || !strings.Contains(os.Getenv("REPORT_FIXTURE_ID"), "sha256:owned-image") {
		t.Fatal("owned fixture was not installed")
	}
	cleanup()
	if os.Getenv("TEST_DATABASE_URL") != "caller-database" || os.Getenv("DATABASE_URL") != "caller-runtime-database" || os.Getenv("REPORT_FIXTURE_ID") != "caller-fixture" {
		t.Fatal("caller environment was not restored")
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"--tmpfs", "--publish 127.0.0.1::5432", "pg_isready", "docker rm --force"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestAccountManagerFixtureCleansUpEveryStartupFailure(t *testing.T) {
	for _, failure := range []string{"run", "port", "inspect", "endpoint"} {
		t.Run(failure, func(t *testing.T) {
			old := prePRRunFixtureCommand
			t.Cleanup(func() { prePRRunFixtureCommand = old })
			removed := false
			prePRRunFixtureCommand = func(_ string, _ []string, _ string, args ...string) ([]byte, error) {
				if args[0] == "rm" {
					removed = true
					return nil, nil
				}
				if args[0] == failure {
					return nil, errors.New("fixture failure")
				}
				if args[0] == "port" {
					if failure == "endpoint" {
						return []byte("0.0.0.0:5432"), nil
					}
					return []byte("127.0.0.1:15432"), nil
				}
				return nil, nil
			}
			if _, err := startAccountManagerPRFixture(t.TempDir()); err == nil || !removed {
				t.Fatalf("failure=%v removed=%v", err, removed)
			}
		})
	}
}

func TestPrePRStopsBeforeMatrixOnReadinessFailure(t *testing.T) {
	w := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", w)
	prePRCheckReadiness = func(string, prePRSelection) error { return errors.New("prerequisite missing") }
	old := prePRRunMatrix
	t.Cleanup(func() { prePRRunMatrix = old })
	prePRRunMatrix = func([]string) error { t.Fatal("expensive gate started"); return nil }
	if err := runPrePR([]string{"--base", "HEAD"}); err == nil || !strings.Contains(err.Error(), "prerequisite missing") {
		t.Fatalf("error=%v", err)
	}
}

func TestPrePRDelegatesBaselineAndReusesAccountReportWithoutDatabase(t *testing.T) {
	w := newPrePRTestWorkspaceWithScript(t, "#!/bin/sh\nprintf '%s\\n' 'policy=true' 'go_modules=[\"workspace-tooling\",\"account-manager\"]' 'node_modules=[]' 'account_manager_postgres=true' 'billing_postgres=false' 'video_cloud_postgres_emqx=false'\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", w)
	oldMatrix, oldCoverage, oldFixture := prePRRunMatrix, prePRRunCoverage, prePRStartAccountManagerFixture
	t.Cleanup(func() {
		prePRRunMatrix, prePRRunCoverage, prePRStartAccountManagerFixture = oldMatrix, oldCoverage, oldFixture
	})
	prePRRunMatrix = func(args []string) error {
		if strings.Join(args, " ") != "--policy-only" {
			t.Fatal(args)
		}
		return nil
	}
	prePRStartAccountManagerFixture = func(string) (func(), error) { t.Fatal("reuse started a DB"); return nil, nil }
	var calls []string
	prePRRunCoverage = func(args []string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	if err := runPrePR([]string{"--base", "HEAD", "--account-manager-report-evidence", "saved-report"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "--profile pr --module account-manager") || !strings.Contains(calls[0], "--account-manager-report-evidence ") || !strings.HasSuffix(calls[0], "/saved-report") || strings.Contains(calls[1], "account-manager,") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestPrePROutputValidationRejectsCollisionAndTraversal(t *testing.T) {
	w := t.TempDir()
	for _, id := range []string{"", ".", "..", "../escape", "nested/name"} {
		if err := checkPrePROutputs(w, id, prePRSelection{}, false); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	selection := prePRSelection{GoModules: []string{"workspace-tooling"}, NodeModules: []string{"cloud-admin-web"}, AccountManagerPostgres: true, VideoCloudPostgresEMQX: true}
	if err := checkPrePROutputs(w, "new", selection, true); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"go", "node", "account-manager-pr", "video-cloud-pr", "ui"} {
		path := filepath.Join(w, ".artifacts", "test-runs", "new-"+suffix)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := checkPrePROutputs(w, "new", selection, true); err == nil {
			t.Fatalf("accepted %s collision", suffix)
		}
		os.RemoveAll(path)
	}
}

func TestPrePRAccountManagerFailureCleansOwnedFixture(t *testing.T) {
	w := newPrePRTestWorkspaceWithScript(t, "#!/bin/sh\nprintf '%s\\n' 'policy=false' 'go_modules=[\"account-manager\"]' 'node_modules=[]' 'account_manager_postgres=true' 'billing_postgres=false' 'video_cloud_postgres_emqx=false'\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", w)
	oldCoverage, oldFixture := prePRRunCoverage, prePRStartAccountManagerFixture
	t.Cleanup(func() { prePRRunCoverage, prePRStartAccountManagerFixture = oldCoverage, oldFixture })
	cleaned := false
	prePRStartAccountManagerFixture = func(string) (func(), error) { return func() { cleaned = true }, nil }
	prePRRunCoverage = func(args []string) error {
		if !strings.Contains(strings.Join(args, " "), "--profile pr --module account-manager") {
			t.Fatal(args)
		}
		return errors.New("report drift")
	}
	if err := runPrePR([]string{"--base", "HEAD"}); err == nil || !strings.Contains(err.Error(), "report drift") || !cleaned {
		t.Fatalf("error=%v cleaned=%v", err, cleaned)
	}
}

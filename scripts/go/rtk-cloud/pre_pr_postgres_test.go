package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPostgresPRFixtureNeverUsesAmbientDatabaseAndRestoresAfterCleanup(t *testing.T) {
	for _, module := range []string{"account-manager", "billing-service"} {
		for _, mode := range []string{"valid", "start-failed", "port-failed", "public-port"} {
			t.Run(module+"/"+mode, func(t *testing.T) {
				original := prePRRunFixtureCommand
				t.Cleanup(func() { prePRRunFixtureCommand = original })
				t.Setenv("TEST_DATABASE_URL", "original-database-must-not-be-used")
				calls := []string{}
				prePRRunFixtureCommand = func(_ string, _ []string, name string, args ...string) ([]byte, error) {
					calls = append(calls, name+" "+strings.Join(args, " "))
					if args[0] == "run" && mode == "start-failed" {
						return nil, errors.New("failed")
					}
					if args[0] == "port" {
						if mode == "port-failed" {
							return nil, errors.New("failed")
						}
						if mode == "public-port" {
							return []byte("0.0.0.0:15432\n"), nil
						}
						return []byte("127.0.0.1:15432\n"), nil
					}
					return nil, nil
				}
				cleanup, err := startPostgresPRFixture(module)
				if mode == "valid" {
					if err != nil || !strings.Contains(os.Getenv("TEST_DATABASE_URL"), "@127.0.0.1:15432/pr_test") {
						t.Fatal("isolated database not selected", err)
					}
					cleanup()
				} else if err == nil {
					t.Fatal("unsafe fixture accepted")
				}
				if os.Getenv("TEST_DATABASE_URL") != "original-database-must-not-be-used" {
					t.Fatal("original environment not restored")
				}
				joined := strings.Join(calls, "\n")
				if !strings.Contains(joined, "--tmpfs /var/lib/postgresql/data:rw,size=2g") || !strings.Contains(joined, "--publish 127.0.0.1::5432") || !strings.Contains(joined, "docker rm --force rtk-pre-pr-pg-"+module+"-") {
					t.Fatal("fixture persistence, port or cleanup scope incorrect", joined)
				}
			})
		}
	}
}

func TestPrePRUsesPostgresPRProfilesForSelectedDatabaseModules(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "coverage-failed"}[fail], func(t *testing.T) {
			workspace := newPrePRTestWorkspaceWithScript(t, "#!/bin/sh\nprintf '%s\\n' 'policy=false' 'go_modules=[\"account-manager\",\"billing-service\"]' 'node_modules=[]' 'account_manager_postgres=true' 'billing_postgres=true' 'video_cloud_postgres_emqx=false'\n")
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			originalCmd, originalCoverage, originalStart := prePRRunCmd, prePRRunCoverage, prePRStartPostgresPRFixture
			t.Cleanup(func() {
				prePRRunCmd, prePRRunCoverage, prePRStartPostgresPRFixture = originalCmd, originalCoverage, originalStart
			})
			prePRRunCmd = func(string, string, ...string) error { return nil }
			started, cleaned := 0, 0
			prePRStartPostgresPRFixture = func(string) (func(), error) { started++; return func() { cleaned++ }, nil }
			prePRRunCoverage = func(args []string) error {
				if !strings.Contains(strings.Join(args, " "), "--profile pr --module") {
					t.Fatal("integration modules downgraded to unit")
				}
				if fail {
					return errors.New("coverage failed")
				}
				return nil
			}
			err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--run-id", "database-pr"})
			if (err != nil) != fail || started != cleaned || !fail && started != 2 || fail && started != 1 {
				t.Fatal("fixture lifecycle or failure result incorrect", err, started, cleaned)
			}
		})
	}
}

func TestNativePostgresPRFixtureRunsIntegrationCoverage(t *testing.T) {
	module := os.Getenv("RTK_NATIVE_PREPR_POSTGRES_MODULE")
	if module == "" {
		t.Skip("select account-manager or billing-service for isolated native PR coverage")
	}
	cleanup, err := startPostgresPRFixture(module)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := runTestCoverage([]string{"--profile", "pr", "--module", module, "--run-id", "native-database-pr-" + module + "-20261007"}); err != nil {
		t.Fatal("native governed PostgreSQL PR coverage failed", err)
	}
}

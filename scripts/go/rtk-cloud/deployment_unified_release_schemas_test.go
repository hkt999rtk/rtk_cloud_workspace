package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func migrationSourceGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Preflight Fixture", "-c", "user.email=fixture@example.invalid", "-C", directory}, args...)
	command := exec.Command("git", argv...)
	for _, setting := range os.Environ() {
		if !strings.HasPrefix(setting, "GIT_") {
			command.Env = append(command.Env, setting)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated migration source fixture failed: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestCompleteSchemaChecksUsePublishedCommitInsteadOfWorkingTree(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "missing-applied-version", "missing-commit", "invalid-revision", "empty-inventory"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				workspace := t.TempDir()
				repository := filepath.Join(workspace, "repos", "rtk_billing")
				migrations := filepath.Join(repository, "migrations")
				writeTestFile(t, filepath.Join(migrations, "README.md"), "fixture")
				if mode != "empty-inventory" {
					writeTestFile(t, filepath.Join(migrations, "001_original.sql"), "-- original")
					writeTestFile(t, filepath.Join(migrations, "002_required.sql"), "-- required")
				}
				writeTestFile(t, filepath.Join(migrations, "directory.sql", "README.md"), "not a migration")
				migrationSourceGit(t, repository, "init", "-q")
				migrationSourceGit(t, repository, "add", ".")
				migrationSourceGit(t, repository, "commit", "-qm", "published migration inventory")
				published := migrationSourceGit(t, repository, "rev-parse", "HEAD")
				// Later commits and local additions/deletions cannot rewrite the
				// migration requirements of the already published candidate.
				writeTestFile(t, filepath.Join(migrations, "003_other_commit.sql"), "-- later")
				migrationSourceGit(t, repository, "add", ".")
				migrationSourceGit(t, repository, "commit", "-qm", "another source revision")
				writeTestFile(t, filepath.Join(migrations, "999_untracked.sql"), "-- unpublished")
				if mode != "empty-inventory" {
					if err := os.Remove(filepath.Join(migrations, "002_required.sql")); err != nil {
						t.Fatal(err)
					}
					writeTestFile(t, filepath.Join(migrations, "001_original.sql"), "-- local edit")
				}
				queryPath := filepath.Join(t.TempDir(), "query")
				t.Setenv("RTK_PINNED_MIGRATION_QUERY", queryPath)
				response := `printf '[]'`
				if mode == "missing-applied-version" {
					response = `printf '["002_required.sql"]'`
				}
				deploymentRuntimeTestKubectl(t, `printf '%s' "$*" > "$RTK_PINNED_MIGRATION_QUERY"`+"\n"+response)
				store := makeIsolatedTestSecretStore(t, environment)
				if mode == "missing-commit" {
					published = strings.Repeat("f", 40)
				} else if mode == "invalid-revision" {
					published = "main"
				}
				release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_BILLING_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:" + strings.Repeat("a", 64), SourceRevision: published}}}
				err := verifyEnvironmentReleaseSchemas(context.Background(), deploymentConfig{Workspace: workspace}, store, release)
				if (err == nil) != (mode == "complete") {
					t.Fatal("complete schema qualification used an unpublished or unavailable inventory", err)
				}
				query, readErr := os.ReadFile(queryPath)
				if mode == "complete" || mode == "missing-applied-version" {
					if readErr != nil || !strings.Contains(string(query), "001_original.sql") || !strings.Contains(string(query), "002_required.sql") || strings.Contains(string(query), "003_other_commit.sql") || strings.Contains(string(query), "999_untracked.sql") || strings.Contains(string(query), "directory.sql") || !strings.Contains(string(query), "TRANSACTION READ ONLY") {
						t.Fatal("schema SQL does not match the exact published commit's inventory")
					}
				} else if !os.IsNotExist(readErr) {
					t.Fatal("unavailable committed inventory fell back to local files or queried the database")
				}
			})
		}
	}
}

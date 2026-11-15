package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIRolloutGitHubEnvironmentCreationAndSecretDestination(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "gh")
	log := filepath.Join(dir, "calls")
	fake := `#!/bin/sh
case "$*" in
 "api repos/hkt999rtk/rtk_cloud_frontend/environments/dev") echo 'HTTP 404' >&2; exit 1 ;;
 "api --method PUT repos/hkt999rtk/rtk_cloud_frontend/environments/dev --input -") cat >/dev/null; echo created >> "$CI_GH_TEST_CALLS" ;;
 "secret set RTK_DEV_ROLLOUT_TARGETS --repo hkt999rtk/rtk_cloud_frontend --env dev") cat >/dev/null; echo stored >> "$CI_GH_TEST_CALLS" ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(tool, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CI_GH_TEST_CALLS", log)
	repo := "hkt999rtk/rtk_cloud_frontend"
	if err := ciRolloutGitHubEnvironment(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if err := ciRolloutGitHubSecret(context.Background(), repo, "RTK_DEV_ROLLOUT_TARGETS", []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := ciRolloutGitHubSecret(context.Background(), repo, "UNRELATED_SECRET", []byte("fixture")); err == nil {
		t.Fatal("unrelated secret accepted")
	}
	raw, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(raw)) != "created\nstored" {
		t.Fatal("unexpected GitHub writes", err)
	}
}

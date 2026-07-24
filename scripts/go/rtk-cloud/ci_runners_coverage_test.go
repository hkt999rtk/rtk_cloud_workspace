package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/runner"
)

func TestCIRunnerProvisionAndArchiveWithIsolatedCLIs(t *testing.T) {
	binDir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "runner.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 AAAATEST coverage@example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
case "$*" in
  *"/linode/instances"*) printf '%s\n' '{"id":101,"ipv4":["203.0.113.10"]}' ;;
  *"/networking/firewalls/"*"/devices"*) printf '%s\n' '{}' ;;
  *"/networking/firewalls"*) printf '%s\n' '{"id":202}' ;;
  *) printf '%s\n' '{}' ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "gh"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("CI_RUNNER_PUBLIC_KEY_PATH", keyPath)
	t.Setenv("CI_RUNNER_ALLOWED_SSH_CIDRS", "198.51.100.10/32,198.51.100.11/32")
	t.Setenv("CI_RUNNER_REGION", "us-test")
	t.Setenv("CI_RUNNER_IMAGE", "linode/test")
	if err := provisionCIRunnerHosts(); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(t.TempDir(), "archive")
	if err := archiveCIRunnerArtifacts([]string{
		"--repo", "hkt999rtk/test", "--run-id", "12345", "--out-dir", outDir,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(outDir, "archive-metadata.json"))
	if err != nil || !strings.Contains(string(body), `"run_id": "12345"`) {
		t.Fatalf("archive metadata = %q, error = %v", body, err)
	}
	if err := runExternal("gh", "--version"); err != nil {
		t.Fatal(err)
	}
}

func TestCIRunnerProvisionValidationBoundaries(t *testing.T) {
	t.Setenv("LINODE_TOKEN", "token")
	t.Setenv("CI_RUNNER_ALLOWED_SSH_CIDRS", "")
	if err := provisionCIRunnerHosts(); err == nil || !strings.Contains(err.Error(), "ALLOWED_SSH_CIDRS") {
		t.Fatalf("allowed CIDR error = %v", err)
	}
	t.Setenv("CI_RUNNER_ALLOWED_SSH_CIDRS", "198.51.100.10/32")
	t.Setenv("CI_RUNNER_PUBLIC_KEY_PATH", filepath.Join(t.TempDir(), "missing.pub"))
	if err := provisionCIRunnerHosts(); err == nil {
		t.Fatal("missing public key unexpectedly passed")
	}
	if err := archiveCIRunnerArtifacts(nil); err == nil || !strings.Contains(err.Error(), "--repo and --run-id") {
		t.Fatalf("archive validation error = %v", err)
	}
	if err := runCIRunnerSession([]string{"--shutdown-policy", "sometimes"}); err == nil {
		t.Fatal("invalid shutdown policy unexpectedly passed")
	}
}

func TestCIRunnerSessionLifecycleWithIsolatedProviderCLIs(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
printf '%s\n' '{"data":[],"pages":1}'
`)
	writeExecutable(t, filepath.Join(binDir, "gh"), `#!/bin/sh
if [ "$1" = "api" ]; then
  printf '%s\n' "$FAKE_GH_RUNNERS"
  exit 0
fi
if [ "$FAKE_GH_FAIL_WATCH" = "1" ] && [ "$2" = "watch" ]; then
  exit 1
fi
exit 0
`)
	type fakeRunner struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	seen := map[string]bool{}
	runners := []fakeRunner{}
	for _, spec := range runner.Specs() {
		if seen[spec.RunnerName] {
			continue
		}
		seen[spec.RunnerName] = true
		runners = append(runners, fakeRunner{Name: spec.RunnerName, Status: "online"})
	}
	payload, err := json.Marshal(map[string]any{"runners": runners})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("FAKE_GH_RUNNERS", string(payload))
	t.Setenv("CI_RUNNER_ONLINE_TIMEOUT_SECONDS", "1")
	t.Setenv("CI_RUNNER_ONLINE_POLL_SECONDS", "0")
	t.Chdir(t.TempDir())

	if err := runCIRunnerSession([]string{"--smoke-only", "--shutdown-policy", "on-success"}); err != nil {
		t.Fatal(err)
	}
	if err := runCIRunnerSession([]string{
		"--account-run-id", "12345", "--rerun=false", "--shutdown-policy", "never",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(".artifacts", "ci-runners", "hkt999rtk-rtk_account_manager", "12345", "archive-metadata.json")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FAKE_GH_FAIL_WATCH", "1")
	if err := runCIRunnerSession([]string{
		"--account-run-id", "54321", "--shutdown-policy", "never",
	}); err == nil {
		t.Fatal("failed workflow watch unexpectedly passed")
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIRolloutManualScriptDispatchesOnlyWhenRequested(t *testing.T) {
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "gh.log")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GH_TEST_LOG\"\nif [ \"$1 $2\" = 'run list' ]; then echo 12345; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) error {
		cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts/rollout-dev-ci.sh")}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GH_TEST_LOG="+log)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("script output: %s", out)
		}
		return err
	}
	if err := invoke("--environment", "dev", "--repository", "hkt999rtk/rtk_cloud_frontend"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "workflow run dev-ci-rollout.yml") || !strings.Contains(text, "run watch 12345") {
		t.Fatalf("manual dispatch did not start and follow requested run: %s", text)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := invoke("--environment", "prod", "--repository", "hkt999rtk/rtk_cloud_frontend"); err == nil {
		t.Fatal("non-dev environment accepted")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("invalid request invoked GitHub")
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinodeDestroyPrintsAcceptanceOnlyAfterSuccessfulRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		accepted bool
	}{
		{"accepted", `{}`, true},
		{"failed", "__ERROR_404__", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeLinodeCurl(t, map[string]string{"/linode/instances/201": tc.response})
			plan := linodeDestroyPlan{Instances: []linodeDestroyResource{{ID: "201", Label: "video-cloud-dev-edge", Path: "/linode/instances/201"}}}
			var err error
			out := captureStdoutForDestroyTest(t, func() {
				err = executeLinodeDestroyPlan("fixture-token", "", plan, false, false, nil, false, nil, false, false, false)
			})
			if (err == nil) != tc.accepted {
				t.Fatalf("request success=%v, want=%v", err == nil, tc.accepted)
			}
			if strings.Contains(out, "DELETE REQUEST ACCEPTED: video-cloud-dev-edge (/linode/instances/201)") != tc.accepted {
				t.Fatalf("incorrect request outcome: %s", out)
			}
			if strings.Contains(out, "DELETED:") {
				t.Fatalf("request acceptance must not claim verified removal: %s", out)
			}
		})
	}
}

func TestLinodeDestroyWrapperResolvesRelativeEnvironmentFromWorkspace(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	stub := `#!/bin/sh
printf 'cwd:%s\n' "$PWD" > "$DESTROY_WRAPPER_CALLS"
for arg do printf 'arg:%s\n' "$arg" >> "$DESTROY_WRAPPER_CALLS"; done
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DESTROY_WRAPPER_CALLS", log)
	cmd := exec.Command("bash", filepath.Join(workspace, "scripts", "destroy-linode-staging-resources.sh"), "--env-root", "cloud_env/dev/runtime", "--loadtest-prefix", "")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper failed: %v %s", err, out)
	}
	got := readTestFile(t, log)
	// Go runs from its module; the absolute workspace flag anchors relative environment inputs.
	want := "cwd:" + filepath.Join(workspace, "scripts", "go") + "\narg:run\narg:./rtk-cloud\narg:--\narg:destroy-linode-staging-resources\narg:--workspace\narg:" + workspace + "\narg:--env-root\narg:cloud_env/dev/runtime\narg:--loadtest-prefix\narg:\n"
	if got != want {
		t.Fatalf("relative environment would select the wrong workspace: %q", got)
	}
}

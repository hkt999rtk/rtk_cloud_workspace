package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutNativeHooksUseSelectedKubernetesBackend(t *testing.T) {
	config := frozenKubernetesFixture(t)
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	backend.RolloutTimeout = 30 * time.Second
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	patchFile := filepath.Join(dir, "patch")
	tool := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CI_TEST_CALLS"
case "$*" in
  *" patch deployment "*) cat > "$CI_TEST_PATCH"; printf '{}\n' ;;
  *" get Deployment "*) printf '{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"namespace":"video-cloud-dev-frontend","name":"frontend","uid":"uid","resourceVersion":"42"},"spec":{"template":{"spec":{"containers":[{"name":"frontend","image":"ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}}}' ;;
  *" rollout status "*) printf 'deployment ready\n' ;;
  *) exit 1 ;;
esac
`
	if err = os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("CI_TEST_CALLS", calls)
	t.Setenv("CI_TEST_PATCH", patchFile)
	store := backend.Store
	repo := "hkt999rtk/rtk_cloud_frontend"
	image := environmentReleaseImage{Reference: "ghcr.io/" + repo + "/frontend@sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("c", 40), Publication: &createVersionPublication{Repository: repo, RunID: 1, Attempt: 1}}
	journal, err := newCIRolloutJournal(store, repo, "main", image, "test-1")
	if err != nil {
		t.Fatal(err)
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}}
	hooks := ciRolloutNativeHooks(backend, plan, journal, nil)
	target := ciRolloutTarget{Namespace: "video-cloud-dev-frontend", Name: "frontend", Container: "frontend", UID: "uid"}
	got, err := hooks.Read(context.Background(), target)
	if err != nil || !strings.Contains(string(got), `"uid":"uid"`) {
		t.Fatalf("read: %s %v", got, err)
	}
	patch := []byte(`[{"op":"test","path":"/metadata/uid","value":"uid"}]`)
	if err = hooks.Patch(context.Background(), target, patch, true); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(patchFile)
	if err != nil || string(written) != string(patch) {
		t.Fatalf("patch input: %s %v", written, err)
	}
	observed, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(observed), "--dry-run=server") || !strings.Contains(string(observed), "--namespace video-cloud-dev-frontend") {
		t.Fatalf("unexpected kubectl calls: %s %v", observed, err)
	}
}

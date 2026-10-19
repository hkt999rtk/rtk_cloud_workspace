package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeScopedPodWaitRequiresReadyAndExactDelete(t *testing.T) {
	raw, _ := json.Marshal(frozenKubernetesFixture(t))
	b, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	b.RolloutTimeout = 30 * time.Second
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "arguments")
	stub := `#!/bin/sh
set -eu
shift 3
printf '%s\n' "$*" > "$RTK_TEST_POD_WAIT_ARGS"
if [ "$RTK_TEST_POD_WAIT_FAIL" = yes ]; then printf 'PRIVATE_POD_DIAGNOSTIC' >&2; exit 1; fi
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_POD_WAIT_ARGS", argsPath)
	t.Setenv("RTK_TEST_POD_WAIT_FAIL", "no")
	r := deploymentplan.Resource{ID: "Pod/video-cloud-dev-platform/redis-migration", Action: "create", Phase: "workload", Desired: deploymentplan.Object{"kind": "Pod"}}
	if err := b.Wait(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsPath)
	if string(args) != "--namespace video-cloud-dev-platform wait --for=condition=Ready Pod/redis-migration --timeout=30s\n" {
		t.Fatal("Pod accepted without exact native Ready wait")
	}
	r.Action = "retire"
	if err := b.Wait(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(argsPath)
	if string(args) != "--namespace video-cloud-dev-platform wait --for=delete Pod/redis-migration --timeout=30s\n" {
		t.Fatal("retired Pod accepted without exact absence wait")
	}
	r.Action = "create"
	t.Setenv("RTK_TEST_POD_WAIT_FAIL", "yes")
	if err := b.Wait(context.Background(), r); err == nil || strings.Contains(err.Error(), "PRIVATE_POD_DIAGNOSTIC") {
		t.Fatal("failed readiness bypassed or leaked private output", err)
	}
	b.RolloutTimeout = 0
	if err := b.Wait(context.Background(), r); err == nil {
		t.Fatal("Pod wait accepted missing configured rollout budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.RolloutTimeout = 30 * time.Second
	if err := b.Wait(ctx, r); err == nil {
		t.Fatal("canceled Pod wait accepted")
	}
}

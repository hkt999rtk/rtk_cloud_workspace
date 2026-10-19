package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestNativeJobFailureStopsCompleteWaitAndPreservesOwner(t *testing.T) {
	for _, mode := range []string{"failed", "owner-changed", "complete"} {
		t.Run(mode, func(t *testing.T) {
			makeIsolatedTestSecretStore(t, "dev")
			config := frozenKubernetesFixture(t)
			raw, _ := json.Marshal(config)
			b, err := newEnvironmentKubernetesBackend(string(raw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			b.RolloutTimeout = 30 * time.Second
			dir := t.TempDir()
			script := `#!/bin/sh
case "$*" in
 *wait*) if [ "$RTK_JOB_CASE" = complete ]; then exit 0; fi; exec sleep 20;;
 *) if [ "$RTK_JOB_CASE" = owner-changed ]; then uid=foreign; else uid=original; fi
 printf '{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"original","namespace":"video-cloud-dev-video-cloud","uid":"%s"},"status":{"conditions":[{"type":"Failed","status":"True","message":"PRIVATE_JOB_TOKEN"}]}}' "$uid";;
esac
`
			path := filepath.Join(dir, "kubectl")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_JOB_CASE", mode)
			resource := deploymentplan.Resource{ID: "Job/video-cloud-dev-video-cloud/original", UID: "original", Action: "retain", Desired: deploymentplan.Object{"kind": "Job"}, Phase: "bootstrap"}
			started := time.Now()
			err = b.Wait(context.Background(), resource)
			if time.Since(started) > 8*time.Second {
				t.Fatal("terminal Job waited for Complete timeout")
			}
			if mode == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var blocker *deploymentplan.Blocker
			expected := "deployment.rollout.job-failed"
			if mode == "owner-changed" {
				expected = "deployment.rollout.job-owner"
			}
			if !errors.As(err, &blocker) || blocker.ID != expected || strings.Contains(err.Error(), "PRIVATE_JOB_TOKEN") {
				t.Fatal("failed owner or terminal state was not safely reported", err)
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeRolloutProgressReportsReadinessAndStopsObservations(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"success", "cancel", "timeout"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				raw, _ := yaml.Marshal(frozenKubernetesFixture(t))
				store := makeIsolatedTestSecretStore(t, environment)
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				backend.RolloutTimeout = 300 * time.Second
				dir := t.TempDir()
				object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": "video-cloud-" + environment + "-support", "name": "elasticsearch", "uid": "original-owner"}, "spec": deploymentplan.Object{"replicas": 1}, "status": deploymentplan.Object{"readyReplicas": 0, "updatedReplicas": 1}}
				resource := deploymentplan.Resource{ID: deploymentplan.ID(object), UID: "original-owner", Action: "update", Phase: "configuration", Desired: deploymentplan.Projection(object)}
				raw, _ = json.Marshal(object)
				writeTestFile(t, filepath.Join(dir, "object.json"), string(raw))
				tool := filepath.Join(dir, "kubectl")
				writeTestFile(t, tool, `#!/usr/bin/env python3
import sys,os,json,time
from pathlib import Path
d=Path(os.environ['RTK_ROLLOUT_PROGRESS_DIR']);a=sys.argv[1:]
if 'get' in a:
 with (d/'reads').open('a') as f:f.write('read\n')
 print((d/'object.json').read_text());sys.exit(0)
if 'rollout' in a:
 if os.environ['RTK_ROLLOUT_PROGRESS_MODE']=='timeout':
  sys.stderr.write('timed out waiting for the condition PRIVATE_ROLLOUT_TOKEN\n');sys.exit(1)
 while not (d/'release').exists():time.sleep(.01)
 sys.exit(0)
sys.exit(2)
`)
				os.Chmod(tool, 0755)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_CLOUD_KUBECTL", tool)
				t.Setenv("RTK_ROLLOUT_PROGRESS_DIR", dir)
				t.Setenv("RTK_ROLLOUT_PROGRESS_MODE", mode)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "timeout" {
					var out bytes.Buffer
					err = runEnvironmentProgress(ctx, &out, "deployment.execution.configuration.apply", func(ctx context.Context) error { return backend.Wait(ctx, resource) })
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.rollout.timeout" || blocker.Resource != resource.ID || !strings.Contains(blocker.Cause, "300s") || strings.Contains(err.Error()+out.String(), "PRIVATE_ROLLOUT_TOKEN") {
						t.Fatal("timeout lost exact resource/budget or leaked stderr", err)
					}
				} else {
					writer := progressTestWriter{lines: make(chan string, 128)}
					done := make(chan error, 1)
					go func() {
						done <- runEnvironmentProgressInterval(ctx, writer, "deployment.execution.configuration.apply", 5*time.Millisecond, func(ctx context.Context) error { return backend.Wait(ctx, resource) })
					}()
					deadline := time.NewTimer(3 * time.Second)
					defer deadline.Stop()
					for {
						select {
						case line := <-writer.lines:
							if strings.Contains(line, "waiting-for="+resource.ID) && strings.Contains(line, "ready=0/1 updated=1/1 rollout-limit=300s") {
								if mode == "cancel" {
									cancel()
								} else {
									writeTestFile(t, filepath.Join(dir, "release"), "release")
								}
								goto completed
							}
						case <-deadline.C:
							cancel()
							<-done
							t.Fatal("rollout heartbeat omitted readiness/resource")
						}
					}
				completed:
					err = <-done
					if mode == "success" && err != nil || mode == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatal("progress changed native wait outcome", err)
					}
					before, _ := os.ReadFile(filepath.Join(dir, "reads"))
					time.Sleep(20 * time.Millisecond)
					after, _ := os.ReadFile(filepath.Join(dir, "reads"))
					if string(before) != string(after) {
						t.Fatal("status queries continued after wait returned")
					}
				}
				entries, err := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
				if err != nil || len(entries) != 0 {
					t.Fatal("rollout observer left private API material", err, len(entries))
				}
			})
		}
	}
}

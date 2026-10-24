package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressDuplicateStartsPreserveActiveWorkAndClock(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "terminal"}[terminal], func(t *testing.T) {
			var screen, log bytes.Buffer
			p := &environmentProgress{out: &screen, log: &log, resources: -1, terminal: terminal, columns: 120}
			ctx := environmentDeploymentProgressRound(context.WithValue(context.Background(), environmentProgressKey{}, p), "preflight")
			stage := "deployment.release.verify.video-cloud-api.checkout-head"
			started := time.Now().Add(-32 * time.Second)
			environmentDeploymentProgressAt(ctx, stage, started)
			end := environmentDeploymentOperation(ctx, "release-ci", "")
			p.checked, p.total = 1, 2
			var workers sync.WaitGroup
			for range 12 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					environmentDeploymentProgress(ctx, stage)
				}()
			}
			workers.Wait()
			if p.started != started || p.checked != 1 || p.total != 2 || len(p.operations) != 1 {
				t.Fatal("duplicate start restarted the clock, erased counts or abandoned active work")
			}
			end()
			environmentDeploymentProgressDone(ctx, stage, nil)
			if strings.Count(screen.String(), "[RUNNING] "+stage) != 1 || !strings.Contains(screen.String(), "round=preflight") || strings.Contains(screen.String(), "elapsed=") || strings.Contains(screen.String(), "[OK]") || p.settled != 1 {
				t.Fatal("duplicate start escaped deduplication or lost round/privacy/outcome contract", screen.String())
			}
			if strings.Count(screen.String(), "\n  Verify checkout HEAD for video-cloud-api\n") != 1 || strings.Contains(screen.String(), "task=") {
				t.Fatal("duplicate starts repeated or lost the indented task record", screen.String())
			}
			if !strings.Contains(log.String(), "[OK] "+stage+" elapsed=32s round=preflight") {
				t.Fatal("deduplication lost the verified private log outcome", log.String())
			}
		})
	}
}

func TestProgressStepsAndRevisitsAnnounceEachSubactionOnce(t *testing.T) {
	var screen, log bytes.Buffer
	p := &environmentProgress{out: &screen, log: &log, resources: -1}
	ctx := context.WithValue(context.Background(), environmentProgressKey{}, p)
	next, _ := environmentDeploymentSteps(ctx)
	head, runs := "deployment.release.verify.mqtt-pki.checkout-head", "deployment.release.verify.mqtt-pki.release-runs"
	next(head)
	started := p.started
	next(head)
	if p.started != started || strings.Contains(log.String(), "[OK] "+head) {
		t.Fatal("repeated subaction invented completion or reset its clock")
	}
	next(runs)
	next(head)
	next("")
	for _, stage := range []string{head, runs} {
		if strings.Count(screen.String(), "[RUNNING] "+stage) != 1 {
			t.Fatal("revisited prerequisite printed another identical start", screen.String())
		}
	}
	if !strings.Contains(log.String(), "[OK] "+head) || !strings.Contains(log.String(), "[OK] "+runs) || strings.Contains(screen.String(), "[OK]") {
		t.Fatal("subaction outcomes no longer remain in the log")
	}
}

func TestProgressRoundsSeparateStartsClocksFailuresAndTerminalTasks(t *testing.T) {
	var screen, log bytes.Buffer
	p := &environmentProgress{out: &screen, log: &log, resources: -1, terminal: true, columns: 160}
	ctx := context.WithValue(context.Background(), environmentProgressKey{}, p)
	stage := "deployment.pki"
	for index, round := range []string{"preflight", "before-execution"} {
		roundCtx := environmentDeploymentProgressRound(ctx, round)
		started := time.Now().Add(-time.Duration(index+1) * time.Minute)
		environmentDeploymentProgressAt(roundCtx, stage, started)
		if p.started != started {
			t.Fatal("a new round reused the preceding round's clock")
		}
		status := "PASS"
		if index == 1 {
			status = "FAIL"
		}
		environmentDeploymentProgressResult(roundCtx, stage, status)
	}
	visible := visibleTerminalProgress(screen.String())
	for _, round := range []string{"preflight", "before-execution"} {
		if strings.Count(screen.String(), "[RUNNING] "+stage+" round="+round) != 1 || !strings.Contains(visible, "["+round+"]") || !strings.Contains(log.String(), "round="+round) {
			t.Fatal("round was lost in durable output, terminal task or private log", screen.String(), log.String())
		}
	}
	if !strings.Contains(screen.String(), "[FAIL] "+stage+" round=before-execution") || strings.Contains(screen.String(), "[OK]") {
		t.Fatal("round's failure or success display contract changed", screen.String())
	}
	invalid := environmentDeploymentProgressRound(ctx, "private-round\ncredential=value")
	if invalid != ctx {
		t.Fatal("an unmaintained round crossed the public display boundary")
	}
}

func TestProgressDedupKeepsChangedCountsResourcesAndHeartbeats(t *testing.T) {
	var screen bytes.Buffer
	err := runEnvironmentProgress(context.Background(), &screen, "deployment.execution.configuration.apply", func(ctx context.Context) error {
		resource := "ConfigMap/video-cloud-dev-platform/app"
		environmentDeploymentResourceProgress(ctx, 0, 2, resource, "update", "read")
		before := screen.String()
		environmentDeploymentResourceProgress(ctx, 0, 2, resource, "update", "read")
		if screen.String() != before {
			t.Fatal("identical resource callback printed another unchanged line")
		}
		environmentDeploymentResourceProgress(ctx, 0, 2, resource, "update", "complete")
		environmentDeploymentResourceProgress(ctx, 1, 2, "Secret/video-cloud-dev-platform/runtime", "retain", "read")
		environmentDeploymentProgress(ctx, "deployment.execution.admission")
		environmentDeploymentAdmissionProgress(ctx, 0, 2)
		environmentDeploymentAdmissionProgress(ctx, 1, 2)
		p := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		p.mu.Lock()
		p.write(true)
		p.mu.Unlock()
		environmentDeploymentAdmissionProgress(ctx, 2, 2)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"resources=0/2", "resources=1/2", "resource=Secret/video-cloud-dev-platform/runtime", "checked=0/2", "checked=1/2", "still running", "checked=2/2"} {
		if !strings.Contains(screen.String(), value) {
			t.Fatal("deduplication hid a meaningful progress change", value, screen.String())
		}
	}
}

func TestProgressReleaseTasksDistinguishActionsAndRoles(t *testing.T) {
	for _, key := range []string{"LKE_VIDEO_CLOUD_IMAGE", "LKE_MQTT_PKI_IMAGE", "LKE_OPENBAO_PKI_IMAGE"} {
		tasks := map[string]bool{}
		for _, step := range []string{"workspace-pin", "checkout-head", "pull", "oci-labels", "ci-runs", "release-runs", "publication-jobs", "workspace-baseline-runs", "backup-runs", "release-run", "artifact-list", "artifact-download"} {
			stage := environmentReleaseProgressStage(key, step)
			task := environmentProgressStageTask(stage)
			role := strings.Split(stage, ".")[3]
			if !strings.Contains(task, role) || strings.HasPrefix(task, "Verify release evidence") || tasks[task] {
				t.Fatal("different roles or subactions have ambiguous task descriptions", stage, task)
			}
			tasks[task] = true
		}
	}
	if task := environmentProgressStageTask("deployment.compatibility.transition"); task == environmentProgressStageTask("deployment.compatibility") || !strings.Contains(task, "transitions") {
		t.Fatal("compile transition and release compatibility tasks remain indistinguishable", task)
	}
	for _, resource := range []struct{ id, want string }{{"Node//", "Node/*"}, {"Pod/video-cloud-dev-platform/", "Pod/video-cloud-dev-platform/*"}} {
		if got := terminalProgressTarget(resource.id); got != resource.want {
			t.Fatal("collection scope is ambiguous in terminal progress", got, resource.want)
		}
	}
}

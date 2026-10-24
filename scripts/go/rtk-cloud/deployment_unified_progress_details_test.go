package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProgressMetricsStayOnHeaderAboveTask(t *testing.T) {
	var out bytes.Buffer
	progress := &environmentProgress{
		out: &out, stage: "deployment.execution.admission", round: "preflight", started: time.Now(),
		checked: 2, total: 4, completed: 1, resources: 2, settled: 3,
		action: "update", step: "read", resource: "Service/ns/api",
	}
	ctx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
	end := environmentDeploymentOperation(ctx, "read", "Secret/ns/runtime")
	defer end()
	progress.write(true)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || lines[1] != "  Read baselines and test exact resource bodies" {
		t.Fatal("progress metrics obscured the separate task description", out.String())
	}
	for _, field := range []string{
		"[RUNNING] deployment.execution.admission", "round=preflight", "checked=2/4", "resources=1/2",
		"action=update step=read resource=Service/ns/api", "queries-finished=3", "active=1",
		"Read current resource Secret/ns/runtime", "still running",
	} {
		if !strings.Contains(lines[0], field) {
			t.Fatal("progress header lost a current metric or work target", field, out.String())
		}
	}
	if strings.Contains(lines[0], "Read baselines") || strings.Contains(out.String(), "task=") || strings.ContainsAny(out.String(), "\x1b\r") {
		t.Fatal("task leaked into the header or plain output lost its readable format", out.String())
	}
}

func TestProgressHeartbeatShowsActualActiveQueriesAndJoins(t *testing.T) {
	writer := progressTestWriter{lines: make(chan string, 128)}
	release, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- runEnvironmentProgressInterval(context.Background(), writer, "deployment.inventory.read", time.Millisecond, func(ctx context.Context) error {
			var ends []func()
			for index := 0; index < 12; index++ {
				ends = append(ends, environmentDeploymentOperation(ctx, "inventory", fmt.Sprintf("Namespace//video-cloud-dev-%02d", index)))
			}
			<-release
			for _, end := range ends {
				end()
				end()
			}
			progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
			progress.mu.Lock()
			defer progress.mu.Unlock()
			if len(progress.operations) != 0 || progress.settled != 12 {
				t.Error("operations were not joined or counted once")
			}
			return nil
		})
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case line := <-writer.lines:
			if strings.Contains(line, "active=12") {
				close(release)
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(line, "Inspect the selected environment inventory") || !strings.Contains(line, "Namespace//video-cloud-dev-") || !strings.Contains(line, "+9 more") || strings.Contains(line, "resources=0/0") || strings.Contains(line, "%") {
					t.Fatal("heartbeat did not identify real work", line)
				}
				return
			}
		case <-deadline.C:
			close(release)
			<-finished
			t.Fatal("heartbeat omitted active query targets")
		}
	}
}

func TestResourceProgressCountsAreMonotonicAndAcceptanceStaysPending(t *testing.T) {
	var out bytes.Buffer
	err := runEnvironmentProgress(context.Background(), &out, "deployment.execution.configuration.apply", func(ctx context.Context) error {
		a, b := "Secret/video-cloud-dev-platform/original", "Service/video-cloud-dev-platform/api"
		environmentDeploymentResourceProgress(ctx, 0, 2, a, "retain", "read")
		environmentDeploymentResourceProgress(ctx, 0, 2, a, "retain", "complete")
		environmentDeploymentResourceProgress(ctx, 1, 2, b, "update", "read")
		environmentDeploymentResourceProgress(ctx, 1, 2, b, "update", "complete")
		progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		before := out.String()
		environmentDeploymentResourceProgress(ctx, 0, 2, a, "retain", "read")
		environmentDeploymentResourceProgress(ctx, 0, 3, a, "retain", "read")
		if out.String() != before || progress.completed != 2 || progress.resource != b || progress.resources != 2 {
			t.Fatal("late callbacks regressed counts or changed resource batch")
		}
		environmentDeploymentProgress(ctx, "deployment.execution.acceptance")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"resources=0/2", "resources=1/2", "resources=2/2", "action=retain step=read resource=Secret/", "Run post-deploy acceptance; deployment is not complete yet"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing meaningful progress", want, out.String())
		}
	}
	if strings.Contains(out.String(), "[PASS]") || strings.Contains(out.String(), "[OK] deployment.execution.acceptance") || strings.Contains(out.String(), "100%") {
		t.Fatal("resource counts falsely completed deployment")
	}
}

func TestProgressTargetsRejectPayloadsAndSupportRealCollections(t *testing.T) {
	var out bytes.Buffer
	err := runEnvironmentProgress(context.Background(), &out, "deployment.pki.runtime-now", func(ctx context.Context) error {
		for _, target := range []string{"Secret/ns/PRIVATE_TOKEN", "Secret/ns/credential=value", "Secret/ns/private\nvalue", "https://token@example.test/path", "Secret/ns/../private", "Secret/ns/a/b"} {
			environmentDeploymentOperation(ctx, "read", target)()
		}
		environmentDeploymentOperation(ctx, "PRIVATE_ACTION", "Secret/ns/original")()
		environmentDeploymentResourceProgress(ctx, 0, 1, "Secret/ns/", "retain", "read")
		end := environmentDeploymentOperation(ctx, "inventory", environmentProgressResourceID("secrets", "video-cloud-dev-platform", ""))
		progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		progress.mu.Lock()
		progress.write(true)
		progress.mu.Unlock()
		end()
		if progress.settled != 1 {
			t.Fatal("invalid metadata or duplicate end counted as a query")
		}
		return nil
	})
	if err != nil || !strings.Contains(out.String(), "Secret/video-cloud-dev-platform/*") || strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), "credential=") || strings.Contains(out.String(), "example.test") {
		t.Fatal("query target leaked payload or lost collection scope", out.String())
	}
}

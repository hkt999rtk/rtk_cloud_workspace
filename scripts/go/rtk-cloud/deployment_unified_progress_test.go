package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type progressTestWriter struct{ lines chan string }

func (w progressTestWriter) Write(p []byte) (int, error) { w.lines <- string(p); return len(p), nil }

func TestUnifiedProgressReportsCurrentStageDuringBlockedWorkAndStops(t *testing.T) {
	writer := progressTestWriter{lines: make(chan string, 32)}
	release, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- runEnvironmentProgressInterval(context.Background(), writer, "deployment.compile", 5*time.Millisecond, func(ctx context.Context) error {
			environmentDeploymentProgress(ctx, "deployment.inventory.read")
			<-release
			return nil
		})
	}()
	next := func() string {
		t.Helper()
		select {
		case line := <-writer.lines:
			return line
		case <-time.After(time.Second):
			t.Fatal("progress was buffered until work completed")
			return ""
		}
	}
	if line := next(); !strings.Contains(line, "deployment.compile elapsed=0s") {
		t.Fatal(line)
	}
	if line := next(); !strings.Contains(line, "deployment.inventory.read elapsed=0s") {
		t.Fatal(line)
	}
	if line := next(); !strings.Contains(line, "deployment.inventory.read") || !strings.Contains(line, "still running") {
		t.Fatal(line)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	for len(writer.lines) > 0 {
		<-writer.lines
	}
	select {
	case line := <-writer.lines:
		t.Fatal("progress continued after results could be printed", line)
	case <-time.After(20 * time.Millisecond):
	}
}
func TestUnifiedProgressPreservesCancellationAndDoesNotPrintPrivateErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := progressTestWriter{lines: make(chan string, 32)}
	private := errors.New("private-provider-token-canary")
	err := runEnvironmentProgressInterval(ctx, writer, "deployment.compile", time.Millisecond, func(ctx context.Context) error {
		environmentDeploymentProgress(ctx, "deployment.execution.admission")
		environmentDeploymentAdmissionProgress(ctx, 0, 2)
		environmentDeploymentAdmissionProgress(ctx, 2, 2)
		cancel()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("cancellation was lost")
		}
		return private
	})
	if err != private {
		t.Fatal("progress changed work result", err)
	}
	for len(writer.lines) > 0 {
		line := <-writer.lines
		if strings.Contains(line, private.Error()) || strings.Contains(line, "[PASS]") {
			t.Fatal("progress leaked an error or fabricated check evidence", line)
		}
	}
	select {
	case line := <-writer.lines:
		t.Fatal("cancelled reporter continued after joining", line)
	case <-time.After(10 * time.Millisecond):
	}
	// Direct library calls without a CLI reporter remain silent and safe.
	environmentDeploymentProgress(context.Background(), "deployment.inventory.read")
	environmentDeploymentAdmissionProgress(context.Background(), 1, 2)
}

func TestUnifiedAdmissionProgressPreservesClockAndMonotonicCounts(t *testing.T) {
	var output bytes.Buffer
	progress := &environmentProgress{out: &output}
	ctx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
	environmentDeploymentProgress(ctx, "deployment.execution.admission")
	started := time.Now().Add(-3 * time.Second)
	progress.started = started
	environmentDeploymentAdmissionProgress(ctx, 0, 8)
	initial := output.String()
	if !strings.Contains(initial, "elapsed=3s checked=0/8") {
		t.Fatalf("initial admission count missing or clock restarted: %s", initial)
	}
	var workers sync.WaitGroup
	for checked := 1; checked < 8; checked++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			environmentDeploymentAdmissionProgress(ctx, checked, 8)
		}()
	}
	workers.Wait()
	if output.String() != initial {
		t.Fatal("intermediate resource counts should use the heartbeat")
	}
	if progress.started != started || progress.checked != 7 || progress.total != 8 {
		t.Fatalf("count updates changed clock or lost monotonic totals: %+v", progress)
	}
	// Late or malformed callbacks cannot make the displayed count regress or
	// silently change the denominator of this resource-check batch.
	for _, count := range [][2]int{{1, 8}, {-1, 8}, {9, 8}, {7, 9}, {7, -1}, {7, 8}} {
		environmentDeploymentAdmissionProgress(ctx, count[0], count[1])
	}
	if output.String() != initial || progress.checked != 7 || progress.total != 8 || progress.started != started {
		t.Fatal("invalid or late callbacks changed admission progress")
	}
	environmentDeploymentAdmissionProgress(ctx, 8, 8)
	environmentDeploymentAdmissionProgress(ctx, 8, 8)
	if got := strings.Count(output.String(), "checked=8/8"); got != 1 {
		t.Fatalf("completion lines = %d, want one: %s", got, output.String())
	}
	if strings.Contains(output.String(), "[PASS]") {
		t.Fatal("finished resource count fabricated PASS evidence")
	}
	environmentDeploymentProgress(ctx, "deployment.pki.before-fence")
	if progress.checked != 0 || progress.total != -1 || !progress.started.After(started) {
		t.Fatal("new stage retained previous admission counters or elapsed clock")
	}
	// A later admission batch can have a different size after a stage change.
	environmentDeploymentProgress(ctx, "deployment.execution.admission")
	environmentDeploymentAdmissionProgress(ctx, 0, 0)
	if !strings.Contains(output.String(), "checked=0/0") {
		t.Fatal("empty admission batch did not report its finished count")
	}
}

func TestUnifiedAdmissionProgressHeartbeatReportsFinishedChecks(t *testing.T) {
	writer := progressTestWriter{lines: make(chan string, 128)}
	counted, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- runEnvironmentProgressInterval(context.Background(), writer, "deployment.execution.admission", 5*time.Millisecond, func(ctx context.Context) error {
			environmentDeploymentAdmissionProgress(ctx, 0, 4)
			environmentDeploymentAdmissionProgress(ctx, 2, 4)
			close(counted)
			<-release
			environmentDeploymentAdmissionProgress(ctx, 4, 4)
			return errors.New("failed-resource-canary")
		})
	}()
	<-counted
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	var lines []string
	for {
		select {
		case line := <-writer.lines:
			lines = append(lines, line)
			if strings.Contains(line, "checked=2/4 still running") {
				close(release)
				goto completed
			}
		case <-deadline.C:
			close(release)
			<-finished
			t.Fatal("heartbeat omitted the finished admission resource count")
		}
	}
completed:
	if err := <-finished; err == nil || err.Error() != "failed-resource-canary" {
		t.Fatal("progress changed resource-check failure", err)
	}
	for len(writer.lines) > 0 {
		lines = append(lines, <-writer.lines)
	}
	output := strings.Join(lines, "")
	if !strings.Contains(output, "checked=0/4") || !strings.Contains(output, "checked=4/4") || strings.Contains(output, "[PASS]") || strings.Contains(output, "failed-resource-canary") {
		t.Fatalf("resource progress leaked an error or fabricated PASS: %s", output)
	}
	select {
	case line := <-writer.lines:
		t.Fatal("admission heartbeat continued after work joined", line)
	case <-time.After(20 * time.Millisecond):
	}
}

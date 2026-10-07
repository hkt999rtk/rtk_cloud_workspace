package main

import (
	"context"
	"errors"
	"strings"
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
	// Direct library calls without a CLI reporter remain silent and safe.
	environmentDeploymentProgress(context.Background(), "deployment.inventory.read")
}

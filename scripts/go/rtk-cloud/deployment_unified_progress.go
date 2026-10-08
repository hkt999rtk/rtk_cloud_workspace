package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

type environmentProgressKey struct{}
type environmentProgress struct {
	mu      sync.Mutex
	out     io.Writer
	stage   string
	started time.Time
	checked int
	total   int
}

// Only maintained stage IDs cross this boundary; never pass command output,
// credentials, input values or provider errors to the progress reporter.
func environmentDeploymentProgress(ctx context.Context, stage string) {
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.stage, progress.started = stage, time.Now()
		progress.checked, progress.total = 0, -1
		fmt.Fprintf(progress.out, "[RUNNING] %s elapsed=0s\n", stage)
	}
}

// Admission counts finished resource checks, including failures. Counts never
// imply PASS and never restart the current stage's elapsed-time clock.
func environmentDeploymentAdmissionProgress(ctx context.Context, checked, total int) {
	if checked < 0 || total < 0 || checked > total {
		return
	}
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		if progress.total >= 0 && (total != progress.total || checked <= progress.checked) {
			return
		}
		first := progress.total < 0
		progress.checked, progress.total = checked, total
		if (first && checked == 0) || checked == total {
			fmt.Fprintf(progress.out, "[RUNNING] %s elapsed=%ds checked=%d/%d\n", progress.stage, int64(time.Since(progress.started)/time.Second), checked, total)
		}
	}
}

func runEnvironmentProgress(ctx context.Context, out io.Writer, stage string, work func(context.Context) error) error {
	return runEnvironmentProgressInterval(ctx, out, stage, 10*time.Second, work)
}
func runEnvironmentProgressInterval(ctx context.Context, out io.Writer, stage string, interval time.Duration, work func(context.Context) error) error {
	progress := &environmentProgress{out: out}
	ctx = context.WithValue(ctx, environmentProgressKey{}, progress)
	environmentDeploymentProgress(ctx, stage)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				progress.mu.Lock()
				if progress.total >= 0 {
					fmt.Fprintf(progress.out, "[RUNNING] %s elapsed=%ds checked=%d/%d still running\n", progress.stage, int64(time.Since(progress.started)/time.Second), progress.checked, progress.total)
				} else {
					fmt.Fprintf(progress.out, "[RUNNING] %s elapsed=%ds still running\n", progress.stage, int64(time.Since(progress.started)/time.Second))
				}
				progress.mu.Unlock()
			}
		}
	}()
	// Always join the reporter before normal results use the same writer,
	// including errors/cancellation. Work stays synchronous; no task is abandoned.
	defer func() { close(done); <-stopped }()
	return work(ctx)
}

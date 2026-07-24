package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/coverage"
	"syscall"
	"time"
)

// This file is injected only into coverage-only image command packages. It
// snapshots counters before Kubernetes terminates a long-running process,
// including services whose production main does not implement graceful
// shutdown. Production images and source modules are not modified.
func init() {
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		return
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		failed := false
		if err := coverage.WriteMetaDir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "runtime coverage metadata flush failed: %v\n", err)
			failed = true
		}
		if err := coverage.WriteCountersDir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "runtime coverage counter flush failed: %v\n", err)
			failed = true
		}
		// Let the application consume the same signal and begin its own
		// graceful shutdown before the coverage-only process exits.
		time.Sleep(500 * time.Millisecond)
		if failed {
			os.Exit(70)
		}
		os.Exit(0)
	}()
}

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostCleanupCRLConvergenceWaitsForActualConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	err := waitPostCleanupCRLConsumers(ctx, time.Millisecond, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("latest signed CRL not yet acknowledged")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("convergence: attempts=%d error=%v", attempts, err)
	}
}

func TestPostCleanupCRLConvergenceHonorsOriginalDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := waitPostCleanupCRLConsumers(ctx, time.Hour, func(context.Context) error {
		attempts++
		cancel()
		return errors.New("unacknowledged latest CRL")
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 || !strings.Contains(err.Error(), "unacknowledged latest CRL") {
		t.Fatalf("deadline/reset/error lost: %v attempts=%d", err, attempts)
	}
}

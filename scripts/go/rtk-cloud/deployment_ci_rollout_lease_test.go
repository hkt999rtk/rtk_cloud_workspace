package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCIRolloutWaitsForActiveDeploymentLease(t *testing.T) {
	attempts := 0
	acquired := false
	read := func(context.Context) (bool, error) { attempts++; return attempts < 3, nil }
	acquire := func(context.Context) (func() error, error) { acquired = true; return func() error { return nil }, nil }
	release, err := waitForCIRolloutLease(context.Background(), time.Millisecond, read, acquire)
	if err != nil || !acquired || attempts < 3 || release == nil {
		t.Fatalf("did not wait for current owner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err = waitForCIRolloutLease(ctx, time.Millisecond, func(context.Context) (bool, error) { return true, nil }, acquire); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("occupied lease did not honor deadline: %v", err)
	}
}

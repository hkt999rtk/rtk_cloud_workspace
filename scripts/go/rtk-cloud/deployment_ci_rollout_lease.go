package main

import (
	"context"
	"errors"
	"time"
)

// A concurrent legitimate deployment is normal: wait for its existing lease
// to be released, never delete it. Retry acquisition only on an observed race.
func waitForCIRolloutLease(ctx context.Context, delay time.Duration, occupied func(context.Context) (bool, error), acquire func(context.Context) (func() error, error)) (func() error, error) {
	if delay <= 0 || occupied == nil || acquire == nil {
		return nil, errors.New("CI rollout: lease reader and acquirer required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		busy, err := occupied(ctx)
		if err != nil {
			return nil, err
		}
		if !busy {
			release, err := acquire(ctx)
			if err == nil {
				return release, nil
			}
			busy, readErr := occupied(ctx)
			if readErr != nil {
				return nil, readErr
			}
			if !busy {
				return nil, err
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

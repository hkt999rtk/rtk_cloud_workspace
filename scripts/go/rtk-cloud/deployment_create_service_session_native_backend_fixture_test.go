package main

import (
	"context"
	"errors"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// sessionNativeFaultBackend wraps the real Kubernetes adapter used by the
// isolated unified fixture. Lease, fencing and all object operations remain on
// that adapter; the wrapper only counts writes and injects one interruption.
type sessionNativeFaultBackend struct {
	environmentCreateKubernetesBackend
	writes         map[string]int
	failAfterPhase string
	failed         bool
}

func (b *sessionNativeFaultBackend) Write(ctx context.Context, action string, object deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	result, err := b.environmentCreateKubernetesBackend.Write(ctx, action, object, dry)
	if err == nil && !dry {
		if b.writes == nil {
			b.writes = make(map[string]int)
		}
		b.writes[deploymentplan.ID(object)]++
	}
	return result, err
}

func (b *sessionNativeFaultBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	if err := b.environmentCreateKubernetesBackend.Wait(ctx, resource); err != nil {
		return err
	}
	if !b.failed && b.failAfterPhase != "" && resource.Phase == b.failAfterPhase {
		b.failed = true
		return errors.New("isolated interruption after native write")
	}
	return nil
}

package main

import (
	"context"
	"io"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The rejected derived input is read only to reach the real preparation path.
// Final proof must come from the corrected standard child and logical closure.
func TestCreateNormalTailWorkloadForwardAliasIndependentFinalProof(t *testing.T) {
	f := newNormalHydratedAliasFixture(t)
	publishRejectedAliasForward(t, f)
	c, err := hydrateAliasForward(t, f)
	if err != nil {
		t.Fatal("read exact unstarted legacy source", err)
	}
	c, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
		t.Fatal("saved input refresh must retain original observations")
		return nil, nil
	})
	if err != nil {
		t.Fatal("prepare corrected input", err)
	}
	if err := checkEnvironmentManagedMQTTConsumers(t.Context(), c.Plan, f.b.Read); err != nil {
		t.Fatal("native public mount contract", err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	if err := executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("actual corrected producer", err)
	}
	if err := executeCreateNormalTailSequence(t.Context(), f.h.Execution, []func(context.Context) error{func(ctx context.Context) error { return f.h.Execution.Check(ctx) }}); err != nil {
		t.Fatal("original logical closure", err)
	}
	checkWorkloadForwardPython(t, f)
}

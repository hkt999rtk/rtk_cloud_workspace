package main

import (
	"context"
	"io"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFailedStageSourceUsesLatestAcceptedOriginalResource(t *testing.T) {
	store, e, p, b := freshStageFixture(t)
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	id := p.Resources[0].ID
	found, err := readLatestAcceptedCreateResource(t.Context(), store, e, b, id)
	if err != nil || deploymentplan.ID(found) != id {
		t.Fatal(found, err)
	}
	deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign-owner"
	if _, err := readLatestAcceptedCreateResource(t.Context(), store, e, b, id); err == nil {
		t.Fatal("replaced live owner accepted")
	}
	if _, err := readLatestAcceptedCreateResource(t.Context(), store, e, b, "ConfigMap/no-such-owner"); err == nil {
		t.Fatal("unknown resource accepted")
	}
}

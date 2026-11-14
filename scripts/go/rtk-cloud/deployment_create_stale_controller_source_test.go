package main

import (
	"context"
	"io"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestStaleControllerSourceUsesLatestRecordedOwner(t *testing.T) {
	store, e, prior, b := freshStageFixture(t)
	body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "pki-controller"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "image": "test-image"}}}}}}
	defs := []deploymentplan.Definition{{Object: body, Phase: "workload"}}
	p, err := deploymentplan.Compile(prior.Inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
	exec := func(name string, p *deploymentplan.Plan) {
		t.Helper()
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, name, p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	exec("controller-first", p)
	id := deploymentplan.ID(body)
	current := deploymentplan.Clone(b.objects[id])
	next := deploymentplan.Clone(current)
	deploymentplan.Map(next["spec"])["replicas"] = float64(2)
	p2, err := deploymentplan.Compile(prior.Inputs, []deploymentplan.Definition{{Object: next, Phase: "workload"}}, []deploymentplan.Object{current})
	if err != nil {
		t.Fatal(err)
	}
	exec("controller-latest", p2)
	stage, got, j, r, err := staleControllerRecordedSource(t.Context(), store, e, b)
	if err != nil || stage != "controller-latest" || got.SHA256() != p2.SHA256() || r.UID != j.Owners[id] {
		t.Fatal("wrong source", err)
	}
	deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "replacement"
	if _, _, _, _, err := staleControllerRecordedSource(t.Context(), store, e, b); err == nil {
		t.Fatal("replacement accepted")
	}
}

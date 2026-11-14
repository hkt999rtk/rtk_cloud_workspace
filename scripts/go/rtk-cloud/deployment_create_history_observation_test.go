package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type historyObservationBackend struct {
	freshStageBackend
	inventories, waits int
	failInventory      bool
}

type historyRetirementBackend struct{ freshStageBackend }

func (b historyRetirementBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action == "retire" && !dry {
		delete(b.objects, deploymentplan.ID(body))
		return deploymentplan.Clone(body), nil
	}
	return b.freshStageBackend.Write(ctx, action, body, dry)
}
func (b historyRetirementBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Action == "retire" {
		return ctx.Err()
	}
	return b.freshStageBackend.Wait(ctx, r)
}

func (b *historyObservationBackend) Inventory(context.Context, string) ([]deploymentplan.Object, error) {
	b.inventories++
	if b.failInventory {
		return nil, errors.New("inventory unavailable")
	}
	var out []deploymentplan.Object
	for _, o := range b.objects {
		out = append(out, deploymentplan.Clone(o))
	}
	return out, nil
}
func (b *historyObservationBackend) Wait(context.Context, deploymentplan.Resource) error {
	b.waits++
	return errors.New("historical preparation is no longer a running workload")
}

func TestAcceptedHistoryUsesOneFreshInventoryWithoutReplayingRollout(t *testing.T) {
	for _, mode := range []string{"completed-preparation", "replacement", "drift", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			store, e, fixture, base := freshStageFixture(t)
			object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "original-preparation", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "prepare", "image": "fixture-image"}}}}}}
			defs := []deploymentplan.Definition{{Object: object, Phase: "workload"}}
			for n := range environmentDeploymentCheckConcurrency {
				name := fmt.Sprintf("fixture-config-%d", n)
				defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": e.Journal.Stack + "-video-cloud"}, "data": deploymentplan.Object{"proof": "fixture"}}, Phase: "configuration"})
			}
			p, err := deploymentplan.Compile(fixture.Inputs, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
			if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "preparation-source", p, base, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
				t.Fatal(err)
			}
			id := deploymentplan.ID(object)
			base.objects[id]["status"] = deploymentplan.Object{"readyReplicas": float64(0)}
			b := &historyObservationBackend{freshStageBackend: base, failInventory: mode == "unavailable"}
			if mode == "replacement" {
				deploymentplan.Map(base.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			}
			if mode == "drift" {
				deploymentplan.Map(base.objects[id]["spec"])["replicas"] = float64(2)
			}
			before := p.SHA256()
			_, err = verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/preparation-source")
			if (err == nil) != (mode == "completed-preparation") {
				t.Fatal("historical ownership misclassified", mode, err)
			}
			if b.waits != 0 || b.inventories != 1 || p.SHA256() != before {
				t.Fatal("history replayed rollout, repeated inventory or changed frozen plan", b.waits, b.inventories)
			}
		})
	}
}

func TestAcceptedRetirementHistoryKeepsNativeAbsenceAndRejectsReplacement(t *testing.T) {
	store, e, fixture, base := freshStageFixture(t)
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "fixture-preparation", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "prepare", "image": "fixture-image"}}}}}}
	initial, err := deploymentplan.Compile(fixture.Inputs, []deploymentplan.Definition{{Object: object, Phase: "workload"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
	apply := func(stage string, p *deploymentplan.Plan) {
		t.Helper()
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, historyRetirementBackend{base}, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	apply("original-source", initial)
	id := initial.Resources[0].ID
	original := deploymentplan.Clone(base.objects[id])
	retire, err := deploymentplan.Compile(initial.Inputs, []deploymentplan.Definition{{Object: original, Phase: "retirement", Retire: true}}, []deploymentplan.Object{original})
	if err != nil {
		t.Fatal(err)
	}
	apply("original-retirement", retire)
	b := &historyObservationBackend{freshStageBackend: base}
	if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/original-retirement"); err != nil {
		t.Fatal("accepted native retirement rejected", err)
	}
	if b.waits != 0 || b.inventories != 0 || base.objects[id] != nil {
		t.Fatal("retirement replayed or observed more than once")
	}
	if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/original-source"); err != nil {
		t.Fatal("earlier source required an owner already retired by its original accepted child", err)
	}
	base.objects[id] = original
	deploymentplan.Map(original["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/original-retirement"); err == nil {
		t.Fatal("retired original owner adopted a replacement")
	}
}

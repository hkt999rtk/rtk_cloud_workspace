package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func newNormalWorkloadForwardFixture(t *testing.T) *normalPostgresAccessFixture {
	t.Helper()
	defs := managedMQTTConsumersFixture(t)
	for i := range defs {
		o := defs[i].Object
		name := deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
		defs[i].Retain = true
		if name == "video-cloud-api-pki" || name == "video-cloud-mqttfoundation" || name == "video-cloud-shadowworker" {
			defs[i].Retain = false
		}
	}
	f := newNormalPostgresAccessFixture(t, defs)
	if err := f.run(t.Context()); err != nil {
		t.Fatal("genuine access prerequisite", err)
	}
	return f
}

func publishNormalWorkloadForwardFixture(t *testing.T, f *normalPostgresAccessFixture) *createNormalTailWorkloadForward {
	t.Helper()
	c := &compiledEnvironmentDeployment{Store: f.store, Plan: f.workload, Configuration: f.cfg, Release: f.artifact.Program.Release, Bindings: f.artifact.Program.Bindings}
	_, err := prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
		var live []deploymentplan.Object
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		return live, nil
	})
	if err != nil {
		t.Fatal("real forward publication", err)
	}
	_, j, err := readCreateNormalTailWorkloadOriginal(f.store, f.h.Execution, f.workload)
	if err != nil {
		t.Fatal(err)
	}
	d, err := readCreateNormalTailWorkloadForwardDescriptor(f.store, f.h.Execution, f.workload, j)
	if err != nil || d == nil {
		t.Fatal("real forward descriptor", err)
	}
	return d
}

func TestCreateNormalTailWorkloadForwardPreservesOriginalAndClosesComposite(t *testing.T) {
	f := newNormalWorkloadForwardFixture(t)
	e := f.h.Execution
	originalPath := filepath.Join(f.store.Root, "deployment/rollback", f.workload.SHA256(), "journal.json")
	before, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	d := publishNormalWorkloadForwardFixture(t, f)
	source, _, err := readCreateNormalTailWorkloadOriginal(f.store, e, f.workload)
	if err != nil || source.SHA256() != f.workload.SHA256() {
		t.Fatal("original source replaced", err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	c := &compiledEnvironmentDeployment{Store: f.store, Plan: d.Target.Plan, Configuration: f.cfg, Release: d.Target.Release, Bindings: d.Target.Bindings}
	if err := executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("actual producer/composite execution", err)
	}
	effective, j, err := readCreateNormalTailWorkload(f.store, e, f.workload)
	if err != nil || !j.Accepted || effective.SHA256() != d.Target.Plan.SHA256() {
		t.Fatal("logical completed response did not select genuine child", err)
	}
	if f.b.writes["StatefulSet/"+e.Journal.Stack+"-platform/postgresql"] != 1 {
		t.Fatal("known Postgres write repeated")
	}
	after, _ := os.ReadFile(originalPath)
	if string(before) != string(after) {
		t.Fatal("original failed journal was changed")
	}
	old, exists, err := f.workload.ReadExecution(f.store.Root)
	if err != nil || !exists || old.Accepted {
		t.Fatal("original accepted truth fabricated")
	}

	if err := executeCreateNormalTailSequence(t.Context(), e, []func(context.Context) error{func(ctx context.Context) error { return e.Check(ctx) }}); err != nil {
		t.Fatal("genuine logical original closure", err)
	}
	if e.Journal.CompletedAt.IsZero() {
		t.Fatal("original owner not durably completed")
	}

}

func TestCreateNormalTailWorkloadForwardRejectsUnknownAndUndeclaredEffects(t *testing.T) {
	for _, mode := range []string{"unknown-write", "changed-known-owner", "unrecorded-adoption", "foreign-mqtt", "foreign-policy"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalWorkloadForwardFixture(t)
			d := publishNormalWorkloadForwardFixture(t, f)
			_, j, err := readCreateNormalTailWorkloadOriginal(f.store, f.h.Execution, f.workload)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown-write":
				j.InFlight = f.workload.Resources[len(j.Completed)].ID
			case "changed-known-owner":
				d.Target.Plan.Resources[0].UID = "foreign"
			case "unrecorded-adoption":
				for i, r := range f.workload.Resources {
					if r.Action == "create" && j.Owners[r.ID] == "" {
						d.Target.Plan.Resources[i].Action = "retain"
						d.Target.Plan.Resources[i].UID = "00000099-1111-4111-8111-111111111111"
						break
					}
				}
			case "foreign-mqtt":
				for i, r := range d.Target.Plan.Resources {
					if r.ID == "Deployment/"+f.h.Execution.Journal.Stack+"-video-cloud/video-cloud-shadowworker" {
						mqttFoundationPrimary(d.Target.Plan.Resources[i].Desired)["image"] = "foreign"
					}
				}
			case "foreign-policy":
				d.Target.Plan.Resources[len(d.Target.Plan.Resources)-1].Desired["spec"] = deploymentplan.Object{}
			}
			if err := validateCreateNormalTailWorkloadForwardPlan(f.workload, j, d.Target.Plan); err == nil {
				t.Fatal("unknown or undeclared forward effect accepted")
			}
		})
	}
}

func TestCreateNormalTailWorkloadForwardUnknownOutcomeNeverRepeats(t *testing.T) {
	for _, mode := range []string{"unknown-forward-write", "unrecorded-worker"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalWorkloadForwardFixture(t)
			d := publishNormalWorkloadForwardFixture(t, f)
			c := &compiledEnvironmentDeployment{Store: f.store, Plan: d.Target.Plan, Configuration: f.cfg, Release: d.Target.Release, Bindings: d.Target.Bindings}
			before := map[string]int{}
			for id, n := range f.b.writes {
				before[id] = n
			}
			if mode == "unknown-forward-write" {
				f.b.failure = "unknown-write"
			} else {
				for _, r := range c.Plan.Resources {
					if r.ID == "Deployment/"+f.h.Execution.Journal.Stack+"-video-cloud/video-cloud-shadowworker" {
						o := deploymentplan.Clone(r.Desired)
						deploymentplan.Map(o["metadata"])["uid"] = "00000088-1111-4111-8111-111111111111"
						f.b.objects[r.ID] = o
					}
				}
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
			run := func() error {
				return executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, f.b, check, check, check, io.Discard, func(context.Context) error { return nil })
			}
			if err := run(); err == nil {
				t.Fatal("uncertain or unrecorded effect passed")
			}
			after := map[string]int{}
			total := 0
			for id, n := range f.b.writes {
				after[id] = n
				total += n - before[id]
			}
			if mode == "unknown-forward-write" && total != 1 || mode == "unrecorded-worker" && total != 0 {
				t.Fatal("unexpected native writes before refusal", total)
			}
			f.b.failure = ""
			if err := run(); err == nil {
				t.Fatal("unknown effect adopted on retry")
			}
			if deploymentplan.Digest(after) != deploymentplan.Digest(f.b.writes) {
				t.Fatal("retry repeated a native write")
			}
			_, j, err := readCreateNormalTailWorkloadOriginal(f.store, f.h.Execution, f.workload)
			if err != nil || j.Accepted {
				t.Fatal("unknown forward fabricated original acceptance", err)
			}
		})
	}
}

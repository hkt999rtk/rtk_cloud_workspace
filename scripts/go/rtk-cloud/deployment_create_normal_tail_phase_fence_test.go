package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type normalTailPhaseBackend struct {
	*normalTailE2EBackend
	reads        []string
	beforeRead   func(string)
	progressHook func(string, string)
	rejectParent bool
}

func (b *normalTailPhaseBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	b.reads = append(b.reads, id)
	if b.beforeRead != nil {
		b.beforeRead(id)
	}
	return b.normalTailE2EBackend.Read(ctx, id)
}

func (b *normalTailPhaseBackend) ExecutionProgress(ctx context.Context, phase, step string) {
	b.normalTailE2EBackend.ExecutionProgress(ctx, phase, step)
	if b.progressHook != nil {
		b.progressHook(phase, step)
	}
}

func (b *normalTailPhaseBackend) CheckParentExecution(ctx context.Context, p *deploymentplan.Plan, root string) error {
	if b.rejectParent {
		return errors.New("original parent changed")
	}
	return b.normalTailE2EBackend.CheckParentExecution(ctx, p, root)
}

func (b *normalTailPhaseBackend) snapshot() map[string]deploymentplan.Object {
	result := map[string]deploymentplan.Object{}
	for id, object := range b.objects {
		result[id] = deploymentplan.Clone(object)
	}
	return result
}

type normalTailPhaseFixture struct {
	store                  secretStore
	h                      *createNormalTailHandoff
	p                      *deploymentplan.Plan
	b                      *normalTailPhaseBackend
	first, retained, later string
}

func newNormalTailPhaseFixture(t *testing.T, createLater bool) normalTailPhaseFixture {
	t.Helper()
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"terminal": deploymentplan.Digest("verified original bootstrap")}}
	config := func(name, uid string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": name, "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "data": deploymentplan.Object{"value": "original"}}
	}
	first := config("first", "00000001-1111-4111-8111-111111111111")
	retained := config("retained", "00000002-1111-4111-8111-111111111111")
	later := config("later", "00000003-1111-4111-8111-111111111111")
	desiredFirst, desiredLater := deploymentplan.Clone(first), deploymentplan.Clone(later)
	deploymentplan.Map(desiredFirst["data"])["value"] = "desired"
	deploymentplan.Map(desiredLater["data"])["value"] = "desired"
	live := []deploymentplan.Object{first, retained}
	if createLater {
		delete(deploymentplan.Map(desiredLater["metadata"]), "uid")
		delete(deploymentplan.Map(desiredLater["metadata"]), "resourceVersion")
	} else {
		live = append(live, later)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("frozen release"), BindingsSHA256: deploymentplan.Digest("original bindings"), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{{Object: desiredFirst, Phase: "configuration"}, {Object: retained, Phase: "configuration", Retain: true}, {Object: desiredLater, Phase: "workload", Dependencies: []string{deploymentplan.ID(first), deploymentplan.ID(retained)}}}, live)
	if err != nil {
		t.Fatal(err)
	}
	b := &normalTailPhaseBackend{normalTailE2EBackend: &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	for _, object := range live {
		b.objects[deploymentplan.ID(object)] = deploymentplan.Clone(object)
	}
	return normalTailPhaseFixture{store, h, p, b, deploymentplan.ID(first), deploymentplan.ID(retained), deploymentplan.ID(later)}
}

func (f normalTailPhaseFixture) run(ctx context.Context, fence func(context.Context, *deploymentplan.Plan) error, phaseFence ...deploymentplan.PhaseFence) error {
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	return executeCreateNormalTailStageBackendWithPhaseFence(ctx, f.store, f.h, "workload", f.p, f.b, check, fence, check, io.Discard, func(context.Context) error { return nil }, phaseFence...)
}

func (f normalTailPhaseFixture) journal(t *testing.T) deploymentplan.Journal {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment", "rollback", f.p.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j deploymentplan.Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestCreateNormalTailPhaseFenceReusesEachFreshObservation(t *testing.T) {
	reads := [2]int{}
	for index, optimized := range []bool{false, true} {
		f := newNormalTailPhaseFixture(t, false)
		sha, fences, snapshots := f.p.SHA256(), 0, 0
		fence := func(context.Context, *deploymentplan.Plan) error { fences++; return nil }
		var phaseFences []deploymentplan.PhaseFence
		if optimized {
			phaseFences = append(phaseFences, func(context.Context, *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
				snapshots++
				objects := f.b.snapshot()
				if snapshots == 2 && deploymentplan.Map(objects[f.first]["data"])["value"] != "desired" {
					t.Fatal("later phase reused its predecessor's observations")
				}
				return objects, nil
			})
		}
		if err := f.run(t.Context(), fence, phaseFences...); err != nil {
			t.Fatal(err)
		}
		j := f.journal(t)
		entry := createOpenBaoACKCompletionEntry(f.h.Execution.Journal, "kubernetes/workload")
		if !j.Accepted || len(j.Completed) != 3 || entry == nil || entry.State != "completed" || f.b.writes[f.first] != 1 || f.b.writes[f.later] != 1 || f.b.held || f.p.SHA256() != sha {
			t.Fatal("observation reuse changed execution, receipt or write ownership")
		}
		if optimized && (snapshots != 2 || fences != 2) || !optimized && fences != 4 {
			t.Fatal("phase reuse bypassed final acceptance or journal fence", snapshots, fences)
		}
		reads[index] = len(f.b.reads)
	}
	if reads[0]-reads[1] != 6 {
		t.Fatal("expected exactly six duplicate phase reads removed", reads)
	}
	t.Logf("backend reads: ordinary=%d, shared phase observations=%d", reads[0], reads[1])
}

func TestCreateNormalTailPhaseFencePreservesDriftAndUnknownOutcomeChecks(t *testing.T) {
	for _, mode := range []string{"next-owner", "next-body", "missing-owner", "nil-owner", "unknown-create", "prewrite-owner", "prewrite-body", "parent", "unknown-write"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalTailPhaseFixture(t, mode == "unknown-create")
			phases, expectedWrites := 0, 0
			failed := f.first
			if mode == "parent" {
				f.b.progressHook = func(_, step string) {
					if step == "fence" {
						f.b.rejectParent = true
					}
				}
			}
			if mode == "unknown-write" {
				f.b.failure = "unknown-write"
				expectedWrites = 1
			}
			phaseFence := func(context.Context, *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
				phases++
				if phases == 2 {
					switch mode {
					case "next-owner":
						deploymentplan.Map(f.b.objects[f.first]["metadata"])["uid"] = "replacement-owner"
					case "next-body":
						deploymentplan.Map(f.b.objects[f.first]["data"])["value"] = "concurrent"
					}
				}
				objects := f.b.snapshot()
				switch mode {
				case "missing-owner", "nil-owner":
					failed = f.retained
					delete(objects, failed)
					if mode == "nil-owner" {
						objects[failed] = nil
					}
					deploymentplan.Map(f.b.objects[failed]["metadata"])["uid"] = "replacement-owner"
				case "unknown-create":
					failed = f.later
					f.b.objects[failed] = deploymentplan.Clone(f.p.Resources[2].Desired)
					deploymentplan.Map(f.b.objects[failed]["metadata"])["uid"] = "unrecorded-owner"
				case "prewrite-owner", "prewrite-body":
					f.b.beforeRead = func(id string) {
						if id == f.first {
							if mode == "prewrite-owner" {
								deploymentplan.Map(f.b.objects[id]["metadata"])["uid"] = "replacement-owner"
							} else {
								deploymentplan.Map(f.b.objects[id]["data"])["value"] = "concurrent"
							}
						}
					}
				}
				return objects, nil
			}
			if mode == "next-owner" || mode == "next-body" {
				expectedWrites = 1
			}
			fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
			if err := f.run(t.Context(), fence, phaseFence); err == nil {
				t.Fatal("concurrent change or unknown write passed")
			}
			j := f.journal(t)
			if j.Accepted || f.b.held || f.b.writes[f.first] != expectedWrites || f.b.writes[f.later] != 0 {
				t.Fatal("failure allowed a dependent or repeated write")
			}
			if mode == "parent" {
				if phases != 0 {
					t.Fatal("phase inventory preceded original parent validation")
				}
			} else if j.Failed != failed {
				t.Fatal("drift lost its exact resource", j.Failed, failed)
			}
			if mode == "missing-owner" || mode == "nil-owner" || mode == "unknown-create" {
				if f.b.reads[len(f.b.reads)-1] != failed {
					t.Fatal("inventory omission was treated as absence")
				}
			}
			if mode == "unknown-write" {
				if j.InFlight != f.first {
					t.Fatal("unknown native outcome was erased")
				}
				if err := f.run(t.Context(), fence, phaseFence); err == nil || f.b.writes[f.first] != 1 || phases != 1 {
					t.Fatal("unknown native write was replayed")
				}
			}
		})
	}
}

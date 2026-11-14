package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type openBaoFirstRecordedWaitCapture struct {
	freshStageBackend
	waits []deploymentplan.Resource
}

func (b *openBaoFirstRecordedWaitCapture) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	b.waits = append(b.waits, resource)
	if environmentOnDeleteOpenBao(resource) && (resource.Action != "retain" || resource.UID == "") {
		return errors.New("OnDelete original owner must use retained readiness")
	}
	return nil
}

func TestOpenBaoFirstRecordedOnDeleteUsesOriginalReadOnlyWait(t *testing.T) {
	for _, mode := range []string{"original", "foreign-owner", "missing-journal"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
			if err != nil {
				t.Fatal(err)
			}
			def := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-secrets", "name": "openbao"}, "spec": deploymentplan.Object{"updateStrategy": deploymentplan.Object{"type": "OnDelete"}}}}
			defs := []deploymentplan.Definition{def}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("foundation", defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, failure: "acceptance"}}
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return b.CheckParentExecution(ctx, p, store.Root)
			}
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "foundation", p, b, fence, fence, func(context.Context, *deploymentplan.Plan) error { return b.trip("acceptance") }, e.Check, io.Discard); err == nil {
				t.Fatal("expected original stage interruption")
			}
			id := deploymentplan.ID(def.Object)
			owner := deploymentplan.String(deploymentplan.Map(b.objects[id]["metadata"])["uid"])
			beforeWrites, beforeParent := b.writes[id], deploymentplan.Digest(e.Journal)
			journal := filepath.Join(store.Root, "deployment", "rollback", p.SHA256(), "journal.json")
			beforeChild, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign-owner":
				deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign"
			case "missing-journal":
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
			}
			observer := &openBaoFirstRecordedWaitCapture{freshStageBackend: b}
			_, err = inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, "foundation", observer)
			if mode == "original" && (err != nil || len(observer.waits) != 1 || observer.waits[0].Action != "retain" || observer.waits[0].UID != owner) {
				t.Fatal("original OnDelete owner was not retained for read-only wait", err)
			}
			if mode != "original" && err == nil {
				t.Fatal("foreign or missing child evidence accepted")
			}
			if b.writes[id] != beforeWrites || deploymentplan.Digest(e.Journal) != beforeParent {
				t.Fatal("read-only qualification changed parent or native resource")
			}
			if mode != "missing-journal" {
				after, err := os.ReadFile(journal)
				if err != nil || !bytes.Equal(after, beforeChild) {
					t.Fatal("read-only qualification changed child journal", err)
				}
			}
		})
	}
}

func TestOpenBaoFirstRecordedStageResumesOriginalOwner(t *testing.T) {
	for _, stage := range []string{"foundation", "foundation-services"} {
		t.Run(stage, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
			if err != nil {
				t.Fatal(err)
			}
			def := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-secrets", "name": "original"}, "data": deploymentplan.Object{"identity": "original"}}}
			defs := []deploymentplan.Definition{def}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, failure: "acceptance"}}
			if got, err := inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, stage, b); err != nil || got != nil {
				t.Fatal("unstarted stage was not recognized", err)
			}
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return b.CheckParentExecution(ctx, p, store.Root)
			}
			run := func() error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, fence, fence, func(ctx context.Context, p *deploymentplan.Plan) error {
					if err := p.Verify(ctx, b); err != nil {
						return err
					}
					return b.trip("acceptance")
				}, e.Check, io.Discard)
			}
			if err := run(); err == nil {
				t.Fatal("isolated original acceptance interruption did not occur")
			}
			id := deploymentplan.ID(def.Object)
			uid := deploymentplan.String(deploymentplan.Map(b.objects[id]["metadata"])["uid"])
			before := b.writes[id]
			got, err := inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, stage, b)
			if err != nil || got == nil || !got.HasJournal || got.Journal.Owners[id] != uid || got.ParentState != "pending" || b.writes[id] != before {
				t.Fatal("original partial stage was not verified read-only", err)
			}
			deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign-owner"
			if _, err := inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, stage, b); err == nil {
				t.Fatal("foreign owner was accepted")
			}
			deploymentplan.Map(b.objects[id]["metadata"])["uid"] = uid
			if err := run(); err != nil || b.writes[id] != before {
				t.Fatal("retry did not preserve original owner", err)
			}
			got, err = inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, stage, b)
			if err != nil || got == nil || !got.Journal.Accepted || got.ParentState != "completed" {
				t.Fatal("accepted original stage unavailable", err)
			}
		})
	}
}

func TestOpenBaoFirstRecordedStageRejectsOrphanAndUnknownWrite(t *testing.T) {
	for _, mode := range []string{"orphan-input", "unknown-inflight"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
			if err != nil {
				t.Fatal(err)
			}
			def := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-secrets", "name": "original"}}}
			defs := []deploymentplan.Definition{def}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("foundation", defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, failure: "acceptance"}}
			if mode == "orphan-input" {
				if err := saveEnvironmentCreateStageInput(t.Context(), store, e, "foundation", p); err != nil {
					t.Fatal(err)
				}
			} else {
				fence := func(ctx context.Context, p *deploymentplan.Plan) error {
					return b.CheckParentExecution(ctx, p, store.Root)
				}
				err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "foundation", p, b, fence, fence, func(context.Context, *deploymentplan.Plan) error { return b.trip("acceptance") }, e.Check, io.Discard)
				if err == nil {
					t.Fatal("expected interruption")
				}
				journal, exists, err := p.ReadExecution(store.Root)
				if err != nil || !exists {
					t.Fatal("missing original journal", err)
				}
				journal.InFlight = deploymentplan.ID(def.Object)
				raw, _ := json.Marshal(journal)
				rel := filepath.Join("deployment", "rollback", p.SHA256(), "journal.json")
				if err := store.write(rel, raw, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inspectOpenBaoFirstRecordedStage(t.Context(), cfg, store, e, "foundation", b); err == nil {
				t.Fatal("unproven stage or write was accepted")
			}
		})
	}
}

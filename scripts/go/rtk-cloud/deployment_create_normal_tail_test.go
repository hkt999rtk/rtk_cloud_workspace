package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailRecordedRoutingStagesResumeWithoutRepeatingRequests(t *testing.T) {
	for _, key := range []string{"configure-edge", "configure-turn-01", "dns/A/dev.example.test", "activate-turn-01"} {
		for _, state := range []string{"unstarted", "pending", "allocated", "completed"} {
			t.Run(key+"/"+state, func(t *testing.T) {
				cfg, store, operator, a := createStateFixture(t, "dev")
				e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				intent := []byte(`{"selected":"original-intent"}`)
				receipt := []byte(`{"outcome":"original-response"}`)
				if state != "unstarted" {
					if err := e.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				if state == "allocated" || state == "completed" {
					if err := e.RecordReceipt(key, receipt); err != nil {
						t.Fatal(err)
					}
				}
				if state == "completed" {
					if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(e.JournalPath)
				checks := 0
				done, err := createNormalTailRecordedStage(t.Context(), e, key, "routing", intent, receipt, func(context.Context) error { checks++; return nil })
				if state == "pending" {
					if err == nil || !done || checks != 0 {
						t.Fatal("uncertain request was adopted")
					}
				} else if err != nil || done != (state != "unstarted") || checks != map[bool]int{true: 1, false: 0}[state != "unstarted"] {
					t.Fatal(done, checks, err)
				}
				after, _ := os.ReadFile(e.JournalPath)
				if state != "allocated" && string(before) != string(after) {
					t.Fatal("read-only stage check rewrote original evidence")
				}
				if state == "allocated" && createOpenBaoACKCompletionEntry(e.Journal, key).State != "completed" {
					t.Fatal("known response was not closed after actual readback")
				}
				_ = store
			})
		}
	}
}

func TestCreateNormalTailRoutingDriftCannotCompleteAnOriginalResponse(t *testing.T) {
	for _, mode := range []string{"intent", "receipt", "phase", "native", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, operator, a := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			intent, receipt := []byte(`{"selected":"original"}`), []byte(`{"response":"original"}`)
			if e.BeginEntry("configure-edge", "routing", intent, time.Now().UTC()) != nil || e.RecordReceipt("configure-edge", receipt) != nil {
				t.Fatal("fixture")
			}
			phase := "routing"
			ctx := t.Context()
			verify := func(context.Context) error { return nil }
			switch mode {
			case "intent":
				intent = []byte(`{"selected":"foreign"}`)
			case "receipt":
				receipt = []byte(`{"response":"foreign"}`)
			case "phase":
				phase = "bootstrap"
			case "native":
				verify = func(context.Context) error { return errors.New("actual owner changed") }
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, _ := os.ReadFile(e.JournalPath)
			if _, err := createNormalTailRecordedStage(ctx, e, "configure-edge", phase, intent, receipt, verify); err == nil {
				t.Fatal("drift was accepted")
			}
			after, _ := os.ReadFile(e.JournalPath)
			if string(before) != string(after) {
				t.Fatal("drift published completion")
			}
		})
	}
}

func TestCreateNormalTailWorkloadUnstartedAdmissionIsResumable(t *testing.T) {
	for _, mode := range []string{"unstarted", "lease-without-journal", "foreign-intent", "missing-target", "foreign-target"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, a := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			var original environmentCreatePlan
			if err := json.Unmarshal(a.Plan, &original); err != nil {
				t.Fatal(err)
			}
			h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"original": deploymentplan.Digest("authenticated original handoff")}}
			bindings := environmentIdentityBindings{Version: 1, Environment: cfg.Environment, Stack: e.Journal.Stack, Owners: map[string]string{}}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(original.Release), BindingsSHA256: deploymentplan.Digest(bindings), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "runtime"}}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Production saves this immutable target before the parent BeginEntry;
			// no child Plan/journal/lease has been published at this boundary.
			if err := saveCreateNormalTailTarget(&compiledEnvironmentDeployment{Store: store, Plan: p, Release: original.Release, Bindings: bindings}, h); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-target" {
				if err := os.Remove(filepath.Join(e.Directory, "normal-tail-target.json")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-target" {
				target := createNormalTailTarget{Schema: createNormalTailTargetSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest("foreign owner"), Pins: h.Pins, Plan: p, Release: original.Release, Bindings: bindings}
				relative, _ := filepath.Rel(store.Root, filepath.Join(e.Directory, "normal-tail-target.json"))
				if err := store.write(relative, mustProviderRecoveryJSON(target), true); err != nil {
					t.Fatal(err)
				}
			}

			intent, err := environmentCreateKubernetesStageIntent(p)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "foreign-intent" {
				intent = json.RawMessage(`{"foreign":"request"}`)
			}
			if err := e.BeginEntry("kubernetes/workload", "workload", intent, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if mode == "lease-without-journal" {
				if err := e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "original-lease", Holder: p.SHA256() + ":original"}); err != nil {
					t.Fatal(err)
				}
			}
			child, _, err := readCreateNormalTailWorkload(store, e, p)
			if mode == "unstarted" {
				if err != nil || child != nil {
					t.Fatal("known unstarted admission blocked", err)
				}
			} else if err == nil {
				t.Fatal("unproved child outcome adopted")
			} else if mode == "lease-without-journal" && !strings.Contains(err.Error(), "workload lease exists without its execution evidence") {
				t.Fatal("lease negative failed before its intended source guard", err)
			} else if (mode == "missing-target" || mode == "foreign-target") && !strings.Contains(err.Error(), "deployment.create.target") {
				t.Fatal("frozen target negative failed at an unrelated guard", err)
			}
			if _, exists, err := p.ReadExecution(store.Root); err != nil || exists {
				t.Fatal("observation created a child journal", err)
			}
		})
	}
}

func TestCreateNormalTailPreparationUsesOriginalChildAcrossInterruption(t *testing.T) {
	for _, failure := range []string{"before-admission", "after-write-configuration", "acceptance", "parent-receipt"} {
		t.Run(failure, func(t *testing.T) {
			cfg, store, operator, a := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"program": deploymentplan.Digest("frozen-product-program")}}
			body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "product-trust"}, "data": deploymentplan.Object{"root": "original-root"}}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("fixed-release"), BindingsSHA256: deploymentplan.Digest("fixed-stage"), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{{Object: body, Phase: "configuration"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, failure: failure}
			qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := b.trip("before-admission"); err != nil {
					return err
				}
				return p.AdmitResumable(ctx, b, store.Root)
			}
			fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			accept := func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := p.Verify(ctx, b); err != nil {
					return err
				}
				return b.trip("acceptance")
			}
			capture := func(context.Context) error { return b.trip("parent-receipt") }
			stage := "product-identities-frozen-successor-trust"
			if executeCreateNormalTailStageBackend(t.Context(), store, h, stage, p, b, qualify, fence, accept, io.Discard, capture) == nil {
				t.Fatal("interruption was not observed")
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if entry == nil || entry.State == "completed" {
				t.Fatal("failed preparation was marked complete")
			}
			originalStart, originalIntent := entry.StartedAt, entry.IntentSHA256
			if err := executeCreateNormalTailStageBackend(t.Context(), store, h, stage, p, b, qualify, fence, accept, io.Discard, capture); err != nil {
				t.Fatal(err)
			}
			entry = createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if entry.State != "completed" || !entry.StartedAt.Equal(originalStart) || entry.IntentSHA256 != originalIntent || b.writes[deploymentplan.ID(body)] != 1 {
				t.Fatal("preparation replayed a write or replaced original intent")
			}
			before := deploymentplan.Digest(*entry)
			if err := executeCreateNormalTailStageBackend(t.Context(), store, h, stage, p, b, qualify, fence, accept, io.Discard, capture); err != nil {
				t.Fatal(err)
			}
			entry = createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if before != deploymentplan.Digest(*entry) || b.writes[deploymentplan.ID(body)] != 1 {
				t.Fatal("accepted stage rewrote original completion or native owner")
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type freshStageBackend struct{ *normalTailE2EBackend }

func (b freshStageBackend) CheckParentExecution(ctx context.Context, p *deploymentplan.Plan, root string) error {
	if root != b.e.Store.Root || p.Environment != b.e.Journal.Environment || p.Stack != b.e.Journal.Stack {
		return errors.New("foreign fresh parent")
	}
	return b.e.Check(ctx)
}

func freshStageFixture(t *testing.T) (secretStore, *environmentCreateExecution, *deploymentplan.Plan, freshStageBackend) {
	t.Helper()
	cfg, store, operator, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "fresh-stage"}, "data": deploymentplan.Object{"root": "protected-original-root"}}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("frozen-fresh-release"), BindingsSHA256: environmentCreateStageBindingsSHA("workload", []deploymentplan.Definition{{Object: body, Phase: "configuration"}})}, []deploymentplan.Definition{{Object: body, Phase: "configuration"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	return store, e, p, b
}

func TestCreateFreshStageResumesOriginalInputAndKnownWrites(t *testing.T) {
	for _, failure := range []string{"before-parent", "before-admission", "after-write-configuration", "acceptance", "parent-receipt"} {
		t.Run(failure, func(t *testing.T) {
			store, e, p, b := freshStageFixture(t)
			b.failure = failure
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return b.CheckParentExecution(ctx, p, store.Root)
			}
			qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := b.trip("before-admission"); err != nil {
					return err
				}
				return p.AdmitResumable(ctx, b, store.Root)
			}
			accept := func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := p.Verify(ctx, b); err != nil {
					return err
				}
				return b.trip("acceptance")
			}
			run := func() error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", p, b, qualify, fence, accept, func(context.Context) error { return b.trip("parent-receipt") }, io.Discard)
			}
			if failure == "before-parent" {
				if err := saveEnvironmentCreateStageInput(t.Context(), store, e, "workload", p); err != nil {
					t.Fatal(err)
				}
			} else if run() == nil {
				t.Fatal("interruption was not reached")
			}
			inputBefore, err := os.ReadFile(environmentCreateStageInputPath(e, "workload"))
			if err != nil {
				t.Fatal(err)
			}
			var original environmentCreateJournalEntry
			if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry != nil {
				original = *entry
			}
			if err := run(); err != nil {
				t.Fatal("known fresh stage could not resume", err)
			}
			inputAfter, _ := os.ReadFile(environmentCreateStageInputPath(e, "workload"))
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
			if string(inputBefore) != string(inputAfter) || entry == nil || entry.State != "completed" || b.writes[p.Resources[0].ID] != 1 || !original.StartedAt.IsZero() && (!entry.StartedAt.Equal(original.StartedAt) || entry.IntentSHA256 != original.IntentSHA256) {
				t.Fatal("fresh retry replaced input, owner or original parent intent")
			}
			before := deploymentplan.Digest(*entry)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			after := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
			if before != deploymentplan.Digest(*after) || b.writes[p.Resources[0].ID] != 1 {
				t.Fatal("accepted child was replayed or parent receipt rewritten")
			}
		})
	}
}

func TestCreateFreshStageRefusesChangedInputAndUnownedChild(t *testing.T) {
	for _, mode := range []string{"changed-input", "orphan-lease", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, e, p, b := freshStageFixture(t)
			if err := saveEnvironmentCreateStageInput(t.Context(), store, e, "workload", p); err != nil {
				t.Fatal(err)
			}
			intent, _ := environmentCreateKubernetesStageIntent(p)
			if err := e.BeginEntry("kubernetes/workload", "workload", intent, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch mode {
			case "changed-input":
				p.StorageSHA256 = deploymentplan.Digest("foreign")
			case "orphan-lease":
				if err := e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "00000001-2222-4222-8222-222222222222", Holder: p.SHA256() + ":unknown"}); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			if executeEnvironmentCreateStagePlan(ctx, store, e, "workload", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard) == nil || len(b.writes) != 0 {
				t.Fatal("unknown fresh child was adopted or applied")
			}
		})
	}
}

func TestCreateOriginalRoutingContextRequiresTheSameHeldExecution(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	_ = store
	for _, mode := range []string{"fresh", "handoff", "absent", "foreign-execution", "foreign-sha", "cancelled", "owner-drift"} {
		t.Run(mode, func(t *testing.T) {
			ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256})
			owner := e.owner
			defer func() { e.owner = owner }()
			switch mode {
			case "handoff":
				ctx = withCreateNormalTailHandoff(t.Context(), &createNormalTailHandoff{Execution: e})
			case "absent":
				ctx = t.Context()
			case "foreign-execution":
				other := *e
				ctx = withEnvironmentCreateBootstrapRuntime(t.Context(), environmentCreateBootstrapRuntime{Execution: &other, ExecutionSHA256: e.Journal.PlanSHA256})
			case "foreign-sha":
				ctx = withEnvironmentCreateBootstrapRuntime(t.Context(), environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: deploymentplan.Digest("foreign")})
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "owner-drift":
				e.owner.OwnerToken = "foreign"
			}
			selected, err := createOriginalRoutingContext(ctx, e)
			valid := mode == "fresh" || mode == "handoff"
			if valid && (!selected || err != nil) || mode == "absent" && (selected || err != nil) || !valid && mode != "absent" && err == nil {
				t.Fatal("routing resumed outside its original held execution")
			}
		})
	}
}

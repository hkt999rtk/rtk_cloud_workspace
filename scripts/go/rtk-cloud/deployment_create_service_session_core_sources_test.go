package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceSessionCoreSourcesRequireAcceptedOwnerAndLiveUID(t *testing.T) {
	store, e, plan, backend := freshStageFixture(t)
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", plan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	id := plan.Resources[0].ID
	sources, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{id})
	if err != nil || len(sources) != 1 || sources[0].Stage != "workload" || !sources[0].SelectedIDs[id] || sources[0].Journal.Owners[id] == "" {
		t.Fatal("accepted original owner was not selected", err)
	}
	if _, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{id, id}); err == nil {
		t.Fatal("duplicate owner accepted")
	}
	if _, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{"Deployment/missing/owner"}); err == nil {
		t.Fatal("owner without recorded stage accepted")
	}
	live := backend.objects[id]
	metadata := deploymentplan.Map(live["metadata"])
	uid := metadata["uid"]
	metadata["uid"] = "different-owner"
	if _, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{id}); err == nil {
		t.Fatal("replacement UID accepted")
	}
	metadata["uid"] = uid
	deploymentplan.Map(live["data"])["root"] = "changed"
	if _, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{id}); err == nil {
		t.Fatal("changed owner desired state accepted")
	}
}

func TestServiceSessionCoreSourceCompilesSameOwnerSIDUpdate(t *testing.T) {
	store, e, template, backend := freshStageFixture(t)
	old := "11111111-1111-4111-8111-111111111111"
	next := "22222222-2222-4222-8222-222222222222"
	body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "controller", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "controller", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old}, deploymentplan.Object{"name": "IDENTITY", "value": "original"}}}}}}}}
	defs := []deploymentplan.Definition{{Object: body, Phase: "workload"}}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("core", defs)
	plan, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "core", plan, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(body)
	ids, err := serviceSessionCoreOwnerIDs(store, e, []string{old}, nil)
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal("original recorded session owner was not discovered", err, ids)
	}
	ids, err = serviceSessionCoreOwnerIDs(store, e, []string{old}, map[string]bool{id: true})
	if err != nil || len(ids) != 0 {
		t.Fatal("application lineage owner was included in core", err, ids)
	}
	sources, err := loadServiceSessionCoreSources(t.Context(), store, e, backend, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{backend.objects[id]}
	candidate, pending, err := compileServiceSessionSIDForward(sources, live, old, next, "core-session-forward")
	if err != nil || len(pending) != 0 || candidate == nil || len(candidate.Resources) != 1 {
		t.Fatal("original core owner was not compiled", err, pending)
	}
	r := candidate.Resources[0]
	if r.Action != "update" || r.ID != id || r.UID != sources[0].Journal.Owners[id] || !deploymentplan.Matches(body, r.Baseline) {
		t.Fatal("candidate does not retain original owner and baseline")
	}
	expected := deploymentplan.Clone(plan.Resources[0].Desired)
	if _, err := serviceSessionSIDSlots(expected, old, next); err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(r.Desired) {
		t.Fatal("candidate changed fields other than the session reference", err)
	}
	// A failed readiness wait must resume the same admitted update and owner.
	backend.failure = "after-write-workload"
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "core-session-forward", candidate, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("expected interruption after updating the original owner")
	}
	before := sources[0].Journal.Owners[id]
	backend.failure, backend.failed = "", false
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "core-session-forward", candidate, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal("original SID update did not resume", err)
	}
	journal, exists, err := candidate.ReadExecution(store.Root)
	if err != nil || !exists || !journal.Accepted || journal.Owners[id] != before || !deploymentplan.Matches(expected, backend.objects[id]) {
		t.Fatal("retry did not preserve the owner or final session reference", err)
	}
}

func TestServiceSessionCoreSourcesFollowOrdinaryRecordedPrefix(t *testing.T) {
	store, e, first, pending, b := ordinaryPendingHistoryFixture(t, "next-phase")
	id := first.Resources[0].ID
	before := createBootstrapStateSnapshot(t, store.Root)
	writes, leases := deploymentplan.Digest(b.writes), b.leases
	sources, err := loadServiceSessionCoreSources(t.Context(), store, e, b, []string{id})
	if err != nil || len(sources) != 1 || sources[0].Stage != "ordinary-pending" || sources[0].Plan.SHA256() != pending.SHA256() || !sources[0].SelectedIDs[id] {
		t.Fatal("core reader ignored the known ordinary native prefix", err)
	}
	if b.waits != 0 || writes != deploymentplan.Digest(b.writes) || leases != b.leases {
		t.Fatal("core source observation waited, wrote or acquired ownership")
	}
	assertRecoveryContractReadOnly(t, store, before)
}

// An original whole-workload prefix includes no-write retains of the custody
// reader and temporary enrollment owners. Such a retain is ownership evidence,
// and must not be interpreted as another Service identity update.
func TestServiceSessionCoreSourcesFollowOrdinaryRetainWithoutInventingWrite(t *testing.T) {
	store, e, first, native := freshStageFixture(t)
	b := &ordinaryPendingHistoryBackend{freshStageBackend: native}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	run := func(stage string, p *deploymentplan.Plan, fence func(context.Context, *deploymentplan.Plan) error) error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, fence, check, func(context.Context) error { return nil }, io.Discard)
	}
	if err := run("core-accepted", first, check); err != nil {
		t.Fatal(err)
	}
	id := first.Resources[0].ID
	tail := deploymentplan.Clone(first.Resources[0].Desired)
	deploymentplan.Map(tail["metadata"])["name"] = "unstarted-tail"
	defs := []deploymentplan.Definition{{Object: deploymentplan.Clone(b.objects[id]), Phase: "configuration", Retain: true}, {Object: tail, Phase: "routing"}}
	inputs := first.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("ordinary-retain-pending", defs)
	pending, err := deploymentplan.Compile(inputs, defs, []deploymentplan.Object{b.objects[id]})
	if err != nil || pending.Resources[0].Action != "retain" {
		t.Fatal(err)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		j, exists, err := p.ReadExecution(store.Root)
		if err != nil {
			return err
		}
		if exists && len(j.Completed) > 0 {
			return errors.New("actual next phase interruption after no-write retain")
		}
		return check(ctx, p)
	}
	if err = run("ordinary-retain-pending", pending, fence); err == nil {
		t.Fatal("interruption missing")
	}
	journal, exists, err := pending.ReadExecution(store.Root)
	if err != nil || !exists || len(journal.Completed) != 1 || journal.Owners[id] == "" || b.writes[id] != 1 {
		t.Fatal("original retain prefix missing", err)
	}
	b.waits, b.denyWait = 0, true
	before := createBootstrapStateSnapshot(t, store.Root)
	writes, leases := deploymentplan.Digest(b.writes), b.leases
	sources, err := loadServiceSessionCoreSources(t.Context(), store, e, b, []string{id})
	if err != nil || len(sources) != 1 || sources[0].Stage != "core-accepted" || sources[0].Plan.SHA256() != first.SHA256() {
		t.Fatal("no-write original retain became a required update", err)
	}
	if b.waits != 0 || deploymentplan.Digest(b.writes) != writes || b.leases != leases {
		t.Fatal("core history observation created an effect")
	}
	assertRecoveryContractReadOnly(t, store, before)
	for _, mode := range []string{"uid", "body", "retained-mutation"} {
		t.Run(mode, func(t *testing.T) {
			r := pending.Resources[0]
			j := journal
			switch mode {
			case "uid":
				j.Owners = map[string]string{id: "foreign"}
			case "body":
				r.Baseline = deploymentplan.Clone(r.Baseline)
				deploymentplan.Map(r.Baseline["data"])["root"] = "foreign"
			case "retained-mutation":
				r.Desired = deploymentplan.Clone(r.Desired)
				deploymentplan.Map(r.Desired["data"])["root"] = "not actually written"
			}
			fake := *pending
			fake.Resources = append([]deploymentplan.Resource(nil), pending.Resources...)
			fake.Resources[0] = r
			if _, ok := followServiceSessionCoreOwner(serviceSessionRecordedPlan{Stage: "base"}, id, journal.Owners[id], first.Resources[0].Desired, b.objects[id], []serviceSessionRecordedPlan{{Stage: "retain", Plan: &fake, Journal: j}}, map[string]int{"base": 0, "retain": 1}, 0); ok {
				t.Fatal("unverified retained owner accepted")
			}
		})
	}
}

// A new ordinary owner is already real even if its enclosing whole-workload
// stage has not passed readiness. Discover it only from the literal native
// write journal, never from the current cluster inventory alone.
func TestServiceSessionCoreNewOwnerFromKnownPendingCreate(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "known-write", true: "unknown-outcome"}[unknown], func(t *testing.T) {
			store, e, template, native := freshStageFixture(t)
			b := &ordinaryPendingHistoryBackend{freshStageBackend: native}
			old, next := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-foundation", "namespace": e.Journal.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "mqtt", "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": old})}}}}}}
			defs := []deploymentplan.Definition{{Object: body, Phase: "workload"}}
			inputs := template.Inputs
			inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("ordinary-new-core", defs)
			p, err := deploymentplan.Compile(inputs, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b.failure = "after-write-workload"
			if unknown {
				b.failure = "unknown-write"
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "ordinary-new-core", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
				t.Fatal("missing interruption")
			}
			id := p.Resources[0].ID
			b.waits, b.denyWait = 0, true
			before := createBootstrapStateSnapshot(t, store.Root)
			writes, leases := deploymentplan.Digest(b.writes), b.leases
			ids, err := serviceSessionCoreOwnerIDs(store, e, []string{old}, nil)
			if unknown {
				if err == nil {
					t.Fatal("unknown native outcome became an original owner")
				}
				return
			}
			if err != nil || len(ids) != 1 || ids[0] != id {
				t.Fatal("known new owner absent from original session sources", err, ids)
			}
			sources, err := loadServiceSessionCoreSources(t.Context(), store, e, b, ids)
			if err != nil || len(sources) != 1 || sources[0].Stage != "ordinary-new-core" || sources[0].Journal.Accepted {
				t.Fatal("known new owner requires enclosing acceptance", err)
			}
			candidate, _, err := compileServiceSessionSIDForward(sources, []deploymentplan.Object{b.objects[id]}, old, next, "continue-new-core")
			if err != nil || candidate == nil || len(candidate.Resources) != 1 || candidate.Resources[0].UID != sources[0].Journal.Owners[id] {
				t.Fatal("new owner cannot continue original session", err)
			}
			if b.waits != 0 || writes != deploymentplan.Digest(b.writes) || leases != b.leases {
				t.Fatal("source discovery caused an effect")
			}
			assertRecoveryContractReadOnly(t, store, before)
			deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign"
			if _, err := loadServiceSessionCoreSources(t.Context(), store, e, b, ids); err == nil {
				t.Fatal("foreign replacement accepted")
			}
		})
	}
}

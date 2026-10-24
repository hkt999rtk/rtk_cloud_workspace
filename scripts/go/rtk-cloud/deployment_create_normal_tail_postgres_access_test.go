package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type normalPostgresAccessFixture struct {
	cfg      deploymentConfig
	store    secretStore
	h        *createNormalTailHandoff
	artifact createNormalTailProgramArtifact
	workload *deploymentplan.Plan
	b        *plannedEnvelopeWaitBackend
}

func newNormalPostgresAccessFixture(t *testing.T, extensions ...[]deploymentplan.Definition) *normalPostgresAccessFixture {
	return newNormalPostgresAccessFixtureWithFrozenInputs(t, nil, nil, extensions...)
}
func newNormalPostgresAccessFixtureWithFrozenInputs(t *testing.T, setup func(*deploymentConfig, *environmentCreateExecution, *createNormalTailProgramArtifact), freeze func(*deploymentplan.Plan), extensions ...[]deploymentplan.Definition) *normalPostgresAccessFixture {
	t.Helper()
	cfg, e, a := normalTailProgramFixture(t)
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "300"
	cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "600"
	if setup != nil {
		setup(&cfg, e, &a)
	}
	a.Program.MaintainedConfigSHA256 = deploymentplan.Digest(cfg)
	store := e.Store
	policy, err := decodeEnvironmentCreateManifests(lkeAllowPostgresClientsNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}))
	if err != nil {
		t.Fatal(err)
	}
	postgres := plannedEnvelopeStatefulSet("postgresql", 1, "data")
	openbao := plannedEnvelopeStatefulSet("openbao", 1, "data", "audit")
	deploymentplan.Map(openbao["metadata"])["namespace"] = e.Journal.Stack + "-secrets"
	deploymentplan.Map(postgres["metadata"])["uid"] = "00000001-1111-4111-8111-111111111111"
	deploymentplan.Map(openbao["metadata"])["uid"] = "00000002-1111-4111-8111-111111111111"
	deploymentplan.Map(postgres["metadata"])["resourceVersion"] = "1"
	deploymentplan.Map(openbao["metadata"])["resourceVersion"] = "1"
	baseline := deploymentplan.Clone(postgres)
	podSpec := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(postgres["spec"])["template"])["spec"])
	deploymentplan.Map(certIssuerObjectList(podSpec["containers"])[0])["image"] = "example.invalid/postgres@sha256:" + deploymentplan.Digest("new image")
	defs := append(policy, deploymentplan.Definition{Object: postgres, Phase: "configuration"}, deploymentplan.Definition{Object: openbao, Phase: "workload", Retain: true})
	var extraLive []deploymentplan.Object
	for _, additional := range extensions {
		for _, d := range additional {
			if freeze == nil || d.Phase == "" {
				d.Phase = "workload"
			}
			delete(deploymentplan.Map(d.Object["metadata"]), "uid")
			defs = append(defs, d)
			if d.Retain {
				o := deploymentplan.Clone(d.Object)
				m := deploymentplan.Map(o["metadata"])
				m["uid"] = "00000033-1111-4111-8111-111111111111"
				m["resourceVersion"] = "1"
				labels := deploymentplan.Map(m["labels"])
				if labels == nil {
					labels = deploymentplan.Object{}
					m["labels"] = labels
				}
				labels["rtk.realtek.com/stack"] = e.Journal.Stack
				extraLive = append(extraLive, o)
			}
		}
	}
	a.Program.Definitions = append(a.Program.Definitions, defs...)
	a.Program.Products.SourceDefinitions = a.Program.Definitions
	a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Program)
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = a.Qualification.PlanSHA256
	}
	a.CheckerSHA256, err = environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"terminal": deploymentplan.Digest("verified original bootstrap")}, OriginalPlan: &environmentCreatePlan{Environment: cfg.Environment, Stack: e.Journal.Stack, Release: a.Program.Release}}
	a.Program.HandoffSHA256 = deploymentplan.Digest(h.Pins)
	// Handoff is part of the immutable program; publish its final value before
	// the first real admission, never alter an admitted fixture afterwards.
	a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Program)
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = a.Qualification.PlanSHA256
	}
	if err := saveCreateNormalTailProgramArtifact(store, e, a); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Environment: cfg.Environment, Generation: a.Generation, Decision: "GO", PlanSHA256: deploymentplan.Digest(a.Program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(a.Program) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, 20*time.Minute, time.Now().UTC(), func(ctx context.Context) error { return e.Check(ctx) }); err != nil {
		t.Fatal(err)
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: a.Program.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(a.Program.Release), BindingsSHA256: deploymentplan.Digest(a.Program.Bindings), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}
	p, err := deploymentplan.Compile(inputs, defs, append([]deploymentplan.Object{baseline, openbao}, extraLive...))
	if err != nil {
		t.Fatal(err)
	}
	if freeze != nil {
		freeze(p)
	}
	if err := saveCreateNormalTailTarget(&compiledEnvironmentDeployment{Plan: p, Store: store, Release: a.Program.Release, Bindings: a.Program.Bindings}, h); err != nil {
		t.Fatal(err)
	}
	b := &plannedEnvelopeWaitBackend{normalTailE2EBackend: &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{deploymentplan.ID(baseline): baseline, deploymentplan.ID(openbao): openbao}, writes: map[string]int{}}, plan: p, failCapture: true}
	for _, o := range extraLive {
		b.objects[deploymentplan.ID(o)] = o
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	firstErr := executeCreateNormalTailWorkloadBackend(t.Context(), store, h, p, b, check, check, check, io.Discard, func(context.Context) error { return nil })
	if firstErr == nil {
		t.Fatal("expected known Postgres capture interruption")
	}
	j, exists, err := p.ReadExecution(store.Root)
	failedIndex := 0
	for i, r := range p.Resources {
		if r.ID == deploymentplan.ID(postgres) {
			failedIndex = i
		}
	}
	if err != nil || !exists || len(j.Completed) != failedIndex || len(j.Owners) != failedIndex+1 || j.InFlight != "" {
		t.Fatalf("original partial workload not genuine: completed=%d owners=%d failed=%s in_flight=%s err=%v first=%v", len(j.Completed), len(j.Owners), j.Failed, j.InFlight, err, firstErr)
	}
	return &normalPostgresAccessFixture{cfg, store, h, a, p, b}
}

func (f *normalPostgresAccessFixture) apply(ctx context.Context, defs []deploymentplan.Definition) error {
	e := f.h.Execution
	p, err := readCreateNormalTailPreparationPlan(f.store, e, createNormalTailPostgresAccessStage)
	if err != nil {
		return err
	}
	if p == nil {
		live := []deploymentplan.Object{}
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.artifact.Program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(createNormalTailPostgresAccessStage, defs), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
		if err != nil {
			return err
		}
	}
	if err := saveCreateNormalTailStagePlan(ctx, f.store, e, createNormalTailPostgresAccessStage, p); err != nil {
		return err
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	return executeCreateNormalTailStageBackend(ctx, f.store, f.h, createNormalTailPostgresAccessStage, p, f.b, check, check, check, io.Discard, func(context.Context) error { return nil })
}

func (f *normalPostgresAccessFixture) run(ctx context.Context) error {
	return ensureCreateNormalTailPostgresAccessWithApply(ctx, f.cfg, f.h, &f.artifact.Program, f.b.Read, f.apply)
}

func TestCreateNormalTailPostgresAccessKeepsOriginalWorkloadAndRetainsFinalPolicy(t *testing.T) {
	f := newNormalPostgresAccessFixture(t)
	path := filepath.Join(f.store.Root, "deployment/rollback", f.workload.SHA256(), "journal.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, j, err := createNormalTailPostgresAccessEvidence(f.store, f.h.Execution)
	if err != nil || p == nil || !j.Accepted {
		t.Fatal("repair did not produce a genuine accepted standard child", err)
	}
	id := "NetworkPolicy/" + f.h.Execution.Journal.Stack + "-platform/allow-postgres-openbao"
	if err := f.run(t.Context()); err != nil || f.b.writes[id] != 1 {
		t.Fatal("accepted policy recreated", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("repair changed original workload evidence", err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	if err := executeCreateNormalTailWorkloadBackend(t.Context(), f.store, f.h, f.workload, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("known original workload could not finish", err)
	}
	// A later legitimate steady transition is owned by the workload lifecycle;
	// an already accepted access correction must not demand an obsolete body.
	openbao := f.b.objects["StatefulSet/"+f.h.Execution.Journal.Stack+"-secrets/openbao"]
	deploymentplan.Map(openbao["spec"])["serviceName"] = "legitimate-later-steady-state"
	if err := f.run(t.Context()); err != nil || f.b.writes[id] != 1 {
		t.Fatal("accepted access retry rejected later owned workload state", err)
	}
	defs, err := retainCreateNormalTailPostgresAccess(f.store, f.h.Execution, f.artifact.Program.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	var selected []deploymentplan.Definition
	for _, d := range defs {
		if deploymentplan.ID(d.Object) == id {
			selected = append(selected, d)
		}
	}
	final, err := deploymentplan.Compile(f.workload.Inputs, selected, []deploymentplan.Object{f.b.objects[id]})
	if err != nil || len(final.Resources) != 1 || final.Resources[0].Action != "retain" || final.Resources[0].UID != j.Owners[id] {
		t.Fatal("final compiler lost accepted policy owner", err)
	}
	if err := verifyCreateNormalTailPostgresAccessFromInventory(t.Context(), f.cfg, f.h, environmentKubernetesBackend{}, f.b.objects); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(f.b.objects[id]["spec"])["ingress"] = []any{}
	if err := verifyCreateNormalTailPostgresAccessFromInventory(t.Context(), f.cfg, f.h, environmentKubernetesBackend{}, f.b.objects); err == nil {
		t.Fatal("changed policy body passed retained fence")
	}
}

func TestCreateNormalTailPostgresAccessRejectsUnknownOwnerAndInterruptedWrite(t *testing.T) {
	for _, mode := range []string{"existing-unrecorded", "original-policy-owner", "original-postgres-body", "unknown-write"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalPostgresAccessFixture(t)
			defs, err := createNormalTailPostgresAccessDefinitions(f.h.Execution.Journal.Stack)
			if err != nil {
				t.Fatal(err)
			}
			id := deploymentplan.ID(defs[0].Object)
			switch mode {
			case "existing-unrecorded":
				o := deploymentplan.Clone(defs[0].Object)
				deploymentplan.Map(o["metadata"])["uid"] = "00000009-9999-4999-8999-999999999999"
				f.b.objects[id] = o
			case "original-policy-owner":
				deploymentplan.Map(f.b.objects["NetworkPolicy/"+f.h.Execution.Journal.Stack+"-platform/allow-postgres-clients"]["metadata"])["uid"] = "foreign"
			case "original-postgres-body":
				deploymentplan.Map(f.b.objects["StatefulSet/"+f.h.Execution.Journal.Stack+"-platform/postgresql"]["spec"])["replicas"] = float64(99)
			case "unknown-write":
				f.b.failure = "unknown-write"
			}
			if err := f.run(t.Context()); err == nil {
				t.Fatal("unrecorded owner or unknown write accepted")
			}
			if mode == "unknown-write" {
				if f.b.writes[id] != 1 {
					t.Fatal("native boundary not reached")
				}
				if err := f.run(t.Context()); err == nil || f.b.writes[id] != 1 {
					t.Fatal("unknown native write repeated")
				}
			} else if f.b.writes[id] != 0 {
				t.Fatal("foreign source allowed repair write")
			}
		})
	}
}

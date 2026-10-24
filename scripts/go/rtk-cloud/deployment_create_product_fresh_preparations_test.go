package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native session/issuance/finalizer are the same real HTTP/crypto fixture
// as the full product protocol. Kubernetes is an isolated boundary adapter;
// every ordinary child plan, lease, write journal and parent receipt is real.
func prepareFreshProductWorkload(t *testing.T, f *productProtocolFixture, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, bindings environmentIdentityBindings) environmentCreateBootstrapApply {
	t.Helper()
	backend := &productOuterTargetBackend{productProtocolBackend: f.b}
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		p, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil {
			return err
		}
		if p == nil {
			var live []deploymentplan.Object
			for _, object := range f.b.objects {
				live = append(live, object)
			}
			p, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: f.cfg.Environment, Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.p.Images), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, live)
			if err != nil {
				return err
			}
		}
		if p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, defs) {
			return environmentCreateBootstrapBlocker("isolated fresh callback changed its original definitions")
		}
		return executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, backend, func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, f.store.Root)
		}, func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.Verify(ctx, backend)
		}, func(context.Context) error { return nil }, io.Discard)
	}
	var consumers []deploymentplan.Definition
	for _, d := range f.l.SourceDefinitions {
		counts := map[string]int{}
		outputs := map[string]environmentCreateProductIdentity{}
		for _, o := range l.Outputs {
			outputs[o.ID] = o
		}
		object := deploymentplan.Clone(d.Object)
		if err := resolveCreateNormalTailProductACK(object, outputs, counts, installed.Ceremony, false); err != nil {
			t.Fatal(err)
		}
		if len(counts) != 0 {
			consumers = append(consumers, deploymentplan.Definition{Object: object, Phase: "workload"})
		}
	}
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	bound, err := bindFreshProductTargetDefinitions(ctx, f.cfg, f.store, bindings, consumers)
	if err != nil || len(bound) != 11 {
		t.Fatal("fresh target must declare all five consumers and six genuine preparation owners", len(bound), err)
	}
	retained := 0
	for _, d := range bound {
		if d.Retain {
			retained++
			if deploymentplan.Map(d.Object["metadata"])["uid"] == "" {
				t.Fatal("fresh preparation lost its original accepted UID")
			}
		}
	}
	if retained != 6 {
		t.Fatal("fresh target has no exact six preparation owners", retained)
	}
	if err = apply(ctx, "workload", bound); err != nil {
		t.Fatal("fresh original workload", err)
	}
	return apply
}

func newFreshProductPreparationFixture(t *testing.T) (*productProtocolFixture, *createNormalTailProductLifecycle, *environmentProductIdentitySession, environmentIdentityBindings, environmentCreateBootstrapApply) {
	t.Helper()
	f := newProductProtocolFixture(t, false)
	f.unscoped = true
	f.runtime.ProductIdentityScope = ""
	f.runtime.PKIClient.mutationScope = ""
	var err error
	f.subjects, err = environmentCreatePlannedServiceSubjects(f.cfg, f.p)
	if err != nil || len(f.subjects) != 14 {
		t.Fatal(err)
	}
	crl, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := createEnvironmentServiceSession(t.Context(), f.cfg, f.p, f.store, f.runtime, environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, RootCRLPEM: crl, RootCRLSecret: "pki-service-root-crl"}, f.subjects, f.apply)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := f.controls(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.apply(t.Context(), "managed-complete-issuer", controls); err != nil {
		t.Fatal(err)
	}
	if err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, s, f.apply); err != nil {
		t.Fatal(err)
	}
	program, err := readFreshProductProgram(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(f.store, l)
	if err != nil {
		t.Fatal(err)
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: f.cfg.Environment, Stack: f.p.Stack, Owners: map[string]string{}}
	for id, object := range f.b.objects {
		bindings.Owners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	if err = applyEnvironmentFreshProductBindings(t.Context(), f.cfg, f.e, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err = writeProductArtifact(f.store, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/bindings.json"), bindings); err != nil {
		t.Fatal(err)
	}
	apply := prepareFreshProductWorkload(t, f, l, installed, bindings)
	f.installed = slices.Clone(f.subjects)
	if err = finalizeEnvironmentCreateServiceSession(t.Context(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, s, nil); err != nil {
		t.Fatal("genuine fresh native finalization", err)
	}
	return f, l, installed, bindings, apply
}

func TestProductIdentityFreshPreparationsRetireBeforeTargetCompileAndRestore(t *testing.T) {
	f, l, installed, bindings, apply := newFreshProductPreparationFixture(t)
	_, _, release := completeEnvironmentRenderFixture("dev")
	before, _ := readEnvironmentCreateStageInput(f.store, f.e, "workload")
	writes := f.b.writes
	if _, ok, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime); err == nil || ok {
		t.Fatal("pending cleanup accepted without its original stage adapter")
	}
	consumerID := "Deployment/" + f.p.Stack + "-video-cloud/" + strings.TrimPrefix(l.Outputs[0].Subject, "service:")
	originalConsumer := deploymentplan.Clone(f.b.objects[consumerID])
	deploymentplan.Map(f.b.objects[consumerID]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	writeProof := deploymentplan.Digest(writes)
	if _, ok, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime, apply); err == nil || ok {
		t.Fatal("steady update adopted a replacement of the original workload consumer")
	}
	if writeProof != deploymentplan.Digest(writes) {
		t.Fatal("replacement owner was written before original consumer verification")
	}
	f.b.objects[consumerID] = originalConsumer
	got, ok, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime, apply)
	if err != nil || !ok {
		t.Fatal("fresh finalized restore must finish original cleanup before owner validation", err)
	}
	if err = productPreparationsRetired(t.Context(), f.cfg, f.store, f.e, l, installed, f.runtime.Backend, true); err != nil {
		t.Fatal(err)
	}
	after, _ := readEnvironmentCreateStageInput(f.store, f.e, "workload")
	if before.SHA256() != after.SHA256() {
		t.Fatal("initial accepted workload was rewritten by final cleanup")
	}
	var defs []deploymentplan.Definition
	for _, resource := range before.Resources {
		if resource.Action != "retain" {
			defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase})
		}
	}
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	final, err := bindFreshProductTargetDefinitions(ctx, f.cfg, f.store, got, defs)
	if err != nil || len(final) != 5 {
		t.Fatal("retired owners leaked into fresh steady target", len(final), err)
	}
	for _, d := range final {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			if mqttFoundationEnv(deploymentplan.Map(raw), "PKI_BOOTSTRAP_SESSION_ID") != "" {
				t.Fatal("final target retained deleted session settings")
			}
		}
	}
	journalBefore := deploymentplan.Digest(f.e.Journal)
	writeBefore := deploymentplan.Digest(writes)
	if _, ok, err = restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime); err != nil || !ok {
		t.Fatal("completed cleanup must restore read-only without a write adapter", err)
	}
	if journalBefore != deploymentplan.Digest(f.e.Journal) || writeBefore != deploymentplan.Digest(writes) {
		t.Fatal("completed restore replayed native writes or changed original evidence")
	}
	// Retiring a preparation is never permission to lose a permanent mirror,
	// claim, or another original bootstrap owner.
	for _, id := range []string{l.Outputs[0].ID, "PersistentVolumeClaim/" + f.p.Stack + "-video-cloud/" + l.Outputs[0].Claim} {
		old := f.b.objects[id]
		delete(f.b.objects, id)
		if _, ok, err = restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime); err == nil || ok {
			t.Fatal("unrelated missing original owner accepted", id)
		}
		f.b.objects[id] = old
	}
	_ = bindings
}

func TestProductIdentityFreshPreparationsResumeKnownDeleteBeforeCompleted(t *testing.T) {
	for _, mode := range []string{"known-response", "completed-prefix", "unknown-in-flight", "foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			f, l, installed, _, apply := newFreshProductPreparationFixture(t)
			_, _, release := completeEnvironmentRenderFixture("dev")
			steadyCalls := 0
			interrupted := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				if stage != "fresh-product-producer-retire" {
					steadyCalls++
					return apply(ctx, stage, defs)
				}
				var live []deploymentplan.Object
				for _, object := range f.b.objects {
					live = append(live, object)
				}
				p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: f.cfg.Environment, Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.p.Images), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, live)
				if err != nil {
					return err
				}
				backend := &productOuterTargetBackend{productProtocolBackend: f.b, retireFailure: "known-write"}
				err = executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, backend, func(ctx context.Context, p *deploymentplan.Plan) error {
					return p.AdmitResumable(ctx, backend, f.store.Root)
				}, func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard)
				if err == nil {
					t.Fatal("missing known delete interruption")
				}
				return err
			}
			if _, ok, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime, interrupted); err == nil || ok || steadyCalls != 1 {
				t.Fatal("did not reach original partial retirement", err)
			}
			p, err := readEnvironmentCreateStageInput(f.store, f.e, "fresh-product-producer-retire")
			if err != nil || p == nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var journal deploymentplan.Journal
			if json.Unmarshal(raw, &journal) != nil || len(journal.Completed) != 0 || len(journal.Owners) != 1 || journal.InFlight != "" {
				t.Fatal("not a genuine known delete-response boundary")
			}
			id, uid := p.Resources[0].ID, p.Resources[0].UID
			if f.b.objects[id] != nil || journal.Owners[id] != uid {
				t.Fatal("first exact owner was not actually deleted")
			}
			switch mode {
			case "completed-prefix":
				journal.Completed = []string{id}
			case "unknown-in-flight":
				journal.InFlight = id
				delete(journal.Owners, id)
			case "foreign-owner":
				journal.Owners[id] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			}
			if mode != "known-response" {
				bytes, _ := json.Marshal(journal)
				if os.WriteFile(path, bytes, 0600) != nil {
					t.Fatal("journal fixture")
				}
			}
			writes := f.b.writes[id]
			_, restored, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime, apply)
			if mode == "unknown-in-flight" || mode == "foreign-owner" {
				if err == nil || restored {
					t.Fatal("unknown or foreign retirement accepted")
				}
				if f.b.writes[id] != writes {
					t.Fatal("uncertain delete was repeated")
				}
				return
			}
			if err != nil || !restored {
				t.Fatal("known original retirement did not resume before bootstrap owner validation", err)
			}
			if f.b.writes[id] != writes {
				t.Fatal("known conditional DELETE was repeated")
			}
			if err = productPreparationsRetired(t.Context(), f.cfg, f.store, f.e, l, installed, f.runtime.Backend, true); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(id, "Deployment/") {
				t.Fatal("retired something outside exact preparations")
			}
		})
	}
}

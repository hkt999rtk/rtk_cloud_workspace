package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type normalTailE2EBackend struct {
	e                       *environmentCreateExecution
	objects                 map[string]deploymentplan.Object
	writes                  map[string]int
	routes                  map[string]string
	phase, failure          string
	stage                   string
	failed, held            bool
	leases                  int
	captureJobLifecycle     bool
	defaultNativeServices   bool
	requireNativeNamespaces bool
}

func (b *normalTailE2EBackend) trip(point string) error {
	if b.failure == point && !b.failed {
		b.failed = true
		return errors.New("isolated interruption: " + point)
	}
	return nil
}
func (b *normalTailE2EBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return deploymentplan.Clone(b.objects[id]), nil
}
func (b *normalTailE2EBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if err := b.e.Check(ctx); err != nil {
		return nil, err
	}
	actual := deploymentplan.Clone(body)
	if b.defaultNativeServices && actual["kind"] == "Service" {
		spec := deploymentplan.Map(actual["spec"])
		if spec["type"] == nil || spec["type"] == "" {
			// Kubernetes applies this default on the original API write.
			spec["type"] = "ClusterIP"
		}
	}
	if dry {
		return actual, nil
	}
	if b.requireNativeNamespaces {
		namespace := deploymentplan.String(deploymentplan.Map(body["metadata"])["namespace"])
		if namespace != "" && b.objects["Namespace//"+namespace] == nil {
			return nil, errors.New("isolated native namespace does not exist before resource write")
		}
	}
	id := deploymentplan.ID(body)
	b.writes[id]++
	uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
	if uid == "" {
		uid = fmt.Sprintf("%08d-1111-4111-8111-111111111111", len(b.objects)+1)
	}
	deploymentplan.Map(actual["metadata"])["uid"] = uid
	deploymentplan.Map(actual["metadata"])["resourceVersion"] = fmt.Sprint(b.writes[id])
	if b.captureJobLifecycle && actual["kind"] == "Job" {
		old := b.objects[id]
		if old != nil && deploymentplan.Map(old["metadata"])["uid"] == uid && deploymentplan.Digest(actual["spec"]) == deploymentplan.Digest(old["spec"]) && deploymentplan.Map(old["status"])["startTime"] != nil {
			actual["status"] = deploymentplan.Clone(deploymentplan.Map(old["status"]))
		} else {
			actual["status"] = deploymentplan.Object{"startTime": time.Now().UTC().Format(time.RFC3339Nano), "active": float64(1)}
		}
	}
	b.objects[id] = actual
	if err := b.trip("unknown-write"); err != nil {
		return nil, err
	}
	return deploymentplan.Clone(actual), nil
}
func (b *normalTailE2EBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.waitObserved(ctx, r); err != nil {
		return err
	}
	b.recordJobCompletion(r)
	return nil
}

func (b *normalTailE2EBackend) recordJobCompletion(r deploymentplan.Resource) {
	job := b.objects[r.ID]
	if !b.captureJobLifecycle || job == nil || job["kind"] != "Job" {
		return
	}
	status := deploymentplan.Map(job["status"])
	if status["startTime"] == nil || status["completionTime"] != nil || status["failed"] != nil {
		return
	}
	status["active"], status["succeeded"] = float64(0), float64(1)
	status["completionTime"] = time.Now().UTC().Format(time.RFC3339Nano)
	status["conditions"] = []any{deploymentplan.Object{"type": "Complete", "status": "True"}}
}

func (b *normalTailE2EBackend) waitObserved(ctx context.Context, r deploymentplan.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.objects[r.ID] == nil {
		return errors.New("missing actual owner")
	}
	if err := b.trip("after-write-" + b.stage + "-" + r.Phase); err != nil {
		return err
	}
	return b.trip("after-write-" + r.Phase)
}
func (b *normalTailE2EBackend) ExecutionProgress(_ context.Context, phase, step string) {
	b.phase = phase
}
func (b *normalTailE2EBackend) CheckParentExecution(ctx context.Context, p *deploymentplan.Plan, root string) error {
	if root != b.e.Store.Root || p.CreateParentSHA256 != b.e.Journal.PlanSHA256 {
		return errors.New("foreign parent")
	}
	return b.e.Check(ctx)
}
func (b *normalTailE2EBackend) RecordParentExecutionLease(ctx context.Context, p *deploymentplan.Plan, owner deploymentplan.LeaseOwner) error {
	return b.e.RecordKubernetesLease(owner)
}
func (b *normalTailE2EBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	if b.held {
		return nil, errors.New("lease occupied")
	}
	b.held = true
	b.leases++
	return normalTailE2ELease{b, p}, nil
}

type normalTailE2ELease struct {
	b *normalTailE2EBackend
	p *deploymentplan.Plan
}

func (l normalTailE2ELease) Owner() deploymentplan.LeaseOwner {
	return deploymentplan.LeaseOwner{ResourceID: "Lease/" + l.p.Stack + "-video-cloud/rtk-deployment-execution", UID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", l.b.leases), Holder: l.p.SHA256() + fmt.Sprint(":attempt-", l.b.leases)}
}
func (l normalTailE2ELease) Check(ctx context.Context) error {
	if !l.b.held {
		return errors.New("lost lease")
	}
	return l.b.e.Check(ctx)
}
func (l normalTailE2ELease) Release(context.Context) error { l.b.held = false; return nil }

// This exercises the actual common proof publisher/consumer and execution
// runner after an independently verified handoff. Native effects are isolated
// in the fake backend; definitions and their dependency are explicit, never
// synthesized by scanning consumer references.
func TestCreateNormalTailE2EQualifiedRecoveryThroughOriginalRelease(t *testing.T) {
	for _, failure := range []string{"before-admission", "before-write-configuration", "before-write-workload", "after-write-configuration", "after-write-workload", "workload-acceptance", "parent-workload-receipt", "external", "dns", "turn", "full-acceptance", "ledger", "closure"} {
		t.Run(failure, func(t *testing.T) { runNormalTailE2E(t, failure, false) })
	}
}
func TestCreateNormalTailE2EUnknownWriteStaysClosed(t *testing.T) {
	runNormalTailE2E(t, "unknown-write", true)
}

func runNormalTailE2E(t *testing.T, failure string, uncertain bool) {
	t.Helper()
	seed, _, _ := completePreflightProofFixture(t, "dev")
	cfg, store, operator, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "600"
	seed.Store, seed.Configuration, seed.Backend.Store = store, cfg, store
	seed.Bindings = environmentIdentityBindings{Version: 1, Environment: "dev", Stack: e.Journal.Stack, Owners: map[string]string{}}
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"terminal": deploymentplan.Digest("verified-isolated-bootstrap-terminal"), "migration": deploymentplan.Digest("verified-isolated-redis-completion")}}
	protected := deploymentplan.Digest(h.Pins)
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "runtime"}, "data": deploymentplan.Object{"root": "unchanged-original-root"}}
	app := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "application"}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "application"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "application"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": seed.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, "envFrom": []any{deploymentplan.Object{"configMapRef": deploymentplan.Object{"name": "runtime"}}}}}}}}}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest("frozen-canonical-inputs"), ReleaseSHA256: deploymentplan.Digest(seed.Release), BindingsSHA256: deploymentplan.Digest(seed.Bindings), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: protected}
	p, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: cm, Phase: "configuration"}, {Object: app, Phase: "workload", Dependencies: []string{deploymentplan.ID(cm)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed.Plan = p
	b := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}, failure: failure}
	checks := func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
		result := map[string]deploymentplan.GateCheck{}
		for _, id := range deploymentplan.RequiredGateIDs(false) {
			id := id
			result[id] = func(ctx context.Context, plan *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
				if err := e.Check(ctx); err != nil {
					return deploymentplan.GateEvidence{}, err
				}
				if deploymentplan.Digest(h.Pins) != protected || plan.SHA256() != p.SHA256() {
					return deploymentplan.GateEvidence{}, errors.New("protected input drift")
				}
				if id == "deployment.execution" {
					if err := p.AdmitResumable(ctx, b, store.Root); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
				}
				return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest([]string{id, p.SHA256(), protected})}, nil
			}
		}
		return result
	}
	deps := unifiedDeploymentDependencies{InvalidateProof: invalidateEnvironmentPreflightProofWithToken, Compile: func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
		return seed, nil
	}, Checks: checks, LiveChecks: checks, CheckExecutionAvailable: func(ctx context.Context, _ deploymentConfig, _ string) error { return e.Check(ctx) }}
	deps.PublishBindings, deps.PublishObservations = publishEnvironmentPreflightBindings, publishEnvironmentPreflightObservation
	deps.PublishProof = func(ctx context.Context, c *compiledEnvironmentDeployment, q deploymentplan.Qualification) error {
		proof, err := saveEnvironmentPreflightProofContext(ctx, c, q, time.Now().UTC())
		c.Proof = proof
		return err
	}
	deps.LoadQualified = func(ctx context.Context, cfg deploymentConfig, operation, sha string) (*compiledEnvironmentDeployment, error) {
		a, err := loadEnvironmentPreflightProof(cfg, store, operation, sha, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if a.Plan.SHA256() != p.SHA256() {
			return nil, errors.New("wrong frozen plan")
		}
		seed.Proof = &a.Proof
		seed.PreflightGeneration = a.Proof.Generation
		return seed, nil
	}
	deps.Accept = func(ctx context.Context, c *compiledEnvironmentDeployment) error {
		if len(b.routes) != 3 || len(b.objects) != 2 || deploymentplan.Digest(h.Pins) != protected {
			return errors.New("complete runtime acceptance missing")
		}
		return b.trip("full-acceptance")
	}
	var cancelAttempt context.CancelFunc
	deps.Execute = func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, qualify func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
		ctx = withCreateNormalTailHandoff(ctx, h)
		fence := func(ctx context.Context, plan *deploymentplan.Plan) error {
			if err := e.Check(ctx); err != nil {
				return err
			}
			return b.trip("before-write-" + b.phase)
		}
		workload := func(ctx context.Context) error {
			return executeCreateNormalTailWorkloadBackend(ctx, store, h, p, b, func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := b.trip("before-admission"); err != nil {
					return err
				}
				return qualify(ctx, p)
			}, fence, func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := p.Verify(ctx, b); err != nil {
					return err
				}
				return b.trip("workload-acceptance")
			}, io.Discard, func(context.Context) error { return b.trip("parent-workload-receipt") })
		}
		route := func(key string) func(context.Context) error {
			return func(ctx context.Context) error {
				intent := mustProviderRecoveryJSON(deploymentplan.Object{"resource": key, "owner": "original-native-owner"})
				receipt := mustProviderRecoveryJSON(deploymentplan.Object{"configuration": "qualified-" + key})
				done, err := createNormalTailRecordedStage(ctx, e, key, "routing", intent, receipt, func(context.Context) error {
					if b.routes[key] != "qualified-"+key {
						return errors.New("actual routing drift")
					}
					return nil
				})
				if err != nil || done {
					return err
				}
				if err := e.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
					return err
				}
				if b.routes[key] != "" {
					return errors.New("unrecorded route cannot be adopted")
				}
				b.routes[key] = "qualified-" + key
				if err := e.RecordReceipt(key, receipt); err != nil {
					return err
				}
				if err := b.trip(key); err != nil {
					return err
				}
				return e.CompleteEntry(key, time.Now().UTC())
			}
		}
		capture := func(ctx context.Context) error {
			ledger, err := readEnvironmentCreateLedger(e)
			if err != nil {
				return err
			}
			for id, o := range b.objects {
				ledger.Kubernetes[id] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
			}
			if err := saveEnvironmentCreateLedger(ctx, e, ledger); err != nil {
				return err
			}
			if err := b.trip("ledger"); err != nil {
				return err
			}
			if failure == "closure" && !b.failed {
				b.failed = true
				cancelAttempt()
			}
			return nil
		}
		return executeCreateNormalTailSteps(ctx, e, workload, route("external"), route("dns"), route("turn"), func(ctx context.Context) error { return accept(ctx, c) }, capture)
	}
	for attempt := 0; attempt < 2; attempt++ {
		preflight := unifiedDeploymentReport{Environment: "dev", Operation: "create"}
		err := runUnifiedCompiledEnvironmentDeployment(t.Context(), cfg, unifiedDeploymentOptions{Action: "preflight", Operation: "create"}, &preflight, io.Discard, io.Discard, deps)
		if uncertain && attempt == 1 {
			if err == nil || preflight.Decision == "GO" {
				t.Fatal("uncertain write obtained GO")
			}
			if !e.Journal.CompletedAt.IsZero() || b.writes[p.Resources[0].ID] != 1 {
				t.Fatal("uncertain write replayed or parent released")
			}
			return
		}
		if err != nil || preflight.Decision != "GO" {
			t.Fatal("complete shared preflight failed", preflight, err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		cancelAttempt = cancel
		report := unifiedDeploymentReport{Environment: "dev", Operation: "create"}
		err = runUnifiedCompiledEnvironmentDeployment(ctx, cfg, unifiedDeploymentOptions{Action: "create", Operation: "create", PlanSHA256: p.SHA256()}, &report, io.Discard, io.Discard, deps)
		cancel()
		if attempt == 0 {
			if err == nil || report.Completed {
				t.Fatal("injected interruption succeeded")
			}
			if e.Journal.CompletedAt.IsZero() == false {
				t.Fatal("interrupted flow released original parent")
			}
			continue
		}
		if err != nil || !report.Completed || e.Journal.CompletedAt.IsZero() {
			t.Fatal("same original execution did not complete", report, err)
		}
	}
	if b.held || len(b.writes) != 2 || b.writes[p.Resources[0].ID] != 1 || b.writes[p.Resources[1].ID] != 1 || deploymentplan.Digest(h.Pins) != protected {
		t.Fatal("recovery replayed writes, changed protected bootstrap or leaked lease", b.writes)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("original active fence not released", err)
	}
	var owner environmentCreateOwner
	if readEnvironmentPreflightJSON(filepath.Join(e.Directory, "owner/owner.json"), &owner) != nil || owner.OwnerToken != e.Journal.OwnerToken || !strings.HasSuffix(owner.Journal, "journal.json") {
		t.Fatal("original owner was not archived")
	}
}

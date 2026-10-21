package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the external Kubernetes lease protocol is simulated here. Production
// acquisition/release code still owns the exact UID and resourceVersion fences.
type historicalNativeLeaseAPI struct{ *normalTailE2EBackend }

func (b historicalNativeLeaseAPI) DeleteExecutionLease(ctx context.Context, id, uid, version string) error {
	if err := b.e.Check(ctx); err != nil {
		return err
	}
	actual := b.objects[id]
	meta := deploymentplan.Map(actual["metadata"])
	if actual == nil || deploymentplan.ID(actual) != id || meta["uid"] != uid || meta["resourceVersion"] != version {
		return errors.New("isolated native lease delete precondition conflict")
	}
	delete(b.objects, id)
	return nil
}

// historicalSerialInterruptedStage models the documented pre-barrier producer:
// each conditional native write was read back and waited before the next write.
// It persists only outcomes that the external native fixture actually returned.
// Unlike the current workload barrier, a failed first consumer leaves later
// consumers unwritten. It never marks a child accepted or creates a GO artifact.
// All completed repair stages continue to use the current production executor.
func (f *acceptedCoreExecutionContract) historicalSerialInterruptedStage(t *testing.T, key string, defs []deploymentplan.Definition, failed string, stopBeforeWait ...bool) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	ctx := context.Background()
	var observed []deploymentplan.Object
	for _, object := range f.native.objects {
		observed = append(observed, object)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(strings.TrimPrefix(key, "kubernetes/"), defs)}, defs, observed)
	if err != nil {
		t.Fatal("historical child compilation", key, err)
	}
	if err := f.e.Check(ctx); err != nil {
		t.Fatal(err)
	}
	backend := historicalAcceptedScopedBackend{normalTailE2EBackend: f.native, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}
	checkParent := func() {
		if err := backend.CheckParentExecution(ctx, p, f.store.Root); err != nil {
			t.Fatal("historical scoped parent fence", err)
		}
	}
	checkParent()
	if err := p.Admit(ctx, backend); err != nil {
		t.Fatal("historical server admission", key, err)
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry(key, strings.TrimPrefix(key, "kubernetes/"), intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var lease deploymentplan.ExecutionLease
	processLoss := len(stopBeforeWait) != 0 && stopBeforeWait[0]
	if processLoss {
		// A lost process leaves the server-side lease allocated. Use the actual
		// lease producer so its owner, version and later conditional deletion
		// come from native protocol effects, never from a fabricated receipt.
		lease, err = acquireEnvironmentExecutionLease(ctx, historicalNativeLeaseAPI{f.native}, p)
	} else {
		lease, err = f.native.AcquireExecution(ctx, p)
	}
	if err != nil {
		t.Fatal(err)
	}
	retainedLease := false
	defer func() {
		if !retainedLease {
			if err := lease.Release(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := backend.RecordParentExecutionLease(ctx, p, lease.Owner()); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join("deployment/rollback", p.SHA256())
	f.write(t, filepath.Join(directory, "plan.json"), p)
	j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Owners: map[string]string{}}
	persist := func() { f.write(t, filepath.Join(directory, "journal.json"), j) }
	persist()
	native := coreContractReadinessFailure{normalTailE2EBackend: f.native, target: failed}
	for _, resource := range p.Resources {
		checkParent()
		if err := lease.Check(ctx); err != nil {
			t.Fatal(err)
		}
		current, err := native.Read(ctx, resource.ID)
		if err != nil {
			t.Fatal(err)
		}
		body, err := resource.Body(current)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			checkParent()
			admitted, err := native.Write(ctx, resource.Action, body, true)
			if err != nil || admitted == nil || !deploymentplan.Matches(resource.Desired, admitted) {
				t.Fatal("historical conditional dry-run", resource.ID, err)
			}
			j.InFlight = resource.ID
			persist()
			checkParent()
			actual, err := native.Write(ctx, resource.Action, body, false)
			if err != nil {
				t.Fatal("historical native write", resource.ID, err)
			}
			owner := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
			if !environmentBootstrapOwnerUUID.MatchString(owner) || resource.UID != "" && owner != resource.UID {
				t.Fatal("historical write changed native owner", resource.ID)
			}
			readback, err := native.Read(ctx, resource.ID)
			if err != nil || readback == nil || deploymentplan.Map(readback["metadata"])["uid"] != owner || !deploymentplan.Matches(resource.Desired, readback) {
				t.Fatal("historical write lacks matching native readback", resource.ID, err)
			}
			j.Owners[resource.ID] = owner
			j.InFlight = ""
			persist()
		} else {
			if current == nil || deploymentplan.Map(current["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, current) {
				t.Fatal("historical retained owner differs", resource.ID)
			}
			j.Owners[resource.ID] = resource.UID
			persist()
		}
		if err := lease.Check(ctx); err != nil {
			t.Fatal(err)
		}
		// The first historical application forward lost its process after the
		// native write/readback and before readiness. Preserve that exact durable
		// boundary; no failure or completion was observed or recorded yet.
		if processLoss && resource.ID == failed {
			if j.Failed != "" || j.InFlight != "" || len(j.Owners) != len(j.Completed)+1 {
				t.Fatal("historical process interruption changed durable progress")
			}
			retainedLease = true
			return p, j
		}
		if resource.Desired["kind"] != "PersistentVolumeClaim" {
			if err := native.Wait(ctx, resource); err != nil {
				if resource.ID != failed {
					t.Fatal("unexpected historical readiness failure", resource.ID, err)
				}
				j.Failed = resource.ID
				persist()
				if j.InFlight != "" || j.Accepted || j.AcceptanceStarted || len(j.Owners) != len(j.Completed)+1 {
					t.Fatal("historical interruption manufactured an accepted or uncertain write")
				}
				return p, j
			}
		}
		j.Completed = append(j.Completed, resource.ID)
		persist()
	}
	t.Fatal("historical child did not reach selected native interruption", failed)
	return nil, j
}

// acceptedHistoricalStage uses the maintained resumable executor for every
// accepted forward in an historical chain. Its parent phase, immutable input,
// native lease, conditional writes and final Verify all come from production.
func (f *acceptedCoreExecutionContract) acceptedHistoricalStage(t *testing.T, stage string, definitions []deploymentplan.Definition) *deploymentplan.Plan {
	t.Helper()
	ctx := context.Background()
	defs := make([]deploymentplan.Definition, len(definitions))
	for i, definition := range definitions {
		defs[i] = definition
		defs[i].Object = deploymentplan.Clone(definition.Object)
		if defs[i].Object["kind"] == "Secret" {
			data := deploymentplan.Map(defs[i].Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for key, value := range deploymentplan.Map(defs[i].Object["stringData"]) {
				data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
			}
			defs[i].Object["data"] = data
			delete(defs[i].Object, "stringData")
		}
	}
	var observed []deploymentplan.Object
	for _, object := range f.native.objects {
		observed = append(observed, object)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, observed)
	if err != nil {
		t.Fatal(stage, err)
	}
	backend := historicalAcceptedScopedBackend{normalTailE2EBackend: f.native, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, f.store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return backend.CheckParentExecution(ctx, p, f.store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
	if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, backend, qualify, fence, accept, func(context.Context) error { return f.e.Check(ctx) }, io.Discard); err != nil {
		t.Fatal("accepted historical forward", stage, err)
	}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	return p
}

// Preserve explicit native terminal/scaled-down outcomes while adding ordinary
// ready Pod observations for the rest of the isolated native inventory.
func historicalNativeInventory(original map[string]deploymentplan.Object) map[string]deploymentplan.Object {
	out := coreContractNativeInventory(original)
	// The small original Core fixture has single-replica listeners. Historical
	// application owners may request several replicas; expose exactly that
	// native controller cardinality, retaining each actual ReplicaSet lineage.
	for id, actual := range original {
		if actual["kind"] != "Deployment" {
			continue
		}
		replicas := envObjectReplicas(actual)
		if replicas <= 1 {
			continue
		}
		meta := deploymentplan.Map(actual["metadata"])
		podID := "Pod/" + deploymentplan.String(meta["namespace"]) + "/" + deploymentplan.String(meta["name"]) + "-pod"
		pod := out[podID]
		if pod == nil {
			continue
		}
		status := deploymentplan.Map(out[id]["status"])
		for _, key := range []string{"replicas", "readyReplicas", "updatedReplicas", "availableReplicas"} {
			status[key] = float64(replicas)
		}
		for ordinal := 1; ordinal < replicas; ordinal++ {
			clone := deploymentplan.Clone(pod)
			cm := deploymentplan.Map(clone["metadata"])
			cm["name"] = fmt.Sprint(cm["name"]) + fmt.Sprint("-", ordinal)
			cm["uid"] = coreContractObserverUUID(deploymentplan.String(meta["uid"]), "Pod", deploymentplan.Map(actual["spec"])["template"], ordinal)
			out[deploymentplan.ID(clone)] = clone
		}
	}
	for id, actual := range original {
		failed := false
		if actual["kind"] == "Job" {
			for _, value := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
				condition := deploymentplan.Map(value)
				if condition["type"] == "Failed" && condition["status"] == "True" {
					failed = true
				}
			}
		}
		zero := actual["kind"] == "Deployment" && fmt.Sprint(deploymentplan.Map(actual["spec"])["replicas"]) == "0"
		if !failed && !zero {
			continue
		}
		owner := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
		if failed {
			out[id]["status"] = deploymentplan.Clone(deploymentplan.Map(actual["status"]))
		}
		if zero {
			out[id]["status"] = deploymentplan.Object{"observedGeneration": float64(1), "replicas": float64(0), "readyReplicas": float64(0), "updatedReplicas": float64(0), "availableReplicas": float64(0), "unavailableReplicas": float64(0)}
		}
		for candidate, object := range out {
			if object["kind"] != "Pod" {
				continue
			}
			for _, value := range certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"]) {
				uid := deploymentplan.String(deploymentplan.Map(value)["uid"])
				if uid == owner || zero && uid == coreContractObserverUUID(owner, "ReplicaSet", deploymentplan.Map(actual["spec"])["template"], 0) {
					delete(out, candidate)
				}
			}
		}
	}
	return out
}

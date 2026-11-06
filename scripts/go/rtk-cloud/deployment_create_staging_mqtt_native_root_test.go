package main

import (
	"context"
	"fmt"
	"io"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The shared native authority fixture uses its own isolated dev namespace.
// Qualification must bind to that selected environment and its actual signed
// Root, not to a fabricated ready document or a name-only trust assumption.
func TestStagingMQTTPlacementQualificationPreservesSignedNativeRoot(t *testing.T) {
	f := newCoreExecutionContractAtBoundary(t, false, true, false, historicalCompleteDomainPolicy(t))
	f.installHistoricalAuthorityProtocol(t)
	ns := f.p.Plan.Stack + "-video-cloud"
	claim := environmentCreateServiceClaimDefinition(f.cfg, ns, "video-cloud-api-service-state")
	app := f.stage(t, "kubernetes/application-client-enrollment", []deploymentplan.Definition{claim})
	claimID := app.Resources[0].ID
	deploymentplan.Map(f.native.objects[claimID]["spec"])["volumeName"] = "original-api-volume"
	f.native.objects[claimID]["status"] = deploymentplan.Object{"phase": "Bound"}
	f.stage(t, "kubernetes/transport-client-enrollment", []deploymentplan.Definition{{Object: stagingTransportQualificationObject("ConfigMap", ns, "accepted-transport", nil), Phase: "configuration"}})
	root := f.trustAuthority(t, "mqtt", 21, false)
	defs := []deploymentplan.Definition{}
	for i := 0; i < 5; i++ {
		defs = append(defs, deploymentplan.Definition{Object: stagingTransportQualificationObject("ConfigMap", ns, fmt.Sprintf("mqtt-original-%d", i), nil), Phase: "configuration"})
	}
	api := stagingTransportQualificationObject("Deployment", ns, "video-cloud-api", deploymentplan.Object{"replicas": float64(2), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": f.p.Plan.Bootstrap.Image}}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", "video-cloud-api-service-state")}}}})
	defs = append(defs, deploymentplan.Definition{Object: api, Phase: "workload"})
	stopped := func(stage string, defs []deploymentplan.Definition) (*deploymentplan.Plan, deploymentplan.Journal) {
		t.Helper()
		var live []deploymentplan.Object
		for _, o := range f.native.objects {
			live = append(live, o)
		}
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, live)
		if err != nil {
			t.Fatal(err)
		}
		f.native.failure = "after-write-workload"
		f.native.failed = false
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
		err = executeEnvironmentCreateStagePlan(t.Context(), f.store, f.e, stage, p, f.native, check, check, check, func(context.Context) error { return nil }, io.Discard)
		if err == nil {
			t.Fatal("expected original API readiness interruption")
		}
		j, exists, err := p.ReadExecution(f.store.Root)
		if err != nil || !exists || j.Accepted || j.InFlight != "" || j.Failed != deploymentplan.ID(api) || len(j.Completed) != 6 || len(j.Owners) != 6 {
			t.Fatal("original failed child not fully recorded", j, err)
		}
		f.native.failure = ""
		return p, j
	}
	original, j := stopped(stagingMQTTRootStage, defs)
	for i, suffix := range []string{"-rwo-", "-rwo-spread-"} {
		d := deploymentplan.Clone(api)
		deploymentplan.Map(d["metadata"])["uid"] = j.Owners[deploymentplan.ID(api)]
		pod := environmentPKIPod(d)
		pod["affinity"] = deploymentplan.Object{"podAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{deploymentplan.Object{"topologyKey": "kubernetes.io/hostname"}}}}
		if i == 1 {
			pod["topologySpreadConstraints"] = []any{deploymentplan.Object{"maxSkew": float64(1), "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway"}}
		}
		next := append([]deploymentplan.Definition(nil), defs...)
		next[len(next)-1].Object = d
		stopped(stagingMQTTRootStage+suffix+original.SHA256()[:12], next)
	}
	current := f.native.objects[deploymentplan.ID(api)]
	current["status"] = deploymentplan.Object{"readyReplicas": float64(1)}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	forward, err := qualifyStagingMQTTRootForward(t.Context(), f.cfg, f.store, &f.p.Plan, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend}, f.operator, true)
	if err != nil {
		t.Fatal("genuine signed original authority did not qualify", err)
	}
	if forward.Root.ID != root.ID || forward.Root.Certificate != root.Certificate || !forward.SingleOwner || forward.OriginalSHA != original.SHA256() || len(forward.PreviousKeys) != 2 {
		t.Fatal("qualified a replacement authority or lost previous attempts")
	}
	changed := 0
	for _, d := range forward.Definitions {
		if !d.Retain {
			changed++
			if deploymentplan.ID(d.Object) != deploymentplan.ID(api) || deploymentplan.Map(d.Object["spec"])["replicas"] != float64(1) || deploymentplan.Digest(environmentPKIPod(d.Object)) != deploymentplan.Digest(environmentPKIPod(current)) {
				t.Fatal("single-owner repair changed original identity or template")
			}
		}
	}
	if changed != 1 {
		t.Fatal("recovery exceeds the single API owner")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	// A live replacement cannot inherit that qualified original checkpoint.
	deploymentplan.Map(current["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	if _, err := qualifyStagingMQTTRootForward(t.Context(), f.cfg, f.store, &f.p.Plan, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend}, f.operator, true); err == nil {
		t.Fatal("replacement API inherited original authority")
	}
}
